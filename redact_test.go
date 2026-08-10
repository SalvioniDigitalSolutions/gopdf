package gopdf

import (
	"bytes"
	"image"
	"image/color"
	"os"
	"regexp"
	"strings"
	"testing"
)

// redactFixture builds a document with text worth removing.
func redactFixture(t *testing.T) []byte {
	t.Helper()
	doc := New()
	doc.SetInfo(Info{Title: "Patient record", Author: "Dr Grace Hopper"})
	page := doc.AddPage()
	page.SetFont(HelveticaBold, 14)
	page.Text(60, 60, "Case notes")
	page.SetFont(Helvetica, 11)
	page.Text(60, 100, "Patient: Ada Lovelace")
	page.Text(60, 120, "Record number 4815162342 filed on 3 May.")
	page.Text(60, 140, "Referred by Charles Babbage of Dorset Street.")
	page.SetFont(Helvetica, 9)
	page.Text(60, 300, "This line is far from anything redacted.")
	return docBytes(t, doc)
}

// extractAll returns every page's text joined.
func extractAll(t *testing.T, data []byte) string {
	t.Helper()
	r, err := NewReader(data)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := 0; i < r.NumPages(); i++ {
		s, err := r.PageText(i)
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString(s)
		b.WriteString("\n")
	}
	return b.String()
}

// assertGone is the property that matters: the text must not be
// extractable, and must not appear anywhere in the raw bytes either.
func assertGone(t *testing.T, out []byte, secrets ...string) {
	t.Helper()
	text := extractAll(t, out)
	for _, s := range secrets {
		if strings.Contains(text, s) {
			t.Errorf("redacted text %q is still extractable", s)
		}
		if bytes.Contains(out, []byte(s)) {
			t.Errorf("redacted text %q is still present in the raw file bytes", s)
		}
	}
}

func redactTo(t *testing.T, rd *Redactor) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := rd.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestRedactLiteralText(t *testing.T) {
	src := redactFixture(t)
	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	rd := Redact(r)
	rd.Text("Ada Lovelace")
	out := redactTo(t, rd)

	assertGone(t, out, "Ada Lovelace")
	text := extractAll(t, out)
	// Everything else must survive.
	for _, keep := range []string{"Case notes", "Patient:", "Charles Babbage",
		"This line is far from anything redacted."} {
		if !strings.Contains(text, keep) {
			t.Errorf("redaction removed %q, which was not marked", keep)
		}
	}
}

func TestRedactPattern(t *testing.T) {
	src := redactFixture(t)
	r, _ := NewReader(src)
	rd := Redact(r)
	rd.Pattern(regexp.MustCompile(`\d{10}`))
	out := redactTo(t, rd)

	assertGone(t, out, "4815162342")
	if !strings.Contains(extractAll(t, out), "Record number") {
		t.Error("the surrounding text should survive")
	}
}

func TestRedactArea(t *testing.T) {
	src := redactFixture(t)
	r, _ := NewReader(src)
	rd := Redact(r)
	// A band across the line at y=140.
	rd.Area(0, 40, 130, 400, 16)
	out := redactTo(t, rd)

	assertGone(t, out, "Charles Babbage", "Dorset Street")
	if !strings.Contains(extractAll(t, out), "Case notes") {
		t.Error("content outside the area should survive")
	}
}

func TestRedactMatchCallback(t *testing.T) {
	src := redactFixture(t)
	r, _ := NewReader(src)
	rd := Redact(r)
	rd.Match(func(run *TextRun) bool { return run.FontSize > 13 })
	out := redactTo(t, rd)

	assertGone(t, out, "Case notes")
	if !strings.Contains(extractAll(t, out), "Patient:") {
		t.Error("smaller text should survive")
	}
}

// TestRedactPreservesLayout checks the point of doing this at character
// level: removing a name must not move the text after it.
func TestRedactPreservesLayout(t *testing.T) {
	doc := New()
	page := doc.AddPage()
	page.SetFont(Helvetica, 12)
	page.Text(60, 100, "Before SECRET after")
	src := docBytes(t, doc)

	r, _ := NewReader(src)
	before := runPositions(t, src)["Before SECRET after"]

	rd := Redact(r)
	rd.Text("SECRET")
	rd.SetOverlay(false)
	out := redactTo(t, rd)

	// The text either side must survive, and the run must still start at
	// the original x: removing characters must not shift the line.
	after := runPositions(t, out)
	var got [2]float64
	var found bool
	for text, pos := range after {
		if strings.Contains(text, "Before") {
			got, found = pos, true
		}
	}
	if !found {
		t.Fatalf("the surviving text is gone; runs are %v", after)
	}
	if d := got[0] - before[0]; d < -0.01 || d > 0.01 {
		t.Errorf("the line moved by %v points", d)
	}
	text := extractAll(t, out)
	if !strings.Contains(text, "Before") || !strings.Contains(text, "after") {
		t.Errorf("text either side of the redaction was lost: %q", text)
	}
	assertGone(t, out, "SECRET")
}

