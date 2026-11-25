package gopdf

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Flowing a paragraph whose text changes length.
//
// The reflow in reflow.go re-wraps a paragraph within the lines it
// already occupies, and requires every line to be one operation in one
// font. That rules out the ordinary case of a bold word inside a
// sentence, and it refuses any replacement that needs more room.
//
// A Flow drops both restrictions. A paragraph is modelled as a run of
// styled spans rather than a run of lines, so a replacement inherits the
// styling of the text it replaces and everything around it keeps its own.
// Re-wrapping measures each span in its own font, and the paragraph may
// end up with more or fewer lines than it started with.

// FlowSpan is a piece of a paragraph that shares one style.
type FlowSpan struct {
	// Text is the span's text.
	Text string
	// FontName is the font resource name in the source file, and
	// FontSize the effective size in points.
	FontName string
	FontSize float64

	style flowStyle
	// inserted marks text this edit introduced, as opposed to text the
	// document already had. Only inserted text may be moved to a
	// fallback font: restyling what the document itself drew would
	// change a page the caller did not ask to change.
	inserted bool
	// gap, on a span with no text, is a horizontal move in text space
	// standing in for a space the font has no glyph to draw. Some
	// producers set every word separately and make the gaps by moving
	// the pen, and their subset fonts carry no space at all.
	gap float64
	// fitted marks inserted text whose size this package chose, to make
	// it fit, rather than took from the document. The distinction
	// matters to the space in front of it, which is the document's and
	// keeps the document's size.
	fitted bool
	// lineBreak marks the space this package puts between two lines of
	// the same paragraph. It is not a character the document drew, and a
	// word hyphenated across the break is joined at it.
	lineBreak bool
}

// flowStyle is everything needed to draw a span the way the source did.
type flowStyle struct {
	fontName    Name
	fontSizeRaw float64
	fillOp      string
	charSpacing float64
	wordSpacing float64
	horizScale  float64
	font        *fontInfo
}

func (s flowStyle) sameAs(o flowStyle) bool {
	return s.fontName == o.fontName && s.fontSizeRaw == o.fontSizeRaw &&
		s.fillOp == o.fillOp && s.charSpacing == o.charSpacing &&
		s.wordSpacing == o.wordSpacing && s.horizScale == o.horizScale &&
		s.font == o.font
}

// advance returns the text-space width of a string in this style, in
// points, or false if the style's font cannot represent it.
func (s flowStyle) advance(text string) (float64, bool) {
	if text == "" {
		return 0, true
	}
	if s.font == nil || s.fontSizeRaw == 0 {
		return 0, false
	}
	codes, err := s.font.encodeText(text)
	if err != nil {
		return 0, false
	}
	em := s.font.stringWidth(codes, s.charSpacing, s.wordSpacing, s.fontSizeRaw)
	return em / 1000 * s.fontSizeRaw * s.horizScale, true
}

// gapWidth is how far apart two words should be in this style when the
// font has no space glyph to put between them.
//
// A font that draws no spaces may still declare a width for one, since
// the width array covers a range of codes rather than the glyphs that
// happen to be present, and that number is the producer's own answer.
// Where even that is missing there is nothing to go on but the
// convention that a space is about a quarter of the size.
func (s flowStyle) gapWidth() float64 {
	if s.font == nil || s.fontSizeRaw == 0 {
		return 0
	}
	if w, ok := s.font.spaceWidth1000(); ok {
		return w / 1000 * s.fontSizeRaw * s.horizScale
	}
	return s.fontSizeRaw * 0.25 * s.horizScale
}

// gapAdjust returns the TJ number that moves the pen forward by width in
// the style currently in force. The numbers in a TJ array are thousandths
// of a unit of text space and are *subtracted* from the displacement, so
// moving forward takes a negative one.
func gapAdjust(s flowStyle, width float64) float64 {
	scale := s.fontSizeRaw * s.horizScale
	if scale == 0 || width == 0 {
		return 0
	}
	return -width * 1000 / scale
}

// spaceText returns the character to set between words in this style. An
// ordinary space is preferred, but a document that writes every gap as a
// non-breaking space embeds a subset with no U+0020 in it, and inserting
// one there would encode to nothing and run the words together.
func (s flowStyle) spaceText() string {
	if s.font == nil {
		return " "
	}
	for _, c := range []string{" ", "\u00A0"} {
		if _, err := s.font.encodeText(c); err == nil {
			return c
		}
	}
	return ""
}

// Flow is a paragraph whose text can be replaced at any length. Each part
// keeps the styling it had, and the paragraph is re-wrapped to its own
// column width, growing or shrinking by whole lines as it needs to.
type Flow struct {
	// X and Y position the first line's baseline, from the top-left of
	// the page.
	X, Y float64
	// Width is the column width in points.
	Width float64
	// LineHeight is the baseline-to-baseline distance in points.
	LineHeight float64

	spans []FlowSpan
	lines []flowLine
	// widthTS is the column width in text space, the unit wrapping uses.
	widthTS float64
	// leadingTS is the baseline step in text space.
	leadingTS float64
	// maxExtra caps how many lines the paragraph may grow by. A negative
	// value means no cap.
	maxExtra int
	onChange func()
	// delta records how many lines the last rewrite added or removed.
	delta int

	// curLines is how many lines the paragraph occupies now, which after
	// a rewrite is no longer how many it started with.
	curLines int
	// moved is set once the paragraph's own operators were rewritten to
	// move it (shiftOriginals).
	moved bool
	// shiftTS is how far the paragraph has been moved down the page, in
	// text space, accumulated over every edit so far.
	shiftTS float64
	// lastPlan is the rewrite in force, kept so the paragraph can be
	// re-placed if something above it later changes height.
	lastPlan *flowPlan
	// fallback supplies a style that can set text the document's own
	// font cannot. It is only ever applied to inserted text.
	fallback func(flowStyle) (flowStyle, bool)
	// mode is how strictly a literal has to sit in the text.
	mode matchMode
	// shrink lets an inserted token be set smaller to fit, down to
	// shrinkFloor points.
	shrink      bool
	shrinkFloor float64
	// fitWidth makes a replacement take the width of what it replaced,
	// by being set smaller, so the paragraph's line breaks do not move.
	fitWidth bool
	// fitFloor is how far such a replacement may be shrunk, as a
	// fraction of the run's size. Zero means the default.
	fitFloor float64
}

