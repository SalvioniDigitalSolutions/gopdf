package gopdf

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
)

// FitMode controls how a text replacement of a different width is fitted
// back into the original layout.
type FitMode int

const (
	// FitAdvance keeps everything that follows the replaced text exactly
	// where it was, by compensating for the width difference. The
	// replacement itself may be shorter or longer than the text it
	// replaced. This is the default and the safest choice.
	FitAdvance FitMode = iota
	// FitScale additionally scales the replacement horizontally so it
	// occupies precisely the original width. Use it when the replacement
	// would otherwise overlap adjacent content.
	FitScale
	// FitNone writes the replacement at its natural width and lets the
	// rest of the line shift, as a word processor would.
	FitNone
)

// TextRun is one run of text in an existing page, as the page's content
// stream draws it: a single show-text operation with its position, font
// and size. Runs are the unit of editing.
type TextRun struct {
	// Text is the run's decoded text.
	Text string
	// X and Y are the position of the run's baseline origin in points,
	// measured from the top-left of the page like the rest of this
	// package's coordinates.
	X, Y float64
	// FontSize is the effective size in points, after transforms.
	FontSize float64
	// FontName is the run's font resource name in the source file.
	FontName string
	// Width is the run's advance width in points.
	Width float64

	target      *editTarget
	start, end  int // byte span of the whole operation in the content stream
	font        *fontInfo
	tm          matrix  // text matrix at the start of the run
	advance     float64 // original advance in 1/1000 em
	charSpacing float64
	wordSpacing float64
	horizScale  float64 // Tz as a fraction (1.0 = 100%)
	fontSizeRaw float64 // the Tf operand, before transforms
	fillOp      string  // the fill-colour operation in force, verbatim
	replaced    bool

	// codes holds the character codes the run originally drew, and
	// codeText how many bytes of Text each of them produced. Together
	// they let a range of the text be mapped back to the codes behind
	// it, so characters can be removed without re-encoding the rest.
	codes    []byte
	codeStep int
	codeText []int

	// style carries a pending restyle, applied when the run is written.
	style *TextStyle
	// owner registers new fonts when a restyle changes the typeface.
	owner styleHost
}

// editTarget is one editable content stream: either the page's own, or
// that of a form XObject the page draws.
type editTarget struct {
	content   []byte
	resources any
	stream    *rawStream // nil for the page's own content
	splices   []splice
}

type splice struct {
	start, end int
	repl       []byte
}

// EditablePage is a page imported from an existing document with its
// content left editable. It embeds *Page, so the whole drawing API is
// available for adding content on top; the text-editing methods rewrite
// the content the source file already had.
//
// Changes are materialized when the document is written.
type EditablePage struct {
	*Page

	doc           *Document
	r             *Reader
	targets       []*editTarget
	runs          []*TextRun
	fit           FitMode
	maxExtraLines int
	apCount       int // appearance streams placed on this page
	flushed       bool
}

// rawOp appends a raw content-stream operator to the page. It is used for
// content that references resources registered directly in the page's own
// resource dictionary.
func (e *EditablePage) rawOp(s string) {
	e.Page.op("%s", s)
}

// SetFitMode selects how replacements of a different width are fitted.
func (e *EditablePage) SetFitMode(m FitMode) { e.fit = m }

// Runs returns every text run on the page, in content order, including
// runs inside form XObjects the page draws.
func (e *EditablePage) Runs() []*TextRun {
	return e.runs
}

// ExtractText returns the page's current text, with line breaks inferred
// from the runs' positions. It reflects any replacements made so far.
//
// This is deliberately not called Text: the embedded Page's Text method
// draws new text on top of the page.
func (e *EditablePage) ExtractText() string {
	var sb strings.Builder
	lastY := math.Inf(1)
	for i, run := range e.runs {
		if i > 0 && math.Abs(run.Y-lastY) > 0.5 {
			sb.WriteByte('\n')
		}
		sb.WriteString(run.Text)
		lastY = run.Y
	}
	return sb.String()
}

