package gopdf

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"strings"
	"testing"
)

// imageFixture builds a document with a colour image, a grey image and a
// translucent one, each drawn at a known place.
func imageFixture(t *testing.T) []byte {
	t.Helper()
	doc := New()
	doc.Compress = false

	rgb := image.NewRGBA(image.Rect(0, 0, 8, 6))
	for y := 0; y < 6; y++ {
		for x := 0; x < 8; x++ {
			rgb.Set(x, y, color.RGBA{R: uint8(x * 30), G: uint8(y * 40), B: 200, A: 255})
		}
	}
	grey := image.NewGray(image.Rect(0, 0, 4, 4))
	for i := range grey.Pix {
		grey.Pix[i] = uint8(i * 16)
	}
	alpha := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for i := 0; i < len(alpha.Pix); i += 4 {
		alpha.Pix[i], alpha.Pix[i+1], alpha.Pix[i+2] = 255, 0, 0
		alpha.Pix[i+3] = uint8(i * 4)
	}

	page := doc.AddPage()
	a, err := doc.AddImage(rgb)
	if err != nil {
		t.Fatal(err)
	}
	b, err := doc.AddImage(grey)
	if err != nil {
		t.Fatal(err)
	}
	c, err := doc.AddImage(alpha)
	if err != nil {
		t.Fatal(err)
	}
	page.DrawImage(a, 50, 60, 120, 90)
	page.DrawImage(b, 200, 60, 40, 40)
	page.DrawImage(c, 300, 60, 40, 40)
	return docBytes(t, doc)
}

