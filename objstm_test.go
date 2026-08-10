package gopdf

import (
	"bytes"
	"strings"
	"testing"
)

// packedFixture builds a document with many small dictionaries, which is
// where packing objects pays off.
func packedFixture(t *testing.T, compressObjects bool) []byte {
	t.Helper()
	doc := New()
	doc.CompressObjects = compressObjects
	doc.SetInfo(Info{Title: "Packed", Author: "gopdf"})
	var first *Page
	for i := 0; i < 12; i++ {
		p := doc.AddPage()
		if first == nil {
			first = p
		}
		p.SetFont(Helvetica, 11)
		p.Text(60, 80, "page body text")
		p.LinkURL(60, 90, 100, 12, "https://example.com")
		p.AddNote(300, 80, "a note", NoteOptions{Author: "A"})
	}
	root := doc.AddOutline(nil, "Contents", first, 0)
	for i, p := range doc.pages {
		doc.AddOutline(root, "Section", p, float64(i))
	}
	return docBytes(t, doc)
}

func TestObjectStreamsRoundTrip(t *testing.T) {
	packed := packedFixture(t, true)
	verifyXref(t, packed)

	if !bytes.Contains(packed, []byte("/Type /ObjStm")) {
		t.Error("no object stream written")
	}
	if !bytes.Contains(packed, []byte("/Type /XRef")) {
		t.Error("no cross-reference stream written")
	}
	if !bytes.HasPrefix(packed, []byte("%PDF-1.5")) {
		t.Errorf("header = %q, want PDF 1.5", packed[:8])
	}
	if bytes.Contains(packed, []byte("\ntrailer\n")) {
		t.Error("a classic trailer was written alongside the cross-reference stream")
	}

	r, err := NewReader(packed)
	if err != nil {
		t.Fatal(err)
	}
	if r.NumPages() != 12 {
		t.Fatalf("read %d pages, want 12", r.NumPages())
	}
	if got := r.Info().Title; got != "Packed" {
		t.Errorf("title = %q", got)
	}
	text, err := r.PageText(3)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "page body text") {
		t.Errorf("page text = %q", text)
	}
	annots, err := r.Annotations(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(annots) != 2 {
		t.Errorf("found %d annotations, want 2", len(annots))
	}
}

func TestObjectStreamsAreSmaller(t *testing.T) {
	plain := packedFixture(t, false)
	packed := packedFixture(t, true)
	if len(packed) >= len(plain) {
		t.Errorf("packing produced %d bytes, no smaller than the %d without",
			len(packed), len(plain))
	}
	t.Logf("%d bytes packed vs %d plain (%.0f%% smaller)",
		len(packed), len(plain), 100*(1-float64(len(packed))/float64(len(plain))))
}

// TestObjectStreamsDefaultUnchanged is the guard on the core writer: the
// feature is opt-in and must not alter ordinary output at all.
func TestObjectStreamsDefaultUnchanged(t *testing.T) {
	doc := New()
	if doc.CompressObjects {
		t.Fatal("object streams should be off by default")
	}
	a := packedFixture(t, false)
	b := packedFixture(t, false)
	if !bytes.Equal(a, b) {
		t.Error("default output is no longer deterministic")
	}
	if !bytes.HasPrefix(a, []byte("%PDF-1.4")) {
		t.Errorf("default header = %q, want PDF 1.4", a[:8])
	}
	if !bytes.Contains(a, []byte("\ntrailer\n")) {
		t.Error("default output lost its classic trailer")
	}
	if bytes.Contains(a, []byte("/Type /ObjStm")) {
		t.Error("default output packed objects without being asked")
	}
}

// TestObjectStreamsKeepStreamsOutside checks that stream objects stay in
// the file body, where the format requires them.
func TestObjectStreamsKeepStreamsOutside(t *testing.T) {
	font, err := LoadFont(testFontPath(t))
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	doc.CompressObjects = true
	page := doc.AddPage()
	page.SetFont(font, 12)
	page.Text(60, 80, "embedded font and an image")
	img, err := doc.AddImage(makeGradientImage(8, 8))
	if err != nil {
		t.Fatal(err)
	}
	page.DrawImage(img, 60, 100, 40, 40)

	out := docBytes(t, doc)
	verifyXref(t, out)
	r, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	text, err := r.PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "embedded font and an image") {
		t.Errorf("text = %q", text)
	}
	imgs, err := r.PageImages(0)
	if err != nil || len(imgs) != 1 {
		t.Fatalf("images = %v, %v", imgs, err)
	}
	if _, err := imgs[0].Decode(); err != nil {
		t.Errorf("image did not decode: %v", err)
	}
}

// TestObjectStreamsIgnoredWhenEncrypted documents the interaction: an
// encrypted document is written the classic way, because strings inside
// an object stream are covered by the stream's own encryption.
func TestObjectStreamsIgnoredWhenEncrypted(t *testing.T) {
	doc := New()
	doc.CompressObjects = true
	doc.Encrypt("pw", "", AllowAll, AES128)
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(60, 80, "secret")

	out := docBytes(t, doc)
	if bytes.Contains(out, []byte("/Type /ObjStm")) {
		t.Error("objects were packed in an encrypted document")
	}
	r, err := NewReaderPassword(out, "pw")
	if err != nil {
		t.Fatal(err)
	}
	text, _ := r.PageText(0)
	if !strings.Contains(text, "secret") {
		t.Errorf("text = %q", text)
	}
}
