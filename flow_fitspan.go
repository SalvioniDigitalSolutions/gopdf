package gopdf

import (
	"fmt"
	"strings"
)

// Fitting a token without moving the page.
//
// The flow engine can set a token to the width of the words it replaces,
// but it gets there by rebuilding the paragraph: every line is re-wrapped
// and re-emitted, and even when the words land back where they started
// the operators drawing them are new ones. Anything painted at a fixed
// place — a highlight rectangle, an underline, a rule, the dots of a
// dash leader — was positioned against the old operators and knows
// nothing of the rewrite.
//
// When the token can be made exactly as wide as what it replaces, none
// of that is necessary. A show-text operation is usually an array of
// strings with kerns between them, and the kerns are what justify a
// line; rewriting the operation throws them away. So the edit goes
// finer: the one string holding the token is replaced, in place, by the
// text before it, the token, a kern that makes up the difference, and
// the text after it. Every other string and every kern in the operation
// is left exactly as it was, and so is every other byte of the page.

// fitHit is one occurrence: where it sits in a run's text, and what
// takes its place.
type fitHit struct {
	at  [2]int
	new string
	// wantPts, when set, is the width the token must claim, in points,
	// because the occurrence ran past this run's own text into the next.
	wantPts float64
}

// fittedRun is one run's worth of planned edit.
type fittedRun struct {
	run  *TextRun
	edit splice
	text string
	hits []fitHit
}

// replacePageFitted applies every substitution the page needs at once,
// setting each token to exactly the width of the text it replaces and
// leaving the rest of the page alone.
//
// All of them together, because each run is rewritten from the bytes the
// document holds: doing one mapping and then another would build the
// second rewrite from the original operation and undo the first.
func (p *UpdatablePage) replacePageFitted(subs []Pseudonym, mode matchMode) (map[string]int, bool) {
	plan, counts, ok := p.planPageFitted(subs, mode)
	if !ok {
		return nil, false
	}
	for _, f := range plan {
		replaceSplice(f.run.target, f.edit)
		var ranges [][2]int
		grow := 0
		for _, h := range f.hits {
			ranges = append(ranges, h.at)
			grow += len(h.new) - (h.at[1] - h.at[0])
		}
		f.run.spaceAt = shiftHitSpaceAt(f.run.spaceAt, f.hits)
		f.run.Text = f.text
		f.run.replaced = true
		_ = ranges
		_ = grow
	}
	return counts, true
}

// planPageFitted works out every rewrite the page needs, or reports that
// one of them cannot be done in place.
func (p *UpdatablePage) planPageFitted(subs []Pseudonym, mode matchMode) (
	[]fittedRun, map[string]int, bool) {
	byRun := map[*TextRun][]fitHit{}
	counts := map[string]int{}
	seen := map[*TextRun]bool{}
	for _, sub := range subs {
		if sub.From == "" || sub.To == "" || !sub.FitWidth {
			continue
		}
		found := false
		for _, chain := range buildChains(p.runs) {
			for _, rg := range literalRanges(chain.text, sub.From, mode) {
				spread := chain.chainRanges(rg[0], rg[1])
				parts := orderedParts(chain, spread)
				if len(parts) == 0 {
					return nil, nil, false
				}
				// A name written across two operations — the producer
				// broke the line, or changed face part way — is still one
				// name. The first run takes the token, at the width the
				// whole occurrence covered on the page; the others give
				// up their share of it and keep their own width, so the
				// words after them do not move either.
				want := 0.0
				if len(parts) > 1 {
					want = spanPoints(parts)
					if want <= 0 {
						return nil, nil, false
					}
				}
				for i, pt := range parts {
					if overlapsAny(byRun[pt.run], pt.at) {
						continue
					}
					h := fitHit{at: pt.at}
					if i == 0 {
						h.new, h.wantPts = sub.To, want
					}
					byRun[pt.run] = append(byRun[pt.run], h)
					seen[pt.run] = true
				}
				found = true
			}
		}
		if found {
			counts[sub.From]++
		}
	}
	if len(byRun) == 0 {
		return nil, nil, false
	}
	fallback := fallbackFor(p)
	var plan []fittedRun
	for _, run := range p.runs {
		hits, ok := byRun[run]
		if !ok || len(hits) == 0 || run.font == nil {
			continue
		}
		sortHits(hits)
		edit, ok := fittedEdit(run, hits, fallback)
		if !ok {
			return nil, nil, false
		}
		plan = append(plan, fittedRun{
			run: run, edit: edit, text: fittedHitText(run, hits), hits: hits,
		})
	}
	if len(plan) != len(byRun) {
		return nil, nil, false
	}
	return plan, counts, true
}