func TestPageImagesDiscovery(t *testing.T) {
	r, err := NewReader(imageFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	imgs, err := r.PageImages(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 3 {
		t.Fatalf("found %d images, want 3", len(imgs))
	}

	first := imgs[0]
	if first.Width != 8 || first.Height != 6 {
		t.Errorf("first image is %dx%d, want 8x6", first.Width, first.Height)
	}
	if first.ColorSpace != "DeviceRGB" {
		t.Errorf("colour space = %q", first.ColorSpace)
	}
	if first.BitsPerComponent != 8 {
		t.Errorf("bpc = %d", first.BitsPerComponent)
	}
	// Placement is reported in page coordinates, from the top-left.
	if math.Abs(first.X-50) > 0.01 || math.Abs(first.Y-60) > 0.01 {
		t.Errorf("placement = (%v, %v), want (50, 60)", first.X, first.Y)
	}
	if math.Abs(first.W-120) > 0.01 || math.Abs(first.H-90) > 0.01 {
		t.Errorf("drawn size = %vx%v, want 120x90", first.W, first.H)
	}
	if imgs[1].ColorSpace != "DeviceGray" {
		t.Errorf("second image colour space = %q, want DeviceGray", imgs[1].ColorSpace)
	}
}

func TestImageDecode(t *testing.T) {
	r, err := NewReader(imageFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	imgs, _ := r.PageImages(0)

	// Colour image: the pixels must come back as they went in.
	m, err := imgs[0].Decode()
	if err != nil {
		t.Fatal(err)
	}
	if m.Bounds().Dx() != 8 || m.Bounds().Dy() != 6 {
		t.Fatalf("decoded %v, want 8x6", m.Bounds())
	}
	got := color.NRGBAModel.Convert(m.At(3, 2)).(color.NRGBA)
	want := color.NRGBA{R: 90, G: 80, B: 200, A: 255}
	if got != want {
		t.Errorf("pixel (3,2) = %v, want %v", got, want)
	}

	// Grey image.
	g, err := imgs[1].Decode()
	if err != nil {
		t.Fatal(err)
	}
	gg := color.GrayModel.Convert(g.At(1, 0)).(color.Gray)
	if gg.Y != 16 {
		t.Errorf("grey pixel = %d, want 16", gg.Y)
	}

	// Translucent image: the soft mask must come back as alpha.
	a, err := imgs[2].Decode()
	if err != nil {
		t.Fatal(err)
	}
	edge := color.NRGBAModel.Convert(a.At(0, 0)).(color.NRGBA)
	if edge.A != 0 {
		t.Errorf("first pixel alpha = %d, want 0", edge.A)
	}
	later := color.NRGBAModel.Convert(a.At(3, 3)).(color.NRGBA)
	if later.A == 0 || later.R != 255 {
		t.Errorf("last pixel = %v, want opaque red", later)
	}
}

func TestImageDecodeJPEG(t *testing.T) {
	doc := New()
	page := doc.AddPage()
	img, err := doc.AddImageReader(bytes.NewReader(makeTestJPEG(t)))
	if err != nil {
		t.Fatal(err)
	}
	page.DrawImage(img, 40, 40, 100, 100)

	r, err := NewReader(docBytes(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	imgs, _ := r.PageImages(0)
	if len(imgs) != 1 {
		t.Fatalf("found %d images, want 1", len(imgs))
	}
	m, err := imgs[0].Decode()
	if err != nil {
		t.Fatalf("JPEG did not decode: %v", err)
	}
	if m.Bounds().Dx() != 32 || m.Bounds().Dy() != 32 {
		t.Errorf("decoded %v, want 32x32", m.Bounds())
	}
}

func TestReplaceImage(t *testing.T) {
	src := imageFixture(t)
	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	imgs, _ := r.PageImages(0)

	replacement := image.NewRGBA(image.Rect(0, 0, 3, 3))
	for i := range replacement.Pix {
		replacement.Pix[i] = 0
	}
	for x := 0; x < 3; x++ {
		for y := 0; y < 3; y++ {
			replacement.Set(x, y, color.RGBA{R: 10, G: 220, B: 30, A: 255})
		}
	}

	u := Update(r)
	if err := u.ReplaceImage(imgs[0], replacement); err != nil {
		t.Fatal(err)
	}
	out := updatedBytes(t, u)
	if !bytes.HasPrefix(out, src) {
		t.Fatal("original bytes were not preserved")
	}
	verifyXref(t, out)

	r2, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	after, err := r2.PageImages(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 3 {
		t.Fatalf("found %d images after replacing, want 3", len(after))
	}
	if after[0].Width != 3 || after[0].Height != 3 {
		t.Errorf("replaced image is %dx%d, want 3x3", after[0].Width, after[0].Height)
	}
	// The placement is untouched: a different pixel size fills the same box.
	if math.Abs(after[0].W-120) > 0.01 || math.Abs(after[0].H-90) > 0.01 {
		t.Errorf("placement changed to %vx%v, want 120x90", after[0].W, after[0].H)
	}
	m, err := after[0].Decode()
	if err != nil {
		t.Fatal(err)
	}
	got := color.NRGBAModel.Convert(m.At(1, 1)).(color.NRGBA)
	if got.G < 200 || got.R > 40 {
		t.Errorf("replaced pixel = %v, want the green that was supplied", got)
	}
	// The other images are untouched.
	if after[1].Width != 4 || after[2].Width != 4 {
		t.Error("replacing one image disturbed the others")
	}
}

func TestReplaceImageWithAlpha(t *testing.T) {
	r, _ := NewReader(imageFixture(t))
	imgs, _ := r.PageImages(0)

	m := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for i := 0; i < len(m.Pix); i += 4 {
		m.Pix[i], m.Pix[i+1], m.Pix[i+2], m.Pix[i+3] = 0, 0, 255, 128
	}
	u := Update(r)
	if err := u.ReplaceImage(imgs[1], m); err != nil {
		t.Fatal(err)
	}
	out := updatedBytes(t, u)
	if !bytes.Contains(out, []byte("/SMask")) {
		t.Error("no soft mask written for the translucent replacement")
	}
	r2, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := r2.PageImages(0)
	decoded, err := after[1].Decode()
	if err != nil {
		t.Fatal(err)
	}
	c := color.NRGBAModel.Convert(decoded.At(0, 0)).(color.NRGBA)
	if c.A != 128 || c.B != 255 {
		t.Errorf("replaced pixel = %v, want half-transparent blue", c)
	}
}

func TestImageErrors(t *testing.T) {
	r, _ := NewReader(imageFixture(t))
	if _, err := r.PageImages(9); err == nil {
		t.Error("expected an error for an out-of-range page")
	}
	// An unbound reference cannot decode or be replaced.
	var loose ImageRef
	if _, err := loose.Decode(); err == nil {
		t.Error("expected an error decoding an unbound image")
	}
	if err := Update(r).ReplaceImage(loose, image.NewGray(image.Rect(0, 0, 1, 1))); err == nil {
		t.Error("expected an error replacing an unbound image")
	}
}

// TestImagesInsideFormXObject finds images the page reaches only through
// a form, which is how imported pages carry them.
func TestImagesInsideFormXObject(t *testing.T) {
	inner, err := NewReader(imageFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	wrapped := New()
	if _, err := wrapped.ImportPage(inner, 0); err != nil {
		t.Fatal(err)
	}
	r, err := NewReader(docBytes(t, wrapped))
	if err != nil {
		t.Fatal(err)
	}
	imgs, err := r.PageImages(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 3 {
		t.Fatalf("found %d images through the form, want 3", len(imgs))
	}
	// Placement survives the nesting.
	if math.Abs(imgs[0].X-50) > 0.01 || math.Abs(imgs[0].W-120) > 0.01 {
		t.Errorf("nested placement = (%v, %v) %vx%v", imgs[0].X, imgs[0].Y, imgs[0].W, imgs[0].H)
	}
}

func TestImageColorSpaceReporting(t *testing.T) {
	r, _ := NewReader(imageFixture(t))
	imgs, _ := r.PageImages(0)
	spaces := make([]string, len(imgs))
	for i, im := range imgs {
		spaces[i] = im.ColorSpace
	}
	if strings.Join(spaces, ",") != "DeviceRGB,DeviceGray,DeviceRGB" {
		t.Errorf("colour spaces = %v", spaces)
	}
}