// joinedText returns the paragraph as a person reads it, with words
// rejoined across the line breaks that hyphenated them.
func (f *Flow) joinedText() string { return readParagraph(f.spans).text }

// holds reports whether old stands in the paragraph, read with its
// divided words joined or as it is drawn (replaceInSpans).
func (f *Flow) holds(old string) bool {
	r := readParagraph(f.spans)
	return containsBounded(r.text, old, f.mode) || (r.flat != r.text && containsBounded(r.flat, old, f.mode))
}

// flowLine is one baseline's worth of runs.
type flowLine struct {
	runs []*TextRun
	y    float64
	tm   matrix
}

// Text returns the paragraph's text, its lines joined with single spaces.
func (f *Flow) Text() string {
	var b strings.Builder
	for _, s := range f.spans {
		b.WriteString(s.Text)
	}
	return b.String()
}

// Spans returns the paragraph's styled pieces, in reading order.
func (f *Flow) Spans() []FlowSpan {
	return append([]FlowSpan(nil), f.spans...)
}

// LineCount returns how many lines the paragraph occupies.
func (f *Flow) LineCount() int { return len(f.lines) }

// LineDelta returns how many lines the last rewrite added, or removed if
// negative. It is zero until the paragraph is rewritten.
func (f *Flow) LineDelta() int { return f.delta }

// SetMaxExtraLines caps how many lines this paragraph may grow by. The
// default for a flow is no cap: pass a value to keep a paragraph from
// running into whatever follows it.
func (f *Flow) SetMaxExtraLines(n int) { f.maxExtra = n }

// Replace substitutes every occurrence of old within the paragraph and
// re-wraps it. The replacement takes the styling of the text it replaces,
// so swapping a word inside a bold phrase leaves it bold, and the rest of
// the paragraph is untouched. It reports how many occurrences changed.
func (f *Flow) Replace(old, new string) (int, error) {
	if old == "" {
		return 0, fmt.Errorf("gopdf: Flow.Replace called with empty search text")
	}
	if !f.holds(old) {
		return 0, nil
	}
	spans, n := replaceInSpans(f.spans, old, new, f.mode, f.fitWidth, f.fitFloor, "", false, f.fallback)
	if n == 0 {
		return 0, nil
	}
	if err := f.SetSpans(spans); err != nil {
		return 0, err
	}
	return n, nil
}

// SetText replaces the whole paragraph with plain text in the styling of
// its first span. Use SetSpans, or Replace, to keep a mixed paragraph
// mixed.
func (f *Flow) SetText(s string) error {
	if len(f.spans) == 0 {
		return fmt.Errorf("gopdf: the paragraph has no text to replace")
	}
	return f.SetSpans([]FlowSpan{{Text: s, style: f.spans[0].style}})
}

// SetSpans replaces the paragraph with the given styled spans. A span
// created by Spans carries its original styling; one built from scratch
// must borrow a style from an existing span, which is what Replace and
// SetText do.
func (f *Flow) SetSpans(spans []FlowSpan) error {
	plan, err := f.planSpans(spans)
	if err != nil {
		return err
	}
	f.applyPlan(plan, 0)
	return nil
}

// planSpans works out a rewrite without applying it, so a page can total
// up how far each paragraph moves before anything is written.
func (f *Flow) planSpans(spans []FlowSpan) (*flowPlan, error) {
	clean := make([]FlowSpan, 0, len(spans))
	for _, s := range spans {
		if s.Text == "" {
			continue
		}
		if s.style.font == nil {
			return nil, fmt.Errorf("gopdf: a span has no style; take one from Spans()")
		}
		clean = append(clean, s)
	}
	if len(clean) == 0 {
		return nil, fmt.Errorf("gopdf: a paragraph cannot be emptied; replace it with a space")
	}
	if shrunk, did := f.shrinkInserted(clean); did {
		clean = shrunk
	}
	lines, err := f.wrap(clean)
	if err != nil {
		return nil, err
	}
	// A paragraph that would come out longer than it stood is first
	// tried a little tighter (wrapTight): the page may have set its
	// lines tighter than its font's own widths add up to, and set again
	// without that a line is a hair wider than itself.
	if len(lines) > len(f.lines) {
		if tight, ok := f.wrapTight(clean); ok && len(tight) < len(lines) {
			lines = tight
		}
	}
	if f.maxExtra >= 0 {
		if extra := len(lines) - len(f.lines); extra > f.maxExtra {
			return nil, fmt.Errorf("gopdf: the text needs %d more line(s) than the paragraph "+
				"occupies; allow them with SetMaxExtraLines or shorten the text", extra)
		}
	}
	plan := &flowPlan{spans: clean, delta: len(lines) - f.curLines, lines: len(lines)}
	plan.ops = make([]string, len(lines))
	for i, line := range lines {
		s, err := f.lineOps(line)
		if err != nil {
			return nil, err
		}
		plan.ops[i] = s
	}
	return plan, nil
}

// flowPlan is a rewrite worked out but not yet written.
type flowPlan struct {
	spans []FlowSpan
	ops   []string
	delta int
	lines int
}

// applyPlan writes a planned rewrite, moved further down the page by
// extraShift.
func (f *Flow) applyPlan(p *flowPlan, extraShift float64) {
	f.spans, f.delta, f.lastPlan = p.spans, p.delta, p
	f.curLines = p.lines
	f.place(extraShift)
}