// runPart is one run's share of an occurrence.
type runPart struct {
	run *TextRun
	at  [2]int
}

// orderedParts puts the runs an occurrence covers into reading order,
// which is the order the operators appear in.
func orderedParts(chain runChain, spread map[*TextRun][][2]int) []runPart {
	var out []runPart
	for _, run := range chain.runs {
		rs, ok := spread[run]
		if !ok {
			continue
		}
		for _, at := range rs {
			out = append(out, runPart{run: run, at: at})
		}
	}
	return out
}

// spanPoints is how far an occurrence reaches across the page, from
// where it starts in the first run to where it ends in the last.
func spanPoints(parts []runPart) float64 {
	first, last := parts[0], parts[len(parts)-1]
	fs := styleOf(first.run)
	ls := styleOf(last.run)
	head, ok1 := fs.advance(first.run.Text[:first.at[0]])
	tail, ok2 := ls.advance(last.run.Text[:last.at[1]])
	if !ok1 || !ok2 {
		return 0
	}
	return (last.run.X + tsToPoints(last.run, tail)) -
		(first.run.X + tsToPoints(first.run, head))
}

// tsToPoints converts a width in the run's text space to points on the
// page, which is what the text matrix scales it by.
func tsToPoints(run *TextRun, ts float64) float64 {
	if run.fontSizeRaw == 0 {
		return ts
	}
	return ts * run.FontSize / run.fontSizeRaw
}

// pointsToTS is the other direction.
func pointsToTS(run *TextRun, pts float64) float64 {
	if run.FontSize == 0 {
		return pts
	}
	return pts * run.fontSizeRaw / run.FontSize
}

// overlapsAny reports whether a range meets one already claimed, which
// is how a shorter mapping is kept out of what a longer one took.
func overlapsAny(hits []fitHit, at [2]int) bool {
	for _, h := range hits {
		if at[0] < h.at[1] && at[1] > h.at[0] {
			return true
		}
	}
	return false
}

func sortHits(hs []fitHit) {
	for i := 1; i < len(hs); i++ {
		for j := i; j > 0 && hs[j].at[0] < hs[j-1].at[0]; j-- {
			hs[j], hs[j-1] = hs[j-1], hs[j]
		}
	}
}

// fittedHitText is the run's text once every occurrence is swapped.
func fittedHitText(run *TextRun, hits []fitHit) string {
	var b strings.Builder
	at := 0
	for _, h := range hits {
		b.WriteString(run.Text[at:h.at[0]])
		b.WriteString(h.new)
		at = h.at[1]
	}
	b.WriteString(run.Text[at:])
	return b.String()
}

// shiftHitSpaceAt moves a run's inferred-space offsets to where they sit
// after the substitutions, dropping any that fell inside one.
func shiftHitSpaceAt(at []int, hits []fitHit) []int {
	if len(at) == 0 {
		return at
	}
	out := make([]int, 0, len(at))
	for _, off := range at {
		shift, dropped := 0, false
		for _, h := range hits {
			switch {
			case off >= h.at[1]:
				shift += len(h.new) - (h.at[1] - h.at[0])
			case off > h.at[0]:
				dropped = true
			}
		}
		if !dropped {
			out = append(out, off+shift)
		}
	}
	return out
}

