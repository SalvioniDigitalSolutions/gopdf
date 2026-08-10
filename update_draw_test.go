package gopdf

import (
	"bytes"
	"image"
	"image/color"
	"strings"
	"testing"
)

// TestUpdateDrawKeepsOriginalStream is the core promise of drawing during
// an incremental update: the page's own content object is never rewritten,
// so the original bytes stand.
func TestUpdateDrawKeepsOriginalStream(t *testing.T) {
	src := invoiceFixture(t)
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
	page.SetFont(HelveticaBold, 20)
	page.SetFillColor(RGB(200, 0, 0))
	page.Text(60, 300, "PAID IN FULL")

	out := updatedBytes(t, u)
	if !bytes.HasPrefix(out, src) {
		t.Fatal("original bytes were not preserved")
	}
	verifyXref(t, out)

	r2, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	text, err := r2.PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "PAID IN FULL") {
		t.Errorf("drawn text missing: %q", text)
	}
	// Everything that was already on the page must still be there.
	for _, want := range []string{"INVOICE 2024-117", "Status: DRAFT", "1950.00"} {
		if !strings.Contains(text, want) {
			t.Errorf("original content lost: %q missing from %q", want, text)
		}
	}
	// The page must now reference two content streams.
	contents, _ := r2.resolve(r2.pages[0].dict["Contents"]).(Array)
	if len(contents) != 2 {
		t.Errorf("page has %d content streams, want 2 (original plus drawn)", len(contents))
	}
}

// TestUpdateDrawResourceNamesDoNotCollide checks that the prefix keeps
// added fonts apart from the ones the source already had.
func TestUpdateDrawResourceNamesDoNotCollide(t *testing.T) {
	src := invoiceFixture(t)
	r, _ := NewReader(src)
	u := Update(r)
	u.SetCompress(false)
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	page.SetFont(TimesBold, 14)
	page.Text(60, 320, "added")

	out := updatedBytes(t, u)
	if page.resPrefix == "" {
		t.Fatal("no resource prefix assigned")
	}
	if !bytes.Contains(out, []byte("/"+page.resPrefix+"F1")) {
		t.Errorf("drawn content does not use the prefixed name %sF1", page.resPrefix)
	}

	r2, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	// The merged resource dictionary must list both the source's fonts
	// and the added one.
	res, _ := r2.resolve(r2.pages[0].dict["Resources"]).(Dict)
	fonts, _ := r2.resolve(res["Font"]).(Dict)
	if len(fonts) < 3 {
		t.Errorf("merged font dictionary has %d entries, want the source's two plus ours", len(fonts))
	}
	if _, ok := fonts[Name(page.resPrefix+"F1")]; !ok {
		t.Error("added font missing from the merged resources")
	}
	for _, name := range []Name{"F1", "F2"} {
		if _, ok := fonts[name]; !ok {
			t.Errorf("source font %s lost from the merged resources", name)
		}
	}
	text, _ := r2.PageText(0)
	if !strings.Contains(text, "added") || !strings.Contains(text, "INVOICE") {
		t.Errorf("combined text = %q", text)
	}
}

// TestUpdateDrawAndEditTogether combines a text edit with new drawing:
// the page ends up with the rewritten stream plus the drawn one.
func TestUpdateDrawAndEditTogether(t *testing.T) {
	src := invoiceFixture(t)
	r, _ := NewReader(src)
	u := Update(r)
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := page.ReplaceText("DRAFT", "FINAL"); err != nil {
		t.Fatal(err)
	}
	page.SetFont(Helvetica, 9)
	page.Text(60, 340, "reviewed and approved")

	r2, err := NewReader(updatedBytes(t, u))
	if err != nil {
		t.Fatal(err)
	}
	text, _ := r2.PageText(0)
	if !strings.Contains(text, "Status: FINAL") {
		t.Errorf("edit did not apply: %q", text)
	}
	if !strings.Contains(text, "reviewed and approved") {
		t.Errorf("drawing did not apply: %q", text)
	}
	if strings.Contains(text, "DRAFT") {
		t.Error("old text survived")
	}
	contents, _ := r2.resolve(r2.pages[0].dict["Contents"]).(Array)
	if len(contents) != 2 {
		t.Errorf("page has %d content streams, want 2", len(contents))
	}
}