// place puts the paragraph where it now belongs, having moved a further
// extraShift down the page. Positioning is rebuilt from the original
// operators every time rather than nudged, so placing a paragraph twice
// lands it in one place rather than two.
func (f *Flow) place(extraShift float64) {
	f.shiftTS += extraShift
	if math.Abs(f.shiftTS) < 1e-9 {
		f.shiftTS = 0
	}
	switch {
	case f.lastPlan != nil:
		f.emit(f.lastPlan.ops, f.shiftTS)
	case f.shiftTS != 0 || f.moved:
		// Moved once, it is written again even where it now belongs
		// where the document set it: what made room above it may have
		// been given back by a later replacement.
		f.shiftOriginals(f.shiftTS)
		f.moved = true
	default:
		return
	}
	if f.onChange != nil {
		f.onChange()
	}
}

// shiftOriginals moves a paragraph that was never rewritten, by giving
// every run an absolute text matrix of its own. Absolute positioning is
// used rather than nudging the first line, because runs on a line may
// carry their own positioning that a relative move would not survive.
func (f *Flow) shiftOriginals(shiftTS float64) {
	for _, line := range f.lines {
		for _, run := range line.runs {
			if run.start >= run.end || run.end > len(run.target.content) {
				continue
			}
			orig := string(run.target.content[run.start:run.end])
			var b strings.Builder
			writeTm(&b, matrix{1, 0, 0, 1, 0, shiftTS}.mul(run.tm))
			b.WriteByte(' ')
			b.WriteString(orig)
			run.spliceRaw(b.String())
			run.replaced = true
		}
	}
}

// emit splices the new line operations over the paragraph's own, moved
// down the page by shiftTS.
func (f *Flow) emit(ops []string, shiftTS float64) {
	shift := func(m matrix) matrix {
		return matrix{1, 0, 0, 1, 0, shiftTS}.mul(m)
	}
	last := len(f.lines) - 1
	for i, line := range f.lines {
		first := line.runs[0]
		var sb strings.Builder
		if i < len(ops) {
			// Each surviving line is placed from its own matrix. Stepping
			// them all from the first would misplace any line the
			// document positions under a different transform.
			writeTm(&sb, shift(line.tm))
			sb.WriteString(ops[i])
		}
		if i == last {
			// The last original line carries any lines beyond it, then
			// restores the text matrix so positioning that follows still
			// lands where it did.
			for k := len(f.lines); k < len(ops); k++ {
				sb.WriteByte(' ')
				writeTm(&sb, shift(lineMatrix(f.lines[last].tm, f.leadingTS, k-last)))
				sb.WriteString(ops[k])
			}
			sb.WriteByte(' ')
			writeTm(&sb, shift(first.tm))
		}
		first.spliceRaw(strings.TrimSpace(sb.String()))
		first.replaced = true
		// Every other run on the line is now accounted for by the ops
		// written above.
		for _, run := range line.runs[1:] {
			run.spliceRaw("")
			run.replaced = true
		}
	}
}

// lineMatrix returns the text matrix for the nth line of a paragraph.
func lineMatrix(base matrix, leading float64, n int) matrix {
	return matrix{1, 0, 0, 1, 0, leading * float64(n)}.mul(base)
}

// textScale returns how far a unit of text space carries on the page for
// a run, which is what converts a leading in points into one in text
// space.
func textScale(run *TextRun) float64 {
	if run.fontSizeRaw == 0 || run.FontSize == 0 {
		return 1
	}
	return run.FontSize / run.fontSizeRaw
}

func writeTm(b *strings.Builder, m matrix) {
	fmt.Fprintf(b, "%s %s %s %s %s %s Tm", fl(m[0]), fl(m[1]), fl(m[2]),
		fl(m[3]), fl(m[4]), fl(m[5]))
}

// lineOps renders one wrapped line as content-stream operations, emitting
// a style change only where the style actually changes.
func (f *Flow) lineOps(line []FlowSpan) (string, error) {
	var b strings.Builder
	var cur flowStyle
	// Whether a span was inserted matters while wrapping, because only
	// inserted text may change font. By the time it is written the
	// distinction is spent, so neighbours in one style become one
	// show-text operation again rather than several.
	//
	// first tracks whether anything has been drawn yet, rather than the
	// index, because a leading gap draws nothing and must not be taken
	// for the span that establishes the line's text state.
	first := true
	for _, span := range coalesceForOutput(line) {
		if span.gap != 0 {
			// A gap is a move, and it is measured against the state
			// already in force rather than its own style, so the number
			// written means what the reader will make of it.
			if first {
				continue // a line does not begin with a gap
			}
			if adj := gapAdjust(cur, span.gap); adj != 0 {
				fmt.Fprintf(&b, " [%s] TJ", fl(adj))
			}
			continue
		}
		st := span.style
		if first || !st.sameAs(cur) {
			if first || st.fontName != cur.fontName || st.fontSizeRaw != cur.fontSizeRaw {
				fmt.Fprintf(&b, " /%s %s Tf", st.fontName, fl(st.fontSizeRaw))
			}
			if st.fillOp != "" && (first || st.fillOp != cur.fillOp) {
				fmt.Fprintf(&b, " %s", st.fillOp)
			}
			if first || st.charSpacing != cur.charSpacing {
				fmt.Fprintf(&b, " %s Tc", fl(st.charSpacing))
			}
			if first || st.wordSpacing != cur.wordSpacing {
				fmt.Fprintf(&b, " %s Tw", fl(st.wordSpacing))
			}
			if first || st.horizScale != cur.horizScale {
				fmt.Fprintf(&b, " %s Tz", fl(st.horizScale*100))
			}
			cur = st
		}
		codes, err := st.font.encodeText(span.Text)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, " <%X> Tj", codes)
		first = false
	}
	return strings.TrimSpace(b.String()), nil
}