// fittedEdits builds one splice per string the occurrences fall in.
//
// Only those strings are rewritten. A token needing a size of its own
// cannot be done this way — Tf is an operator and an array holds only
// strings and numbers — so a shrunk token is declined here and left to
// the paragraph engine, which can set it at any size it likes.
func fittedEdit(run *TextRun, hits []fitHit,
	fallback func(flowStyle) (flowStyle, bool)) (splice, bool) {
	st := styleOf(run)
	if st.font == nil || st.fontSizeRaw == 0 || len(run.pieces) == 0 {
		return splice{}, false
	}
	if run.op != "Tj" && run.op != "TJ" {
		return splice{}, false
	}
	body, ok := rebuildRun(run, st, hits, fallback)
	if !ok {
		return splice{}, false
	}
	return splice{start: run.start, end: run.end, repl: []byte(body)}, true
}

// rebuildRun writes the operation again with every occurrence replaced.
//
// Each element the occurrences do not touch is copied out of the content
// stream byte for byte — the kerns among them are what justify the line,
// and reproducing them from measurements would not give the same numbers
// back. Only the strings holding a token are new, and where a token
// needs a face or a size of its own the operation is split around it,
// since a Tf cannot be written among the elements of an array.
func rebuildRun(run *TextRun, st flowStyle, hits []fitHit,
	fallback func(flowStyle) (flowStyle, bool)) (string, bool) {
	data := run.target.content
	if run.start < 0 || run.end > len(data) || len(run.pieces) == 0 {
		return "", false
	}
	var b strings.Builder
	cursor := run.start
	done := map[int]bool{}
	// A lone Tj shows one string and takes no other operand, so its
	// replacement cannot be a sequence: the operation becomes an array
	// and a TJ, which can hold the token, the kern after it and the
	// words either side.
	if run.op == "Tj" {
		b.WriteString("[")
		cursor = run.pieces[0].start
	}

	// Which occurrence, if any, a stretch of the run's text falls in.
	inHit := func(from, to int) (int, bool) {
		for i, h := range hits {
			if from < h.at[1] && to > h.at[0] {
				return i, true
			}
		}
		return 0, false
	}

	for pi, pc := range run.pieces {
		hi, covered := inHit(pc.from, pc.to)
		if !covered {
			if run.op == "Tj" {
				if err := writeCodes(&b, st, run.Text[pc.from:pc.to]); err != nil {
					return "", false
				}
			} else {
				b.Write(data[cursor:pc.end]) // whatever precedes it, and it
			}
			cursor = pc.end
			continue
		}
		h := hits[hi]
		if !done[hi] {
			// The first string this occurrence touches. Everything
			// before it stands; then the text ahead of the token.
			if run.op != "Tj" {
				b.Write(data[cursor:pc.start])
			}
			if h.at[0] > pc.from {
				if err := writeCodes(&b, st, run.Text[pc.from:h.at[0]]); err != nil {
					return "", false
				}
			}
			if !writeFitted(&b, run, st, h, pi, hits, fallback) {
				return "", false
			}
			done[hi] = true
		}
		// Whatever of this string lies past the occurrence is drawn as
		// it was; what lay inside it is gone, kerns and all.
		if pc.to > h.at[1] {
			from := h.at[1]
			if from < pc.from {
				from = pc.from
			}
			if err := writeCodes(&b, st, run.Text[from:pc.to]); err != nil {
				return "", false
			}
		}
		cursor = pc.end
	}
	if run.op == "Tj" {
		b.WriteString(" ] TJ")
	} else {
		b.Write(data[cursor:run.end])
	}
	return b.String(), true
}

// writeFitted puts one token in, at the size that makes it as wide as
// what it replaces, with a kern for the remainder.
func writeFitted(b *strings.Builder, run *TextRun, st flowStyle, h fitHit,
	pi int, hits []fitHit, fallback func(flowStyle) (flowStyle, bool)) bool {
	tok, _, ok := tokenStyle(st, h.new, fallback)
	if !ok {
		return false
	}
	want, ok := hitWidth(run, st, h.at, pi)
	if !ok {
		return false
	}
	if h.wantPts > 0 {
		// The occurrence reached past this run: the token claims the
		// whole of what it covered, and the runs after it give up theirs.
		want = pointsToTS(run, h.wantPts)
	}
	size, pad, ok := fitTokenSize(tok, h.new, want)
	if !ok {
		return false
	}
	codes, err := tok.font.encodeText(h.new)
	if err != nil {
		return false
	}
	if size == st.fontSizeRaw && tok.fontName == st.fontName {
		fmt.Fprintf(b, " <%X>", codes)
		if pad != 0 {
			fmt.Fprintf(b, " %s", fl(pad))
		}
		return true
	}
	// A face or a size of its own: out of the array, set it, and back in.
	b.WriteString(" ] TJ")
	fmt.Fprintf(b, " /%s %s Tf [ <%X>", tok.fontName, fl(size), codes)
	if pad != 0 {
		fmt.Fprintf(b, " %s", fl(pad))
	}
	fmt.Fprintf(b, " ] TJ /%s %s Tf [", st.fontName, fl(st.fontSizeRaw))
	return true
}