// EditPage imports a page from an existing document with its content
// stream left editable, preserving the page's own resources, media box
// and rotation exactly.
//
// Unlike ImportPage, which places the source page as an opaque form
// XObject, EditPage keeps the original operators so their text can be
// rewritten in place.
func (d *Document) EditPage(r *Reader, index int) (*EditablePage, error) {
	if index < 0 || index >= len(r.pages) {
		return nil, fmt.Errorf("gopdf: page %d out of range (document has %d pages)", index, r.NumPages())
	}
	pi := r.pages[index]
	content, err := r.pageContent(pi.dict)
	if err != nil {
		return nil, fmt.Errorf("gopdf: editing page %d: %w", index, err)
	}
	im := &importer{r: r, d: d, memo: d.importMemo(r)}

	// Resources are copied as a direct dictionary (with its category
	// sub-dictionaries also direct) so new resources can be merged in.
	resources, err := im.copyResourcesFlat(pi.resources)
	if err != nil {
		return nil, fmt.Errorf("gopdf: editing page %d: %w", index, err)
	}

	w := pi.mediaBox[2] - pi.mediaBox[0]
	h := pi.mediaBox[3] - pi.mediaBox[1]
	p := d.AddPageSize(PageSize{W: w, H: h})
	p.rotate = pi.rotate
	p.mediaBox = &[4]float64{pi.mediaBox[0], pi.mediaBox[1], pi.mediaBox[2], pi.mediaBox[3]}
	p.ownResources = resources
	p.resPrefix = freeResourcePrefix(resources)

	e := &EditablePage{Page: p, doc: d, r: r, fit: FitAdvance}
	pageTarget := &editTarget{content: content, resources: pi.resources}

	// Collect the page's own runs, descending into form XObjects, whose
	// copied streams stay editable too.
	sc := &runScanner{
		r:         r,
		mediaBox:  p.mediaBox,
		targets:   []*editTarget{pageTarget},
		seen:      make(map[Ref]bool),
		infos:     make(map[any]*fontInfo),
		adoptForm: im.copiedStream,
	}
	sc.scan(pageTarget, pi.resources, identityMatrix, 0)
	e.targets, e.runs = sc.targets, sc.runs
	for _, run := range e.runs {
		run.owner = e
	}

	d.editables = append(d.editables, e)
	return e, nil
}

// copyResourcesFlat deep-copies a resource dictionary, keeping the top
// level and its category sub-dictionaries direct so entries can be merged.
func (im *importer) copyResourcesFlat(res any) (Dict, error) {
	src, ok := im.r.resolve(res).(Dict)
	if !ok {
		return Dict{}, nil
	}
	out := make(Dict, len(src))
	for k, v := range src {
		switch k {
		case "Font", "XObject", "ExtGState", "Shading", "Pattern", "ColorSpace":
			sub, ok := im.r.resolve(v).(Dict)
			if !ok {
				break
			}
			cp := make(Dict, len(sub))
			for sk, sv := range sub {
				c, err := im.copy(sv, 0)
				if err != nil {
					return nil, err
				}
				cp[sk] = c
			}
			out[k] = cp
			continue
		}
		c, err := im.copy(v, 0)
		if err != nil {
			return nil, err
		}
		out[k] = c
	}
	return out, nil
}

// freeResourcePrefix picks a prefix for this library's own resource names
// that cannot collide with any name the source page already uses.
func freeResourcePrefix(res Dict) string {
	used := make(map[string]bool)
	for _, cat := range []Name{"Font", "XObject", "ExtGState"} {
		if sub, ok := res[cat].(Dict); ok {
			for k := range sub {
				used[string(k)] = true
			}
		}
	}
	for _, prefix := range []string{"Gp", "GpX", "GpXX", "GpXXX"} {
		clash := false
		for name := range used {
			if strings.HasPrefix(name, prefix) {
				clash = true
				break
			}
		}
		if !clash {
			return prefix
		}
	}
	return "GopdfGenerated"
}

// --- scanning runs out of content streams ---