// TestRedactIsNotAnIncrementalUpdate is the security property: the
// original bytes must not be carried into the output.
func TestRedactIsNotAnIncrementalUpdate(t *testing.T) {
	src := redactFixture(t)
	r, _ := NewReader(src)
	rd := Redact(r)
	rd.Text("Ada Lovelace")
	out := redactTo(t, rd)

	if bytes.HasPrefix(out, src) {
		t.Fatal("the output begins with the original file, so nothing was removed")
	}
	if n := bytes.Count(out, []byte("%%EOF")); n != 1 {
		t.Errorf("the output has %d revisions; a redacted file must have one", n)
	}
}

func TestRedactStripsMetadata(t *testing.T) {
	src := redactFixture(t)
	r, _ := NewReader(src)
	rd := Redact(r)
	rd.Text("Ada Lovelace")
	out := redactTo(t, rd)

	if bytes.Contains(out, []byte("Dr Grace Hopper")) {
		t.Error("the author name survived in the metadata")
	}
	r2, _ := NewReader(out)
	if got := r2.Info().Author; got != "" {
		t.Errorf("Info().Author = %q, want empty", got)
	}

	// And it can be kept when asked.
	r3, _ := NewReader(src)
	keep := Redact(r3)
	keep.Text("Ada Lovelace")
	keep.StripMetadata(false)
	out2 := redactTo(t, keep)
	r4, _ := NewReader(out2)
	if got := r4.Info().Author; got != "Dr Grace Hopper" {
		t.Errorf("with StripMetadata(false), Author = %q", got)
	}
}

func TestRedactMarks(t *testing.T) {
	src := redactFixture(t)
	r, _ := NewReader(src)
	rd := Redact(r)
	rd.Text("Ada Lovelace")
	marks, err := rd.Marks()
	if err != nil {
		t.Fatal(err)
	}
	if len(marks) != 1 {
		t.Fatalf("got %d marks, want 1: %+v", len(marks), marks)
	}
	m := marks[0]
	if m.Kind != RedactText {
		t.Errorf("kind = %q", m.Kind)
	}
	if m.Text != "Ada Lovelace" {
		t.Errorf("mark text = %q", m.Text)
	}
	if m.Page != 0 {
		t.Errorf("page = %d", m.Page)
	}
	if m.W <= 0 || m.H <= 0 {
		t.Errorf("mark has no extent: %+v", m)
	}
	// Marks must not have changed the document.
	if got := extractAll(t, src); !strings.Contains(got, "Ada Lovelace") {
		t.Error("calling Marks modified the source")
	}
}

func TestRedactOverlayIsDrawn(t *testing.T) {
	src := redactFixture(t)
	r, _ := NewReader(src)
	rd := Redact(r)
	rd.Area(0, 40, 90, 300, 20)
	rd.SetFill(RGB(255, 0, 0))
	out := redactTo(t, rd)

	r2, _ := NewReader(out)
	content, err := r2.pageContent(r2.pages[0].dict)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(content, []byte("1 0 0 rg")) {
		t.Error("the overlay box was not painted in the requested colour")
	}
	if !bytes.Contains(content, []byte(" re f")) {
		t.Error("no filled rectangle was emitted")
	}
}