// wrap breaks styled spans into lines no wider than the column, measuring
// each piece in its own font.
func (f *Flow) wrap(spans []FlowSpan) ([][]FlowSpan, error) {
	lines, _, err := f.wrapWithin(spans, f.widthTS)
	return lines, err
}

// kernSlack is how much wider than its column a line may measure in its
// font's own widths and still be set on one line, drawn that much
// narrower (wrapTight): what the kerns between pairs of letters, or the
// spaces of a justified line set close, take out of a line — not what
// tracking a heading by a fifth does.
const kernSlack = 1.03

// wrapTight wraps the paragraph with lines allowed kernSlack over the
// column, and draws each line that is over it narrower by as much, so
// that none stands out of the column.
//
// A page sets a line as wide as it likes: with kerns between pairs of
// letters, a heading of one line is a hair narrower than the sum of its
// font's widths, and so is the fullest line of a caption. Set again
// without the kerns, such a line did not fit the width it had stood in:
// its last word went to a line of its own, the paragraph was a line
// longer for a replacement no wider than what it replaced, and what
// stood under it had to move.
func (f *Flow) wrapTight(spans []FlowSpan) ([][]FlowSpan, bool) {
	if f.widthTS <= 0 {
		return nil, false
	}
	lines, widths, err := f.wrapWithin(spans, f.widthTS*kernSlack)
	if err != nil {
		return nil, false
	}
	for i, w := range widths {
		if w <= f.widthTS {
			continue
		}
		by := f.widthTS / w
		narrowed := make([]FlowSpan, len(lines[i]))
		copy(narrowed, lines[i])
		for k := range narrowed {
			narrowed[k].style.horizScale *= by
			narrowed[k].gap *= by
		}
		lines[i] = narrowed
	}
	return lines, true
}

// wrapWithin is wrap for a column of the given width, and says how wide
// each line it made measures.
func (f *Flow) wrapWithin(spans []FlowSpan, widthTS float64) ([][]FlowSpan, []float64, error) {
	words, err := flowWords(spans, f.fallback)
	if err != nil {
		return nil, nil, err
	}
	var lines [][]FlowSpan
	var widths []float64
	var line []FlowSpan
	var used float64

	push := func() {
		if len(line) > 0 {
			lines = append(lines, mergeSpans(line))
			widths = append(widths, used)
			line = nil
			used = 0
		}
	}
	for _, w := range words {
		if w.newline {
			push()
			continue
		}
		gap, joiner := 0.0, ""
		if len(line) > 0 {
			gap, joiner = w.spaceWidth, w.spaceText
			if joiner == "" && gap <= 0 {
				// No glyph to draw the space with and no width to move by
				// either. Setting the words flush against each other is
				// worse than declining.
				return nil, nil, fmt.Errorf("gopdf: font /%s can neither draw a space "+
					"nor say how wide one is, so the words in this paragraph "+
					"cannot be separated", w.spaceStyle.fontName)
			}
			if used+gap+w.width > widthTS {
				push()
				gap, joiner = 0, ""
			}
		}
		switch {
		case joiner != "":
			// The joining space belongs to the style before it, which is
			// what a word processor would do.
			line = append(line, FlowSpan{Text: joiner, style: w.spaceStyle,
				FontName: string(w.spaceStyle.fontName)})
		case gap > 0:
			// This font has no space in it, because the document never
			// drew one: its producer set each word separately and moved
			// the pen between them. Doing the same is the only way to
			// re-wrap the paragraph, and it is what the page already did.
			line = append(line, FlowSpan{gap: gap, style: w.spaceStyle,
				FontName: string(w.spaceStyle.fontName)})
		}
		line = append(line, w.parts...)
		used += gap + w.width
	}
	push()
	if len(lines) == 0 {
		lines, widths = [][]FlowSpan{{spans[0]}}, []float64{0}
	}
	return lines, widths, nil
}

// flowWord is one whitespace-delimited word, which may itself span
// several styles.
type flowWord struct {
	parts       []FlowSpan
	width       float64
	spaceWidth  float64
	spaceText   string
	spaceStyle  flowStyle
	newline     bool
	styleOfHead flowStyle
	// fittedHead is set when the word begins with text this package
	// resized, whose size the preceding space must not inherit.
	fittedHead bool
}

// flowWords splits styled spans into words, keeping each word's styling.
func flowWords(spans []FlowSpan, fallback func(flowStyle) (flowStyle, bool)) ([]flowWord, error) {
	var out []flowWord
	var cur flowWord
	flush := func() {
		if len(cur.parts) > 0 {
			out = append(out, cur)
		}
		cur = flowWord{}
	}
	var last flowStyle
	for _, span := range spans {
		// The decision to fall back is made once for the whole span, not
		// word by word. A token set partly in the document's font and
		// partly in another is encoded against two different tables, and
		// the join between them is where a character comes out wrong.
		if span.inserted && fallback != nil {
			if _, ok := span.style.advance(span.Text); !ok {
				if alt, made := fallback(span.style); made {
					if _, ok := alt.advance(span.Text); ok {
						span.style = alt
					}
				}
			}
		}
		last = span.style
		for _, chunk := range splitKeepingBreaks(span.Text) {
			switch {
			case chunk == "\n":
				flush()
				out = append(out, flowWord{newline: true})
			case strings.TrimSpace(chunk) == "":
				flush()
			default:
				style := span.style
				w, ok := style.advance(chunk)
				if !ok && span.inserted && fallback != nil {
					// The span as a whole could not be set in either
					// font — a token with a rune outside both — so each
					// word is given its best chance before refusing.
					if alt, made := fallback(style); made {
						if w2, ok2 := alt.advance(chunk); ok2 {
							style, w, ok = alt, w2, true
						}
					}
				}
				if !ok {
					return nil, fmt.Errorf("gopdf: font /%s cannot measure %q",
						span.style.fontName, chunk)
				}
				if len(cur.parts) == 0 {
					cur.styleOfHead = style
					cur.fittedHead = span.fitted
				}
				cur.parts = append(cur.parts, FlowSpan{
					Text: chunk, style: style,
					FontName: string(style.fontName),
					FontSize: span.FontSize,
					inserted: span.inserted,
				})
				cur.width += w
			}
		}
	}
	flush()
	// Every word carries the width of the space that would precede it.
	for i := range out {
		st := out[i].styleOfHead
		if st.font == nil {
			st = last
		}
		// A token set smaller to fit did not make the space in front of
		// it smaller. That space is the document's own, and setting it
		// at the token's size closes up the gap before the token by as
		// much as the token was shrunk — visible as a word running into
		// the thing that replaced the next one.
		if out[i].fittedHead && i > 0 {
			if prev := out[i-1].parts; len(prev) > 0 && prev[len(prev)-1].style.font != nil {
				st = prev[len(prev)-1].style
			}
		}
		out[i].spaceStyle = st
		out[i].spaceText = st.spaceText()
		if out[i].spaceText == "" {
			out[i].spaceWidth = st.gapWidth()
		} else if w, ok := st.advance(out[i].spaceText); ok {
			out[i].spaceWidth = w
		}
	}
	return out, nil
}