// runScanner walks content streams and collects their text runs. It is
// shared by EditPage, which copies content into a new document, and by
// Updater, which rewrites objects in the original file; adoptForm is the
// only difference between them.
type runScanner struct {
	r        *Reader
	mediaBox *[4]float64
	runs     []*TextRun
	targets  []*editTarget
	seen     map[Ref]bool
	// infos caches font metadata across every content stream of the page,
	// so the record of which glyphs the document actually draws is
	// complete before any replacement is encoded.
	infos map[any]*fontInfo
	// adoptForm claims a form XObject for editing and returns the stream
	// whose data should carry the edits back, or nil to scan read-only.
	adoptForm func(entry any) *rawStream
}

// contentToken is a lexed content-stream token with its byte span.
type contentToken struct {
	val        any
	start, end int
}

func tokenizeContent(data []byte) []contentToken {
	var out []contentToken
	p := &parser{data: data}
	for {
		p.skipWS()
		start := p.pos
		v, err := p.next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			p.pos = start + 1
			continue
		}
		out = append(out, contentToken{val: v, start: start, end: p.pos})
		if len(out) > 1<<21 {
			return out
		}
	}
}

// textState is the subset of the PDF text state that affects a run's
// position and width.
type textState struct {
	fontName    Name
	fontSize    float64
	charSpacing float64
	wordSpacing float64
	horizScale  float64
	rise        float64
	leading     float64
	// fillOp is the source text of the operation that last set the fill
	// colour, so a restyled run can put it back exactly — whatever colour
	// space it used.
	fillOp string
}

