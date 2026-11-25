package gopdf

import (
	"bytes"
	"image"
	"math"
	"strings"
	"testing"
)

// fitValues replaces each value on page one of src, the token fitted to
// its value's width as the paragraph engine does it for a caller that
// asks for the page it had, and gives the page that comes of it.
func fitValues(t *testing.T, src []byte, values ...string) []byte {
	t.Helper()
	u := Update(NewReaderOrFail(t, src))
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range values {
		if n, err := page.replaceTextFlowFit(v, fitTo, true, 0, ""); err != nil || n == 0 {
			t.Fatalf("%q: replaced in %d paragraph(s), %v", v, n, err)
		}
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// linesOf reads page one line by line: the text of each baseline, in
// the order the lines stand.
func linesOf(t *testing.T, data []byte) []string {
	t.Helper()
	var ys []float64
	text := map[float64]string{}
	for _, f := range fragsOf(t, data) {
		if strings.TrimSpace(f.Text) == "" {
			continue
		}
		if _, seen := text[f.Y]; !seen {
			ys = append(ys, f.Y)
		}
		text[f.Y] += f.Text
	}
	out := make([]string, len(ys))
	for i, y := range ys {
		out[i] = collapse(text[y])
	}
	return out
}

// A heading over its text, set in the text's size at the text's margin,
// is a line of the paragraph the engine reads there. A token shorter
// than the value it replaces left the heading short, and wrapped anew
// the heading took the first words of the text under it.
func TestFlowAHeadingKeepsItsLine(t *testing.T) {
	src := flowDoc(t,
		"Report on Massimo Bernasconi",
		"The text under the heading runs to the full width of its",
		"column and ends on this second line of the paragraph.")
	out := fitValues(t, src, "Massimo Bernasconi")
	want := []string{
		"Report on " + fitTo,
		"The text under the heading runs to the full width of its",
		"column and ends on this second line of the paragraph.",
	}
	got := linesOf(t, out)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("the page reads\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// The lines nothing was replaced in keep the operators the document
// drew them with: set again from their words, a line loses what its
// producer did between them — here the kerns of a TJ.
func TestFlowLinesNothingWasReplacedInAreNotWrittenAgain(t *testing.T) {
	const kerned = "[(The text under) -20 ( it is set with) 15 ( kerns between its words,)] TJ"
	src := rawPageDoc(t, "BT /F1 11 Tf 1 0 0 1 60 700 Tm (Report on Massimo Bernasconi) Tj "+
		"1 0 0 1 60 686 Tm "+kerned+" "+
		"1 0 0 1 60 672 Tm (and ends on a line of plain words.) Tj ET\n")
	out := fitValues(t, src, "Massimo Bernasconi")
	content := string(pageContent(t, out))
	if !strings.Contains(content, kerned) {
		t.Errorf("the kerned line was written again:\n%s", content)
	}
	if !strings.Contains(content, "(and ends on a line of plain words.) Tj") {
		t.Errorf("the last line was written again:\n%s", content)
	}
	if got := linesOf(t, out); len(got) != 3 || got[0] != "Report on "+fitTo {
		t.Errorf("the page reads %q", got)
	}
}

// centredDoc is a title as a word processor's PDF sets one: every line
// begins at the margin with a space, and stands in the middle of the
// page by the word spacing that makes that space wide, or by a move
// after it. An empty line stands over the title and under it.
func centredDoc(t *testing.T) []byte {
	t.Helper()
	sp := Helvetica.TextWidth(" ", 11)
	acme := Helvetica.TextWidth("ACME", 11)
	before := Helvetica.TextWidth("Before", 11)
	return rawPageDoc(t, "BT /F1 11 Tf "+
		"1 0 0 1 60 700 Tm 0 Tw ( ) Tj "+
		"1 0 0 1 60 686 Tm 100 Tw ( ACME ) Tj 0 Tw "+fl(sp+100+acme+sp)+" 0 Td (HOLDING) Tj "+
		"1 0 0 1 60 672 Tm 120 Tw ( Before ) Tj 0 Tw "+fl(sp+120+before+sp)+" 0 Td (the) Tj "+
		"1 0 0 1 60 658 Tm ( ) Tj "+fl(130.0)+" 0 Td (COMMISSION OF TRADE) Tj "+
		"1 0 0 1 60 644 Tm ( ) Tj "+
		"1 0 0 1 60 630 Tm (Release No. 2494) Tj ET\n")
}

// The lines of a centred title stay lines, each where it stood, and so
// do the empty lines around them. Wrapped anew they were one line: the
// space that centred each of them was dropped where it began a line and
// drawn at its full width between the words, and the line ran off the
// sheet.
func TestFlowACentredTitleKeepsItsLines(t *testing.T) {
	src := centredDoc(t)
	out := fitValues(t, src, "ACME HOLDING", "COMMISSION OF TRADE")
	want := []string{fitTo, "Before the", fitTo, "Release No. 2494"}
	got := linesOf(t, out)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the page reads\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	// Each line on its own baseline, the empty lines between them kept.
	wasY, isY := lineYs(t, src), lineYs(t, out)
	if len(wasY) != len(isY) {
		t.Fatalf("%d lines, there were %d", len(isY), len(wasY))
	}
	for i := range wasY {
		if math.Abs(wasY[i]-isY[i]) > 0.01 {
			t.Errorf("line %d stands at %.1f, it stood at %.1f", i, isY[i], wasY[i])
		}
	}
	// A token begins where its line's first word began: set in by wide
	// spaces (ACME) and by a move (COMMISSION).
	starts := func(data []byte, word string) float64 {
		for _, f := range fragsOf(t, data) {
			if i := strings.Index(f.Text, word); i >= 0 {
				return f.X + leadOf(f, i)
			}
		}
		t.Fatalf("no fragment holds %q", word)
		return 0
	}
	tokens := 0
	for _, f := range fragsOf(t, out) {
		i := strings.Index(f.Text, fitTo)
		if i < 0 {
			continue
		}
		at := f.X + leadOf(f, i)
		want := starts(src, "ACME")
		if tokens == 1 {
			want = starts(src, "COMMISSION")
		}
		if math.Abs(at-want) > 0.6 {
			t.Errorf("token %d begins at %.1f, its line's first word began at %.1f", tokens, at, want)
		}
		tokens++
	}
	if tokens != 2 {
		t.Errorf("%d tokens on the page, want 2", tokens)
	}
	// And the line between them is the document's own.
	for _, word := range []string{"Before", "the"} {
		if was, is := starts(src, word), starts(out, word); math.Abs(was-is) > 0.01 {
			t.Errorf("%q begins at %.1f, it began at %.1f", word, is, was)
		}
	}
}

// leadOf is how far into a fragment its byte i is drawn.
func leadOf(f TextFragment, i int) float64 {
	n := len([]rune(f.Text[:i]))
	if n < len(f.Advances) {
		return f.Advances[n]
	}
	return 0
}

// A heading that no longer fits its line takes a second one, and the
// text under it begins on a line of its own: what a line can no longer
// hold runs on into the next where the column ended the line, not
// where the document did.
func TestFlowAHeadingThatGrowsTakesALineOfItsOwn(t *testing.T) {
	src := flowDoc(t,
		"Li, Li, Li, Li, Li, Li, Li and Li signed",
		"The text under the heading runs to the full width of the column",
		"it stands in and ends on this second line of the paragraph here.")
	out := fitValues(t, src, "Li")
	got := linesOf(t, out)
	if len(got) != 4 {
		t.Fatalf("the page has %d lines, want the heading on two and the text on two:\n  %s",
			len(got), strings.Join(got, "\n  "))
	}
	if !strings.HasSuffix(got[1], "signed") || strings.Contains(got[1], "The text") {
		t.Errorf("the heading's second line reads %q", got[1])
	}
	if got[2] != "The text under the heading runs to the full width of the column" {
		t.Errorf("the text under the heading begins %q", got[2])
	}
}

// Where the column ended a line, what the line can no longer hold runs
// on into the next, as it does in a paragraph wrapped anew: the
// paragraph is no line longer than its words need.
func TestFlowRunningTextRunsOnIntoTheNextLine(t *testing.T) {
	src := flowDoc(t,
		"Li and Li and Li and Li came on the following morning to",
		"sign the agreement that had been drawn up the day before in",
		"the office.")
	out := fitValues(t, src, "Li")
	got := linesOf(t, out)
	if len(got) != 3 {
		t.Fatalf("the paragraph has %d lines, it had 3:\n  %s", len(got), strings.Join(got, "\n  "))
	}
	if !strings.Contains(got[1], "sign the agreement") || strings.HasPrefix(got[1], "sign") {
		t.Errorf("the second line reads %q: what the first could not hold did not run on into it", got[1])
	}
}

// noRoomDoc is a paragraph of three lines over a picture, where under
// says so. A value in its first line becomes a shorter token, and seven
// values of two letters in its second become a wider one each: with
// its lines kept the first line stays short and the paragraph needs a
// fourth; wrapped anew, the first line takes what the second can no
// longer hold, and three lines hold it all.
func noRoomDoc(t *testing.T, under bool) []byte {
	t.Helper()
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 11)
	page.Text(60, 100, "Report by Massimo Bernasconi and by the others who were there")
	page.Text(60, 114, "with Li and Li and Li and Li and Li and Li and Li, who signed it")
	page.Text(60, 128, "all at once in the office of the firm.")
	if under {
		img := image.NewGray(image.Rect(0, 0, 40, 40))
		for i := range img.Pix {
			img.Pix[i] = uint8(40 + i%160)
		}
		pic, err := doc.AddImage(img)
		if err != nil {
			t.Fatal(err)
		}
		page.DrawImage(pic, 60, 136, 300, 120)
	}
	return docBytes(t, doc)
}

// A page with no room for the line that keeping a paragraph's lines
// costs is wrapped anew, as every page was: the paragraph takes the
// fewest lines its words need, and the page is saved rather than
// refused.
func TestFlowAPageWithNoRoomIsWrappedAnew(t *testing.T) {
	const first = "Report by " + fitTo + " and by the others who were there"
	// With room: the lines kept, and one line more.
	got := linesOf(t, fitValues(t, noRoomDoc(t, false), "Massimo Bernasconi", "Li"))
	if len(got) != 4 || got[0] != first {
		t.Fatalf("with room, want the first line kept and four lines:\n  %s", strings.Join(got, "\n  "))
	}
	// Without: three lines, the first one filled.
	got = linesOf(t, fitValues(t, noRoomDoc(t, true), "Massimo Bernasconi", "Li"))
	if len(got) != 3 {
		t.Fatalf("over a picture the paragraph has %d lines, want 3:\n  %s", len(got), strings.Join(got, "\n  "))
	}
	if got[0] == first {
		t.Errorf("the first line was kept, and the paragraph still has three lines: the page tests nothing")
	}
}

// A first line set in by a move after the space it begins with begins
// as far in when it is written again.
func TestFlowAFirstLineKeepsItsIndent(t *testing.T) {
	src := rawPageDoc(t, "BT /F1 11 Tf 1 0 0 1 60 700 Tm "+
		"[( ) -2500 (The firm of Massimo Bernasconi came to the)] TJ "+
		"1 0 0 1 60 686 Tm (meeting on the following morning and signed.) Tj ET\n")
	out := fitValues(t, src, "Massimo Bernasconi")
	starts := func(data []byte) float64 {
		for _, f := range fragsOf(t, data) {
			if i := strings.Index(f.Text, "The firm"); i >= 0 {
				return f.X + leadOf(f, i)
			}
		}
		t.Fatalf("no fragment holds the first words: %+v", fragsOf(t, data))
		return 0
	}
	if was, is := starts(src), starts(out); math.Abs(was-is) > 0.5 {
		t.Errorf("the first line begins at %.1f, it began at %.1f", is, was)
	}
	if got := linesOf(t, out); len(got) != 2 || !strings.Contains(got[0], fitTo) {
		t.Errorf("the page reads %q", got)
	}
}

// twoParagraphsDoc is noRoomDoc's paragraph with a second one under it
// in its column, and a picture under that where under says so. The
// second names a value of two letters eight times and needs a third
// line for the tokens, however it is wrapped.
func twoParagraphsDoc(t *testing.T, under bool) []byte {
	t.Helper()
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 11)
	page.Text(60, 100, "Report by Massimo Bernasconi and by the others who were there")
	page.Text(60, 114, "with Li and Li and Li and Li and Li and Li and Li, who signed it")
	page.Text(60, 128, "all at once in the office of the firm.")
	page.Text(60, 156, "Jo und Jo und Jo und Jo und Jo und Jo und Jo und Jo")
	page.Text(60, 170, "kamen am folgenden Morgen zur Unterschrift.")
	if under {
		img := image.NewGray(image.Rect(0, 0, 40, 40))
		for i := range img.Pix {
			img.Pix[i] = uint8(40 + i%160)
		}
		pic, err := doc.AddImage(img)
		if err != nil {
			t.Fatal(err)
		}
		page.DrawImage(pic, 60, 190, 300, 120)
	}
	return docBytes(t, doc)
}

// The lines a paragraph was given to keep its own are given back when a
// later replacement finds the page without room: every paragraph of the
// page is then wrapped anew, as every page was, before the shorter
// stand-in is taken for a token the page could hold.
func TestFlowKeptLinesAreGivenBackWhereThePageNeedsThem(t *testing.T) {
	replace := func(src []byte) []string {
		u := Update(NewReaderOrFail(t, src))
		page, err := u.Page(0)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range []string{"Massimo Bernasconi", "Li", "Jo"} {
			if n, err := page.replaceTextFlowFit(v, fitTo, true, 0, "[R]"); err != nil || n == 0 {
				t.Fatalf("%q: replaced in %d paragraph(s), %v", v, n, err)
			}
		}
		var buf bytes.Buffer
		if _, err := u.WriteTo(&buf); err != nil {
			t.Fatal(err)
		}
		return linesOf(t, buf.Bytes())
	}
	const first = "Report by " + fitTo + " and by the others who were there"
	// With room: the first paragraph keeps its lines and takes a fourth,
	// the second takes a third.
	got := replace(twoParagraphsDoc(t, false))
	if len(got) != 7 || got[0] != first {
		t.Fatalf("with room, want four lines and three, the first line kept:\n  %s", strings.Join(got, "\n  "))
	}
	// Over a picture there is room for six lines, not seven: the first
	// paragraph gives its fourth line back, and every token stands.
	got = replace(twoParagraphsDoc(t, true))
	all := strings.Join(got, "\n  ")
	if len(got) != 6 {
		t.Fatalf("over a picture the page has %d lines, want 6:\n  %s", len(got), all)
	}
	if strings.Contains(all, "[R]") || strings.Count(all, fitTo) != 16 {
		t.Errorf("want the token sixteen times and the shorter stand-in nowhere:\n  %s", all)
	}
}
