package gopdf

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// FormRef describes a form XObject that runs when a page is drawn.
type FormRef struct {
	// Name is the resource name the form was first invoked by. It is
	// empty for a form that runs without being named: an annotation's
	// appearance, or the group of a soft mask.
	Name Name
	// Page is the 0-based index of the page that runs it.
	Page int

	ref Ref
}

// ObjectNumber identifies the form XObject behind this reference. A form
// is listed once per page however often the page invokes it, and the
// same number on two pages is one form used by both.
func (f FormRef) ObjectNumber() int { return f.ref.Num }

// maxFormRuns bounds how many content streams one page may have followed
// for it. Each stream is read once per set of resources it can run
// under, which is finite but, in a file built for the purpose, large.
// Past the bound PageForms reports an error instead of a partial list.
const maxFormRuns = 1 << 18

// PageForms lists the form XObjects whose content runs when a page is
// drawn, each one once.
//
// PageImages says what a page paints and where. This answers the
// question underneath it: of the forms a page's resources offer, which
// does anything use? A resource dictionary lists what content MAY
// invoke, and files list more than they invoke — a producer hands every
// page one dictionary, an editor deletes a block and leaves its form
// behind. A form that is on no page's list is in the file and on no
// page.
//
// Since that is the question, the list errs towards inclusion. It
// follows every content stream the page can run, not only what Do
// reaches from the page's own content:
//
//   - the page's content, and the forms it invokes, to any depth;
//   - the glyph procedures of the Type 3 fonts, the tiling patterns and
//     the soft-mask groups in each resource dictionary that comes into
//     use, whether or not the content selects them;
//   - the appearance streams of the page's annotations, in every state.
//
// And it resolves a name the way the most forgiving viewer does: in the
// stream's own resources first, then in those of whatever invoked it,
// out to the page's. A form with no /Resources of its own takes its
// names from there by rule; some viewers extend the same courtesy to a
// name a form's own dictionary lacks. The first dictionary that has the
// name wins, so a form that brings its own /Fm0 never stands in for a
// different /Fm0 listed on the page.
//
// PageForms returns an error rather than a partial list when a content
// stream it has to follow cannot be decoded, or is too large to read
// whole: half an answer to "does anything use this" is no answer.
func (r *Reader) PageForms(page int) ([]FormRef, error) {
	if page < 0 || page >= len(r.pages) {
		return nil, fmt.Errorf("gopdf: page %d out of range", page)
	}
	pi := r.pages[page]
	content, err := r.pageContent(pi.dict)
	if err != nil {
		return nil, err
	}
	names, cut := contentDoNames(content)
	if cut {
		return nil, fmt.Errorf("gopdf: page %d: content too large to read whole", page+1)
	}

	// A page draws with the resources of the nearest node of the page
	// tree that has any. Some viewers merge the dictionaries of every
	// node above it instead, so those are behind it in the chain: a
	// name the page's own dictionary lacks is looked for further up.
	var scopes []any
	for node, hops := pi.dict, 0; node != nil && hops < 64; hops++ {
		if res, ok := node["Resources"]; ok {
			scopes = append(scopes, res)
		}
		node, _ = r.resolve(node["Parent"]).(Dict)
	}
	sc := &formScanner{r: r, page: page, listed: map[int]bool{}, ran: map[string]bool{}}
	for i := len(scopes) - 1; i >= 0; i-- {
		sc.base, _ = sc.enter(sc.base, scopes[i], "page/"+strconv.Itoa(i))
	}
	chain := sc.base
	for _, name := range names {
		sc.invoke(name, chain, true)
	}

	// An annotation's look is a form of its own, drawn over the page.
	// Every state is followed, and the rollover and pressed looks with
	// the normal one: which of them shows is the viewer's business.
	if annots, ok := r.resolve(pi.dict["Annots"]).(Array); ok {
		for _, a := range annots {
			annot, ok := r.resolve(a).(Dict)
			if !ok {
				continue
			}
			ap, ok := r.resolve(annot["AP"]).(Dict)
			if !ok {
				continue
			}
			for _, key := range sortedKeys(ap) {
				switch look := r.resolve(ap[key]).(type) {
				case *rawStream:
					sc.form("", ap[key], chain)
				case Dict:
					for _, state := range sortedKeys(look) {
						sc.form("", look[state], chain)
					}
				}
			}
		}
	}

	if sc.err != nil {
		return nil, fmt.Errorf("gopdf: page %d: %w", page+1, sc.err)
	}
	return sc.out, nil
}

// formScope is one resource dictionary in use.
type formScope struct {
	// id tells two dictionaries apart: the object number of an indirect
	// one, the owner of one written inline.
	id  string
	res Dict
}

