package gopdf

import (
	"bytes"
	"image"
	"math"
	"strings"
	"testing"
)

// columnsDoc is a page of two columns as a layout program draws one:
// the left column, the right column, a footer, and the title at the top
// of the page last. In the left column the second paragraph follows the
// first at the distance of one empty line.
func columnsDoc(t *testing.T) []byte {
	t.Helper()
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 11)
	page.Text(60, 100, "Left one mentions ACME in its first")
	page.Text(60, 114, "line and ends on its second line.")
	page.Text(60, 142, "Left two follows it at the distance")
	page.Text(60, 156, "of one empty line between them.")
	page.Text(330, 100, "Right one stands beside the first")
	page.Text(330, 114, "and has nothing to do with it.")
	page.Text(330, 142, "Right two stands under it.")
	page.Text(60, 700, "Footer line of the page.")
	page.Text(60, 60, "Title of the page")
	return docBytes(t, doc)
}

// moved is how far down the page the run holding want stands in out
// from where it stood in src.
func moved(t *testing.T, src, out []byte, want string) float64 {
	t.Helper()
	return yOf(t, out, want) - yOf(t, src, want)
}

// A paragraph that grows needs room below it, in its own column: the
// paragraph the page set under it keeps its distance, and nothing else
// on the page moves. The displacement was a running total over the
// paragraphs in the order the file draws them, so a line more in the
// first column moved the second column, the footer, and the title at
// the top of the page, which this page draws last.
func TestFlowGrowthMovesItsOwnColumnOnly(t *testing.T) {
	src := columnsDoc(t)
	_, e, flows := editFlows(t, src)
	if _, err := e.ReplaceTextFlow("ACME", "a considerably longer corporate name"); err != nil {
		t.Fatal(err)
	}
	delta := flows[0].LineDelta()
	if delta <= 0 {
		t.Fatalf("the paragraph did not grow: delta=%d", delta)
	}
	out := saveDoc(t, e)
	if got, want := moved(t, src, out, "Left two"), float64(delta)*flows[0].LineHeight; math.Abs(got-want) > 0.6 {
		t.Errorf("the paragraph under it moved by %.2f, want %.2f", got, want)
	}
	for _, still := range []string{"Left one", "Right one", "Right two", "Footer line", "Title of the page"} {
		if got := moved(t, src, out, still); math.Abs(got) > 0.6 {
			t.Errorf("%q moved by %.2f, and stands in another column, far below, or above", still, got)
		}
	}
}

// What stands well below a paragraph that grows moves only as far as it
// must to keep a line of its own, and what stands below that not at
// all.
func TestFlowGrowthMovesWhatStandsFarBelowOnlyAsFarAsItMust(t *testing.T) {
	src := cascadeDoc(t)
	// One line more: the second paragraph, six lines down, stays.
	_, e, flows := editFlows(t, src)
	if _, err := e.ReplaceTextFlow("ACME", "a considerably longer corporate name"); err != nil {
		t.Fatal(err)
	}
	if flows[0].LineDelta() <= 0 {
		t.Fatalf("the paragraph did not grow: delta=%d", flows[0].LineDelta())
	}
	out := saveDoc(t, e)
	for _, still := range []string{"Second paragraph", "Third paragraph"} {
		if got := moved(t, src, out, still); math.Abs(got) > 0.6 {
			t.Errorf("%q moved by %.2f; there was room above it", still, got)
		}
	}
	// Lines enough to reach it: the second paragraph gives way, by what
	// is needed and no more, and the third stays.
	_, e, flows = editFlows(t, src)
	long := strings.Repeat("and yet another clause of the name ", 8)
	if _, err := e.ReplaceTextFlow("ACME", long); err != nil {
		t.Fatal(err)
	}
	first, second := flows[0], flows[1]
	end := first.Y + float64(first.curLines-1)*first.LineHeight
	want := end + second.LineHeight - second.Y
	if want <= 0 {
		t.Fatalf("fixture: the first paragraph ends at %.0f, short of the second at %.0f", end, second.Y)
	}
	out = saveDoc(t, e)
	if got := moved(t, src, out, "Second paragraph"); math.Abs(got-want) > 0.6 {
		t.Errorf("the second paragraph moved by %.2f, want %.2f", got, want)
	}
	if got := moved(t, src, out, "Third paragraph"); math.Abs(got) > 0.6 {
		t.Errorf("the third paragraph moved by %.2f; there was room above it", got)
	}
}

