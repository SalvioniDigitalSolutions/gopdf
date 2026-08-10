package gopdf

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

// styledFixture draws three lines in one font and colour, so a restyle of
// the middle one can be checked against its neighbours.
func styledFixture(t *testing.T) []byte {
	t.Helper()
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 12)
	page.SetFillColor(RGB(20, 20, 20))
	page.Text(60, 100, "first line")
	page.Text(60, 130, "second line")
	page.Text(60, 160, "third line")
	return docBytes(t, doc)
}

func restyleRun(t *testing.T, src []byte, want string, s TextStyle) []byte {
	t.Helper()
	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	u := Update(r)
	u.SetCompress(false)
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	var target *TextRun
	for _, run := range page.Runs() {
		if run.Text == want {
			target = run
		}
	}
	if target == nil {
		t.Fatalf("run %q not found", want)
	}
	if err := target.Restyle(s); err != nil {
		t.Fatal(err)
	}
	return updatedBytes(t, u)
}

func TestRestyleSize(t *testing.T) {
	src := styledFixture(t)
	out := restyleRun(t, src, "second line", TextStyle{Size: 22})
	verifyXref(t, out)

	r, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	page, err := doc.EditPage(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	sizes := map[string]float64{}
	for _, run := range page.Runs() {
		sizes[run.Text] = run.FontSize
	}
	if math.Abs(sizes["second line"]-22) > 0.01 {
		t.Errorf("restyled run is %vpt, want 22", sizes["second line"])
	}
	// The neighbours must be untouched: the size change is undone after
	// the run it applies to.
	for _, other := range []string{"first line", "third line"} {
		if math.Abs(sizes[other]-12) > 0.01 {
			t.Errorf("%q became %vpt, want 12", other, sizes[other])
		}
	}
}

func TestRestyleColor(t *testing.T) {
	src := styledFixture(t)
	red := RGB(220, 30, 30)
	out := restyleRun(t, src, "second line", TextStyle{Color: &red})

	if !bytes.Contains(out, []byte("0.863 0.118 0.118 rg")) {
		t.Error("the new colour was not emitted")
	}
	// The previous colour operation must be restored right after.
	if !bytes.Contains(out, []byte("0.078 0.078 0.078 rg")) {
		t.Error("the original colour was not restored after the run")
	}
	r, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := r.PageText(0)
	for _, want := range []string{"first line", "second line", "third line"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lost: %q missing", want)
		}
	}
}

func TestRestyleFont(t *testing.T) {
	src := styledFixture(t)
	out := restyleRun(t, src, "second line", TextStyle{Font: TimesBoldItalic, Size: 15})
	verifyXref(t, out)

	if !bytes.Contains(out, []byte("Times-BoldItalic")) {
		t.Error("the new font was not embedded in the update")
	}
	r, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	text, err := r.PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "second line") {
		t.Errorf("restyled text lost: %q", text)
	}
	// The font must be in the page's resources under a prefixed name.
	res, _ := r.resolve(r.pages[0].dict["Resources"]).(Dict)
	fonts, _ := r.resolve(res["Font"]).(Dict)
	var found bool
	for _, v := range fonts {
		fd, _ := r.resolve(v).(Dict)
		if base, _ := r.resolve(fd["BaseFont"]).(Name); base == "Times-BoldItalic" {
			found = true
		}
	}
	if !found {
		t.Error("the new font is not in the page's resource dictionary")
	}
}

func TestRestyleEmbeddedFont(t *testing.T) {
	font, err := LoadFont(testFontPath(t))
	if err != nil {
		t.Fatal(err)
	}
	src := styledFixture(t)
	r, _ := NewReader(src)
	u := Update(r)
	u.SetCompress(false)
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range page.Runs() {
		if run.Text != "third line" {
			continue
		}
		if err := run.Restyle(TextStyle{Font: font, Size: 14}); err != nil {
			t.Fatal(err)
		}
	}
	out := updatedBytes(t, u)

	r2, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	text, err := r2.PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	// The embedded font must carry a ToUnicode map, so the restyled run
	// still extracts.
	if !strings.Contains(text, "third line") {
		t.Errorf("restyled text does not extract: %q", text)
	}
	if !bytes.Contains(out, []byte("/Subtype /Type0")) {
		t.Error("the embedded font was not written as a composite font")
	}
}