// splitKeepingBreaks splits text into words, runs of spaces and explicit
// newlines, so wrapping can tell a break from a gap.
func splitKeepingBreaks(s string) []string {
	var out []string
	var word strings.Builder
	flush := func() {
		if word.Len() > 0 {
			out = append(out, word.String())
			word.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r == '\n':
			flush()
			out = append(out, "\n")
		case r == ' ' || r == '\t' || r == '\r':
			flush()
			out = append(out, " ")
		default:
			word.WriteRune(r)
		}
	}
	flush()
	return out
}

// mergeSpans joins neighbouring spans that share a style, so a line emits
// one show-text operation per style rather than one per word.
func mergeSpans(in []FlowSpan) []FlowSpan {
	var out []FlowSpan
	for _, s := range in {
		// Inserted and original text never merge, even in the same
		// style: merging would spread the flag onto text the document
		// already had, and with it permission to restyle that text.
		if n := len(out); n > 0 && out[n-1].style.sameAs(s.style) &&
			out[n-1].inserted == s.inserted && out[n-1].gap == 0 && s.gap == 0 &&
			!out[n-1].lineBreak && !s.lineBreak {
			out[n-1].Text += s.Text
			continue
		}
		out = append(out, s)
	}
	return out
}

// replaceInSpans applies a string replacement across styled spans. Text
// that stays keeps its style, and inserted text takes the style of the
// first character it replaces.
//
// Matching reads the paragraph as a person would: a word hyphenated
// across a line break is one word, and a literal counts only where it
// stands on its own. A replacement covers the whole of what it matched,
// so a split word takes its dangling hyphen and the break with it and the
// paragraph re-wraps without either.
//
// With fit, a replacement is set at the size that gives it the width of
// what it replaces, measured in the face it will be set in: the run's
// own, or the stand-in face fallback names where the document's subset
// has no glyph for it (tokenStyle, as the editor that keeps a page as
// it was measures it). Measured in the run's own font alone, a token a
// subset could not write — a bracket, a capital the page never drew —
// had no width, was not fitted at all, and went in at the run's full
// size: on a page laid out again every such token was wider than its
// value, the paragraphs grew, and a page filled to its foot was
// refused.
//
// alt, with fit, is a shorter stand-in for new. It is set where no face
// at hand writes new; and, with short, where even the floor leaves new
// wider than the words it replaces (holdsAt), as that editor has taken
// it all along (resolveFitTokens). Without short, new is set at the
// floor there and the paragraph is a little longer for it: what a page
// with room to spare has always been given, and still is (replaceFlows
// asks for short only on a page that has none).
func replaceInSpans(spans []FlowSpan, old, new string, mode matchMode,
	fit bool, floor float64, alt string, short bool,
	fallback func(flowStyle) (flowStyle, bool)) ([]FlowSpan, int) {
	reading := readParagraph(spans)
	text := reading.flat

	// Which bytes of the flattened text belong to which span, and where
	// each span begins.
	var owner []int
	starts := make([]int, len(spans))
	for i, s := range spans {
		starts[i] = len(owner)
		for range []byte(s.Text) {
			owner = append(owner, i)
		}
	}

	// Matches are sought in the joined reading and mapped back, and in
	// the paragraph as it is drawn, the hyphen that ends a line kept:
	// "Veltranet-" over "supported" is a compound as often as it is a
	// divided word, and joined it read "Veltranetsupported", in which
	// a marked "Veltranet" was no word of its own.
	var hits [][2]int
	for _, rg := range literalRanges(reading.text, old, mode) {
		lo, hi, ok := reading.rangeInOriginal(rg[0], rg[1])
		if ok {
			hits = append(hits, [2]int{lo, hi})
		}
	}
	if reading.text != text {
		hits = append(hits, literalRanges(text, old, mode)...)
		sort.Slice(hits, func(a, b int) bool {
			if hits[a][0] != hits[b][0] {
				return hits[a][0] < hits[b][0]
			}
			return hits[a][1] > hits[b][1]
		})
	}
	if len(hits) == 0 {
		return spans, 0
	}

	var out []FlowSpan
	emit := func(from, to int) {
		for at := from; at < to; {
			s := owner[at]
			end := at
			for end < to && owner[end] == s {
				end++
			}
			out = append(out, FlowSpan{
				Text: text[at:end], style: spans[s].style,
				FontName: spans[s].FontName, FontSize: spans[s].FontSize,
				inserted: spans[s].inserted,
				fitted:   spans[s].fitted,
				// The space between two lines stays one where it is kept
				// whole. Measured from the paragraph's start instead of the
				// span's, it was kept only at the very start: every other
				// line end of a paragraph with a replacement in it became
				// an ordinary space, and a word divided there two words.
				lineBreak: spans[s].lineBreak && at == starts[s] && end == starts[s]+len(spans[s].Text),
			})
			at = end
		}
	}
	// What the occurrence itself took, measured piece by piece in each
	// piece's own style: a match may run across a style change, and half
	// of it may have been set in a different font from the other half.
	widthOf := func(from, to int) (float64, bool) {
		total := 0.0
		for at := from; at < to; {
			s := owner[at]
			end := at
			for end < to && owner[end] == s {
				end++
			}
			w, ok := spans[s].style.advance(text[at:end])
			if !ok {
				return 0, false
			}
			total += w
			at = end
		}
		return total, true
	}

	at, n := 0, 0
	for _, h := range hits {
		if h[0] < at {
			continue // overlaps one already taken
		}
		n++
		emit(at, h[0])
		host := spans[owner[h[0]]]
		if new != "" {
			ins := FlowSpan{Text: new, style: host.style,
				FontName: host.FontName, FontSize: host.FontSize,
				inserted: true}
			// Each occurrence is fitted on its own, since the run behind
			// this one may be in a different font or size from the last.
			if fit {
				if want, ok := widthOf(h[0], h[1]); ok {
					set, _, can := tokenStyle(host.style, new, fallback)
					if alt != "" && (!can || (short && !holdsAt(set, new, want, floor))) {
						if less, _, ok := tokenStyle(host.style, alt, fallback); ok {
							ins.Text, set, can = alt, less, true
						}
					}
					if !can {
						set = host.style // no face writes it: planSpans says so
					}
					if size, did := fitSize(set, ins.Text, want, floor); did {
						ins.FontSize *= size / ins.style.fontSizeRaw
						ins.style.fontSizeRaw = size
						ins.fitted = true
					}
				}
			}
			out = append(out, ins)
		}
		at = h[1]
	}
	emit(at, len(text))
	return mergeSpans(out), n
}