func (sc *runScanner) scan(target *editTarget, resources any, base matrix, depth int) {
	if depth > maxFormDepth {
		return
	}
	r := sc.r
	fonts := newFontDecoders(r, resources)
	fontInfoFor := func(name Name) *fontInfo {
		// Identify a font by its indirect reference where possible, so
		// the same font reached through different resource dictionaries
		// shares one record.
		entry := fonts.fonts[name]
		key := any(entry)
		if _, isRef := entry.(Ref); !isRef {
			key = fmt.Sprintf("%p/%s", target, name)
		}
		if fi, ok := sc.infos[key]; ok {
			return fi
		}
		dict, _ := r.resolve(entry).(Dict)
		fi := newFontInfo(r, name, dict, fonts.get(name))
		sc.infos[key] = fi
		return fi
	}

	ctm := base
	var ctmStack []matrix
	tm, tlm := identityMatrix, identityMatrix
	st := textState{horizScale: 1}
	var stStack []textState

	tokens := tokenizeContent(target.content)
	var operands []contentToken
	opStart := 0

	translateLine := func(tx, ty float64) {
		tlm = matrix{tlm[0], tlm[1], tlm[2], tlm[3],
			tx*tlm[0] + ty*tlm[2] + tlm[4], tx*tlm[1] + ty*tlm[3] + tlm[5]}
		tm = tlm
	}
	num := func(i int) float64 {
		if i < len(operands) {
			if f, ok := toFloat(operands[i].val); ok {
				return f
			}
		}
		return 0
	}

	// record builds a TextRun for a show-text operation and advances the
	// text matrix by the run's width.
	record := func(pieces []any, end int) {
		fi := fontInfoFor(st.fontName)
		var text strings.Builder
		var advanceEm float64
		var encoded []byte
		var codeText []int
		for _, piece := range pieces {
			switch v := piece.(type) {
			case String:
				part, spans := fi.decoder.decodeSpans(v)
				text.WriteString(part)
				codeText = append(codeText, spans...)
				encoded = append(encoded, v...)
				fi.observe(v)
				advanceEm += fi.stringWidth(v, st.charSpacing, st.wordSpacing, st.fontSize)
			default:
				if f, ok := toFloat(piece); ok {
					advanceEm -= f
				}
			}
		}
		full := tm.mul(ctm)
		x, y := full.apply(0, 0)
		// Report positions in this package's top-left origin, matching
		// the coordinates the drawing API uses.
		box := sc.mediaBox
		if box != nil {
			x -= box[0]
			y = box[3] - y
		}
		// The effective size is the text-space scale applied to the font
		// size by the text and current transformation matrices.
		scale := math.Hypot(full[2], full[3])
		widthText := advanceEm / 1000 * st.fontSize * st.horizScale

		run := &TextRun{
			Text:        text.String(),
			X:           x,
			Y:           y,
			tm:          tm,
			FontSize:    st.fontSize * scale,
			FontName:    string(st.fontName),
			Width:       widthText * math.Hypot(full[0], full[1]),
			codes:       encoded,
			codeStep:    codeStepFor(fi),
			codeText:    codeText,
			target:      target,
			start:       opStart,
			end:         end,
			font:        fi,
			advance:     advanceEm,
			charSpacing: st.charSpacing,
			wordSpacing: st.wordSpacing,
			horizScale:  st.horizScale,
			fontSizeRaw: st.fontSize,
			fillOp:      st.fillOp,
		}
		if run.Text != "" {
			sc.runs = append(sc.runs, run)
		}
		tm = matrix{1, 0, 0, 1, widthText, 0}.mul(tm)
	}

	for _, tok := range tokens {
		op, isOp := tok.val.(opKeyword)
		if !isOp {
			if len(operands) == 0 {
				opStart = tok.start
			}
			if len(operands) < 32 {
				operands = append(operands, tok)
			}
			continue
		}
		if len(operands) == 0 {
			opStart = tok.start
		}
		switch string(op) {
		case "q":
			if len(ctmStack) < 64 {
				ctmStack = append(ctmStack, ctm)
				stStack = append(stStack, st)
			}
		case "Q":
			if n := len(ctmStack); n > 0 {
				ctm, st = ctmStack[n-1], stStack[n-1]
				ctmStack, stStack = ctmStack[:n-1], stStack[:n-1]
			}
		case "cm":
			if len(operands) >= 6 {
				var m matrix
				for i := 0; i < 6; i++ {
					m[i] = num(i)
				}
				ctm = m.mul(ctm)
			}
		case "Do":
			if len(operands) >= 1 {
				if n, ok := operands[0].val.(Name); ok {
					sc.scanForm(n, resources, ctm, depth)
				}
			}
		case "BT":
			tm, tlm = identityMatrix, identityMatrix
		case "Tm":
			if len(operands) >= 6 {
				for i := 0; i < 6; i++ {
					tlm[i] = num(i)
				}
				tm = tlm
			}
		case "Td":
			translateLine(num(0), num(1))
		case "TD":
			st.leading = -num(1)
			translateLine(num(0), num(1))
		case "TL":
			st.leading = num(0)
		case "T*":
			translateLine(0, -st.leading)
		case "g", "rg", "k", "sc", "scn":
			// Keep the operation's own source text; restoring it verbatim
			// works for any colour space.
			if len(operands) > 0 {
				st.fillOp = string(target.content[operands[0].start:tok.end])
			}
		case "Tc":
			st.charSpacing = num(0)
		case "Tw":
			st.wordSpacing = num(0)
		case "Tz":
			st.horizScale = num(0) / 100
		case "Ts":
			st.rise = num(0)
		case "Tf":
			if len(operands) >= 2 {
				if n, ok := operands[0].val.(Name); ok {
					st.fontName = n
				}
				st.fontSize = num(1)
			}
		case "Tj":
			if len(operands) >= 1 {
				if s, ok := operands[0].val.(String); ok {
					record([]any{s}, tok.end)
				}
			}
		case "'":
			translateLine(0, -st.leading)
			if len(operands) >= 1 {
				if s, ok := operands[0].val.(String); ok {
					record([]any{s}, tok.end)
				}
			}
		case "\"":
			if len(operands) >= 3 {
				st.wordSpacing = num(0)
				st.charSpacing = num(1)
				translateLine(0, -st.leading)
				if s, ok := operands[2].val.(String); ok {
					record([]any{s}, tok.end)
				}
			}
		case "TJ":
			if len(operands) >= 1 {
				if arr, ok := operands[0].val.(Array); ok {
					record([]any(arr), tok.end)
				}
			}
		}
		operands = operands[:0]
	}
}