// hitWidth is how wide an occurrence is on the page: its glyphs, and the
// kerns between the strings it spans. On a justified line those kerns
// are most of the width between one word and the next.
func hitWidth(run *TextRun, st flowStyle, at [2]int, from int) (float64, bool) {
	w, ok := st.advance(run.Text[at[0]:at[1]])
	if !ok {
		return 0, false
	}
	data := run.target.content
	scale := st.fontSizeRaw * st.horizScale
	for i := from; i+1 < len(run.pieces); i++ {
		a, next := run.pieces[i], run.pieces[i+1]
		if next.from >= at[1] {
			break
		}
		if a.end > len(data) || next.start > len(data) || a.end > next.start {
			break
		}
		for _, t := range tokenizeContent(data[a.end:next.start]) {
			if f, ok := toFloat(t.val); ok {
				w += -f / 1000 * scale
			}
		}
	}
	return w, true
}

// fitTokenSize returns the size to set text at so it occupies want, and
// the kern that takes up whatever is left over.
//
// A token wider than what it replaces is set smaller, down to the floor.
// A narrower one keeps its size and is padded: shrinking is a concession
// to necessity, and enlarging text nobody asked to enlarge is not.
func fitTokenSize(st flowStyle, text string, want float64) (size, pad float64, ok bool) {
	size = st.fontSizeRaw
	if s, shrunk := fitSize(st, text, want); shrunk {
		size = s
	}
	set := st
	set.fontSizeRaw = size
	w, ok := set.advance(text)
	if !ok {
		return 0, 0, false
	}
	if w > want+0.001 {
		return 0, 0, false // even the floor leaves it too wide
	}
	if scale := size * st.horizScale; scale != 0 {
		pad = -(want - w) * 1000 / scale
	}
	if pad > -0.001 && pad < 0.001 {
		pad = 0
	}
	return size, pad, true
}

// tokenStyle is the style the token is set in: the run's own, or a
// stand-in where the document's subset has no glyph for it. Only the
// token moves; the run's own words keep the face they had.
func tokenStyle(st flowStyle, new string,
	fallback func(flowStyle) (flowStyle, bool)) (flowStyle, bool, bool) {
	if _, err := st.font.encodeText(new); err == nil {
		return st, false, true
	}
	if fallback == nil {
		return st, false, false
	}
	alt, made := fallback(st)
	if !made {
		return st, false, false
	}
	if _, err := alt.font.encodeText(new); err != nil {
		return st, false, false
	}
	return alt, true, true
}

// writeCodes appends a string as an array element, or nothing when there
// is nothing to draw.
func writeCodes(b *strings.Builder, st flowStyle, text string) error {
	if text == "" {
		return nil
	}
	codes, err := st.font.encodeText(text)
	if err != nil {
		return err
	}
	fmt.Fprintf(b, " <%X>", codes)
	return nil
}

// replaceSplice records an edit for a byte span, dropping any edit
// already recorded for the same one. A second substitution can land in a
// string the first already rewrote — "da Lugano in 6600 Locarno" is one
// string and two names — and the later splice is built from the text as
// it then stands, so it supersedes rather than fights it.
func replaceSplice(t *editTarget, sp splice) {
	for i := range t.splices {
		if t.splices[i].start == sp.start && t.splices[i].end == sp.end {
			t.splices[i] = sp
			return
		}
	}
	t.splices = append(t.splices, sp)
}
