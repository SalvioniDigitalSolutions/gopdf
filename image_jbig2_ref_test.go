package gopdf

import (
	"bytes"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Checking the JBIG2 decoder against another implementation.
//
// The round-trip tests prove this package's encoder and decoder agree
// with each other, which is worth something and is not the same as being
// right: two halves written from one misreading agree perfectly. Poppler
// ships a JBIG2 decoder written by other people from the same
// specification, so putting a stream through both and comparing the
// pixels is the check that closes that gap.
//
// The comparison runs in both directions at once. A stream this package
// encoded and Poppler decodes tests the encoder; the same stream decoded
// here tests the decoder; and the two agreeing on every pixel is hard to
// arrange by accident.
//
// Poppler is not a build dependency, so these skip where it is absent.

// popplerDecodeJBIG2 puts a JBIG2 stream in a PDF and asks Poppler for
// the pixels.
func popplerDecodeJBIG2(t *testing.T, stream []byte, w, h int) *bitmap {
	t.Helper()
	if _, err := exec.LookPath("pdfimages"); err != nil {
		t.Skip("pdfimages is not installed; the reference comparison needs Poppler")
	}
	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "scan.pdf")
	if err := os.WriteFile(pdfPath, jbig2PDF(t, stream, w, h), 0o600); err != nil {
		t.Fatal(err)
	}
	// -png keeps one byte per pixel of grey rather than a packed bitmap.
	base := filepath.Join(dir, "img")
	out, err := exec.Command("pdfimages", "-png", pdfPath, base).CombinedOutput()
	if err != nil {
		t.Fatalf("pdfimages refused the file: %v\n%s", err, out)
	}
	matches, _ := filepath.Glob(base + "-*.png")
	if len(matches) == 0 {
		t.Fatalf("pdfimages extracted nothing\n%s", out)
	}
	f, err := os.Open(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("the extracted image does not decode: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != w || b.Dy() != h {
		t.Fatalf("Poppler extracted %dx%d, want %dx%d", b.Dx(), b.Dy(), w, h)
	}
	// Back to one byte per pixel, 1 for black, as this package holds it.
	got := newBitmap(w, h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			if r>>8 < 128 && g>>8 < 128 && bl>>8 < 128 {
				got.set(x, y, 1)
			}
		}
	}
	return got
}