// A paragraph that loses a line leaves the line empty: nothing on the
// page moves up. (A caption stood under its picture, and a line less in
// the paragraph over the picture pulled it up into it.)
func TestFlowNothingMovesUpWhenAParagraphShrinks(t *testing.T) {
	src := columnsDoc(t)
	_, e, flows := editFlows(t, src)
	if _, err := e.ReplaceTextFlow(
		"Left one mentions ACME in its first line and ends on its second line.", "Tiny."); err != nil {
		t.Fatal(err)
	}
	if flows[0].LineDelta() >= 0 {
		t.Fatalf("the paragraph did not shrink: delta=%d", flows[0].LineDelta())
	}
	out := saveDoc(t, e)
	for _, still := range []string{"Left two", "Right one", "Footer line", "Title of the page"} {
		if got := moved(t, src, out, still); math.Abs(got) > 0.6 {
			t.Errorf("%q moved by %.2f", still, got)
		}
	}
}

// Room made under a paragraph is given back when the paragraph is short
// again: the one under it stands where the document set it.
func TestFlowRoomIsGivenBack(t *testing.T) {
	src := columnsDoc(t)
	_, e, flows := editFlows(t, src)
	if _, err := e.ReplaceTextFlow("ACME", "a considerably longer corporate name"); err != nil {
		t.Fatal(err)
	}
	if flows[0].LineDelta() <= 0 {
		t.Fatalf("the paragraph did not grow: delta=%d", flows[0].LineDelta())
	}
	if got := flows[1].shiftPoints(); got < 1 {
		t.Fatalf("the paragraph under it did not move: %.2f", got)
	}
	if _, err := e.ReplaceTextFlow("a considerably longer corporate name", "XY"); err != nil {
		t.Fatal(err)
	}
	if got := moved(t, src, saveDoc(t, e), "Left two"); math.Abs(got) > 0.6 {
		t.Errorf("the paragraph under it stands %.2f from where the document set it", got)
	}
}

