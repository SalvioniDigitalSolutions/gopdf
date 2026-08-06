package gopdf

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestFloatFormat(t *testing.T) {
	cases := map[float64]string{
		0:        "0",
		1:        "1",
		-1:       "-1",
		1.5:      "1.5",
		1.25:     "1.25",
		1.2345:   "1.234",
		-0.0001:  "0",
		595.28:   "595.28",
		100.0001: "100",
	}
	for in, want := range cases {
		if got := fl(in); got != want {
			t.Errorf("fl(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestEscapeString(t *testing.T) {
	got := string(escapeString([]byte(`a(b)c\d` + "\n")))
	want := `a\(b\)c\\d\n`
	if got != want {
		t.Errorf("escapeString = %q, want %q", got, want)
	}
}

func TestWinAnsiEncode(t *testing.T) {
	cases := []struct {
		in   string
		want []byte
	}{
		{"abc", []byte{'a', 'b', 'c'}},
		{"é", []byte{0xE9}},
		{"€", []byte{0x80}},
		{"—", []byte{0x97}},
		{"“x”", []byte{0x93, 'x', 0x94}},
		{"漢", []byte{'?'}},
	}
	for _, c := range cases {
		if got := winAnsiEncode(c.in); !bytes.Equal(got, c.want) {
			t.Errorf("winAnsiEncode(%q) = % X, want % X", c.in, got, c.want)
		}
	}
}

func TestTextWidth(t *testing.T) {
	// Helvetica AFM widths: H=722 e=556 l=222 o=556.
	want := float64(722+556+222+222+556) * 10 / 1000
	if got := Helvetica.TextWidth("Hello", 10); math.Abs(got-want) > 1e-9 {
		t.Errorf("Helvetica width = %v, want %v", got, want)
	}
	// Courier is monospaced at 600 for every character.
	if got := Courier.TextWidth("Hé漢!", 10); math.Abs(got-24) > 1e-9 {
		t.Errorf("Courier width = %v, want 24", got)
	}
}

func TestPdfTextString(t *testing.T) {
	if got := pdfTextString("plain (ascii)"); got != `(plain \(ascii\))` {
		t.Errorf("ascii string = %q", got)
	}
	if got := pdfTextString("café"); got != "<FEFF00630061006600E9>" {
		t.Errorf("unicode string = %q", got)
	}
}

func TestEmptyDocument(t *testing.T) {
	var buf bytes.Buffer
	if _, err := New().WriteTo(&buf); err == nil {
		t.Fatal("expected error for document with no pages")
	}
}

// buildTestDoc creates a document exercising text, graphics and both image
// code paths.
func buildTestDoc(t *testing.T) *Document {
	t.Helper()
	doc := New()
	doc.CreationDate = doc.CreationDate.UTC()
	doc.SetInfo(Info{Title: "Test — Résumé", Author: "gopdf"})

	page := doc.AddPage()
	page.SetFont(HelveticaBold, 18)
	page.SetFillColor(RGB(20, 40, 90))
	page.Text(72, 90, "Structure test")
	page.SetStrokeColor(Black)
	page.SetDash(4, 2)
	page.Line(72, 100, 400, 100)
	page.SetDash()
	page.Rect(72, 120, 100, 50, FillStroke)
	page.Circle(300, 145, 25, Stroke)
	page.Polygon(Fill, 400, 120, 450, 170, 350, 170)

	// Opaque image.
	solid := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := range solid.Pix {
		solid.Pix[i] = 0xFF
	}
	img1, err := doc.AddImage(solid)
	if err != nil {
		t.Fatal(err)
	}
	page.DrawImage(img1, 72, 200, 40, 40)

	// Translucent image, must produce an SMask.
	trans := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for i := 3; i < len(trans.Pix); i += 4 {
		trans.Pix[i] = 128
	}
	img2, err := doc.AddImage(trans)
	if err != nil {
		t.Fatal(err)
	}
	page.DrawImage(img2, 130, 200, 40, 40)

	page2 := doc.AddPageSize(Letter.Landscape())
	page2.SetFont(TimesItalic, 12)
	page2.Text(72, 72, "second page — landscape")
	return doc
}

// verifyXref checks the file's framing: header, EOF marker, a startxref
// that points at the xref table, and xref entries that each point at the
// matching "N 0 obj" header. It returns the object count.
func verifyXref(t *testing.T, out []byte) int {
	t.Helper()
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Error("missing PDF header")
	}
	if !bytes.HasSuffix(out, []byte("%%EOF\n")) {
		t.Error("missing EOF marker")
	}
	m := regexp.MustCompile(`startxref\n(\d+)\n%%EOF\n$`).FindSubmatch(out)
	if m == nil {
		t.Fatal("missing startxref")
	}
	xrefOff, _ := strconv.Atoi(string(m[1]))
	if !bytes.HasPrefix(out[xrefOff:], []byte("xref\n")) {
		t.Fatalf("startxref %d does not point at xref table", xrefOff)
	}
	lines := strings.Split(string(out[xrefOff:]), "\n")
	if len(lines) < 3 {
		t.Fatal("truncated xref table")
	}
	var count int
	fmt.Sscanf(lines[1], "0 %d", &count)
	for i := 1; i < count; i++ {
		off, err := strconv.Atoi(strings.Fields(lines[2+i])[0])
		if err != nil {
			t.Fatalf("bad xref entry %d: %q", i, lines[2+i])
		}
		want := []byte(fmt.Sprintf("%d 0 obj\n", i))
		if !bytes.HasPrefix(out[off:], want) {
			t.Errorf("xref entry %d (offset %d) does not point at %q", i, off, want)
		}
	}
	return count - 1
}

func TestDocumentStructure(t *testing.T) {
	doc := buildTestDoc(t)
	var buf bytes.Buffer
	n, err := doc.WriteTo(&buf)
	if err != nil {
		t.Fatal(err)
	}
	out := buf.Bytes()
	if int64(len(out)) != n {
		t.Errorf("WriteTo returned %d, wrote %d bytes", n, len(out))
	}

	// Expected: catalog, pages, info, 2 fonts, images (2 + 1 smask),
	// 2 pages * 2 objects.
	wantObjs := 3 + 2 + 3 + 4
	if got := verifyXref(t, out); got != wantObjs {
		t.Errorf("object count = %d, want %d", got, wantObjs)
	}

	for _, want := range []string{"/SMask", "/Type /Catalog", "/Count 2", "/BaseFont /Helvetica-Bold", "/BaseFont /Times-Italic", "<FEFF"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("output missing %q", want)
		}
	}
}

func TestUncompressedContent(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 12)
	page.Text(100, 200, "find me")
	page.Rect(10, 20, 30, 40, Stroke)
	var buf bytes.Buffer
	if _, err := doc.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	// Baseline at y=200 on an A4 page: 841.89 - 200 = 641.89.
	for _, want := range []string{"(find me) Tj", "100 641.89 Td", "/F1 12 Tf", "10 781.89 30 40 re S"} {
		if !strings.Contains(out, want) {
			t.Errorf("content stream missing %q", want)
		}
	}
}

