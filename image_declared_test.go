package gopdf

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

// edgeTileJPEG is the right-hand tile of a scan stored in 64-pixel
// tiles on a page 16 pixels wider than three tiles: a whole 64x32 tile
// whose first 16 columns are the page (paper) and whose other 48 are
// what the scanning software left in its buffer — here a dark block,
// in real files the text of the tile before.
func edgeTileJPEG(t *testing.T) []byte {
	t.Helper()
	m := image.NewGray(image.Rect(0, 0, 64, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 64; x++ {
			v := uint8(245)
			if x >= 24 && x < 60 && y >= 8 && y < 24 {
				v = 10
			}
			m.SetGray(x, y, color.Gray{Y: v})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, m, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const edgeTileDict = "/Width 16 /Height 32 /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /DCTDecode"

// The tile is drawn into a 16 x 32 point box at (100, 100).
const edgeTileContent = "q 16 0 0 32 100 100 cm /Im0 Do Q"

func edgeTileImage(t *testing.T, pdf []byte) ImageRef {
	t.Helper()
	r, err := NewReader(pdf)
	if err != nil {
		t.Fatal(err)
	}
	imgs, err := r.PageImages(0)
	if err != nil || len(imgs) != 1 {
		t.Fatalf("PageImages: %v, %d images", err, len(imgs))
	}
	return imgs[0]
}

func darkPixels(m image.Image, box image.Rectangle) int {
	n := 0
	for y := box.Min.Y; y < box.Max.Y; y++ {
		for x := box.Min.X; x < box.Max.X; x++ {
			if color.GrayModel.Convert(m.At(x, y)).(color.Gray).Y < 128 {
				n++
			}
		}
	}
	return n
}

// The part of a picture's data outside its declared size is on no page:
// decoding leaves it out, drawing the page does not squeeze it into the
// picture's box, and the JPEG is not handed out whole.
func TestImageDataBeyondTheDeclaredSizeStaysHidden(t *testing.T) {
	pdf := rawImagePage(t, edgeTileJPEG(t), edgeTileDict, edgeTileContent)
	im := edgeTileImage(t, pdf)

	m, err := im.Decode()
	if err != nil {
		t.Fatal(err)
	}
	if b := m.Bounds(); b.Dx() != 16 || b.Dy() != 32 {
		t.Fatalf("decoded %v, want the declared 16x32", b)
	}
	if n := darkPixels(m, m.Bounds()); n > 0 {
		t.Errorf("%d dark pixels in the decoded picture: the hidden part came along", n)
	}

	r, _ := NewReader(pdf)
	page, err := r.RenderPage(0, RenderOpts{DPI: 72, IncludeRasterImages: true})
	if err != nil {
		t.Fatal(err)
	}
	// The box, in pixels from the top-left at 72 dpi: x 100-116,
	// y 792-132 .. 792-100.
	box := image.Rect(100, 792-132, 116, 792-100)
	if n := darkPixels(page, box); n > 0 {
		t.Errorf("%d dark pixels drawn in the picture's box: the hidden part was squeezed into view", n)
	}

	if !im.HoldsUnshownPixels() {
		t.Error("HoldsUnshownPixels = false for a 64-pixel JPEG declared 16 wide")
	}
	if _, ok := im.JPEG(); ok {
		t.Error("JPEG() handed out the whole stream of a picture that hides part of it")
	}
	// Pixels another decoder made of the same stream are cut alike.
	whole, err := jpeg.Decode(bytes.NewReader(edgeTileJPEG(t)))
	if err != nil {
		t.Fatal(err)
	}
	if f := im.FitDecoded(whole); f.Bounds().Dx() != 16 || darkPixels(f, f.Bounds()) > 0 {
		t.Errorf("FitDecoded left %v with %d dark pixels", f.Bounds(), darkPixels(f, f.Bounds()))
	}
}

// A picture whose data is its declared size is untouched by all of it.
func TestImageDataOfTheDeclaredSizeIsUnchanged(t *testing.T) {
	data := edgeTileJPEG(t)
	pdf := rawImagePage(t, data, "/Width 64 /Height 32 /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /DCTDecode",
		"q 64 0 0 32 100 100 cm /Im0 Do Q")
	im := edgeTileImage(t, pdf)
	m, err := im.Decode()
	if err != nil {
		t.Fatal(err)
	}
	if b := m.Bounds(); b.Dx() != 64 || b.Dy() != 32 {
		t.Fatalf("decoded %v, want 64x32", b)
	}
	if darkPixels(m, m.Bounds()) == 0 {
		t.Error("the block that is part of this picture is gone")
	}
	if im.HoldsUnshownPixels() {
		t.Error("HoldsUnshownPixels = true for a picture of its declared size")
	}
	if got, ok := im.JPEG(); !ok || !bytes.Equal(got, data) {
		t.Error("JPEG() no longer hands out a picture's own stream")
	}
}

// Fax data wider than the picture (a fax tile at a page's edge, or a
// strip narrower than its transmission width) holds columns no viewer
// shows.
func TestFaxDataWiderThanThePictureHoldsUnshownPixels(t *testing.T) {
	fax, err := os.ReadFile(filepath.Join("testdata", "ccitt", "bw-gopher.ccitt_group4"))
	if err != nil {
		t.Skip("fax fixture missing:", err)
	}
	narrow := rawImagePage(t, fax, "/Width 100 /Height 55 /BitsPerComponent 1 /ColorSpace /DeviceGray "+
		"/Filter /CCITTFaxDecode /DecodeParms << /K -1 /Columns 153 /Rows 55 >>", "q 100 0 0 55 100 600 cm /Im0 Do Q")
	if !edgeTileImage(t, narrow).HoldsUnshownPixels() {
		t.Error("HoldsUnshownPixels = false for fax data 153 columns wide in a 100-pixel picture")
	}
	full := rawImagePage(t, fax, "/Width 153 /Height 55 /BitsPerComponent 1 /ColorSpace /DeviceGray "+
		"/Filter /CCITTFaxDecode /DecodeParms << /K -1 /Columns 153 /Rows 55 >>", "q 153 0 0 55 100 600 cm /Im0 Do Q")
	if edgeTileImage(t, full).HoldsUnshownPixels() {
		t.Error("HoldsUnshownPixels = true for fax data of the picture's own width")
	}
}

func TestJPXSizeReadsTheSIZMarker(t *testing.T) {
	// SOC, SIZ (Lsiz, Rsiz), Xsiz=600, Ysiz=400, XOsiz=100, YOsiz=0.
	cs := []byte{0xFF, 0x4F, 0xFF, 0x51, 0, 41, 0, 0,
		0, 0, 2, 88, 0, 0, 1, 144, 0, 0, 0, 100, 0, 0, 0, 0}
	w, h, ok := jpxSize(append([]byte("jp2 box header"), cs...))
	if !ok || w != 500 || h != 400 {
		t.Errorf("jpxSize = %d x %d, %v; want 500 x 400", w, h, ok)
	}
	if _, _, ok := jpxSize([]byte("no codestream here")); ok {
		t.Error("jpxSize found a size where there is no codestream")
	}
}