// formScanner follows a page's content streams, recording the forms
// they invoke.
type formScanner struct {
	r    *Reader
	page int
	out  []FormRef
	// base is the chain the page's own content runs under: the page's
	// resources, then those of its ancestors in the page tree.
	base []formScope
	// listed holds the object numbers already in out.
	listed map[int]bool
	// ran holds what has been followed: a content stream under a chain
	// of resource dictionaries, or the fonts, patterns and soft masks of
	// a chain's innermost dictionary.
	ran  map[string]bool
	runs int
	err  error
}

// chainKey names a chain of resource dictionaries, innermost first.
func chainKey(chain []formScope) string {
	var b strings.Builder
	for _, s := range chain {
		b.WriteString(s.id)
		b.WriteByte('|')
	}
	return b.String()
}

// streamID names a content stream.
func streamID(ref Ref, stm *rawStream) string {
	if ref.Num > 0 {
		return "S" + strconv.Itoa(ref.Num)
	}
	return fmt.Sprintf("S%p", stm)
}

// spend counts one unit of work against the page's budget.
func (sc *formScanner) spend() bool {
	if sc.err != nil {
		return false
	}
	if sc.runs++; sc.runs > maxFormRuns {
		sc.err = errors.New("too many nested content streams to follow")
		return false
	}
	return true
}

// enter brings a resource dictionary into use in front of chain, and
// follows the content its fonts, patterns and soft masks carry. It
// returns the chain to resolve names against, and whether the
// dictionary was there to bring: a stream without one keeps the chain
// it was invoked under.
func (sc *formScanner) enter(chain []formScope, res any, owner string) ([]formScope, bool) {
	d, ok := sc.r.resolve(res).(Dict)
	if !ok {
		return chain, false
	}
	id := owner
	if ref, isRef := res.(Ref); isRef {
		id = "R" + strconv.Itoa(ref.Num)
	}
	// A dictionary already in the chain moves to the front instead of
	// appearing twice: the first one to have a name wins, so the second
	// appearance could never be asked. It is also what makes a form
	// that invokes itself come back to a chain already followed.
	next := make([]formScope, 0, len(chain)+1)
	next = append(next, formScope{id: id, res: d})
	for _, s := range chain {
		if s.id != id {
			next = append(next, s)
		}
	}
	sc.carriers(next)
	return next, true
}

// carriers follows the content streams that the innermost dictionary of
// a chain carries without their being XObjects: Type 3 glyph
// procedures, tiling patterns, soft-mask groups. Whether the content
// selects them is not asked — they are few, and wrongly taking one for
// used only lengthens the list.
func (sc *formScanner) carriers(chain []formScope) {
	key := "c:" + chainKey(chain)
	if sc.ran[key] || !sc.spend() {
		return
	}
	sc.ran[key] = true
	r, head := sc.r, chain[0]

	if fonts, ok := r.resolve(head.res["Font"]).(Dict); ok {
		for _, name := range sortedKeys(fonts) {
			fd, ok := r.resolve(fonts[name]).(Dict)
			if !ok || r.resolve(fd["Subtype"]) != Name("Type3") {
				continue
			}
			procs, ok := r.resolve(fd["CharProcs"]).(Dict)
			if !ok {
				continue
			}
			fontID := head.id + "/Font/" + string(name)
			if ref, isRef := fonts[name].(Ref); isRef {
				fontID = "F" + strconv.Itoa(ref.Num)
			}
			_, hasRes := r.resolve(fd["Resources"]).(Dict)
			for _, glyph := range sortedKeys(procs) {
				stm, ok := r.resolve(procs[glyph]).(*rawStream)
				if !ok {
					continue
				}
				ref, _ := procs[glyph].(Ref)
				if !hasRes {
					// The common glyph draws a path and invokes
					// nothing; a font may carry hundreds, and none of
					// those needs following.
					if names, err := r.streamDoNames(ref, stm); err == nil && len(names) == 0 {
						continue
					}
				}
				sc.run(ref, stm, fd["Resources"], fontID, chain)
			}
		}
	}

	if patterns, ok := r.resolve(head.res["Pattern"]).(Dict); ok {
		for _, name := range sortedKeys(patterns) {
			// A tiling pattern is a stream; a shading pattern is a
			// dictionary and draws no content.
			if stm, ok := r.resolve(patterns[name]).(*rawStream); ok {
				ref, _ := patterns[name].(Ref)
				sc.run(ref, stm, stm.dict["Resources"], streamID(ref, stm), chain)
			}
		}
	}

	if states, ok := r.resolve(head.res["ExtGState"]).(Dict); ok {
		for _, name := range sortedKeys(states) {
			gs, ok := r.resolve(states[name]).(Dict)
			if !ok {
				continue
			}
			if mask, ok := r.resolve(gs["SMask"]).(Dict); ok {
				sc.form("", mask["G"], chain)
			}
		}
	}
}

