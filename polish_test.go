package gopdf

import (
	"bytes"
	"image"
	"image/color"
	"strings"
	"testing"
)

func TestKerning(t *testing.T) {
	font, err := LoadFont(testFontPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if font.ttf.kern == nil {
		t.Skip("test font has no format-0 kern table")
	}
	av := font.TextWidth("AV", 100)
	sum := font.TextWidth("A", 100) + font.TextWidth("V", 100)
	if av >= sum {
		t.Errorf("kerned AV (%v) not narrower than A+V (%v)", av, sum)
	}

	// The content stream must carry the adjustment in a TJ array.
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(font, 12)
	page.Text(10, 10, "AV")
	var buf bytes.Buffer
	if _, err := doc.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "> ") || !strings.Contains(buf.String(), "] TJ") {
		t.Error("content stream missing kerned TJ array")
	}
}

func TestAccentedWidths(t *testing.T) {
	// Accented letters share their base letter's width in the standard
	// fonts, so "café" must measure exactly.
	for _, f := range []*Font{Helvetica, HelveticaBold, TimesRoman, TimesItalic} {
		if got, want := f.TextWidth("é", 10), f.TextWidth("e", 10); got != want {
			t.Errorf("%s: width(é) = %v, want %v", f.Name(), got, want)
		}
		if got, want := f.TextWidth(" ", 10), f.TextWidth(" ", 10); got != want {
			t.Errorf("%s: width(nbsp) = %v, want %v", f.Name(), got, want)
		}
		// The ellipsis is 1000 units in all four families.
		if got, want := f.TextWidth("…", 10), 10.0; got != want {
			t.Errorf("%s: width(…) = %v, want %v", f.Name(), got, want)
		}
	}
	if got, want := Helvetica.TextWidth("×", 10), Helvetica.TextWidth("+", 10); got != want {
		t.Errorf("width(×) = %v, want %v", got, want)
	}
}

func TestGrayImage(t *testing.T) {
	m := image.NewGray(image.Rect(0, 0, 6, 4))
	for i := range m.Pix {
		m.Pix[i] = uint8(i * 10)
	}
	doc := New()
	if _, err := doc.AddImage(m); err != nil {
		t.Fatal(err)
	}
	img := doc.images[0]
	if img.colorSpace != "DeviceGray" {
		t.Errorf("colorSpace = %s, want DeviceGray", img.colorSpace)
	}
	if len(img.data) != 6*4 {
		t.Errorf("data length = %d, want %d", len(img.data), 6*4)
	}
	if img.data[7] != m.Pix[7] {
		t.Error("gray samples not copied faithfully")
	}
}

func TestRGBAUnpremultiply(t *testing.T) {
	m := image.NewRGBA(image.Rect(0, 0, 1, 1))
	// Premultiplied half-transparent pure red: R=128, A=128.
	m.SetRGBA(0, 0, color.RGBA{R: 128, A: 128})
	doc := New()
	if _, err := doc.AddImage(m); err != nil {
		t.Fatal(err)
	}
	img := doc.images[0]
	if img.data[0] != 255 || img.data[1] != 0 || img.data[2] != 0 {
		t.Errorf("un-premultiplied pixel = %v, want [255 0 0]", img.data[:3])
	}
	if img.smask == nil || img.smask[0] != 128 {
		t.Errorf("smask = %v, want [128]", img.smask)
	}
}

func TestStateDeduplication(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	red := RGB(255, 0, 0)
	page.SetFillColor(red)
	page.Rect(0, 0, 10, 10, Fill)
	page.SetFillColor(red) // no-op: already current
	page.Push()
	page.SetFillColor(White)
	page.Pop()
	page.SetFillColor(red) // no-op: Pop restored red
	page.SetLineWidth(2)
	page.SetLineWidth(2) // no-op
	page.SetDash(1, 2)
	page.SetDash(1, 2) // no-op

	var buf bytes.Buffer
	if _, err := doc.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for op, want := range map[string]int{"1 0 0 rg": 1, "2 w": 1, "[1 2] 0 d": 1} {
		if got := strings.Count(out, op); got != want {
			t.Errorf("%q emitted %d times, want %d", op, got, want)
		}
	}
	if !strings.Contains(out, "1 1 1 rg") {
		t.Error("white fill inside Push/Pop was lost")
	}
}

func TestDeterministicOutput(t *testing.T) {
	build := func() *Document {
		doc := buildTestDoc(t)
		doc.CreationDate = doc.CreationDate.Truncate(0)
		return doc
	}
	doc := build()
	var a, b bytes.Buffer
	if _, err := doc.WriteTo(&a); err != nil {
		t.Fatal(err)
	}
	if _, err := doc.WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Error("two WriteTo calls on the same document differ")
	}
}

func TestJpegIsAdobe(t *testing.T) {
	// SOI + APP14 "Adobe" segment.
	adobe := []byte{0xFF, 0xD8, 0xFF, 0xEE, 0x00, 0x0E, 'A', 'd', 'o', 'b', 'e',
		0, 100, 0, 0, 0, 0}
	if !jpegIsAdobe(adobe) {
		t.Error("Adobe APP14 marker not detected")
	}
	plain := []byte{0xFF, 0xD8, 0xFF, 0xDB, 0x00, 0x04, 0, 0}
	if jpegIsAdobe(plain) {
		t.Error("false positive on plain JPEG")
	}
}

func BenchmarkTextPage(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		doc := New()
		for p := 0; p < 10; p++ {
			page := doc.AddPage()
			page.SetFont(Helvetica, 10)
			for line := 0; line < 60; line++ {
				page.Text(72, 72+float64(line)*12,
					"The quick brown fox jumps over the lazy dog, 0123456789.")
			}
		}
		var buf bytes.Buffer
		if _, err := doc.WriteTo(&buf); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAddImageNRGBA(b *testing.B) {
	m := image.NewNRGBA(image.Rect(0, 0, 256, 256))
	for i := range m.Pix {
		m.Pix[i] = uint8(i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		doc := New()
		if _, err := doc.AddImage(m); err != nil {
			b.Fatal(err)
		}
	}
}
