package gopdf

import (
	"math"
	"strings"
	"testing"
)

// paragraphFixture writes a wrapped paragraph plus a heading and a footer,
// so reflow can be checked against neighbours that must not move.
func paragraphFixture(t *testing.T) []byte {
	t.Helper()
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(HelveticaBold, 14)
	page.Text(60, 70, "Terms of Service")
	page.SetFont(Helvetica, 11)
	page.TextWrapped(60, 100, 300, 16,
		"The provider grants the customer a limited licence to use the "+
			"service for internal business purposes only, subject to the "+
			"restrictions described in this agreement and any applicable law.")
	page.SetFont(Helvetica, 9)
	page.Text(60, 400, "Page 1 of 1")
	return docBytes(t, doc)
}

func editablePageOf(t *testing.T, data []byte) (*Document, *EditablePage) {
	t.Helper()
	r, err := NewReader(data)
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	doc.Compress = false
	e, err := doc.EditPage(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	return doc, e
}

func TestBlocksGrouping(t *testing.T) {
	_, e := editablePageOf(t, paragraphFixture(t))
	blocks := e.Blocks()
	if len(blocks) != 3 {
		var got []string
		for _, b := range blocks {
			got = append(got, b.Text)
		}
		t.Fatalf("found %d blocks, want 3: %q", len(blocks), got)
	}
	if blocks[0].Text != "Terms of Service" {
		t.Errorf("heading block = %q", blocks[0].Text)
	}
	body := blocks[1]
	if len(body.Lines()) < 3 {
		t.Errorf("body block has %d lines, want at least 3", len(body.Lines()))
	}
	if !strings.HasPrefix(body.Text, "The provider grants") ||
		!strings.HasSuffix(body.Text, "applicable law.") {
		t.Errorf("body text = %q", body.Text)
	}
	if math.Abs(body.LineHeight-16) > 0.01 {
		t.Errorf("line height = %v, want 16", body.LineHeight)
	}
	if body.Width <= 0 || body.Width > 305 {
		t.Errorf("column width = %v, want ~300", body.Width)
	}
	if blocks[2].Text != "Page 1 of 1" {
		t.Errorf("footer block = %q", blocks[2].Text)
	}
}

// TestReflowShorterText replaces a paragraph with text needing fewer
// lines; surrounding blocks must not move.
func TestReflowShorterText(t *testing.T) {
	src := paragraphFixture(t)
	doc, e := editablePageOf(t, src)

	before := map[string][2]float64{}
	for _, run := range e.Runs() {
		before[run.Text] = [2]float64{run.X, run.Y}
	}

	blocks := e.Blocks()
	if err := blocks[1].SetText("A short licence grant."); err != nil {
		t.Fatal(err)
	}

	r2, err := NewReader(docBytes(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	d2 := New()
	e2, err := d2.EditPage(r2, 0)
	if err != nil {
		t.Fatal(err)
	}
	text := e2.ExtractText()
	if !strings.Contains(text, "A short licence grant.") {
		t.Errorf("reflowed text missing: %q", text)
	}
	if strings.Contains(text, "internal business purposes") {
		t.Errorf("old paragraph text survived: %q", text)
	}
	// The heading and footer must be untouched, at the same coordinates.
	for _, keep := range []string{"Terms of Service", "Page 1 of 1"} {
		var found bool
		for _, run := range e2.Runs() {
			if run.Text != keep {
				continue
			}
			found = true
			want := before[keep]
			if math.Abs(run.X-want[0]) > 0.01 || math.Abs(run.Y-want[1]) > 0.01 {
				t.Errorf("%q moved from %v to (%v,%v)", keep, want, run.X, run.Y)
			}
		}
		if !found {
			t.Errorf("%q disappeared", keep)
		}
	}
}

// TestReflowWrapsAcrossLines checks that a longer replacement is broken
// across the paragraph's existing lines rather than overflowing one.
func TestReflowWrapsAcrossLines(t *testing.T) {
	src := paragraphFixture(t)
	doc, e := editablePageOf(t, src)
	blocks := e.Blocks()
	body := blocks[1]
	origLines := len(body.Lines())
	colWidth := body.Width

	// Same length ballpark, different words, so it still fits.
	err := body.SetText("The supplier hereby grants to the client a strictly " +
		"limited right to make use of the platform for internal purposes, " +
		"subject to the terms below.")
	if err != nil {
		t.Fatal(err)
	}

	r2, _ := NewReader(docBytes(t, doc))
	d2 := New()
	e2, err := d2.EditPage(r2, 0)
	if err != nil {
		t.Fatal(err)
	}
	var body2 *TextBlock
	for _, b := range e2.Blocks() {
		if strings.HasPrefix(b.Text, "The supplier hereby") {
			body2 = b
		}
	}
	if body2 == nil {
		t.Fatalf("reflowed paragraph not found: %q", e2.ExtractText())
	}
	if len(body2.Lines()) > origLines {
		t.Errorf("paragraph grew to %d lines, original had %d",
			len(body2.Lines()), origLines)
	}
	// No line may exceed the original column width.
	for i, line := range body2.Lines() {
		if line.Width > colWidth+0.5 {
			t.Errorf("line %d is %v wide, column is %v: %q",
				i, line.Width, colWidth, line.Text)
		}
	}
}

// TestReflowRefusesOverflow is the layout guarantee: text that needs more
// lines than the paragraph has is refused, because reflow cannot move
// whatever follows it down the page.
func TestReflowRefusesOverflow(t *testing.T) {
	src := paragraphFixture(t)
	doc, e := editablePageOf(t, src)
	body := e.Blocks()[1]

	long := strings.Repeat("The provider grants a limited licence. ", 12)
	err := body.SetText(long)
	if err == nil {
		t.Fatal("expected an error for text that does not fit")
	}
	if !strings.Contains(err.Error(), "more line") {
		t.Errorf("unhelpful error: %v", err)
	}
	// Nothing may have changed.
	out := docBytes(t, doc)
	r2, _ := NewReader(out)
	text, _ := r2.PageText(0)
	if !strings.Contains(text, "internal business purposes") {
		t.Errorf("refused reflow still altered the page: %q", text)
	}

	// With an allowance, the same text is accepted and wraps onto the
	// extra lines.
	doc2, e2 := editablePageOf(t, src)
	e2.SetMaxExtraLines(12)
	if err := e2.Blocks()[1].SetText(long); err != nil {
		t.Fatal(err)
	}
	r3, err := NewReader(docBytes(t, doc2))
	if err != nil {
		t.Fatal(err)
	}
	d3 := New()
	e3, err := d3.EditPage(r3, 0)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	for _, run := range e3.Runs() {
		if strings.Contains(run.Text, "limited licence") {
			count++
		}
	}
	if count < 6 {
		t.Errorf("expected the long text spread over many lines, saw %d: %q",
			count, e3.ExtractText())
	}
}

// TestReflowExtraLinesKeepFollowingContent confirms the text line matrix
// is restored, so content positioned after the paragraph stays put even
// when extra lines were inserted.
func TestReflowExtraLinesKeepFollowingContent(t *testing.T) {
	src := paragraphFixture(t)
	_, ref := editablePageOf(t, src)
	var footerBefore [2]float64
	for _, run := range ref.Runs() {
		if run.Text == "Page 1 of 1" {
			footerBefore = [2]float64{run.X, run.Y}
		}
	}

	doc, e := editablePageOf(t, src)
	e.SetMaxExtraLines(4)
	body := e.Blocks()[1]
	if err := body.SetText(body.Text + " Additional clauses apply to renewals, " +
		"terminations and any subsequent amendments to this agreement."); err != nil {
		t.Fatal(err)
	}
	r2, err := NewReader(docBytes(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	d2 := New()
	e2, err := d2.EditPage(r2, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, run := range e2.Runs() {
		if run.Text != "Page 1 of 1" {
			continue
		}
		found = true
		if math.Abs(run.X-footerBefore[0]) > 0.01 ||
			math.Abs(run.Y-footerBefore[1]) > 0.01 {
			t.Errorf("footer moved from %v to (%v,%v) after extra lines",
				footerBefore, run.X, run.Y)
		}
	}
	if !found {
		t.Error("footer disappeared after reflow with extra lines")
	}
}

func TestReplaceTextReflow(t *testing.T) {
	src := paragraphFixture(t)
	doc, e := editablePageOf(t, src)
	n, err := e.ReplaceTextReflow("internal business purposes only",
		"any lawful purpose whatsoever")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("rewrote %d paragraphs, want 1", n)
	}
	r2, err := NewReader(docBytes(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	d2 := New()
	e2, _ := d2.EditPage(r2, 0)
	text := e2.ExtractText()
	if !strings.Contains(strings.Join(strings.Fields(text), " "),
		"any lawful purpose whatsoever") {
		t.Errorf("reflowed text = %q", text)
	}
}

// TestReflowRealWorldParagraph reflows a paragraph in a PDF produced by
// another toolchain.
func TestReflowRealWorldParagraph(t *testing.T) {
	const path = "/System/Library/ProductDocuments/ProductGuides/ENERGY STAR.pdf"
	r, err := Open(path)
	if err != nil {
		t.Skip("system sample PDF not available")
	}
	doc := New()
	e, err := doc.EditPage(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	blocks := e.Blocks()
	var multi int
	for _, b := range blocks {
		if len(b.Lines()) > 1 {
			multi++
		}
	}
	if multi == 0 {
		t.Fatalf("no multi-line paragraphs detected in %d blocks", len(blocks))
	}
	// Shorten a paragraph using only words it already contains.
	n, err := e.ReplaceTextReflow("saves money and helps conserve valuable resources.",
		"conserves resources.")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rewrote %d paragraphs, want 1", n)
	}
	r2, err := NewReader(docBytes(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	text, _ := r2.PageText(0)
	flat := strings.Join(strings.Fields(text), " ")
	if !strings.Contains(flat, "conserves resources.") {
		t.Errorf("reflowed text not found: %q", flat)
	}
	if strings.Contains(flat, "helps conserve valuable") {
		t.Errorf("old text survived: %q", flat)
	}
}