// scanForm descends into a form XObject, making its copied content stream
// editable as well.
func (sc *runScanner) scanForm(name Name, resources any, ctm matrix, depth int) {
	r := sc.r
	res, ok := r.resolve(resources).(Dict)
	if !ok {
		return
	}
	xobjects, ok := r.resolve(res["XObject"]).(Dict)
	if !ok {
		return
	}
	entry := xobjects[name]
	if ref, isRef := entry.(Ref); isRef {
		if sc.seen[ref] {
			return
		}
		sc.seen[ref] = true
		defer delete(sc.seen, ref)
	}
	stm, ok := r.resolve(entry).(*rawStream)
	if !ok || r.resolve(stm.dict["Subtype"]) != Name("Form") {
		return
	}
	content, err := r.decodeStream(stm.dict, stm.data)
	if err != nil {
		return
	}
	// Claim a writable stream for this form so edits reach the output.
	writable := sc.adoptForm(entry)
	if writable == nil {
		return
	}
	inner := ctm
	if mArr, ok := r.resolve(stm.dict["Matrix"]).(Array); ok && len(mArr) == 6 {
		var m matrix
		valid := true
		for i, e := range mArr {
			f, ok := toFloat(r.resolve(e))
			if !ok {
				valid = false
				break
			}
			m[i] = f
		}
		if valid {
			inner = m.mul(ctm)
		}
	}
	formRes := stm.dict["Resources"]
	if formRes == nil {
		formRes = resources
	}
	target := &editTarget{content: content, resources: formRes, stream: writable}
	sc.targets = append(sc.targets, target)
	sc.scan(target, formRes, inner, depth+1)
}

// copiedStream returns a document's copy of a source stream, creating it
// if the resource graph has not been copied yet. It is the adoptForm
// implementation used when importing into a new document.
func (im *importer) copiedStream(entry any) *rawStream {
	cp, err := im.copy(entry, 0)
	if err != nil {
		return nil
	}
	if rr, ok := cp.(rawRef); ok {
		stm, _ := im.d.raw[rr].(*rawStream)
		return stm
	}
	stm, _ := cp.(*rawStream)
	return stm
}

// --- replacement ---

// ReplaceText replaces every occurrence of old with new across the page,
// rewriting the original content stream in place. It returns the number of
// occurrences replaced.
//
// Matching happens within a single show-text operation: text that a
// generator split across separate operations is matched per run. Use Runs
// to inspect exactly how the page is laid out.
//
// The replacement is encoded with the run's own font, so it renders
// identically to the surrounding text. If that font has no glyph for one
// of the replacement's characters, ReplaceText reports an error and
// changes nothing.
func (e *EditablePage) ReplaceText(old, new string) (int, error) {
	if old == "" {
		return 0, fmt.Errorf("gopdf: ReplaceText called with empty search text")
	}
	return e.ReplaceFunc(func(run *TextRun) (string, bool) {
		if !strings.Contains(run.Text, old) {
			return "", false
		}
		return strings.ReplaceAll(run.Text, old, new), true
	})
}

// ReplaceFunc rewrites runs for which fn returns true, replacing the run's
// text with the returned string. It returns the number of runs rewritten;
// on error nothing is changed.
func (e *EditablePage) ReplaceFunc(fn func(*TextRun) (string, bool)) (int, error) {
	return replaceRuns(e.runs, fn, e.fit)
}

// replaceRuns rewrites the runs fn selects. Every replacement is encoded
// before any is applied, so a font that cannot represent one of them
// leaves the document untouched.
func replaceRuns(runs []*TextRun, fn func(*TextRun) (string, bool), fit FitMode) (int, error) {
	type pending struct {
		run  *TextRun
		text string
	}
	var todo []pending
	for _, run := range runs {
		if run.replaced {
			continue
		}
		if text, ok := fn(run); ok {
			todo = append(todo, pending{run, text})
		}
	}
	encoded := make([][]byte, len(todo))
	for i, p := range todo {
		codes, err := p.run.font.encodeText(p.text)
		if err != nil {
			return 0, err
		}
		encoded[i] = codes
	}
	for i, p := range todo {
		p.run.applySplice(encoded[i], fit)
		p.run.Text = p.text
		p.run.replaced = true
	}
	return len(todo), nil
}

