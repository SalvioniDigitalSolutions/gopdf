package gopdf

import (
	"bytes"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Some producers never draw a space. They set each word on its own and
// make the gaps by moving the pen, so the subset font they embed has no
// space glyph in it at all — there was never one to include. Re-wrapping
// such a paragraph used to be refused, because writing the words with
// nothing between them is worse than declining. Doing what the producer
// did is better than either.

// gapWords are the words of the fixture, set separately so that no space
// is ever drawn and none is embedded.
var gapWords = []string{"COSTITUZIONE", "DI", "SOCIETA", "A", "GARANZIA", "LIMITATA"}

// spacelessDoc lays the words out at computed positions in an embedded
// font, reproducing what a word processor's PDF export does.
func spacelessDoc(t *testing.T, size float64) []byte {
	t.Helper()
	font, err := LoadFont(testFontPath(t))
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	doc.Compress = false
	p := doc.AddPage()
	p.SetFont(font, size)
	x := 60.0
	for _, w := range gapWords {
		p.Text(x, 100, w)
		x += font.TextWidth(w, size) + font.TextWidth(" ", size)
	}
	return docBytes(t, doc)
}

// TestSpacelessFixtureHasNoSpaceGlyph guards the fixture itself. If the
// font ever gained a space, every test below would pass for the wrong
// reason.
func TestSpacelessFixtureHasNoSpaceGlyph(t *testing.T) {
	_, _, flows := editFlows(t, spacelessDoc(t, 12))
	if len(flows) != 1 {
		t.Fatalf("got %d flows, want 1", len(flows))
	}
	st := flows[0].Spans()[0].style
	if _, err := st.font.encodeText(" "); err == nil {
		t.Fatal("the fixture font can set a space, so it does not exercise the gap path")
	}
	if got := flows[0].Text(); !strings.Contains(got, "COSTITUZIONE DI") {
		t.Errorf("the words did not read as separate: %q", got)
	}
}

// TestSpacelessParagraphIsReplaceable is the failure this fixes: a
// pseudonymization that refused because the font had no space in it.
func TestSpacelessParagraphIsReplaceable(t *testing.T) {
	src := spacelessDoc(t, 12)
	var buf bytes.Buffer
	res, err := Pseudonymize(NewReaderOrFail(t, src), &buf,
		[]Pseudonym{{From: "SOCIETA", To: "[REDACTED]"}})
	if err != nil {
		t.Fatalf("a paragraph in a font with no space glyph was refused: %v", err)
	}
	if res.Total() != 1 {
		t.Fatalf("replaced %d paragraphs, want 1", res.Total())
	}
	text, err := NewReaderOrFail(t, buf.Bytes()).PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	// The words must still read as words: that is the whole reason the
	// old behaviour refused rather than writing them flush.
	got := strings.Join(strings.Fields(text), " ")
	if want := "COSTITUZIONE DI [REDACTED] A GARANZIA LIMITATA"; got != want {
		t.Errorf("the page reads\n got  %q\n want %q", got, want)
	}
}

// TestSpacelessGapIsAMove checks the mechanism rather than the outcome:
// the separator is a positioning operator, not a glyph, because there is
// no glyph to be had.
func TestSpacelessGapIsAMove(t *testing.T) {
	src := spacelessDoc(t, 12)
	_, e, flows := editFlows(t, src)
	if _, err := flows[0].Replace("SOCIETA", "[REDACTED]"); err != nil {
		t.Fatal(err)
	}
	ops := flows[0].lastPlan.ops
	if len(ops) == 0 {
		t.Fatal("the rewrite produced no operators")
	}
	line := ops[0]
	if !strings.Contains(line, "TJ") {
		t.Errorf("no positioning was written, so the words are flush:\n%s", line)
	}
	if strings.Contains(line, "Tw") && !strings.Contains(line, "0 Tw") {
		t.Errorf("word spacing was used for a font with no space:\n%s", line)
	}
	// A line never opens with a move: that would indent it.
	if strings.HasPrefix(strings.TrimSpace(line), "[") {
		t.Errorf("the line begins with a move:\n%s", line)
	}
	_ = e
}

// TestSpacelessKeepsTheGapWidth: the move has to be the width of a
// space, or the words either collide or drift apart.
func TestSpacelessKeepsTheGapWidth(t *testing.T) {
	const size = 12
	src := spacelessDoc(t, size)
	var buf bytes.Buffer
	if _, err := Pseudonymize(NewReaderOrFail(t, src), &buf,
		[]Pseudonym{{From: "SOCIETA", To: "[REDACTED]"}}); err != nil {
		t.Fatal(err)
	}
	frags, err := NewReaderOrFail(t, buf.Bytes()).PageTextFragments(0)
	if err != nil {
		t.Fatal(err)
	}
	var last TextFragment
	gaps := 0
	for _, f := range frags {
		if strings.TrimSpace(f.Text) == "" {
			continue
		}
		// The token needs glyphs this subset has not got, so it is set in
		// a fallback font — one that does have a space, and draws it.
		// Those separators are characters rather than moves and are
		// measured by the width of the glyph, not the distance.
		drawn := strings.HasPrefix(f.Text, " ") || strings.HasSuffix(last.Text, " ")
		if last.Text != "" && !drawn && math.Abs(last.Y-f.Y) < 0.5 {
			gap := f.X - (last.X + last.W)
			// A quarter of the size is the convention used when the font
			// declares no space of its own.
			if want := size * 0.25; math.Abs(gap-want) > 0.35 {
				t.Errorf("the gap before %q is %.3f, want about %.3f",
					f.Text, gap, want)
			}
			gaps++
		}
		last = f
	}
	if gaps < 3 {
		t.Fatalf("only %d moves were measured; the fixture is not exercising them", gaps)
	}
}

// TestSpacelessAgreesWithPoppler is the check that matters most: an
// independent reader has to see the same words.
func TestSpacelessAgreesWithPoppler(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext is not installed")
	}
	src := spacelessDoc(t, 12)
	var buf bytes.Buffer
	if _, err := Pseudonymize(NewReaderOrFail(t, src), &buf,
		[]Pseudonym{{From: "SOCIETA", To: "[REDACTED]"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "out.pdf")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("pdftotext", path, "-").Output()
	if err != nil {
		t.Fatalf("pdftotext: %v", err)
	}
	got := strings.Join(strings.Fields(string(out)), " ")
	if want := "COSTITUZIONE DI [REDACTED] A GARANZIA LIMITATA"; got != want {
		t.Errorf("Poppler reads\n got  %q\n want %q", got, want)
	}
}

// --- the pieces, on their own ---

// gapStyle is a style whose font can set letters but not a space, which
// is the condition the whole feature exists for.
func gapStyle(size float64) flowStyle {
	return flowStyle{font: &fontInfo{
		encode: map[rune][]byte{'a': {'a'}, 'b': {'b'}},
		built:  true,
	}, fontSizeRaw: size, horizScale: 1}
}

func TestGapWidth(t *testing.T) {
	// With no space in the font there is nothing to measure, so the
	// convention stands in: a quarter of the size.
	if got, want := gapStyle(10).gapWidth(), 2.5; math.Abs(got-want) > 1e-9 {
		t.Errorf("gapWidth = %v, want %v", got, want)
	}
	// Horizontal scaling applies to a gap as it does to a glyph.
	st := gapStyle(10)
	st.horizScale = 0.5
	if got, want := st.gapWidth(), 1.25; math.Abs(got-want) > 1e-9 {
		t.Errorf("scaled gapWidth = %v, want %v", got, want)
	}
	// A font that does have a space is measured rather than guessed at.
	withSpace := flowStyle{font: &fontInfo{
		encode: map[rune][]byte{' ': {32}},
		widths: map[uint32]float64{32: 500},
		built:  true,
	}, fontSizeRaw: 10, horizScale: 1}
	if got, want := withSpace.gapWidth(), 5.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("declared gapWidth = %v, want %v", got, want)
	}
	// Nothing to go on at all.
	if got := (flowStyle{}).gapWidth(); got != 0 {
		t.Errorf("a style with no font gave a gap of %v", got)
	}
	if got := (flowStyle{font: &fontInfo{built: true}}).gapWidth(); got != 0 {
		t.Errorf("a style with no size gave a gap of %v", got)
	}
}

func TestGapAdjust(t *testing.T) {
	// A TJ number is subtracted from the displacement, so moving the pen
	// forward takes a negative one.
	if got, want := gapAdjust(gapStyle(12), 3), -250.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("gapAdjust = %v, want %v", got, want)
	}
	// The number is relative to the size in force, so half the size is
	// twice the number for the same distance on the page.
	if got, want := gapAdjust(gapStyle(6), 3), -500.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("gapAdjust at 6pt = %v, want %v", got, want)
	}
	st := gapStyle(12)
	st.horizScale = 0.5
	if got, want := gapAdjust(st, 3), -500.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("scaled gapAdjust = %v, want %v", got, want)
	}
	for _, c := range []struct {
		name  string
		st    flowStyle
		width float64
	}{
		{"no size", flowStyle{horizScale: 1}, 3},
		{"no scale", flowStyle{fontSizeRaw: 12}, 3},
		{"no width", gapStyle(12), 0},
	} {
		if got := gapAdjust(c.st, c.width); got != 0 {
			t.Errorf("%s: gapAdjust = %v, want 0", c.name, got)
		}
	}
}