// TestUpdateDrawRichContent exercises the resource kinds beyond fonts:
// images, transparency and gradients all have to reach the page.
func TestUpdateDrawRichContent(t *testing.T) {
	src := invoiceFixture(t)
	r, _ := NewReader(src)
	u := Update(r)
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	img, err := u.AddImage(makeGradientImage(32, 32))
	if err != nil {
		t.Fatal(err)
	}
	page.DrawImage(img, 400, 60, 60, 60)
	page.Push()
	page.SetAlpha(0.4, 0.4)
	page.SetFillColor(RGB(0, 120, 200))
	page.Circle(430, 200, 40, Fill)
	page.Pop()
	if err := page.FillGradientRect(60, 400, 200, 40, GradientHorizontal,
		Stop(0, RGB(240, 240, 240)), Stop(1, RGB(120, 160, 220))); err != nil {
		t.Fatal(err)
	}

	out := updatedBytes(t, u)
	verifyXref(t, out)
	r2, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	res, _ := r2.resolve(r2.pages[0].dict["Resources"]).(Dict)
	for _, cat := range []Name{"XObject", "ExtGState", "Shading"} {
		sub, ok := r2.resolve(res[cat]).(Dict)
		if !ok || len(sub) == 0 {
			t.Errorf("no %s entries reached the page's resources", cat)
			continue
		}
		// Each entry must resolve to a real object.
		for name, v := range sub {
			if r2.resolve(v) == nil {
				t.Errorf("%s /%s does not resolve", cat, name)
			}
		}
	}
	// The original text is untouched.
	text, _ := r2.PageText(0)
	if !strings.Contains(text, "INVOICE 2024-117") {
		t.Errorf("original content lost: %q", text)
	}
}

// makeGradientImage builds a small opaque image for drawing tests.
func makeGradientImage(w, h int) image.Image {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.Set(x, y, color.RGBA{R: uint8(x * 8), G: uint8(y * 8), B: 160, A: 255})
		}
	}
	return m
}

// TestUpdateDrawEncrypted confirms drawn content is encrypted with the
// file's own key, like everything else the update appends.
func TestUpdateDrawEncrypted(t *testing.T) {
	doc := New()
	doc.Encrypt("pw", "", AllowAll, AES128)
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(60, 80, "original")
	src := docBytes(t, doc)

	r, err := NewReaderPassword(src, "pw")
	if err != nil {
		t.Fatal(err)
	}
	u := Update(r)
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	page.SetFont(HelveticaBold, 14)
	page.Text(60, 120, "stamped while encrypted")

	out := updatedBytes(t, u)
	if bytes.Contains(out, []byte("stamped while encrypted")) {
		t.Error("drawn content was appended unencrypted")
	}
	r2, err := NewReaderPassword(out, "pw")
	if err != nil {
		t.Fatal(err)
	}
	text, _ := r2.PageText(0)
	if !strings.Contains(text, "stamped while encrypted") {
		t.Errorf("drawn text did not decrypt: %q", text)
	}
	if !strings.Contains(text, "original") {
		t.Errorf("original content lost: %q", text)
	}
}

func TestUpdateDrawNothingChangesNothing(t *testing.T) {
	src := invoiceFixture(t)
	r, _ := NewReader(src)
	u := Update(r)
	if _, err := u.Page(0); err != nil { // opened but nothing drawn or edited
		t.Fatal(err)
	}
	if out := updatedBytes(t, u); !bytes.Equal(out, src) {
		t.Error("opening a page without changing it should reproduce the original")
	}
}