// --- building flows from a page ---

// buildFlows groups runs into paragraphs that may mix styles within a
// line, which is what separates a flow from the stricter TextBlock.
func buildFlows(runs []*TextRun, onChange func(), fallback func(flowStyle) (flowStyle, bool)) []*Flow {
	lines := groupLines(runs)
	var out []*Flow
	for i := 0; i < len(lines); {
		j := i + 1
		leading := 0.0
		for j < len(lines) {
			gap, ok := continuesFlow(lines[i], lines[j-1], lines[j], leading)
			if !ok {
				break
			}
			leading = gap
			j++
		}
		if f := newFlow(lines[i:j], onChange); f != nil {
			f.fallback = fallback
			out = append(out, f)
		}
		i = j
	}
	return out
}

// groupLines collects runs sharing a baseline, in left-to-right order.
func groupLines(runs []*TextRun) []flowLine {
	var out []flowLine
	for i := 0; i < len(runs); {
		j := i + 1
		for j < len(runs) && math.Abs(runs[j].Y-runs[i].Y) < 0.5 &&
			runs[j].target == runs[i].target {
			j++
		}
		group := append([]*TextRun(nil), runs[i:j]...)
		sort.SliceStable(group, func(a, b int) bool { return group[a].X < group[b].X })
		out = append(out, flowLine{runs: group, y: runs[i].Y, tm: group[0].tm})
		i = j
	}
	return out
}

// continuesFlow reports whether a line carries on the paragraph, and the
// leading it sits at. leading is the step established by the lines so
// far, or zero while the paragraph is still one line long.
//
// The checks are what separates a paragraph from a heading that happens
// to sit above one: the same left edge, the same size of type, and a
// leading that does not change part-way down.
func continuesFlow(first, prev, next flowLine, leading float64) (float64, bool) {
	a, b := first.runs[0], next.runs[0]
	if a.target != b.target || math.Abs(a.X-b.X) > 0.5 {
		return 0, false
	}
	size := a.FontSize
	if size <= 0 || math.Abs(b.FontSize-size) > 0.01 {
		return 0, false
	}
	gap := next.y - prev.y
	if gap <= 0.1 || gap > size*2.5 {
		return 0, false
	}
	if leading == 0 {
		return gap, true // this gap establishes the leading
	}
	if math.Abs(gap-leading) > 0.5 {
		return 0, false
	}
	return gap, true
}