func TestAddImageReaderPNG(t *testing.T) {
	m := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			m.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 32), G: uint8(y * 32), B: 100, A: 255})
		}
	}
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, m); err != nil {
		t.Fatal(err)
	}
	doc := New()
	img, err := doc.AddImageReader(&pngBuf)
	if err != nil {
		t.Fatal(err)
	}
	if img.Width() != 8 || img.Height() != 8 {
		t.Errorf("image size = %dx%d, want 8x8", img.Width(), img.Height())
	}
	if doc.images[0].dct {
		t.Error("PNG must not be embedded as DCT")
	}
	if doc.images[0].smask != nil {
		t.Error("opaque PNG must not produce an SMask")
	}
	if len(doc.images[0].data) != 8*8*3 {
		t.Errorf("raw data length = %d, want %d", len(doc.images[0].data), 8*8*3)
	}
}

func TestPageDefaults(t *testing.T) {
	doc := New()
	page := doc.AddPage()
	if page.Width() != A4.W || page.Height() != A4.H {
		t.Errorf("default page = %vx%v, want A4", page.Width(), page.Height())
	}
	// Text without SetFont falls back to Helvetica 12.
	page.Text(10, 10, "x")
	if page.font != Helvetica || page.fontSize != 12 {
		t.Errorf("default font = %v %v", page.font, page.fontSize)
	}
	if got := Letter.Landscape(); got.W != 792 || got.H != 612 {
		t.Errorf("Letter.Landscape() = %v", got)
	}
}
