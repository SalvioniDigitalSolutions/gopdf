package gopdf

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// faxFixture hand-builds a one-page PDF whose only content is an image
// XObject holding raw CCITT data, described by imageDict. The Document
// API cannot author this — it re-encodes pixels — and that is the point:
// these files look like real scanned faxes.
func faxFixture(t *testing.T, fax []byte, imageDict string) []byte {
	t.Helper()
	var buf bytes.Buffer
	offsets := map[int]int{}
	add := func(num int, body string) {
		offsets[num] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", num, body)
	}
	buf.WriteString("%PDF-1.4\n")
	add(1, "<< /Type /Catalog /Pages 2 0 R >>")
	add(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	add(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R "+
		"/Resources << /XObject << /Im0 5 0 R >> >> >>")
	content := "q 153 0 0 55 100 600 cm /Im0 Do Q"
	add(4, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
	offsets[5] = buf.Len()
	fmt.Fprintf(&buf, "5 0 obj\n<< /Type /XObject /Subtype /Image %s /Length %d >>\nstream\n", imageDict, len(fax))
	buf.Write(fax)
	buf.WriteString("\nendstream\nendobj\n")

	xrefOff := buf.Len()
	buf.WriteString("xref\n0 6\n0000000000 65535 f \n")
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xrefOff)
	return buf.Bytes()
}

// gopherPNG loads the reference rendering the CCITT fixtures encode.
func gopherPNG(t *testing.T) image.Image {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "ccitt", "bw-gopher.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// decodeFaxPDF embeds the named CCITT fixture in a PDF and decodes it
// back through PageImages.
func decodeFaxPDF(t *testing.T, fixture, imageDict string) image.Image {
	t.Helper()
	fax, err := os.ReadFile(filepath.Join("testdata", "ccitt", fixture))
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewReader(faxFixture(t, fax, imageDict))
	if err != nil {
		t.Fatal(err)
	}
	imgs, err := r.PageImages(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 1 {
		t.Fatalf("found %d images, want 1", len(imgs))
	}
	m, err := imgs[0].Decode()
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return m
}

func diffImages(t *testing.T, got, want image.Image) {
	t.Helper()
	if got.Bounds().Dx() != want.Bounds().Dx() || got.Bounds().Dy() != want.Bounds().Dy() {
		t.Fatalf("decoded %v, want %v", got.Bounds(), want.Bounds())
	}
	bad := 0
	for y := 0; y < want.Bounds().Dy(); y++ {
		for x := 0; x < want.Bounds().Dx(); x++ {
			g := color.GrayModel.Convert(got.At(got.Bounds().Min.X+x, got.Bounds().Min.Y+y)).(color.Gray)
			w := color.GrayModel.Convert(want.At(want.Bounds().Min.X+x, want.Bounds().Min.Y+y)).(color.Gray)
			if g != w {
				bad++
			}
		}
	}
	if bad > 0 {
		t.Errorf("%d pixels differ from the reference image", bad)
	}
}

func TestDecodeCCITTGroup4(t *testing.T) {
	const dict = "/Width 153 /Height 55 /BitsPerComponent 1 /ColorSpace /DeviceGray " +
		"/Filter /CCITTFaxDecode /DecodeParms << /K -1 /Columns 153 /Rows 55 >>"
	diffImages(t, decodeFaxPDF(t, "bw-gopher.ccitt_group4", dict), gopherPNG(t))
}

func TestDecodeCCITTGroup4BlackIs1(t *testing.T) {
	// An encoder asked for BlackIs1 writes the runs swapped, so the
	// stream is the plain encoding of the inverted picture; decoding it
	// with BlackIs1 honoured lands back on the original.
	const dict = "/Width 153 /Height 55 /BitsPerComponent 1 /ColorSpace /DeviceGray " +
		"/Filter /CCITTFaxDecode /DecodeParms << /K -1 /Columns 153 /Rows 55 /BlackIs1 true >>"
	diffImages(t, decodeFaxPDF(t, "bw-gopher-inverted.ccitt_group4", dict), gopherPNG(t))
}

func TestDecodeCCITTGroup4ByteAligned(t *testing.T) {
	const dict = "/Width 153 /Height 55 /BitsPerComponent 1 /ColorSpace /DeviceGray " +
		"/Filter /CCITTFaxDecode /DecodeParms << /K -1 /Columns 153 /Rows 55 /EncodedByteAlign true >>"
	diffImages(t, decodeFaxPDF(t, "bw-gopher-aligned.ccitt_group4", dict), gopherPNG(t))
}

func TestDecodeCCITTGroup3(t *testing.T) {
	const dict = "/Width 153 /Height 55 /BitsPerComponent 1 /ColorSpace /DeviceGray " +
		"/Filter /CCITTFaxDecode /DecodeParms << /K 0 /Columns 153 /Rows 55 >>"
	diffImages(t, decodeFaxPDF(t, "bw-gopher.ccitt_group3", dict), gopherPNG(t))
}

func TestDecodeCCITTGroup3TwoDimensionalRefused(t *testing.T) {
	fax, err := os.ReadFile(filepath.Join("testdata", "ccitt", "bw-gopher.ccitt_group4"))
	if err != nil {
		t.Fatal(err)
	}
	const dict = "/Width 153 /Height 55 /BitsPerComponent 1 /ColorSpace /DeviceGray " +
		"/Filter /CCITTFaxDecode /DecodeParms << /K 4 /Columns 153 /Rows 55 >>"
	r, err := NewReader(faxFixture(t, fax, dict))
	if err != nil {
		t.Fatal(err)
	}
	imgs, err := r.PageImages(0)
	if err != nil || len(imgs) != 1 {
		t.Fatalf("PageImages: %v (%d images)", err, len(imgs))
	}
	if _, err := imgs[0].Decode(); err == nil {
		t.Fatal("expected an error for K > 0")
	}
}

func TestImageObjectNumberIdentity(t *testing.T) {
	doc := New()
	page := doc.AddPage()
	grey := image.NewGray(image.Rect(0, 0, 4, 4))
	img, err := doc.AddImage(grey)
	if err != nil {
		t.Fatal(err)
	}
	other, err := doc.AddImage(image.NewGray(image.Rect(0, 0, 2, 2)))
	if err != nil {
		t.Fatal(err)
	}
	page.DrawImage(img, 10, 10, 50, 50)
	page.DrawImage(img, 100, 10, 50, 50)
	page.DrawImage(other, 200, 10, 50, 50)

	r, err := NewReader(docBytes(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	imgs, err := r.PageImages(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 3 {
		t.Fatalf("found %d draws, want 3", len(imgs))
	}
	if imgs[0].ObjectNumber() == 0 {
		t.Fatal("expected a non-zero object number")
	}
	if imgs[0].ObjectNumber() != imgs[1].ObjectNumber() {
		t.Errorf("same picture drawn twice reports objects %d and %d",
			imgs[0].ObjectNumber(), imgs[1].ObjectNumber())
	}
	if imgs[2].ObjectNumber() == imgs[0].ObjectNumber() {
		t.Error("different pictures share an object number")
	}
}

func TestSetInfoEmptyLeavesNoMetadata(t *testing.T) {
	doc := New()
	doc.SetInfo(Info{})
	doc.CreationDate = time.Time{}
	page := doc.AddPage()
	page.SetFont(Helvetica, 12)
	page.Text(72, 72, "clean")

	out := docBytes(t, doc)
	for _, banned := range []string{"/Producer", "/Creator", "/Title", "/Author", "/CreationDate"} {
		if bytes.Contains(out, []byte(banned)) {
			t.Errorf("metadata entry %s present in a document that asked for none", banned)
		}
	}

	// The default, when nobody calls SetInfo, still names the library.
	plain := New()
	plain.AddPage()
	if !bytes.Contains(docBytes(t, plain), []byte("/Producer (gopdf)")) {
		t.Error("default document no longer stamps Producer gopdf")
	}
}