// invoke resolves the name of a Do against a chain and follows the form
// it finds. own reports whether the stream doing the invoking brought
// resources of its own.
func (sc *formScanner) invoke(name Name, chain []formScope, own bool) {
	first := func(scopes []formScope) {
		for _, s := range scopes {
			xobjects, _ := sc.r.resolve(s.res["XObject"]).(Dict)
			if entry, ok := xobjects[name]; ok {
				sc.form(name, entry, chain)
				return
			}
		}
	}
	first(chain)
	// A form without resources takes its names from the stream that
	// invoked it — or, read to the letter of the rule it comes from,
	// from the page. Where the two differ, both are followed.
	if !own {
		first(sc.base)
	}
}

// form records a form XObject and follows its content.
func (sc *formScanner) form(name Name, entry any, chain []formScope) {
	stm, ok := sc.r.resolve(entry).(*rawStream)
	if !ok {
		return
	}
	// An XObject that does not say what it is gets read as a form: that
	// is what a viewer shown one by Do would have to do with it.
	switch sc.r.resolve(stm.dict["Subtype"]) {
	case Name("Image"), Name("PS"):
		return
	}
	ref, _ := entry.(Ref)
	if ref.Num > 0 && !sc.listed[ref.Num] {
		sc.listed[ref.Num] = true
		sc.out = append(sc.out, FormRef{Name: name, Page: sc.page, ref: ref})
	}
	sc.run(ref, stm, stm.dict["Resources"], streamID(ref, stm), chain)
}

// run follows one content stream under a chain. res is the resource
// dictionary the stream brings, if any, and owner names whoever holds
// it: the stream itself, or the font a glyph procedure belongs to.
func (sc *formScanner) run(ref Ref, stm *rawStream, res any, owner string, chain []formScope) {
	if sc.err != nil {
		return
	}
	chain, own := sc.enter(chain, res, owner)
	key := streamID(ref, stm) + "@" + chainKey(chain)
	if sc.ran[key] || !sc.spend() {
		return
	}
	sc.ran[key] = true
	names, err := sc.r.streamDoNames(ref, stm)
	if err != nil {
		if ref.Num > 0 {
			err = fmt.Errorf("content stream of object %d: %w", ref.Num, err)
		}
		sc.err = err
		return
	}
	for _, name := range names {
		sc.invoke(name, chain, own)
	}
}

// doNames is what one content stream invokes, kept so a stream shared
// by many pages is decoded once.
type doNames struct {
	names []Name
	err   error
}

// streamDoNames returns the names a content stream passes to Do.
func (r *Reader) streamDoNames(ref Ref, stm *rawStream) ([]Name, error) {
	if ref.Num > 0 {
		if e, ok := r.doNames[ref.Num]; ok {
			return e.names, e.err
		}
	}
	var e doNames
	content, err := r.decodeStream(stm.dict, stm.data)
	switch {
	case err != nil:
		e.err = err
	default:
		var cut bool
		if e.names, cut = contentDoNames(content); cut {
			e = doNames{err: errors.New("too large to read whole")}
		}
	}
	if ref.Num > 0 {
		if r.doNames == nil {
			r.doNames = map[int]doNames{}
		}
		r.doNames[ref.Num] = e
	}
	return e.names, e.err
}

// contentDoNames returns the names a content stream passes to Do, in
// order of first use. cut reports that the stream was too large to lex
// whole.
//
// The stream is read twice. The lexer reads it as tokens, which is
// right everywhere except inside an inline image: the image's bytes are
// lexed as if they were operators, and a stray "(" among them opens a
// string that swallows whatever follows — an invocation included. So
// the bytes are also searched for a name followed by Do, which finds
// that invocation and, now and then, one that is really text inside a
// string. A name too many is the mistake this list can afford.
func contentDoNames(content []byte) (names []Name, cut bool) {
	seen := map[Name]bool{}
	add := func(n Name) {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}

	tokens, cut := tokenizeContentLimited(content)
	var last any
	for _, tok := range tokens {
		op, isOp := tok.val.(opKeyword)
		if !isOp {
			last = tok.val
			continue
		}
		if op == "Do" {
			if n, ok := last.(Name); ok {
				add(n)
			}
		}
		last = nil
	}

	for i := 0; i+1 < len(content); i++ {
		if content[i] != 'D' || content[i+1] != 'o' {
			continue
		}
		if i+2 < len(content) && isRegular(content[i+2]) {
			continue // a longer word that begins with Do
		}
		end := i
		for end > 0 && isWS(content[end-1]) {
			end--
		}
		start := end
		for start > 0 && isRegular(content[start-1]) {
			start--
		}
		// A name needs its slash, and Do needs to stand apart from it:
		// "/FmDo" is one name, not an invocation.
		if end == i || start == 0 || content[start-1] != '/' {
			continue
		}
		p := &parser{data: content[start-1 : end]}
		if n, err := p.name(); err == nil {
			if name, ok := n.(Name); ok {
				add(name)
			}
		}
	}
	return names, cut
}