// SetText rewrites a single run's text. It reports an error, and changes
// nothing, if the run's font cannot represent the new text.
func (run *TextRun) SetText(s string, mode FitMode) error {
	codes, err := run.font.encodeText(s)
	if err != nil {
		return err
	}
	run.applySplice(codes, mode)
	run.Text = s
	run.replaced = true
	return nil
}

// applySplice records the content-stream edit that draws codes in place of
// the run's original operation.
func (run *TextRun) applySplice(codes []byte, mode FitMode) {
	newEm := run.font.stringWidth(codes, run.charSpacing, run.wordSpacing, run.fontSizeRaw)

	var b strings.Builder
	switch mode {
	case FitScale:
		// Scale horizontally so the replacement occupies the original
		// width exactly, then restore the previous scaling.
		scale := 100 * run.horizScale
		if newEm != 0 {
			scale = run.advance / newEm * 100 * run.horizScale
		}
		fmt.Fprintf(&b, "%s Tz <%X> Tj %s Tz", fl(scale), codes, fl(100*run.horizScale))
	case FitNone:
		fmt.Fprintf(&b, "<%X> Tj", codes)
	default: // FitAdvance
		// A trailing TJ adjustment absorbs the width difference, so the
		// next operation starts exactly where it did before.
		delta := newEm - run.advance
		if math.Abs(delta) < 0.001 {
			fmt.Fprintf(&b, "<%X> Tj", codes)
		} else {
			fmt.Fprintf(&b, "[<%X> %s] TJ", codes, fl(delta))
		}
	}
	setup, restore := run.styleOps()
	run.target.splices = append(run.target.splices, splice{
		start: run.start, end: run.end,
		repl: []byte(setup + b.String() + restore),
	})
}

// flush materializes every recorded edit into the page's content and into
// the copied form XObject streams. It is idempotent.
func (e *EditablePage) flush() error {
	for _, t := range e.targets {
		if len(t.splices) > 0 {
			content, err := applySplices(t.content, t.splices)
			if err != nil {
				return err
			}
			t.content = content
			t.splices = nil
			if t.stream != nil {
				// Rewrite the copied XObject with the edited content.
				data, err := flateCompress(content)
				if err != nil {
					return err
				}
				t.stream.data = data
				t.stream.dict["Filter"] = Name("FlateDecode")
				delete(t.stream.dict, "DecodeParms")
			}
		}
	}
	// The page's own stream becomes the page content, ahead of anything
	// drawn on top through the Page API.
	e.Page.prelude = e.targets[0].content
	e.flushed = true
	return nil
}

// applySplices rewrites data with the given non-overlapping replacements.
func applySplices(data []byte, splices []splice) ([]byte, error) {
	sorted := append([]splice(nil), splices...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].start < sorted[j].start })
	for i := 1; i < len(sorted); i++ {
		if sorted[i].start < sorted[i-1].end {
			return nil, fmt.Errorf("gopdf: overlapping text edits at byte %d", sorted[i].start)
		}
	}
	out := make([]byte, 0, len(data)+64*len(sorted))
	prev := 0
	for _, s := range sorted {
		if s.start < prev || s.end > len(data) {
			return nil, fmt.Errorf("gopdf: text edit out of range")
		}
		out = append(out, data[prev:s.start]...)
		out = append(out, s.repl...)
		prev = s.end
	}
	return append(out, data[prev:]...), nil
}

// codeStepFor returns how many bytes one character code occupies.
func codeStepFor(fi *fontInfo) int {
	if fi != nil && fi.cid {
		return 2
	}
	return 1
}
