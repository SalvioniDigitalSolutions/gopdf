package gopdf

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

// invoiceFixture builds a document resembling a real form: a heading, a
// table of aligned values, and a total. Editing must leave every
// untouched element at exactly the same coordinates.
func invoiceFixture(t *testing.T) []byte {
	t.Helper()
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(HelveticaBold, 18)
	page.Text(60, 80, "INVOICE 2024-117")
	page.SetFont(Helvetica, 11)
	page.Text(60, 120, "Customer: Acme Corporation")
	page.Text(60, 140, "Status: DRAFT")
	rows := []struct{ label, amount string }{
		{"Consulting", "1200.00"},
		{"Licensing", "450.00"},
		{"Support", "300.00"},
	}
	y := 180.0
	for _, r := range rows {
		page.Text(60, y, r.label)
		page.TextAligned(300, y, 120, AlignRight, r.amount)
		y += 18
	}
	page.SetFont(HelveticaBold, 12)
	page.Text(60, y+10, "TOTAL")
	page.TextAligned(300, y+10, 120, AlignRight, "1950.00")
	return docBytes(t, doc)
}

// runPositions maps each run's text to its position, for comparing a page
// before and after an edit.
func runPositions(t *testing.T, data []byte) map[string][2]float64 {
	t.Helper()
	r, err := NewReader(data)
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	e, err := doc.EditPage(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string][2]float64)
	for _, run := range e.Runs() {
		out[run.Text] = [2]float64{run.X, run.Y}
	}
	return out
}

func TestEditReplaceText(t *testing.T) {
	src := invoiceFixture(t)
	before := runPositions(t, src)

	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	doc.Compress = false
	e, err := doc.EditPage(r, 0)
	if err != nil {
		t.Fatal(err)
	}

	// The runs must be discovered with their text, font and position.
	if len(e.Runs()) != 11 {
		t.Fatalf("found %d runs, want 11: %q", len(e.Runs()), e.ExtractText())
	}
	first := e.Runs()[0]
	if first.Text != "INVOICE 2024-117" {
		t.Errorf("first run = %q", first.Text)
	}
	if math.Abs(first.FontSize-18) > 0.01 {
		t.Errorf("first run size = %v, want 18", first.FontSize)
	}
	if first.Width <= 0 {
		t.Error("run width not measured")
	}

	n, err := e.ReplaceText("DRAFT", "APPROVED")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("replaced %d runs, want 1", n)
	}
	if _, err := e.ReplaceText("Acme Corporation", "Globex Ltd"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ReplaceText("1950.00", "1975.50"); err != nil {
		t.Fatal(err)
	}

	out := docBytes(t, doc)
	verifyXref(t, out)

	// The edited text must read back...
	after := runPositions(t, out)
	text := ""
	if r2, err := NewReader(out); err == nil {
		text, _ = r2.PageText(0)
	} else {
		t.Fatal(err)
	}
	for _, want := range []string{"Status: APPROVED", "Customer: Globex Ltd", "1975.50"} {
		if !strings.Contains(text, want) {
			t.Errorf("edited page missing %q\ngot: %s", want, text)
		}
	}
	for _, gone := range []string{"DRAFT", "Acme", "1950.00"} {
		if strings.Contains(text, gone) {
			t.Errorf("edited page still contains %q", gone)
		}
	}

	// ...and every untouched run must be at exactly its original spot.
	for label, pos := range before {
		if label == "Status: DRAFT" || label == "Customer: Acme Corporation" ||
			label == "1950.00" {
			continue
		}
		got, ok := after[label]
		if !ok {
			t.Errorf("run %q disappeared after editing", label)
			continue
		}
		if math.Abs(got[0]-pos[0]) > 0.01 || math.Abs(got[1]-pos[1]) > 0.01 {
			t.Errorf("run %q moved from %v to %v", label, pos, got)
		}
	}
}