func TestRestyleRejectsUnavailableGlyph(t *testing.T) {
	font, err := LoadFont(testFontPath(t))
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 12)
	page.Text(60, 100, "漢字テスト")
	src := docBytes(t, doc)
	_ = src

	// A standard font cannot hold characters outside WinAnsi.
	doc2 := New()
	p2 := doc2.AddPage()
	p2.SetFont(font, 12)
	p2.Text(60, 100, "Ωмир")
	r, err := NewReader(docBytes(t, doc2))
	if err != nil {
		t.Fatal(err)
	}
	u := Update(r)
	page2, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	runs := page2.Runs()
	if len(runs) == 0 {
		t.Fatal("no runs found")
	}
	err = runs[0].Restyle(TextStyle{Font: Helvetica})
	if err == nil {
		t.Fatal("expected an error restyling Cyrillic into a standard font")
	}
	if !strings.Contains(err.Error(), "cannot represent") &&
		!strings.Contains(err.Error(), "no glyph") {
		t.Errorf("unhelpful error: %v", err)
	}
}

// TestRestyleMultiByteText guards a crash: the encoded bytes are one per
// rune, so they must not be indexed with the byte offsets a range over a
// string produces.
func TestRestyleMultiByteText(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 12)
	// Characters outside ASCII that WinAnsi does cover.
	page.Text(60, 100, "ENERGY STAR® Compliance — café")
	src := docBytes(t, doc)

	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	u := Update(r)
	page2, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	runs := page2.Runs()
	if len(runs) == 0 {
		t.Fatal("no runs found")
	}
	if err := runs[0].Restyle(TextStyle{Font: HelveticaBold, Size: 15}); err != nil {
		t.Fatalf("restyling text with multi-byte characters failed: %v", err)
	}
	r2, err := NewReader(updatedBytes(t, u))
	if err != nil {
		t.Fatal(err)
	}
	text, _ := r2.PageText(0)
	if !strings.Contains(text, "ENERGY STAR® Compliance — café") {
		t.Errorf("restyled text = %q", text)
	}
}

// TestRestyleScaledTextMatrix covers the common idiom of setting a
// nominal size with Tf and scaling it through the text matrix: the
// requested size must be the one the reader ends up seeing.
func TestRestyleScaledTextMatrix(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.doc.addFont(Helvetica)
	// /F1 1 Tf with a matrix scaling by 12 renders at 12pt.
	page.op("BT /F1 1 Tf 12 0 0 12 60 700 Tm (scaled heading) Tj ET")
	src := docBytes(t, doc)

	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	before := New()
	bp, err := before.EditPage(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := bp.Runs()[0].FontSize; math.Abs(got-12) > 0.01 {
		t.Fatalf("fixture renders at %vpt, want 12", got)
	}

	u := Update(r)
	page2, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := page2.Runs()[0].Restyle(TextStyle{Size: 18}); err != nil {
		t.Fatal(err)
	}
	r2, err := NewReader(updatedBytes(t, u))
	if err != nil {
		t.Fatal(err)
	}
	d2 := New()
	p2, err := d2.EditPage(r2, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := p2.Runs()[0].FontSize
	if math.Abs(got-18) > 0.01 {
		t.Errorf("restyled run renders at %vpt, want the 18 that was asked for", got)
	}
}

func TestRestyleValidation(t *testing.T) {
	src := styledFixture(t)
	r, _ := NewReader(src)
	u := Update(r)
	page, _ := u.Page(0)
	run := page.Runs()[0]

	if err := run.Restyle(TextStyle{}); err != nil {
		t.Errorf("an empty restyle should do nothing, got %v", err)
	}
	if err := run.Restyle(TextStyle{Size: -3}); err == nil {
		t.Error("expected an error for a negative size")
	}
	// A run with no owning page cannot take a new font.
	loose := &TextRun{Text: "x"}
	if err := loose.Restyle(TextStyle{Font: Helvetica}); err == nil {
		t.Error("expected an error restyling a run with no page")
	}
}

// TestRestyleOnEditablePage covers the rebuild path as well as the
// in-place one.
func TestRestyleOnEditablePage(t *testing.T) {
	src := styledFixture(t)
	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	doc.Compress = false
	page, err := doc.EditPage(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	blue := RGB(20, 60, 200)
	for _, run := range page.Runs() {
		if run.Text != "first line" {
			continue
		}
		if err := run.Restyle(TextStyle{Font: HelveticaBold, Size: 18, Color: &blue}); err != nil {
			t.Fatal(err)
		}
	}
	out := docBytes(t, doc)
	verifyXref(t, out)

	r2, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	d2 := New()
	p2, err := d2.EditPage(r2, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range p2.Runs() {
		switch run.Text {
		case "first line":
			if math.Abs(run.FontSize-18) > 0.01 {
				t.Errorf("restyled run is %vpt, want 18", run.FontSize)
			}
		case "second line", "third line":
			if math.Abs(run.FontSize-12) > 0.01 {
				t.Errorf("%q became %vpt, want 12", run.Text, run.FontSize)
			}
		}
	}
}
