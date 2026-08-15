package gopdf

import (
	"bufio"
	"image"
	"math"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestFragmentsBasics(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(HelveticaBold, 18)
	page.Text(60, 80, "Heading")
	page.SetFont(Helvetica, 11)
	page.Text(60, 120, "Body text here")
	src := docBytes(t, doc)

	r := NewReaderOrFail(t, src)
	frags, err := r.PageTextFragments(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 2 {
		t.Fatalf("got %d fragments, want 2: %+v", len(frags), frags)
	}
	// Content-stream order, which detection offsets depend on.
	if frags[0].Text != "Heading" || frags[1].Text != "Body text here" {
		t.Errorf("out of order: %q then %q", frags[0].Text, frags[1].Text)
	}
	// BaseFont, not the resource name.
	if frags[0].FontName != "Helvetica-Bold" {
		t.Errorf("FontName = %q, want the BaseFont", frags[0].FontName)
	}
	if frags[1].FontName != "Helvetica" {
		t.Errorf("FontName = %q", frags[1].FontName)
	}
	if frags[0].FontSize != 18 || frags[1].FontSize != 11 {
		t.Errorf("sizes = %v, %v", frags[0].FontSize, frags[1].FontSize)
	}
	// Baseline start, top-left origin, as ImageRef uses.
	if math.Abs(frags[0].X-60) > 0.01 || math.Abs(frags[0].Y-80) > 0.01 {
		t.Errorf("position = (%v,%v), want (60,80)", frags[0].X, frags[0].Y)
	}
	// Advance width from the font's own metrics.
	if want := HelveticaBold.TextWidth("Heading", 18); math.Abs(frags[0].W-want) > 0.05 {
		t.Errorf("W = %v, want %v", frags[0].W, want)
	}
	if frags[0].RenderMode != 0 || frags[0].Invisible() {
		t.Errorf("render mode = %d", frags[0].RenderMode)
	}
}

// TestFragmentsSubsetPrefix checks the subset prefix survives, since it
// is what distinguishes one embedded subset from another.
func TestFragmentsSubsetPrefix(t *testing.T) {
	src := subsetFontDoc(t, "Some subsetted text", false)
	r := NewReaderOrFail(t, src)
	frags, err := r.PageTextFragments(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) == 0 {
		t.Fatal("no fragments")
	}
	if !strings.Contains(frags[0].FontName, "+") {
		t.Errorf("FontName = %q, want a subset prefix", frags[0].FontName)
	}
	if !strings.HasSuffix(frags[0].FontName, "Helvetica") {
		t.Errorf("FontName = %q", frags[0].FontName)
	}
}

// TestFragmentsRenderMode covers the OCR layer under a scan: mode 3 draws
// nothing, and a caller skips it rather than reading the page twice.
func TestFragmentsRenderMode(t *testing.T) {
	content := "BT /F1 10 Tf 1 0 0 1 60 700 Tm (visible) Tj ET\n" +
		"BT /F1 10 Tf 3 Tr 1 0 0 1 60 680 Tm (hidden) Tj ET\n"
	r := NewReaderOrFail(t, rawPageDoc(t, content))
	frags, err := r.PageTextFragments(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 2 {
		t.Fatalf("got %d fragments", len(frags))
	}
	if frags[0].RenderMode != 0 || frags[0].Invisible() {
		t.Errorf("the first should be visible: mode %d", frags[0].RenderMode)
	}
	if frags[1].RenderMode != 3 || !frags[1].Invisible() {
		t.Errorf("the second should be mode 3: mode %d", frags[1].RenderMode)
	}
	// Mode 7 adds to the clip and paints nothing either.
	if !(TextFragment{RenderMode: 7}).Invisible() {
		t.Error("mode 7 should count as invisible")
	}
	// The render mode does not leak out of its text object.
	if frags[0].RenderMode == frags[1].RenderMode {
		t.Error("Tr was not scoped")
	}
}

// TestFragmentsUnmappedGlyph is the offset guarantee: a code the font
// gives no mapping for becomes U+FFFD rather than vanishing, because
// dropping it silently moves everything after it.
func TestFragmentsUnmappedGlyph(t *testing.T) {
	run := &TextRun{
		Text:     "AB",
		codeText: []int{1, 0, 1}, // the middle code mapped to nothing
	}
	if got := fragmentText(run); got != "A�B" {
		t.Errorf("fragmentText = %q, want %q", got, "A�B")
	}
	// Nothing missing means nothing changed.
	if got := fragmentText(&TextRun{Text: "AB", codeText: []int{1, 1}}); got != "AB" {
		t.Errorf("fragmentText = %q", got)
	}
	// A multi-byte mapping is carried across whole.
	multi := &TextRun{Text: "éx", codeText: []int{2, 0, 1}}
	if got := fragmentText(multi); got != "é�x" {
		t.Errorf("fragmentText = %q", got)
	}
	// No span information at all leaves the text alone.
	if got := fragmentText(&TextRun{Text: "AB"}); got != "AB" {
		t.Errorf("fragmentText = %q", got)
	}
}

// TestFragmentsInsideForms checks the recursion and matrix composition:
// text inside a form XObject arrives positioned on the page.
func TestFragmentsInsideForms(t *testing.T) {
	inner := New()
	ip := inner.AddPage()
	ip.SetFont(Helvetica, 12)
	ip.Text(40, 60, "Inside a form")
	innerBytes := docBytes(t, inner)

	ir := NewReaderOrFail(t, innerBytes)
	outer := New()
	if _, err := outer.ImportPage(ir, 0); err != nil {
		t.Fatal(err)
	}
	src := docBytes(t, outer)

	r := NewReaderOrFail(t, src)
	frags, err := r.PageTextFragments(0)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, f := range frags {
		if strings.Contains(f.Text, "Inside a form") {
			found = true
			if f.X <= 0 || f.Y <= 0 {
				t.Errorf("the form's matrix was not composed: %+v", f)
			}
			if f.FontSize <= 0 {
				t.Errorf("no effective size: %+v", f)
			}
		}
	}
	if !found {
		t.Fatalf("form text was not reached; got %+v", frags)
	}
}

func TestFragmentsRangeAndErrors(t *testing.T) {
	r := NewReaderOrFail(t, redactFixture(t))
	if _, err := r.PageTextFragments(-1); err == nil {
		t.Error("a negative page should be refused")
	}
	if _, err := r.PageTextFragments(99); err == nil {
		t.Error("a page past the end should be refused")
	}
	// A stream of junk must produce an error or nothing, never a panic.
	junk := rawPageDoc(t, "BT /F1 (unterminated \x00\x01\x02 Tj ET q q q cm cm\n")
	r2 := NewReaderOrFail(t, junk)
	if _, err := r2.PageTextFragments(0); err != nil {
		t.Logf("junk reported as an error, which is fine: %v", err)
	}
}

// TestFragmentsMatchPageText checks the two readings agree on content,
// so swapping one for the other does not change what a document says.
func TestFragmentsMatchPageText(t *testing.T) {
	src := redactFixture(t)
	r := NewReaderOrFail(t, src)
	frags, err := r.PageTextFragments(0)
	if err != nil {
		t.Fatal(err)
	}
	// Fragments carry no separators of their own: they are operations,
	// not lines. Joined with spaces they should say the same as the page.
	var joined strings.Builder
	for i, f := range frags {
		if i > 0 {
			joined.WriteByte(' ')
		}
		joined.WriteString(f.Text)
	}
	page, err := r.PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	if collapse(joined.String()) != collapse(page) {
		t.Errorf("fragments and PageText disagree:\n frags %q\n page  %q",
			collapse(joined.String()), collapse(page))
	}
}

// --- the image draw matrix ---

func TestImageRefMatrixAndRotation(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	m := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := range m.Pix {
		m.Pix[i] = 0xA0
	}
	img, err := doc.AddImage(m)
	if err != nil {
		t.Fatal(err)
	}
	page.DrawImage(img, 100, 100, 80, 40)
	page.Push()
	page.RotateAt(90, 300, 300)
	page.DrawImage(img, 260, 260, 80, 40)
	page.Pop()
	src := docBytes(t, doc)

	r := NewReaderOrFail(t, src)
	imgs, err := r.PageImages(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 2 {
		t.Fatalf("got %d placements, want 2", len(imgs))
	}
	upright, rotated := imgs[0], imgs[1]
	if !upright.Upright() {
		t.Errorf("the first placement should be square to the page: %v", upright.Matrix)
	}
	if got := upright.Rotation(); math.Abs(got) > 0.01 {
		t.Errorf("upright rotation = %v", got)
	}
	if math.Abs(upright.W-80) > 0.01 || math.Abs(upright.H-40) > 0.01 {
		t.Errorf("upright box = %vx%v, want 80x40", upright.W, upright.H)
	}

	if rotated.Upright() {
		t.Error("the rotated placement should not report as upright")
	}
	if got := rotated.Rotation(); math.Abs(got-90) > 0.5 && math.Abs(got-270) > 0.5 {
		t.Errorf("rotated rotation = %v, want a quarter turn", got)
	}
	// Turned a quarter, the bounding box has swapped its sides.
	if math.Abs(rotated.W-40) > 0.5 || math.Abs(rotated.H-80) > 0.5 {
		t.Errorf("rotated box = %vx%v, want 40x80", rotated.W, rotated.H)
	}
	if rotated.Matrix == ([6]float64{}) {
		t.Error("the placement matrix is empty")
	}
}

func TestUnitSquareBounds(t *testing.T) {
	// A quarter turn maps the unit square onto itself, offset.
	lo, hi := unitSquareBounds(matrix{0, 1, -1, 0, 5, 5})
	if math.Abs(lo[0]-4) > 1e-9 || math.Abs(hi[0]-5) > 1e-9 ||
		math.Abs(lo[1]-5) > 1e-9 || math.Abs(hi[1]-6) > 1e-9 {
		t.Errorf("bounds = %v..%v", lo, hi)
	}
	// A plain scale is its own box.
	lo, hi = unitSquareBounds(matrix{3, 0, 0, 2, 1, 1})
	if lo != [2]float64{1, 1} || hi != [2]float64{4, 3} {
		t.Errorf("bounds = %v..%v", lo, hi)
	}
}

// TestTextStateIsRestoredByQ is the regression for a decoding bug that
// only showed on documents with footnotes: q and Q save and restore the
// graphics state, and the font is part of it. Restoring only the
// transform left the wrong font current after a footnote marker, so the
// rest of the paragraph decoded through the wrong table and came out as
// gibberish.
func TestTextStateIsRestoredByQ(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 11)
	// Two fonts, the second used only inside a q/Q, as a footnote marker
	// or a small-caps run would be.
	page.op("BT /F1 11 Tf 1 0 0 1 60 700 Tm (before ) Tj")
	page.op("q /F2 7 Tf (11) Tj Q")
	page.op("( after) Tj ET")
	page.SetFont(HelveticaBold, 7) // registers F2
	src := docBytes(t, doc)

	r := NewReaderOrFail(t, src)
	frags, err := r.PageTextFragments(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) < 3 {
		t.Fatalf("got %d fragments, want at least 3: %+v", len(frags), frags)
	}
	// The size must go back to what it was before the q.
	first, last := frags[0], frags[len(frags)-1]
	if first.FontSize != last.FontSize {
		t.Errorf("the size was not restored: %v before, %v after",
			first.FontSize, last.FontSize)
	}
	if first.FontName != last.FontName {
		t.Errorf("the font was not restored: %q before, %q after",
			first.FontName, last.FontName)
	}
	// And the same through PageText, which is where the bug lived.
	got, err := r.PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(collapse(got), "before 11 after") {
		t.Errorf("PageText = %q", collapse(got))
	}
}

// TestPageTextAgreesWithPdftotext measures extraction against an
// independent implementation over a corpus named by GOPDF_CORPUS. It is
// skipped without one, or without pdftotext.
func TestPageTextAgreesWithPdftotext(t *testing.T) {
	list := os.Getenv("GOPDF_CORPUS")
	if list == "" {
		t.Skip("set GOPDF_CORPUS to a file listing PDFs")
	}
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext is not installed")
	}
	f, err := os.Open(list)
	if err != nil {
		t.Skip(err)
	}
	defer f.Close()

	words := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, w := range strings.Fields(s) {
			m[w] = true
		}
		return m
	}
	var files int
	var total float64
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() && files < 400 {
		path := strings.TrimSpace(sc.Text())
		if path == "" {
			continue
		}
		ref, err := exec.Command("pdftotext", path, "-").Output()
		if err != nil || len(ref) == 0 {
			continue
		}
		r, err := Open(path)
		if err != nil {
			continue
		}
		var b strings.Builder
		bad := false
		for i := 0; i < r.NumPages(); i++ {
			s, err := r.PageText(i)
			if err != nil {
				bad = true
				break
			}
			b.WriteString(s)
			b.WriteString("\n")
		}
		if bad {
			continue
		}
		a, c := words(b.String()), words(string(ref))
		if len(c) == 0 {
			continue
		}
		inter := 0
		for w := range a {
			if c[w] {
				inter++
			}
		}
		union := len(a) + len(c) - inter
		if union == 0 {
			continue
		}
		files++
		total += float64(inter) / float64(union)
	}
	if files == 0 {
		t.Skip("no comparable documents")
	}
	mean := total / float64(files)
	t.Logf("%d files, mean word agreement with pdftotext %.4f", files, mean)
	// A floor, not a target. The remaining difference is mostly
	// documents whose fonts carry a built-in encoding this package does
	// not read, where both tools produce something and only one of them
	// is right. The floor is here to catch a regression that halves the
	// agreement, not to chase the last few points.
	if mean < 0.75 {
		t.Errorf("agreement fell to %.4f", mean)
	}
}