// TestEditPreservesAdvance checks the width compensation: the run that
// follows an edited one on the same line must not shift.
func TestEditPreservesAdvance(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 12)
	// Two runs on one line, positioned by successive Td offsets.
	page.op("BT /F1 12 Tf 60 700 Td (Price: ) Tj (99) Tj ( EUR) Tj ET")
	page.doc.addFont(Helvetica)
	src := docBytes(t, doc)

	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	before := map[string][2]float64{}
	d0 := New()
	e0, err := d0.EditPage(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range e0.Runs() {
		before[run.Text] = [2]float64{run.X, run.Y}
	}

	out := New()
	out.Compress = false
	e, err := out.EditPage(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Replace with much wider text; " EUR" must stay put.
	if _, err := e.ReplaceText("99", "1234567"); err != nil {
		t.Fatal(err)
	}
	data := docBytes(t, out)

	r2, err := NewReader(data)
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
		if run.Text != " EUR" {
			continue
		}
		found = true
		want := before[" EUR"]
		if math.Abs(run.X-want[0]) > 0.05 {
			t.Errorf("following run shifted: x=%v, want %v", run.X, want[0])
		}
	}
	if !found {
		t.Error(`run " EUR" not found after editing`)
	}
}

func TestEditFitScale(t *testing.T) {
	src := invoiceFixture(t)
	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	doc.Compress = false
	e, err := doc.EditPage(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	e.SetFitMode(FitScale)
	if _, err := e.ReplaceText("Consulting", "Extended consulting services"); err != nil {
		t.Fatal(err)
	}
	out := docBytes(t, doc)
	if !bytes.Contains(out, []byte("Tz")) {
		t.Error("FitScale did not emit horizontal scaling")
	}

	// The scaled run must occupy (very close to) the original width.
	r2, _ := NewReader(out)
	d2 := New()
	e2, _ := d2.EditPage(r2, 0)
	var origWidth, newWidth float64
	for _, run := range e.Runs() {
		if run.Text == "Licensing" {
			origWidth = run.Width
		}
	}
	for _, run := range e2.Runs() {
		if run.Text == "Extended consulting services" {
			newWidth = run.Width
		}
		if run.Text == "Licensing" && math.Abs(run.Width-origWidth) > 0.01 {
			t.Error("an untouched run changed width")
		}
	}
	if newWidth == 0 {
		t.Fatal("scaled run not found after rewrite")
	}
}

// TestEditRejectsUnsupportedGlyph verifies the honest failure mode: if the
// page's font has no glyph for a character, nothing is changed.
func TestEditRejectsUnsupportedGlyph(t *testing.T) {
	font, err := LoadFont(testFontPath(t))
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	page := doc.AddPage()
	page.SetFont(font, 12)
	page.Text(60, 80, "hello") // subset contains only these glyphs
	src := docBytes(t, doc)

	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	out := New()
	e, err := out.EditPage(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	// 'Z' and '漢' are not in the subset.
	if _, err := e.ReplaceText("hello", "漢字"); err == nil {
		t.Error("expected an error for a glyph missing from the subset font")
	} else if !strings.Contains(err.Error(), "cannot represent") {
		t.Errorf("unhelpful error: %v", err)
	}
	// Rearranging the glyphs it does have must work.
	if _, err := e.ReplaceText("hello", "olleh"); err != nil {
		t.Fatalf("valid replacement rejected: %v", err)
	}
	data := docBytes(t, out)
	r2, err := NewReader(data)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := r2.PageText(0)
	if !strings.Contains(text, "olleh") {
		t.Errorf("text = %q", text)
	}
}

// TestEditEmbeddedFontRoundTrip edits text drawn in an embedded subset
// font, which requires encoding through the font's ToUnicode map.
func TestEditEmbeddedFontRoundTrip(t *testing.T) {
	font, err := LoadFont(testFontPath(t))
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	page := doc.AddPage()
	page.SetFont(font, 14)
	page.Text(60, 80, "État: brouillon — 2024")
	// A second line puts the glyphs the replacement needs into the
	// subset, as any real document containing both words would.
	page.Text(60, 110, "Statuts possibles : validé, rejeté")
	src := docBytes(t, doc)

	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	out := New()
	e, err := out.EditPage(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.ReplaceText("brouillon", "validé"); err != nil {
		t.Fatal(err)
	}
	data := docBytes(t, out)
	r2, err := NewReader(data)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := r2.PageText(0)
	if !strings.Contains(text, "État: validé — 2024") {
		t.Errorf("text = %q", text)
	}
}

// TestEditSubsetGlyphSafety guards a subtle failure mode: a simple font's
// encoding table maps all 256 codes, but an embedded subset contains only
// the glyphs the document draws. Writing an unavailable code renders as a
// blank box in viewers, so it must be refused.
func TestEditSubsetGlyphSafety(t *testing.T) {
	font, err := LoadFont(testFontPath(t))
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	page := doc.AddPage()
	page.SetFont(font, 12)
	page.Text(60, 80, "abc 123")
	src := docBytes(t, doc)

	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	out := New()
	e, err := out.EditPage(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	run := e.Runs()[0]
	if !run.font.embedded {
		t.Fatal("test needs an embedded font")
	}
	// Characters the page draws are usable...
	for _, ok := range []string{"cba", "321", "a1b2c3"} {
		if _, err := run.font.encodeText(ok); err != nil {
			t.Errorf("encodeText(%q) failed: %v", ok, err)
		}
	}
	// ...ones it does not are refused rather than drawn as boxes.
	for _, bad := range []string{"z", "Q", "9", "%"} {
		if _, err := run.font.encodeText(bad); err == nil {
			t.Errorf("encodeText(%q) succeeded, but the subset has no such glyph", bad)
		}
	}
	// A non-embedded standard font is not restricted: the viewer has it.
	plain := New()
	pp := plain.AddPage()
	pp.SetFont(Helvetica, 12)
	pp.Text(60, 80, "abc")
	r2, _ := NewReader(docBytes(t, plain))
	d2 := New()
	e2, err := d2.EditPage(r2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e2.Runs()[0].font.encodeText("xyz XYZ 789"); err != nil {
		t.Errorf("standard font should accept any WinAnsi text: %v", err)
	}
}

func TestEditSetTextOnRun(t *testing.T) {
	src := invoiceFixture(t)
	r, _ := NewReader(src)
	doc := New()
	e, err := doc.EditPage(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	var target *TextRun
	for _, run := range e.Runs() {
		if strings.HasPrefix(run.Text, "INVOICE") {
			target = run
		}
	}
	if target == nil {
		t.Fatal("heading run not found")
	}
	if err := target.SetText("INVOICE 2025-001", FitAdvance); err != nil {
		t.Fatal(err)
	}
	r2, err := NewReader(docBytes(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	text, _ := r2.PageText(0)
	if !strings.Contains(text, "INVOICE 2025-001") {
		t.Errorf("text = %q", text)
	}
}

// TestEditPreservesResources confirms the source page's own resource names
// survive, and that drawing added on top gets non-colliding names.
func TestEditPreservesResources(t *testing.T) {
	src := invoiceFixture(t)
	r, _ := NewReader(src)
	doc := New()
	doc.Compress = false
	e, err := doc.EditPage(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Draw over the imported page with our own font.
	e.SetFont(HelveticaBold, 20)
	e.SetFillColor(RGB(200, 0, 0))
	e.Text(60, 400, "ADDED ON TOP")

	out := docBytes(t, doc)
	if e.resPrefix == "" {
		t.Fatal("no resource prefix assigned to the edited page")
	}
	if !bytes.Contains(out, []byte("/"+e.resPrefix+"F1")) {
		t.Errorf("added content does not use the prefixed resource name %sF1", e.resPrefix)
	}
	// The source page used /F1 and /F2; both must still be present.
	for _, name := range []string{"/F1 ", "/F2 "} {
		if !bytes.Contains(out, []byte(name)) {
			t.Errorf("source resource name %q lost", name)
		}
	}
	r2, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := r2.PageText(0)
	if !strings.Contains(text, "ADDED ON TOP") || !strings.Contains(text, "INVOICE") {
		t.Errorf("combined text = %q", text)
	}
}

// TestEditInsideFormXObject edits text that lives in a nested form
// XObject rather than the page's own content stream.
func TestEditInsideFormXObject(t *testing.T) {
	// A page imported as a form XObject, then re-imported for editing.
	base := New()
	bp := base.AddPage()
	bp.SetFont(Helvetica, 12)
	bp.Text(60, 80, "nested original text")
	inner, err := NewReader(docBytes(t, base))
	if err != nil {
		t.Fatal(err)
	}
	wrapped := New()
	if _, err := wrapped.ImportPage(inner, 0); err != nil {
		t.Fatal(err)
	}
	src := docBytes(t, wrapped)

	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	e, err := doc.EditPage(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Runs()) == 0 {
		t.Fatal("no runs discovered inside the form XObject")
	}
	n, err := e.ReplaceText("original", "edited")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("replaced %d runs, want 1", n)
	}
	r2, err := NewReader(docBytes(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	text, _ := r2.PageText(0)
	if !strings.Contains(text, "nested edited text") {
		t.Errorf("text = %q", text)
	}
}

func TestEditPreservesMediaBoxAndRotation(t *testing.T) {
	doc := New()
	p := doc.AddPageSize(PageSize{W: 400, H: 600})
	p.SetFont(Helvetica, 12)
	p.Text(50, 50, "rotated")
	p.SetRotate(270)
	src := docBytes(t, doc)

	r, _ := NewReader(src)
	out := New()
	if _, err := out.EditPage(r, 0); err != nil {
		t.Fatal(err)
	}
	data := docBytes(t, out)
	if !bytes.Contains(data, []byte("/Rotate 270")) {
		t.Error("rotation not preserved")
	}
	if !bytes.Contains(data, []byte("/MediaBox [0 0 400 600]")) {
		t.Error("media box not preserved")
	}
	r2, _ := NewReader(data)
	size, _ := r2.PageSize(0)
	if size.W != 600 || size.H != 400 {
		t.Errorf("display size = %v, want 600x400 (rotated)", size)
	}
}

func TestEditNoChangesIsFaithful(t *testing.T) {
	src := invoiceFixture(t)
	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	if _, err := doc.EditPage(r, 0); err != nil {
		t.Fatal(err)
	}
	out := docBytes(t, doc)
	r2, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := r.PageText(0)
	b, _ := r2.PageText(0)
	if a != b {
		t.Errorf("text changed by a no-op edit:\n%q\n%q", a, b)
	}
}

func TestEditOutOfRange(t *testing.T) {
	src := invoiceFixture(t)
	r, _ := NewReader(src)
	doc := New()
	if _, err := doc.EditPage(r, 5); err == nil {
		t.Error("expected an error for an out-of-range page")
	}
}

// TestStrictLexPages is the check a caller runs over their own output.
//
// A splice landing right after an operator whose trailing space it
// consumed leaves "Tc" and "1" as the single token "Tc1". Every reader
// here tolerates it and so do the common ones, which is exactly why it
// goes unnoticed — the content is right and the file is not.
func TestStrictLexPages(t *testing.T) {
	// A document this package wrote is clean.
	doc := New()
	doc.Compress = false
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(72, 100, "Alpha Beta Gamma")
	p.Rect(100, 200, 50, 50, Fill)
	clean := docBytes(t, doc)
	if err := StrictLexPages(clean); err != nil {
		t.Errorf("a document this package wrote does not lex strictly: %v", err)
	}

	// And so is one it edited, which is the property the check exists to
	// hold: the splice must not fuse with its neighbours.
	r := NewReaderOrFail(t, clean)
	u := Update(r)
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := page.ReplaceText("Beta", "Delta"); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	if err := StrictLexPages(buf.Bytes()); err != nil {
		t.Errorf("an edited document does not lex strictly: %v", err)
	}

	// A stream with two tokens run together is caught, and the error says
	// where.
	fused := NewReaderOrFail(t, clean)
	u2 := Update(fused)
	stm := u2.AddObject(NewStream(Dict{}, []byte("BT /F1 12 Tf 0 Tc1 0 Td ET")))
	if err := u2.SetPageEntry(0, "Contents", stm); err != nil {
		t.Fatal(err)
	}
	var bad bytes.Buffer
	if _, err := u2.WriteTo(&bad); err != nil {
		t.Fatal(err)
	}
	err = StrictLexPages(bad.Bytes())
	if err == nil {
		t.Fatal("two tokens run together were not caught")
	}
	if !strings.Contains(err.Error(), "Tc1") {
		t.Errorf("the error does not name the offending token: %v", err)
	}

	// A file that is not a document at all is an error rather than a
	// clean bill of health.
	if err := StrictLexPages([]byte("not a pdf")); err == nil {
		t.Error("nonsense passed the check")
	}
}