// newFlow assembles a paragraph from its lines.
func newFlow(lines []flowLine, onChange func()) *Flow {
	if len(lines) == 0 || len(lines[0].runs) == 0 {
		return nil
	}
	f := &Flow{lines: lines, onChange: onChange, maxExtra: -1, curLines: len(lines)}
	head := lines[0].runs[0]
	f.X, f.Y = head.X, head.Y

	for _, line := range lines {
		var lineTS, linePt float64
		for _, run := range line.runs {
			lineTS += run.advance / 1000 * run.fontSizeRaw * run.horizScale
			linePt += run.Width
		}
		if lineTS > f.widthTS {
			f.widthTS = lineTS
		}
		if linePt > f.Width {
			f.Width = linePt
		}
	}
	if len(lines) > 1 {
		f.LineHeight = lines[1].y - lines[0].y
	} else {
		// A paragraph of one line has no measured leading, and a
		// replacement that needs a second line would otherwise print it
		// on top of the first. Fall back to normal single spacing.
		f.LineHeight = head.FontSize * 1.2
	}
	// The step is taken from the leading on the page rather than from the
	// difference between two text matrices: a document may position each
	// line with its own transform, leaving those matrices identical while
	// the lines are plainly apart. Text space runs upwards, so a later
	// line sits at a lower value.
	f.leadingTS = -f.LineHeight / textScale(head)

	// The spans, in reading order, with the space between lines made
	// explicit so a re-wrap can put the break somewhere else.
	for i, line := range lines {
		if i > 0 {
			f.spans = append(f.spans, FlowSpan{Text: " ", lineBreak: true,
				style: styleOf(line.runs[0]), FontName: line.runs[0].FontName})
		}
		var prev *TextRun
		for _, run := range line.runs {
			if run.Text == "" {
				continue
			}
			// A document may set a line one fragment at a time, with the
			// gaps that make the words carried by positioning rather than
			// by spaces. Reading those runs straight through gives
			// "ContractwithMarcoBianchi" and nothing matches. The gap is
			// judged by the rule extraction uses, so a paragraph reads
			// here the way PageText reports it.
			if prev != nil {
				gap := run.X - (prev.X + prev.Width)
				if needsSpace(prev.Text, run.Text, gap, prev.spaceWidthPts()) {
					f.spans = append(f.spans, FlowSpan{Text: " ",
						style: styleOf(prev), FontName: prev.FontName})
				}
			}
			// A run can carry gaps of its own, where one operation drew
			// "9.2.1" and then moved the pen before "Messaggio". The
			// reader sees two words there and so must this, or a
			// replacement of the second one finds nothing to replace.
			// The pieces are emitted with the same space between them
			// that a gap between two runs gets.
			for i, piece := range splitAtGaps(run) {
				if i > 0 {
					f.spans = append(f.spans, FlowSpan{Text: " ",
						style: styleOf(run), FontName: run.FontName})
				}
				f.spans = append(f.spans, FlowSpan{
					Text: piece, style: styleOf(run),
					FontName: run.FontName, FontSize: run.FontSize,
				})
			}
			prev = run
		}
	}
	f.spans = mergeSpans(f.spans)
	if len(f.spans) == 0 {
		return nil
	}
	return f
}

// styleOf captures how a run is drawn.
func styleOf(run *TextRun) flowStyle {
	return flowStyle{
		fontName:    Name(run.FontName),
		fontSizeRaw: run.fontSizeRaw,
		fillOp:      run.fillOp,
		charSpacing: run.charSpacing,
		wordSpacing: run.wordSpacing,
		horizScale:  run.horizScale,
		font:        run.font,
	}
}

// Flows groups the page's text into paragraphs that can be replaced at
// any length. Unlike Blocks, a line built from several runs — a bold word
// inside a sentence — stays part of its paragraph and keeps its styling.
//
// The same paragraphs are returned every time, so edits accumulate
// instead of two calls fighting over the same operators.
func (e *EditablePage) Flows() []*Flow {
	if e.flows == nil {
		e.flows = buildFlows(e.runs, nil, fallbackFor(e))
	}
	return e.flows
}

// Flows groups the page's text into paragraphs that can be replaced at
// any length, keeping each part's styling.
func (p *UpdatablePage) Flows() []*Flow {
	if p.flows == nil {
		p.flows = buildFlows(p.runs, nil, fallbackFor(p))
	}
	return p.flows
}

// ReplaceTextFlow replaces occurrences of old across the page's
// paragraphs, re-wrapping each one it changes. Unlike ReplaceTextReflow
// it handles paragraphs of mixed styling, gives the replacement the
// styling of the text it replaces, and lets a paragraph grow or shrink by
// whole lines. It returns the number of paragraphs rewritten.
func (e *EditablePage) ReplaceTextFlow(old, new string) (int, error) {
	return replaceFlows(e.Flows(), old, new, e.Page.h, false, 0, "", nil)
}

// ReplaceTextFlow replaces occurrences of old across the page's
// paragraphs, re-wrapping each one it changes and keeping its styling.
func (p *UpdatablePage) ReplaceTextFlow(old, new string) (int, error) {
	return p.replaceTextFlowFit(old, new, false, 0, "")
}

// replaceTextFlowFit is ReplaceTextFlow with the option of setting each
// replacement to the width of the text it replaces, and alt where even
// the floor cannot bring new to that width (replaceInSpans).
func (p *UpdatablePage) replaceTextFlowFit(old, new string, fit bool,
	floor float64, alt string) (int, error) {
	pi := p.u.r.pages[p.index]
	return replaceFlows(p.Flows(), old, new, pi.mediaBox[3]-pi.mediaBox[1], fit, floor, alt, p.pictureBoxes())
}

// pictureBox is where a page draws a picture, in points from the
// top-left of the page.
type pictureBox struct{ x, y, w, h float64 }

// pictureBoxes lists where the page's pictures stand, for the check that
// no paragraph grows into one (checkPictures). Read once: a text edit
// moves no picture.
func (p *UpdatablePage) pictureBoxes() []pictureBox {
	if p.pictures != nil {
		return p.pictures
	}
	p.pictures = []pictureBox{}
	imgs, err := p.u.r.PageImages(p.index)
	if err != nil {
		return p.pictures
	}
	for _, im := range imgs {
		// A rule or a dot drawn as a picture hides no line of text.
		if im.W < 8 || im.H < 8 {
			continue
		}
		p.pictures = append(p.pictures, pictureBox{im.X, im.Y, im.W, im.H})
	}
	return p.pictures
}

