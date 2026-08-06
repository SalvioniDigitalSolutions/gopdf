package gopdf

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"strings"
	"testing"
)

// makeTestJPEG encodes a small JPEG for exercising the DCT passthrough.
func makeTestJPEG(t *testing.T) []byte {
	t.Helper()
	m := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			m.Set(x, y, color.RGBA{R: uint8(x * 8), G: uint8(y * 8), B: 90, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, m, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// encryptedDoc builds a small document with text, an outline, a link and
// metadata, protected with the given method.
func encryptedDoc(t *testing.T, user, owner string, perms Permissions, method EncryptionMethod) []byte {
	t.Helper()
	doc := New()
	doc.SetInfo(Info{Title: "Confidential — Ünicode", Author: "gopdf"})
	doc.Encrypt(user, owner, perms, method)
	page := doc.AddPage()
	page.SetFont(Helvetica, 14)
	page.Text(72, 100, "secret contents")
	page.Text(72, 130, "second secret line")
	page.LinkURL(72, 90, 120, 16, "https://example.com/secret")
	doc.AddOutline(nil, "Secret chapter", page, 0)
	return docBytes(t, doc)
}

func TestEncryptRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method EncryptionMethod
		want   string
	}{
		{"AES128", AES128, "/AESV2"},
		{"AES256", AES256, "/AESV3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := encryptedDoc(t, "userpw", "ownerpw", AllowPrint|AllowCopy, tc.method)
			verifyXref(t, data)

			// The plaintext must not survive anywhere in the file.
			if bytes.Contains(data, []byte("secret contents")) {
				t.Error("content stream was written unencrypted")
			}
			if bytes.Contains(data, []byte("example.com/secret")) {
				t.Error("annotation URI was written unencrypted")
			}
			if bytes.Contains(data, []byte("Secret chapter")) {
				t.Error("outline title was written unencrypted")
			}
			if !bytes.Contains(data, []byte(tc.want)) {
				t.Errorf("output does not declare %s", tc.want)
			}

			// The wrong password must be refused...
			if _, err := NewReaderPassword(data, "nope"); !errors.Is(err, ErrPasswordRequired) {
				t.Errorf("wrong password: got %v, want ErrPasswordRequired", err)
			}
			if _, err := NewReader(data); !errors.Is(err, ErrPasswordRequired) {
				t.Errorf("empty password: got %v, want ErrPasswordRequired", err)
			}

			// ...and both real passwords accepted.
			for _, pw := range []string{"userpw", "ownerpw"} {
				r, err := NewReaderPassword(data, pw)
				if err != nil {
					t.Fatalf("password %q: %v", pw, err)
				}
				if !r.IsEncrypted() {
					t.Error("IsEncrypted = false")
				}
				if got := r.Info().Title; got != "Confidential — Ünicode" {
					t.Errorf("Title = %q", got)
				}
				text, err := r.PageText(0)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(text, "secret contents") ||
					!strings.Contains(text, "second secret line") {
					t.Errorf("decrypted text = %q", text)
				}
				annots, _ := r.resolve(r.pages[0].dict["Annots"]).(Array)
				if len(annots) != 1 {
					t.Fatalf("annots = %d, want 1", len(annots))
				}
				ad, _ := r.resolve(annots[0]).(Dict)
				action, _ := r.resolve(ad["A"]).(Dict)
				uri, _ := r.resolve(action["URI"]).(String)
				if string(uri) != "https://example.com/secret" {
					t.Errorf("decrypted URI = %q", uri)
				}
			}
		})
	}
}

// TestEncryptEmptyUserPassword covers the common "anyone may open it, but
// permissions are restricted" configuration.
func TestEncryptEmptyUserPassword(t *testing.T) {
	data := encryptedDoc(t, "", "ownerpw", AllowPrint, AES128)
	r, err := NewReader(data) // no password needed
	if err != nil {
		t.Fatal(err)
	}
	text, _ := r.PageText(0)
	if !strings.Contains(text, "secret contents") {
		t.Errorf("text = %q", text)
	}
	if _, err := NewReaderPassword(data, "ownerpw"); err != nil {
		t.Errorf("owner password rejected: %v", err)
	}
}

// TestEncryptedImagesAndFonts exercises the binary paths: an embedded
// TrueType font program and a JPEG-encoded image, both of which take
// different code paths through the writer.
func TestEncryptedImagesAndFonts(t *testing.T) {
	font, err := LoadFont(testFontPath(t))
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	doc.Encrypt("pw", "", AllowAll, AES256)
	page := doc.AddPage()
	page.SetFont(font, 12)
	page.Text(72, 72, "Ünicode in an embedded font")

	jpeg := makeTestJPEG(t)
	img, err := doc.AddImageReader(bytes.NewReader(jpeg))
	if err != nil {
		t.Fatal(err)
	}
	page.DrawImage(img, 72, 100, 50, 50)

	data := docBytes(t, doc)
	// The raw JPEG bytes must not appear verbatim in the output.
	if bytes.Contains(data, jpeg[:64]) {
		t.Error("JPEG data was embedded unencrypted")
	}
	r, err := NewReaderPassword(data, "pw")
	if err != nil {
		t.Fatal(err)
	}
	text, err := r.PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Ünicode in an embedded font") {
		t.Errorf("text = %q", text)
	}
}

func TestPermissionBits(t *testing.T) {
	// Reserved low bits clear, high bits set, granted bits present.
	p := uint32(permissionBits(AllowPrint | AllowCopy))
	if p&0x3 != 0 {
		t.Error("reserved bits 1-2 must be zero")
	}
	if p&uint32(AllowPrint) == 0 || p&uint32(AllowCopy) == 0 {
		t.Error("granted permissions missing")
	}
	if p&uint32(AllowModify) != 0 {
		t.Error("ungranted permission present")
	}
	if p&0xFFFFF000 != 0xFFFFF000 {
		t.Error("reserved high bits must be set")
	}
	if permissionBits(AllowAll)&0x3 != 0 {
		t.Error("AllowAll must still clear reserved bits")
	}
}

// TestEncryptedMergePreservesContent decrypts a protected file and merges
// it into a new, unprotected document.
func TestEncryptedMergePreservesContent(t *testing.T) {
	data := encryptedDoc(t, "pw", "", AllowAll, AES128)
	r, err := NewReaderPassword(data, "pw")
	if err != nil {
		t.Fatal(err)
	}
	out := New()
	if err := out.AppendPDF(r); err != nil {
		t.Fatal(err)
	}
	plain := docBytes(t, out)
	if !containsInDeflate(plain, "secret contents") {
		t.Error("imported page lost its content")
	}
	if _, err := NewReader(plain); err != nil {
		t.Fatal(err)
	}
}

func TestDocumentIDStable(t *testing.T) {
	build := func() *Document {
		d := New()
		d.CreationDate = d.CreationDate.Truncate(0)
		d.SetInfo(Info{Title: "stable"})
		p := d.AddPage()
		p.SetFont(Helvetica, 12)
		p.Text(10, 10, "same")
		return d
	}
	a, b := build(), build()
	a.CreationDate = b.CreationDate
	if !bytes.Equal(a.documentID(), b.documentID()) {
		t.Error("document ID is not stable across identical documents")
	}
	c := build()
	c.CreationDate = b.CreationDate
	c.pages[0].Text(10, 30, "different")
	if bytes.Equal(b.documentID(), c.documentID()) {
		t.Error("document ID ignores page content")
	}
}