func TestRedactImageRegion(t *testing.T) {
	doc := New()
	page := doc.AddPage()
	// A picture split into two halves of distinct colours.
	m := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 40; x++ {
			if x < 20 {
				m.Set(x, y, color.RGBA{R: 200, A: 255})
			} else {
				m.Set(x, y, color.RGBA{B: 200, A: 255})
			}
		}
	}
	img, err := doc.AddImage(m)
	if err != nil {
		t.Fatal(err)
	}
	page.DrawImage(img, 100, 100, 80, 40)
	src := docBytes(t, doc)

	r, _ := NewReader(src)
	rd := Redact(r)
	// Cover the left half of the drawn image only.
	rd.Area(0, 100, 100, 40, 40)
	rd.SetOverlay(false)
	out := redactTo(t, rd)

	r2, _ := NewReader(out)
	imgs, err := r2.PageImages(0)
	if err != nil || len(imgs) == 0 {
		t.Fatalf("the image is gone entirely: %v", err)
	}
	got, err := imgs[0].Decode()
	if err != nil {
		t.Fatal(err)
	}
	b := got.Bounds()
	// The left half must now be the fill colour, the right half untouched.
	lr, _, _, _ := got.At(b.Min.X+2, b.Min.Y+10).RGBA()
	if lr>>8 > 20 {
		t.Errorf("the redacted half of the image is not blacked out (red=%d)", lr>>8)
	}
	_, _, rb, _ := got.At(b.Max.X-3, b.Min.Y+10).RGBA()
	if rb>>8 < 150 {
		t.Errorf("the untouched half of the image was altered (blue=%d)", rb>>8)
	}
}