// A token the document's own font cannot write is fitted all the same:
// measured in the stand-in face it is set in. A subset has no bracket,
// so the token had no width in it, was not fitted at all, and went in
// at the run's full size — wider than its value, on every page that
// came to be laid out again.
func TestFlowTokenASubsetCannotWriteIsFitted(t *testing.T) {
	const text = "Der Vertrag mit Massimo gilt ab heute."
	src := subsetFontDoc(t, text, false)
	var value TextFragment
	for _, f := range fragsOf(t, src) {
		value = f
	}
	bodySize := value.FontSize
	// The widths of this subset are 600 a letter.
	want := float64(len("Massimo")) * 600 / 1000 * bodySize

	u := Update(NewReaderOrFail(t, src))
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := page.replaceTextFlowFit("Massimo", fitTo, true, 0, ""); err != nil || n != 1 {
		t.Fatalf("replaced %d, %v", n, err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range fragsOf(t, buf.Bytes()) {
		if !strings.Contains(f.Text, fitTo) {
			continue
		}
		found = true
		if f.FontSize >= bodySize-0.01 {
			t.Errorf("the token is set at %.2f, the size of the text around it: it was not fitted", f.FontSize)
		}
		if f.W > want+0.5 {
			t.Errorf("the token is %.1f wide, its value was %.1f", f.W, want)
		}
	}
	if !found {
		t.Fatalf("the token is not on the page: %+v", fragsOf(t, buf.Bytes()))
	}
}

// shortValuesDoc is a paragraph of two full lines that names a value
// of two letters eight times, over a picture where under says so: the
// token asked for, set at the floor, is wider than each of them, and
// the paragraph needs a third line for it.
func shortValuesDoc(t *testing.T, under bool) []byte {
	t.Helper()
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 11)
	page.Text(60, 100, "Li und Li und Li und Li und Li und Li und Li und Li")
	page.Text(60, 114, "kamen am folgenden Morgen zur Unterschrift her.")
	if under {
		img := image.NewGray(image.Rect(0, 0, 40, 40))
		for i := range img.Pix {
			img.Pix[i] = uint8(40 + i%160)
		}
		pic, err := doc.AddImage(img)
		if err != nil {
			t.Fatal(err)
		}
		page.DrawImage(pic, 60, 122, 300, 120)
	}
	return docBytes(t, doc)
}

// fitShortValues replaces the two-letter value of shortValuesDoc, the
// shorter stand-in "[R]" named, and gives the page's text and lines.
func fitShortValues(t *testing.T, src []byte) (string, int) {
	t.Helper()
	u := Update(NewReaderOrFail(t, src))
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := page.replaceTextFlowFit("Li", fitTo, true, 0, "[R]"); err != nil || n != 1 {
		t.Fatalf("replaced in %d paragraph(s), %v", n, err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	text, err := NewReaderOrFail(t, buf.Bytes()).PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	return collapse(text), len(lineYs(t, buf.Bytes()))
}

// A page with room for the lines it costs is given the token that was
// asked for, set at the floor where the floor leaves it wider than its
// value: the paragraph is a line longer, as it always was on such a
// page, and the shorter stand-in is not taken.
func TestFlowKeepsTheTokenWhereThePageHasRoom(t *testing.T) {
	text, lines := fitShortValues(t, shortValuesDoc(t, false))
	if n := strings.Count(text, fitTo); n != 8 {
		t.Errorf("the token stands %d times, the value stood 8 times: %q", n, text)
	}
	if strings.Contains(text, "[R]") {
		t.Errorf("the shorter stand-in was taken on a page with room: %q", text)
	}
	if lines <= 2 {
		t.Fatalf("the paragraph has %d lines: the token did not make it longer, "+
			"and the page tests nothing", lines)
	}
}

// A page with no room for them — here a picture under the paragraph —
// takes the shorter stand-in the caller named wherever the token was
// the wider, as the editor that keeps a page as it was takes it: every
// value keeps its width and the paragraph its lines. The page was
// refused, and its values went under bars.
func TestFlowTakesTheShorterStandInWhereThePageHasNoRoom(t *testing.T) {
	text, lines := fitShortValues(t, shortValuesDoc(t, true))
	if n := strings.Count(text, "[R]"); n != 8 {
		t.Errorf("the shorter stand-in stands %d times, the value stood 8 times: %q", n, text)
	}
	if strings.Contains(text, fitTo) {
		t.Errorf("the token was set where it has no room: %q", text)
	}
	if lines != 2 {
		t.Errorf("the paragraph has %d lines, it had 2", lines)
	}
	// Without a stand-in to take, such a page is refused as before.
	u := Update(NewReaderOrFail(t, shortValuesDoc(t, true)))
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := page.replaceTextFlowFit("Li", fitTo, true, 0, ""); err == nil ||
		!strings.Contains(err.Error(), "no longer fits the page") {
		t.Errorf("no stand-in named, a line more into the picture: %v", err)
	}
}

// A line the page set a hair tighter than its font's widths add up to
// (kerns between its words) stays one line when it is set again with a
// replacement no wider than what it replaces. Measured against the
// width it was drawn in, a heading of one line was wider than itself:
// its last word went to a line of its own, under the picture below it.
func TestFlowALineSetTighterThanItsFontStaysALine(t *testing.T) {
	src := rawPageDoc(t, "BT /F1 11 Tf 1 0 0 1 60 700 Tm "+
		"[(Vertrag) 25 ( mit) 25 ( Marco) 25 ( Bianchi) 25 ( geschlossen)] TJ ET\n")
	u := Update(NewReaderOrFail(t, src))
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := page.replaceTextFlowFit("Marco", fitTo, true, 0, ""); err != nil || n != 1 {
		t.Fatalf("replaced %d, %v", n, err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	if lines := lineYs(t, buf.Bytes()); len(lines) != 1 {
		t.Errorf("the line became %d lines", len(lines))
	}
	// And it ends no further right than it did.
	end := func(data []byte) float64 {
		right := 0.0
		for _, f := range fragsOf(t, data) {
			right = math.Max(right, f.X+f.W)
		}
		return right
	}
	if was, is := end(src), end(buf.Bytes()); is > was+0.3 {
		t.Errorf("the line ends at %.2f, it ended at %.2f", is, was)
	}
}

// A paragraph that grows into the picture under it is refused in the
// words a page that grows off the sheet is refused in: its last lines
// would stand over the picture or lie under it.
func TestFlowGrowthIntoAPictureIsRefused(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 11)
	page.Text(60, 100, "The first paragraph mentions ACME in")
	page.Text(60, 114, "its first line and ends on the second.")
	img := image.NewGray(image.Rect(0, 0, 40, 40))
	for i := range img.Pix {
		img.Pix[i] = uint8(40 + i%160)
	}
	pic, err := doc.AddImage(img)
	if err != nil {
		t.Fatal(err)
	}
	page.DrawImage(pic, 60, 122, 200, 120)
	src := docBytes(t, doc)

	replace := func(to string) error {
		u := Update(NewReaderOrFail(t, src))
		p, err := u.Page(0)
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.ReplaceTextFlow("ACME", to)
		return err
	}
	if err := replace("a considerably longer corporate name"); err == nil ||
		!strings.Contains(err.Error(), "no longer fits the page") {
		t.Errorf("a line more, into the picture: %v", err)
	}
	// One that keeps its length is no matter of the picture's.
	if err := replace("ABCD"); err != nil {
		t.Errorf("a replacement of the same length was refused: %v", err)
	}
}