// replaceFlows rewrites every paragraph containing old and moves the ones
// below it to make room, so a paragraph that grows does not print over
// what follows.
//
// Every rewrite is planned first. A font that cannot represent the new
// text then fails before anything has been written, rather than half way
// down the page.
func replaceFlows(flows []*Flow, old, new string, pageHeight float64,
	fit bool, floor float64, alt string, pictures []pictureBox) (int, error) {
	if old == "" {
		return 0, fmt.Errorf("gopdf: ReplaceTextFlow called with empty search text")
	}
	planAll := func(short bool) ([]*flowPlan, int, error) {
		plans := make([]*flowPlan, len(flows))
		n := 0
		for i, f := range flows {
			if !f.holds(old) {
				continue
			}
			spans, count := replaceInSpans(f.spans, old, new, f.mode,
				fit || f.fitWidth, floorOr(floor, f.fitFloor), alt, short, f.fallback)
			if count == 0 {
				continue
			}
			plan, err := f.planSpans(spans)
			if err != nil {
				return nil, 0, err
			}
			plans[i] = plan
			n++
		}
		return plans, n, nil
	}
	// The token asked for is planned first, set at the floor where the
	// floor leaves it a little wider than its value: on a page with
	// room for the lines that costs, it is what the page gets, as it
	// always did. Each paragraph stands where the ones above it in its
	// column then leave it room (columnShifts).
	plans, n, err := planAll(false)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	down := columnShifts(flows, plans)
	// A page that has no room for it — a paragraph off the sheet, or
	// into a picture — takes the caller's shorter stand-in wherever
	// the token was the wider: every value keeps its width, and no
	// paragraph grows for this replacement. A page that worked with
	// the token never comes here, and is written as it was.
	if alt != "" && !roomFor(flows, plans, down, pageHeight, pictures) {
		if less, m, err := planAll(true); err == nil && m > 0 {
			plans, n, down = less, m, columnShifts(flows, less)
		}
	}
	for i, f := range flows {
		extra := 0.0
		if f.LineHeight > 0 {
			extra = down[i]*f.leadingTS/f.LineHeight - f.shiftTS
		}
		if plans[i] != nil {
			f.applyPlan(plans[i], extra)
			continue
		}
		f.place(extra)
	}
	// A paragraph that grew past the bottom of the page has put its text
	// where the file holds it and no reader sees it: it extracts, it
	// verifies, and it is invisible. Saying it does not fit is the only
	// honest answer, and the paragraphs are already placed so the
	// caller's own copy shows what went wrong.
	if err := checkPageOverflow(flows, pageHeight); err != nil {
		return 0, err
	}
	// And one that grew into a picture has put its last lines under it,
	// or over it: the same answer.
	if err := checkPictures(flows, pictures); err != nil {
		return 0, err
	}
	return n, nil
}

// attachedWithin is how far under a paragraph, in lines, the next one of
// its column may begin and still be set off from it by the page: the
// distance between two paragraphs, a heading and its text. Further down
// stands something of its own — the caption of a picture between the
// two, a footer.
const attachedWithin = 2.5

// columnShifts says how far down the page each paragraph has to stand
// from where the document set it, in points, now that the paragraphs
// are as long as plans (or their last rewrite) make them.
//
// A paragraph that grew needs room below it, and only below it: the
// paragraphs of its own column. What the page set off from it by the
// distance between two paragraphs keeps that distance; what stands
// further down moves only as far as it must to keep a line of its own.
// Nothing moves up: a paragraph that lost a line leaves the line empty,
// and everything else where the reader knows it.
//
// The displacement used to be a running total over the paragraphs in
// the order the file draws them, added to every paragraph drawn later.
// A file draws its page in any order: a line more in the first column
// of a newsletter pushed down its second column, the titles at the top
// of the page (drawn last) into the heading under them, and the footer
// off the sheet, for which the copy was refused; a line less pulled a
// caption up into its picture.
func columnShifts(flows []*Flow, plans []*flowPlan) []float64 {
	lines := func(i int) int {
		if plans[i] != nil {
			return plans[i].lines
		}
		return flows[i].curLines
	}
	order := make([]int, len(flows))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return flows[order[a]].Y < flows[order[b]].Y })
	down := make([]float64, len(flows))
	for at, b := range order {
		lower := flows[b]
		// The paragraph that ends nearest above this one in its column,
		// as the document set them.
		above, foot := -1, 0.0
		for _, a := range order[:at] {
			upper := flows[a]
			end := upper.Y + float64(len(upper.lines)-1)*upper.LineHeight
			if end >= lower.Y || !sharesColumn(upper, lower) {
				continue
			}
			if above < 0 || end > foot {
				above, foot = a, end
			}
		}
		if above < 0 {
			continue
		}
		upper := flows[above]
		now := upper.Y + down[above] + float64(lines(above)-1)*upper.LineHeight
		need := now + lower.LineHeight - lower.Y
		if gap := lower.Y - foot; gap <= attachedWithin*math.Max(upper.LineHeight, lower.LineHeight) {
			need = now + gap - lower.Y
		}
		if need > 0 {
			down[b] = need
		}
	}
	return down
}

// sharesColumn reports whether two paragraphs stand one above the other:
// whether the widths they are set in overlap.
func sharesColumn(a, b *Flow) bool {
	left := math.Max(a.X, b.X)
	right := math.Min(a.X+a.Width, b.X+b.Width)
	return right-left > 1
}

// coalesceForOutput joins neighbouring spans that share a style,
// disregarding whether the edit inserted them.
func coalesceForOutput(in []FlowSpan) []FlowSpan {
	var out []FlowSpan
	for _, s := range in {
		if n := len(out); n > 0 && out[n-1].style.sameAs(s.style) &&
			out[n-1].gap == 0 && s.gap == 0 {
			out[n-1].Text += s.Text
			continue
		}
		out = append(out, s)
	}
	return out
}

// splitAtGaps cuts a run's text where the operation left a word break of
// its own, by the rule extraction uses.
func splitAtGaps(run *TextRun) []string {
	if len(run.spaceAt) == 0 {
		return []string{run.Text}
	}
	var out []string
	last := 0
	for _, off := range run.spaceAt {
		if off <= last || off > len(run.Text) {
			continue
		}
		out = append(out, run.Text[last:off])
		last = off
	}
	return append(out, run.Text[last:])
}