// jbig2PDF is the smallest document that carries one JBIG2 image.
func jbig2PDF(t *testing.T, stream []byte, w, h int) []byte {
	t.Helper()
	doc := New()
	doc.Compress = false
	doc.AddPage()
	r := NewReaderOrFail(t, docBytes(t, doc))
	u := Update(r)
	img := u.AddObject(NewStream(Dict{
		"Type": Name("XObject"), "Subtype": Name("Image"),
		"Width": int64(w), "Height": int64(h),
		"ColorSpace": Name("DeviceGray"), "BitsPerComponent": int64(1),
		"Filter": Name("JBIG2Decode"),
	}, stream))
	res, _ := r.InheritedPageValue(0, "Resources").(Dict)
	merged := res.Clone()
	if merged == nil {
		merged = Dict{}
	}
	merged["XObject"] = Dict{"Im0": img}
	if err := u.SetPageEntry(0, "Resources", merged); err != nil {
		t.Fatal(err)
	}
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	page.op("q 200 0 0 100 100 600 cm /Im0 Do Q")
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// comparePixels reports the first pixel two bitmaps disagree on.
func comparePixels(t *testing.T, what string, got, want *bitmap) {
	t.Helper()
	if got.w != want.w || got.h != want.h {
		t.Fatalf("%s: %dx%d against %dx%d", what, got.w, got.h, want.w, want.h)
	}
	diff := 0
	first := ""
	for y := 0; y < want.h; y++ {
		for x := 0; x < want.w; x++ {
			if got.at(x, y) != want.at(x, y) {
				diff++
				if first == "" {
					first = pixelDesc(x, y, got.at(x, y), want.at(x, y))
				}
			}
		}
	}
	if diff != 0 {
		t.Errorf("%s: %d of %d pixels differ; first at %s",
			what, diff, want.w*want.h, first)
	}
}

func pixelDesc(x, y int, got, want byte) string {
	return fmt.Sprintf("(%d,%d) is %d and should be %d", x, y, got, want)
}

// TestJBIG2AgainstPopplerGenericRegion puts a generic region through both
// decoders.
func TestJBIG2AgainstPopplerGenericRegion(t *testing.T) {
	const w, h = 61, 43
	src := jbig2Fixture(w, h)

	for template := 0; template < 4; template++ {
		stream := append(jbig2PageInfo(w, h), jbig2Stream(t, src, template)...)

		// This package's answer.
		pix, err := decodeJBIG2(stream, nil, w, h)
		if err != nil {
			t.Fatalf("template %d: %v", template, err)
		}
		mine := &bitmap{w: w, h: h, pix: pix}

		// Poppler's answer, from the same bytes.
		theirs := popplerDecodeJBIG2(t, stream, w, h)

		comparePixels(t, fmt.Sprintf("template %d against Poppler", template),
			mine, theirs)
		// And both must be the picture that went in, or the two agree on
		// something neither was asked for.
		comparePixels(t, fmt.Sprintf("template %d against the original", template),
			mine, src)
	}
}

// TestJBIG2AgainstPopplerTypicalPrediction covers the shortcut that codes
// a repeated row as one bit, which is where a decoder most easily loses
// its place in the stream.
func TestJBIG2AgainstPopplerTypicalPrediction(t *testing.T) {
	const w, h = 48, 32
	src := newBitmap(w, h)
	for y := 8; y < 24; y++ {
		for x := 6; x < 42; x++ {
			src.set(x, y, 1)
		}
	}
	stream := append(jbig2PageInfo(w, h), jbig2StreamTPGDON(t, src)...)

	pix, err := decodeJBIG2(stream, nil, w, h)
	if err != nil {
		t.Fatal(err)
	}
	mine := &bitmap{w: w, h: h, pix: pix}
	comparePixels(t, "typical prediction against the original", mine, src)
	comparePixels(t, "typical prediction against Poppler", mine,
		popplerDecodeJBIG2(t, stream, w, h))
}

// TestJBIG2AgainstPopplerSymbolCoded is the important one: a dictionary
// of shapes and a text region placing them, which is how a scanner codes
// a page of prose and the part with the most to get wrong.
func TestJBIG2AgainstPopplerSymbolCoded(t *testing.T) {
	symbols := letterShapes()
	// The dictionary sorts by height then width, so the text region's
	// numbers refer to the sorted order.
	sorted := []*bitmap{symbols[1], symbols[0]}
	const w, h = 40, 24
	places := []placement{{0, 2, 2}, {1, 9, 2}, {0, 16, 2}, {1, 4, 13}}

	want := newBitmap(w, h)
	for _, p := range places {
		drawSymbol(want, sorted[p.id], p.s, p.t, 1, false)
	}

	stream := append(jbig2PageInfo(w, h), encodeSymbolDict(t, symbols, 0)...)
	stream = append(stream, encodeTextRegion(t, w, h, sorted, places)...)

	pix, err := decodeJBIG2(stream, nil, w, h)
	if err != nil {
		t.Fatalf("decoding a symbol-coded page: %v", err)
	}
	mine := &bitmap{w: w, h: h, pix: pix}
	comparePixels(t, "symbol-coded page against the original", mine, want)
	comparePixels(t, "symbol-coded page against Poppler", mine,
		popplerDecodeJBIG2(t, stream, w, h))
}

// TestJBIG2AgainstPopplerGlobals: a scanned document keeps its dictionary
// in a globals stream shared by every page, and both decoders have to
// find it there.
func TestJBIG2AgainstPopplerGlobals(t *testing.T) {
	if _, err := exec.LookPath("pdfimages"); err != nil {
		t.Skip("pdfimages is not installed")
	}
	symbols := letterShapes()
	sorted := []*bitmap{symbols[1], symbols[0]}
	const w, h = 40, 20
	places := []placement{{0, 3, 3}, {1, 11, 3}}

	want := newBitmap(w, h)
	for _, p := range places {
		drawSymbol(want, sorted[p.id], p.s, p.t, 1, false)
	}
	globals := encodeSymbolDict(t, symbols, 0)
	region := append(jbig2PageInfo(w, h), encodeTextRegion(t, w, h, sorted, places)...)

	pix, err := decodeJBIG2(region, globals, w, h)
	if err != nil {
		t.Fatal(err)
	}
	mine := &bitmap{w: w, h: h, pix: pix}
	comparePixels(t, "with a globals stream, against the original", mine, want)

	// The same split, in a document Poppler reads.
	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "scan.pdf")
	if err := os.WriteFile(pdfPath, jbig2PDFWithGlobals(t, region, globals, w, h),
		0o600); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(dir, "img")
	if out, err := exec.Command("pdfimages", "-png", pdfPath, base).CombinedOutput(); err != nil {
		t.Fatalf("pdfimages refused the file: %v\n%s", err, out)
	}
	matches, _ := filepath.Glob(base + "-*.png")
	if len(matches) == 0 {
		t.Fatal("pdfimages extracted nothing")
	}
	f, err := os.Open(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	theirs := newBitmap(w, h)
	b := img.Bounds()
	for y := 0; y < h && y < b.Dy(); y++ {
		for x := 0; x < w && x < b.Dx(); x++ {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			if r>>8 < 128 && g>>8 < 128 && bl>>8 < 128 {
				theirs.set(x, y, 1)
			}
		}
	}
	comparePixels(t, "with a globals stream, against Poppler", mine, theirs)
}

// jbig2PDFWithGlobals builds a document whose image points at a shared
// globals stream, as a multi-page scan does.
func jbig2PDFWithGlobals(t *testing.T, region, globals []byte, w, h int) []byte {
	t.Helper()
	doc := New()
	doc.Compress = false
	doc.AddPage()
	r := NewReaderOrFail(t, docBytes(t, doc))
	u := Update(r)
	g := u.AddObject(NewStream(Dict{}, globals))
	img := u.AddObject(NewStream(Dict{
		"Type": Name("XObject"), "Subtype": Name("Image"),
		"Width": int64(w), "Height": int64(h),
		"ColorSpace": Name("DeviceGray"), "BitsPerComponent": int64(1),
		"Filter":      Name("JBIG2Decode"),
		"DecodeParms": Dict{"JBIG2Globals": g},
	}, region))
	res, _ := r.InheritedPageValue(0, "Resources").(Dict)
	merged := res.Clone()
	if merged == nil {
		merged = Dict{}
	}
	merged["XObject"] = Dict{"Im0": img}
	if err := u.SetPageEntry(0, "Resources", merged); err != nil {
		t.Fatal(err)
	}
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	page.op("q 200 0 0 100 100 600 cm /Im0 Do Q")
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// jbig2Fixture is a picture with the shapes a scan is made of.
func jbig2Fixture(w, h int) *bitmap {
	b := newBitmap(w, h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var v byte
			switch {
			case x == 0 || y == 0 || x == w-1 || y == h-1:
				v = 1
			case x == y:
				v = 1
			case x > 30 && x < 45 && y > 10 && y < 20:
				v = 1
			case (x*7+y*13)%23 == 0:
				v = 1
			}
			b.set(x, y, v)
		}
	}
	return b
}

// jbig2StreamTPGDON wraps a bitmap coded with the typical-prediction
// shortcut turned on.
func jbig2StreamTPGDON(t *testing.T, b *bitmap) []byte {
	t.Helper()
	at := defaultAT(0)
	enc := newMQEncoder()
	cx := make([]mqContext, 1<<16)
	ltp := false
	for y := 0; y < b.h; y++ {
		same := y > 0 && bytes.Equal(b.pix[y*b.w:(y+1)*b.w], b.pix[(y-1)*b.w:y*b.w])
		bit := 0
		if same != ltp {
			bit = 1
		}
		enc.encode(&cx[tpgdonContext(0)], bit)
		if bit == 1 {
			ltp = !ltp
		}
		if ltp {
			continue
		}
		for x := 0; x < b.w; x++ {
			enc.encode(&cx[genericContext(b, x, y, 0, at)], int(b.at(x, y)))
		}
	}
	coded := enc.flush()

	var region bytes.Buffer
	put32 := func(v int) {
		region.Write([]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
	}
	put32(b.w)
	put32(b.h)
	put32(0)
	put32(0)
	region.WriteByte(0)
	region.WriteByte(1 << 3) // flags: arithmetic, template 0, TPGDON on
	for _, a := range at {
		region.Write([]byte{byte(int8(a[0])), byte(int8(a[1]))})
	}
	region.Write(coded)
	return segmentWrap(1, 38, region.Bytes())
}

// jbig2PageInfo is the page information segment.
//
// Poppler refuses a stream whose first segment is not one of these, and
// it is right to: the specification says a page's segments open with it.
// This package's decoder does not need the segment and ignores it, which
// is the leniency a reader should have and not an excuse for a writer.
func jbig2PageInfo(w, h int) []byte {
	var b bytes.Buffer
	put32 := func(v int) {
		b.Write([]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
	}
	put32(w)
	put32(h)
	put32(0)              // horizontal resolution, unstated
	put32(0)              // vertical resolution
	b.WriteByte(1)        // flags: the page is lossless
	b.Write([]byte{0, 0}) // not striped
	return segmentWrap(0, 48, b.Bytes())
}