// TestFlowWrapsWithoutASpaceGlyph replaces the old refusal. Two words in
// a font with no space are separated by a move, and the move carries the
// width the space would have had.
func TestFlowWrapsWithoutASpaceGlyph(t *testing.T) {
	f := &Flow{widthTS: 1000, maxExtra: -1}
	st := gapStyle(10)
	lines, err := f.wrap([]FlowSpan{{Text: "a b", style: st}})
	if err != nil {
		t.Fatalf("wrapping two words with no space glyph was refused: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	var gaps int
	for _, s := range lines[0] {
		if s.gap != 0 {
			gaps++
			if math.Abs(s.gap-2.5) > 1e-9 {
				t.Errorf("the gap is %v, want a quarter of the size", s.gap)
			}
			if s.Text != "" {
				t.Errorf("a gap should draw nothing, got %q", s.Text)
			}
		}
	}
	if gaps != 1 {
		t.Errorf("got %d gaps between two words, want 1", gaps)
	}
	// A single word needs no separator at all.
	one, err := f.wrap([]FlowSpan{{Text: "ab", style: st}})
	if err != nil {
		t.Fatalf("a single word should not need a space: %v", err)
	}
	for _, s := range one[0] {
		if s.gap != 0 {
			t.Error("a single word was given a gap")
		}
	}
}

// TestFlowRefusesWithNoWidthEither is the case that is still hopeless:
// no glyph to draw the space with, and no width to move by.
func TestFlowRefusesWithNoWidthEither(t *testing.T) {
	f := &Flow{widthTS: 1000, maxExtra: -1}
	st := gapStyle(10)
	st.horizScale = 0 // a degenerate Tz leaves no distance to move
	_, err := f.wrap([]FlowSpan{{Text: "a b", style: st}})
	if err == nil {
		t.Fatal("wrapping with neither a space nor a width should be refused")
	}
	if !strings.Contains(err.Error(), "space") {
		t.Errorf("the error should say why: %v", err)
	}
}

// TestLineOpsWritesTheMove pins the operator, since that is what a
// reader actually sees.
func TestLineOpsWritesTheMove(t *testing.T) {
	f := &Flow{widthTS: 1000, maxExtra: -1}
	st := gapStyle(12)
	got, err := f.lineOps([]FlowSpan{
		{Text: "a", style: st},
		{gap: 3, style: st},
		{Text: "b", style: st},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "[-250] TJ") {
		t.Errorf("the move is missing or wrong:\n%s", got)
	}
	if strings.Index(got, "TJ") < strings.Index(got, "<61> Tj") {
		t.Errorf("the move comes before the word it follows:\n%s", got)
	}
	// A leading gap would indent the line, so it is dropped.
	lead, err := f.lineOps([]FlowSpan{{gap: 3, style: st}, {Text: "a", style: st}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(lead, "TJ") {
		t.Errorf("a leading gap was written:\n%s", lead)
	}
	// And the line still establishes its own text state.
	for _, op := range []string{"Tf", "Tc", "Tw", "Tz"} {
		if !strings.Contains(lead, op) {
			t.Errorf("%s was not written after a dropped leading gap:\n%s", op, lead)
		}
	}
}

// TestGapSurvivesMerging: a gap draws nothing, so a merge that went by
// style alone would fold it into its neighbour and lose the space.
func TestGapSurvivesMerging(t *testing.T) {
	st := gapStyle(12)
	in := []FlowSpan{
		{Text: "a", style: st},
		{gap: 3, style: st},
		{Text: "b", style: st},
	}
	for _, c := range []struct {
		name string
		got  []FlowSpan
	}{
		{"mergeSpans", mergeSpans(in)},
		{"coalesceForOutput", coalesceForOutput(in)},
	} {
		gaps := 0
		for _, s := range c.got {
			if s.gap != 0 {
				gaps++
			}
		}
		if gaps != 1 {
			t.Errorf("%s left %d gaps of 1: %+v", c.name, gaps, c.got)
		}
	}
}

// TestSpaceWidth1000 covers the measurement all of this rests on. Asking
// for code 32 is only meaningful in a simple font; a CID font numbers
// its glyphs itself, and answering from /DW gives a full em where a
// space is closer to a quarter of one.
func TestSpaceWidth1000(t *testing.T) {
	simple := &fontInfo{
		encode: map[rune][]byte{' ': {32}, 'a': {'a'}},
		widths: map[uint32]float64{32: 278},
		built:  true,
	}
	if w, ok := simple.spaceWidth1000(); !ok || math.Abs(w-278) > 1e-9 {
		t.Errorf("simple font: got %v, %v; want 278, true", w, ok)
	}
	// A font with no space says so, rather than reporting the default
	// width of a code it does not use.
	none := &fontInfo{
		encode:   map[rune][]byte{'a': {'a'}},
		defWidth: 1000,
		built:    true,
	}
	if w, ok := none.spaceWidth1000(); ok {
		t.Errorf("a font with no space reported one of %v", w)
	}
	// A CID font is asked through its own encoding, not through code 32.
	cid := &fontInfo{
		cid:      true,
		encode:   map[rune][]byte{' ': {0, 3}},
		widths:   map[uint32]float64{3: 260},
		defWidth: 1000,
		built:    true,
	}
	if w, ok := cid.spaceWidth1000(); !ok || math.Abs(w-260) > 1e-9 {
		t.Errorf("CID font: got %v, %v; want 260, true", w, ok)
	}
}
