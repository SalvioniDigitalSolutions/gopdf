package gopdf

import (
	"bytes"
	"math"
	"testing"
)

// fragOf is the fragment of the page that reads text.
func fragOf(t *testing.T, data []byte, text string) TextFragment {
	t.Helper()
	for _, f := range fragsOf(t, data) {
		if f.Text == text {
			return f
		}
	}
	t.Fatalf("no fragment reads %q: %+v", text, fragsOf(t, data))
	return TextFragment{}
}

// fitOnRawPage replaces old on a page of the given content, the token
// fitted to its value's width, and gives the page before and after.
func fitOnRawPage(t *testing.T, content, old string) (src, out []byte) {
	t.Helper()
	src = rawPageDoc(t, content)
	u := Update(NewReaderOrFail(t, src))
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := page.replaceTextFlowFit(old, fitTo, true, 0, ""); err != nil || n != 1 {
		t.Fatalf("replaced in %d paragraph(s), %v", n, err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return src, buf.Bytes()
}

// What a page draws after a paragraph that was written again is drawn
// as the document drew it. A stream sets only what it changes: the
// text after a paragraph that ends on a token set smaller named no
// font of its own, and was drawn at the token's size.
func TestFlowTextAfterAParagraphEndingOnAFittedTokenKeepsItsSize(t *testing.T) {
	const after = "This line names no font of its own."
	src, out := fitOnRawPage(t, "BT /F1 11 Tf 1 0 0 1 60 700 Tm "+
		"(Der Vertrag wurde unterzeichnet von Li) Tj "+
		"1 0 0 1 300 600 Tm ("+after+") Tj ET\n", "Li")
	was, is := fragOf(t, src, after), fragOf(t, out, after)
	if math.Abs(is.FontSize-was.FontSize) > 0.01 || math.Abs(is.W-was.W) > 0.01 {
		t.Errorf("the text after the paragraph is set at %.2f, %.2f wide; it was at %.2f, %.2f wide",
			is.FontSize, is.W, was.FontSize, was.W)
	}
}

// And as wide: a line set a little narrower to stay a line (wrapTight)
// left its narrowing to the text after it.
func TestFlowTextAfterALineSetNarrowerKeepsItsWidth(t *testing.T) {
	const after = "This line sets no scaling of its own."
	src, out := fitOnRawPage(t, "BT /F1 11 Tf 1 0 0 1 60 700 Tm "+
		"[(Vertrag) 25 ( mit) 25 ( Marco) 25 ( Bianchi) 25 ( geschlossen)] TJ "+
		"1 0 0 1 300 600 Tm ("+after+") Tj ET\n", "Marco")
	was, is := fragOf(t, src, after), fragOf(t, out, after)
	if math.Abs(is.W-was.W) > 0.01 {
		t.Errorf("the text after the paragraph is %.2f wide, it was %.2f", is.W, was.W)
	}
}

// A page drawn in one text object, each line placed by a move from the
// line before it. A paragraph that grows moves the paragraph the page
// set under it and nothing else: the footer far below keeps its place.
// It is not written again, and it took the move of what was drawn
// before it — off the sheet, at the foot of a page, with no check to
// say so, since by the reckoning it had not moved.
func TestFlowGrowthLeavesWhatARelativeMoveDrawsInPlace(t *testing.T) {
	const footer = "A footer far below, placed by a move."
	src := rawPageDoc(t, "BT /F1 11 Tf 1 0 0 1 60 700 Tm (The first paragraph mentions ACME in) Tj "+
		"0 -14 Td (its first line and ends on the second.) Tj "+
		"0 -28 Td (The second paragraph follows it at the) Tj "+
		"0 -14 Td (distance of one empty line.) Tj "+
		"0 -300 Td ("+footer+") Tj ET\n")
	u := Update(NewReaderOrFail(t, src))
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := page.ReplaceTextFlow("ACME", "a considerably longer corporate name"); err != nil || n != 1 {
		t.Fatalf("replaced in %d paragraph(s), %v", n, err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.Bytes()
	const second = "The second paragraph follows it at the"
	if by := fragOf(t, out, second).Y - fragOf(t, src, second).Y; math.Abs(by-14) > 0.1 {
		t.Fatalf("the paragraph under the one that grew moved by %.1f, want one line (14): "+
			"the page tests nothing", by)
	}
	was, is := fragOf(t, src, footer), fragOf(t, out, footer)
	if math.Abs(is.Y-was.Y) > 0.1 || math.Abs(is.X-was.X) > 0.1 {
		t.Errorf("the footer stands at (%.1f, %.1f), it stood at (%.1f, %.1f)", is.X, is.Y, was.X, was.Y)
	}
}