func TestRedactWholeImage(t *testing.T) {
	doc := New()
	page := doc.AddPage()
	m := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := range m.Pix {
		m.Pix[i] = 0xC0
	}
	img, _ := doc.AddImage(m)
	page.DrawImage(img, 50, 50, 60, 60)
	src := docBytes(t, doc)

	r, _ := NewReader(src)
	imgs, err := r.PageImages(0)
	if err != nil || len(imgs) != 1 {
		t.Fatalf("expected one image, got %d (%v)", len(imgs), err)
	}
	rd := Redact(r)
	rd.Image(imgs[0])
	out := redactTo(t, rd)

	r2, _ := NewReader(out)
	got, err := r2.PageImages(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 0 && (got[0].Width > 1 || got[0].Height > 1) {
		t.Errorf("the image survived at %dx%d", got[0].Width, got[0].Height)
	}
}

func TestRedactAnnotations(t *testing.T) {
	doc := New()
	page := doc.AddPage()
	page.SetFont(Helvetica, 11)
	page.Text(60, 100, "See the note")
	page.AddNote(60, 90, "The informant is Ada Lovelace", NoteOptions{})
	src := docBytes(t, doc)

	r, _ := NewReader(src)
	if annots, _ := r.Annotations(0); len(annots) != 1 {
		t.Fatalf("fixture has %d annotations, want 1", len(annots))
	}
	rd := Redact(r)
	rd.Area(0, 40, 70, 300, 60)
	out := redactTo(t, rd)

	assertGone(t, out, "The informant is Ada Lovelace")
	r2, _ := NewReader(out)
	if annots, _ := r2.Annotations(0); len(annots) != 0 {
		t.Errorf("%d annotations survived inside the redacted area", len(annots))
	}
}

func TestRedactEveryPage(t *testing.T) {
	doc := New()
	for i := 0; i < 3; i++ {
		p := doc.AddPage()
		p.SetFont(Helvetica, 12)
		p.Text(60, 100, "Reference SECRET-CODE on every page")
	}
	src := docBytes(t, doc)

	r, _ := NewReader(src)
	rd := Redact(r)
	rd.Text("SECRET-CODE")
	out := redactTo(t, rd)

	assertGone(t, out, "SECRET-CODE")
	r2, _ := NewReader(out)
	if r2.NumPages() != 3 {
		t.Errorf("page count changed to %d", r2.NumPages())
	}
}

func TestRedactNothingMarked(t *testing.T) {
	src := redactFixture(t)
	r, _ := NewReader(src)
	rd := Redact(r)
	rd.StripMetadata(false)
	out := redactTo(t, rd)

	// A redaction with nothing marked still rewrites the file, and must
	// preserve it exactly as text.
	want := extractAll(t, src)
	if got := extractAll(t, out); got != want {
		t.Errorf("an empty redaction changed the text\n got %q\nwant %q", got, want)
	}
}

func TestRewritePreservesDocument(t *testing.T) {
	src := redactFixture(t)
	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := Rewrite(r, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.Bytes()
	if got, want := extractAll(t, out), extractAll(t, src); got != want {
		t.Errorf("rewriting changed the text\n got %q\nwant %q", got, want)
	}
	r2, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	if r2.NumPages() != r.NumPages() {
		t.Errorf("page count %d became %d", r.NumPages(), r2.NumPages())
	}
	if r2.Info().Title != "Patient record" {
		t.Errorf("metadata lost: %+v", r2.Info())
	}
}

// TestRewriteDropsSupersededObjects is why a rewrite is the right output
// for redaction: an incremental update leaves the old object behind.
func TestRewriteDropsSupersededObjects(t *testing.T) {
	doc := New()
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(60, 100, "ORIGINAL WORDING")
	src := docBytes(t, doc)

	r, _ := NewReader(src)
	u := Update(r)
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := page.ReplaceText("ORIGINAL WORDING", "REVISED WORDING"); err != nil {
		t.Fatal(err)
	}
	var updated bytes.Buffer
	if _, err := u.WriteTo(&updated); err != nil {
		t.Fatal(err)
	}
	// The incremental update still contains the original, by design.
	if !bytes.Contains(updated.Bytes(), []byte("ORIGINAL")) {
		t.Skip("the fixture compressed the original away; nothing to prove here")
	}

	r2, err := NewReader(updated.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	var clean bytes.Buffer
	if _, err := Rewrite(r2, &clean); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(clean.Bytes(), []byte("ORIGINAL")) {
		t.Error("the superseded content survived the rewrite")
	}
	if !strings.Contains(extractAll(t, clean.Bytes()), "REVISED WORDING") {
		t.Error("the current content was lost")
	}
}

func TestRedactRangeMerging(t *testing.T) {
	cases := []struct {
		in   [][2]int
		n    int
		want [][2]int
	}{
		{[][2]int{{2, 5}, {4, 8}}, 10, [][2]int{{2, 8}}},
		{[][2]int{{5, 7}, {0, 2}}, 10, [][2]int{{0, 2}, {5, 7}}},
		{[][2]int{{0, 20}}, 10, [][2]int{{0, 10}}},
		{[][2]int{{3, 3}}, 10, nil},
		{[][2]int{{-4, 2}}, 10, [][2]int{{0, 2}}},
	}
	for _, c := range cases {
		got := mergeRanges(c.in, c.n)
		if len(got) != len(c.want) {
			t.Errorf("mergeRanges(%v, %d) = %v, want %v", c.in, c.n, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("mergeRanges(%v, %d) = %v, want %v", c.in, c.n, got, c.want)
				break
			}
		}
	}
}

func TestRedactRectGeometry(t *testing.T) {
	a := rect{0, 0, 10, 10}
	if !a.intersects(rect{5, 5, 15, 15}) {
		t.Error("overlapping rectangles should intersect")
	}
	if a.intersects(rect{10, 10, 20, 20}) {
		t.Error("rectangles touching at a corner should not intersect")
	}
	if !a.contains(rect{2, 2, 8, 8}) {
		t.Error("a fully enclosed rectangle should be contained")
	}
	if a.contains(rect{2, 2, 12, 8}) {
		t.Error("a rectangle sticking out should not be contained")
	}
	if !a.valid() || (rect{5, 5, 5, 9}).valid() {
		t.Error("valid() is wrong")
	}
}

func TestRedactVectorArtwork(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFillColor(RGB(200, 30, 30))
	page.Rect(100, 100, 60, 40, Fill) // inside the area
	page.Circle(400, 400, 20, Fill)   // far outside
	src := docBytes(t, doc)

	r, _ := NewReader(src)
	rd := Redact(r)
	rd.Area(0, 80, 80, 120, 100)
	rd.SetOverlay(false)
	out := redactTo(t, rd)

	marks, err := rd.Marks()
	if err != nil {
		t.Fatal(err)
	}
	paths := 0
	for _, m := range marks {
		if m.Kind == RedactPath {
			paths++
		}
	}
	if paths != 1 {
		t.Errorf("removed %d paths, want 1: %+v", paths, marks)
	}

	r2, _ := NewReader(out)
	content, err := r2.pageContent(r2.pages[0].dict)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(content, []byte("100 701.89 60 40 re")) {
		t.Error("the rectangle inside the redacted area survived")
	}
	// The circle is drawn with curves; it must still be there.
	if !bytes.Contains(content, []byte(" c\n")) && !bytes.Contains(content, []byte(" c ")) {
		t.Error("the artwork outside the area was removed too")
	}
}

func TestRedactPartialArtworkIsReported(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFillColor(Black)
	page.Rect(100, 100, 200, 40, Fill) // straddles the area's right edge
	src := docBytes(t, doc)

	r, _ := NewReader(src)
	rd := Redact(r)
	rd.Area(0, 80, 80, 100, 100)
	if _, err := rd.Marks(); err != nil {
		t.Fatal(err)
	}
	n, err := rd.PartialArtwork()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("PartialArtwork() = %d, want 1", n)
	}
}

// TestRedactKeepsClipPaths checks that a path establishing a clip is
// never deleted: dropping it would change everything drawn after it.
func TestRedactKeepsClipPaths(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.Push()
	page.Rect(100, 100, 40, 40, ClipPath)
	page.SetFillColor(RGB(0, 0, 255))
	page.Rect(90, 90, 200, 200, Fill)
	page.Pop()
	src := docBytes(t, doc)

	r, _ := NewReader(src)
	rd := Redact(r)
	rd.Area(0, 90, 90, 80, 80)
	rd.SetOverlay(false)
	out := redactTo(t, rd)

	r2, _ := NewReader(out)
	content, err := r2.pageContent(r2.pages[0].dict)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(content, []byte("W n")) {
		t.Error("the clipping path was removed, which changes later drawing")
	}
}

// TestRedactAreaPartialLine covers the precise case: an area that crosses
// only part of a line must take out just the characters under it.
func TestRedactAreaPartialLine(t *testing.T) {
	doc := New()
	page := doc.AddPage()
	page.SetFont(Helvetica, 12)
	page.Text(60, 100, "KEEPLEFT MIDDLEWORD KEEPRIGHT")
	src := docBytes(t, doc)

	r, _ := NewReader(src)
	runs := runPositions(t, src)
	if len(runs) != 1 {
		t.Fatalf("fixture should be one run, got %v", runs)
	}
	// Measure where the middle word sits.
	full := Helvetica.TextWidth("KEEPLEFT MIDDLEWORD KEEPRIGHT", 12)
	pre := Helvetica.TextWidth("KEEPLEFT ", 12)
	mid := Helvetica.TextWidth("MIDDLEWORD", 12)
	if full <= 0 {
		t.Fatal("no metrics")
	}

	rd := Redact(r)
	rd.Area(0, 60+pre, 92, mid, 16)
	rd.SetOverlay(false)
	out := redactTo(t, rd)

	text := extractAll(t, out)
	if strings.Contains(text, "MIDDLEWORD") {
		t.Errorf("the word under the area survived: %q", text)
	}
	for _, keep := range []string{"KEEPLEFT", "KEEPRIGHT"} {
		if !strings.Contains(text, keep) {
			t.Errorf("%q was removed but was not under the area: %q", keep, text)
		}
	}
	assertGone(t, out, "MIDDLEWORD")
}

func TestRedactSaveToFile(t *testing.T) {
	src := redactFixture(t)
	r, _ := NewReader(src)
	rd := Redact(r)
	rd.Text("Ada Lovelace")

	path := t.TempDir() + "/redacted.pdf"
	if err := rd.Save(path); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assertGone(t, out, "Ada Lovelace")

	if err := rd.Save(t.TempDir() + "/missing/dir/x.pdf"); err == nil {
		t.Error("saving into a missing directory should fail")
	}
}

func TestRedactKeepAnnotations(t *testing.T) {
	doc := New()
	page := doc.AddPage()
	page.SetFont(Helvetica, 11)
	page.Text(60, 100, "See the note")
	page.AddNote(60, 90, "a comment", NoteOptions{})
	src := docBytes(t, doc)

	r, _ := NewReader(src)
	rd := Redact(r)
	rd.Area(0, 40, 70, 300, 60)
	rd.KeepAnnotations(true)
	out := redactTo(t, rd)

	r2, _ := NewReader(out)
	annots, err := r2.Annotations(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(annots) != 1 {
		t.Errorf("KeepAnnotations(true) kept %d annotations, want 1", len(annots))
	}
}

// TestRedactInsideFormXObject checks that content nested in a form is
// redacted too, not just the page's own stream.
func TestRedactInsideFormXObject(t *testing.T) {
	inner := New()
	ip := inner.AddPage()
	ip.SetFont(Helvetica, 12)
	ip.Text(60, 100, "NESTED SECRET inside a form")
	innerBytes := docBytes(t, inner)

	ir, err := NewReader(innerBytes)
	if err != nil {
		t.Fatal(err)
	}
	outer := New()
	if _, err := outer.ImportPage(ir, 0); err != nil {
		t.Fatal(err)
	}
	src := docBytes(t, outer)
	if !strings.Contains(extractAll(t, src), "NESTED SECRET") {
		t.Fatal("the fixture does not contain the nested text")
	}

	r, _ := NewReader(src)
	rd := Redact(r)
	rd.Text("NESTED SECRET")
	out := redactTo(t, rd)

	assertGone(t, out, "NESTED SECRET")
	if !strings.Contains(extractAll(t, out), "inside a form") {
		t.Error("the rest of the form's text was lost")
	}
}

func TestRedactRotatedTextIsRemovedWhole(t *testing.T) {
	doc := New()
	page := doc.AddPage()
	page.Push()
	page.RotateAt(30, 200, 200)
	page.SetFont(Helvetica, 12)
	page.Text(150, 200, "ANGLED SECRET TEXT")
	page.Pop()
	src := docBytes(t, doc)

	r, _ := NewReader(src)
	rd := Redact(r)
	// An area over part of the rotated run: splitting it at a character
	// boundary is not safe, so the whole run must go.
	rd.Area(0, 100, 100, 200, 200)
	out := redactTo(t, rd)
	assertGone(t, out, "ANGLED SECRET TEXT")
}

func TestRedactLiteralRanges(t *testing.T) {
	got := literalRanges("abcabcabc", "abc")
	want := [][2]int{{0, 3}, {3, 6}, {6, 9}}
	if len(got) != len(want) {
		t.Fatalf("literalRanges = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("literalRanges = %v, want %v", got, want)
		}
	}
	if r := literalRanges("abc", "zzz"); len(r) != 0 {
		t.Errorf("a missing literal should match nothing, got %v", r)
	}
}

func TestRedactIgnoresEmptyMarks(t *testing.T) {
	src := redactFixture(t)
	r, _ := NewReader(src)
	rd := Redact(r)
	rd.Text("")              // ignored
	rd.Pattern(nil)          // ignored
	rd.Area(0, 10, 10, 0, 5) // zero width, ignored
	rd.StripMetadata(false)
	out := redactTo(t, rd)
	if got, want := extractAll(t, out), extractAll(t, src); got != want {
		t.Errorf("empty marks changed the document")
	}
}

// TestRedactAcrossRuns covers a word a content stream split in two. Found
// against real documents, where "Administration" was drawn as two
// operations and a per-run search left it in the file.
func TestRedactAcrossRuns(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 12)
	// Two adjacent operations that read as one word.
	page.Text(60, 100, "Administra")
	w := Helvetica.TextWidth("Administra", 12)
	page.Text(60+w, 100, "tion follows")
	src := docBytes(t, doc)

	if !strings.Contains(extractAll(t, src), "Administration") {
		t.Fatal("the fixture does not read as one word")
	}
	r, _ := NewReader(src)
	rd := Redact(r)
	rd.Text("Administration")
	marks, err := rd.Marks()
	if err != nil {
		t.Fatal(err)
	}
	if len(marks) != 2 {
		t.Errorf("a word split over two runs should mark both, got %d: %+v", len(marks), marks)
	}
	out := redactTo(t, rd)
	assertGone(t, out, "Administration")
	if !strings.Contains(extractAll(t, out), "follows") {
		t.Error("the rest of the second run was lost")
	}
}

// TestRedactGlyphByGlyphRun covers text positioned one character at a
// time, as justified documents often are.
func TestRedactGlyphByGlyphRun(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 12)
	x := 60.0
	for _, ch := range "SECRETWORD" {
		page.Text(x, 100, string(ch))
		x += Helvetica.TextWidth(string(ch), 12)
	}
	src := docBytes(t, doc)
	if !strings.Contains(extractAll(t, src), "SECRETWORD") {
		t.Fatal("the fixture does not read as one word")
	}

	r, _ := NewReader(src)
	rd := Redact(r)
	rd.Text("SECRETWORD")
	out := redactTo(t, rd)
	assertGone(t, out, "SECRETWORD")
}

// TestRedactDoesNotMatchAcrossLines checks the other side of that
// heuristic: two lines must not be joined into a word that is not there.
func TestRedactDoesNotMatchAcrossLines(t *testing.T) {
	doc := New()
	page := doc.AddPage()
	page.SetFont(Helvetica, 12)
	page.Text(60, 100, "cat")
	page.Text(60, 130, "alogue of items")
	src := docBytes(t, doc)

	r, _ := NewReader(src)
	rd := Redact(r)
	rd.Text("catalogue")
	marks, err := rd.Marks()
	if err != nil {
		t.Fatal(err)
	}
	if len(marks) != 0 {
		t.Errorf("text on two lines was joined into a match: %+v", marks)
	}
	out := redactTo(t, rd)
	text := extractAll(t, out)
	for _, keep := range []string{"cat", "alogue of items"} {
		if !strings.Contains(text, keep) {
			t.Errorf("%q was removed: %q", keep, text)
		}
	}
}

// TestRedactKeepsTextWhenFontCannotReEncode is the regression for the
// worst failure found on real files: a font whose encoding cannot be
// inverted made partial redaction fall back to deleting the whole line.
// Removal must slice the codes the run already drew instead.
func TestRedactSlicesOriginalCodes(t *testing.T) {
	src := redactFixture(t)
	r, _ := NewReader(src)

	doc := New()
	e, err := doc.EditPage(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	var target *TextRun
	for _, run := range e.Runs() {
		if strings.Contains(run.Text, "Record number") {
			target = run
		}
	}
	if target == nil {
		t.Fatal("fixture run not found")
	}
	if len(target.codes) == 0 || len(target.codeText) == 0 {
		t.Fatal("the run did not record its original character codes")
	}
	// The codes behind a slice of the text must round-trip to that text.
	lo := strings.Index(target.Text, "4815162342")
	got := target.codeSlice(lo, lo+len("4815162342"))
	if string(got) != "4815162342" {
		t.Errorf("codeSlice returned %q, want the digits", got)
	}
	// And the whole run maps to the whole code string.
	if all := target.codeSlice(0, len(target.Text)); len(all) != len(target.codes) {
		t.Errorf("codeSlice over the whole run gave %d codes, want %d",
			len(all), len(target.codes))
	}
}

// TestRedactVerifiesItsOwnWork checks the safety net: if a document draws
// text in a way redaction cannot reach, the output is withheld rather
// than handed back looking redacted.
func TestRedactVerifiesItsOwnWork(t *testing.T) {
	src := redactFixture(t)
	r, _ := NewReader(src)
	rd := Redact(r)
	rd.Text("Ada Lovelace")
	if _, err := rd.Marks(); err != nil {
		t.Fatal(err)
	}
	// A clean redaction passes its own check.
	if _, err := rd.WriteTo(new(bytes.Buffer)); err != nil {
		t.Fatalf("a sound redaction was rejected: %v", err)
	}

	// Now sabotage the plan so the text survives, and confirm it is caught.
	r2, _ := NewReader(src)
	bad := Redact(r2)
	bad.Text("Ada Lovelace")
	if err := bad.plan(); err != nil {
		t.Fatal(err)
	}
	bad.rw.replace = map[int]any{} // discard every rewritten object
	var buf bytes.Buffer
	_, err := bad.WriteTo(&buf)
	if err == nil {
		t.Fatal("text that survived redaction was not detected")
	}
	if !strings.Contains(err.Error(), "still readable") {
		t.Errorf("unhelpful error: %v", err)
	}
	if buf.Len() != 0 {
		t.Error("a document that failed its check should not be written out")
	}
}

func TestRedactVerifyCanBeTurnedOff(t *testing.T) {
	src := redactFixture(t)
	r, _ := NewReader(src)
	rd := Redact(r)
	rd.Text("Ada Lovelace")
	rd.SetVerify(false)
	if err := rd.plan(); err != nil {
		t.Fatal(err)
	}
	rd.rw.replace = map[int]any{}
	var buf bytes.Buffer
	if _, err := rd.WriteTo(&buf); err != nil {
		t.Fatalf("with verification off, writing should not fail: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("nothing was written")
	}
}

func TestRedactVerifiesPatterns(t *testing.T) {
	src := redactFixture(t)
	r, _ := NewReader(src)
	rd := Redact(r)
	rd.Pattern(regexp.MustCompile(`\d{10}`))
	if err := rd.plan(); err != nil {
		t.Fatal(err)
	}
	rd.rw.replace = map[int]any{}
	_, err := rd.WriteTo(new(bytes.Buffer))
	if err == nil {
		t.Fatal("a surviving pattern match was not detected")
	}
	if !strings.Contains(err.Error(), "still matches") {
		t.Errorf("unhelpful error: %v", err)
	}
}
