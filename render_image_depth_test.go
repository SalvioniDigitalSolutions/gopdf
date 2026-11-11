package gopdf

import (
	"bytes"
	"image"
	"image/color"
	"strings"
	"testing"
)

// An image says how its samples are packed — how many bits each, in
// which colour space, whether it is a stencil, whether its values run
// backwards. The page scanner read all of that; the renderer read the
// width and the height and assumed the rest, so a one-bit scan stored
// as plain samples was drawn as if every bit were a byte: a page of
// noise where the scan should be. These tests hold the two readers to
// the same picture.

// The fixtures draw /Im0 153×55 pt with its lower-left corner at
// (100, 600) on a 612×792 page: left edge x=100, top edge y=137 from
// the top. Each picture is eight samples wide, so one sample is about
// 19 pt across; the probes sit in the middle of a sample.
const (
	depthDPI  = 100.0
	depthMidY = 792 - 600 - 27.0
)

func sampleX(i, of int) float64 { return 100 + 153*(float64(i)+0.5)/float64(of) }

func renderFixture(t *testing.T, data []byte) image.Image {
	t.Helper()
	r, err := NewReader(data)
	if err != nil {
		t.Fatal(err)
	}
	img, err := r.RenderPage(0, RenderOpts{DPI: depthDPI, IncludeVector: true, IncludeRasterImages: true})
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func decodeFixture(t *testing.T, data []byte) image.Image {
	t.Helper()
	r, err := NewReader(data)
	if err != nil {
		t.Fatal(err)
	}
	imgs, err := r.PageImages(0)
	if err != nil || len(imgs) != 1 {
		t.Fatalf("images: %d (%v)", len(imgs), err)
	}
	m, err := imgs[0].Decode()
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func wantRGB(t *testing.T, what string, img image.Image, x float64, r, g, b uint8) {
	t.Helper()
	gr, gg, gb, _ := at(t, img, depthDPI, x, depthMidY)
	if !near(gr, r, 8) || !near(gg, g, 8) || !near(gb, b, 8) {
		t.Errorf("%s: drawn %d,%d,%d, want %d,%d,%d", what, gr, gg, gb, r, g, b)
	}
}

const plainDo = "q 153 0 0 55 100 600 cm /Im0 Do Q"

func TestRenderOneBitGrayImage(t *testing.T) {
	// Eight one-bit samples a row: four black, four white.
	data := rawImagePage(t, []byte{0x0F, 0x0F},
		"/Width 8 /Height 2 /BitsPerComponent 1 /ColorSpace /DeviceGray", plainDo)
	img := renderFixture(t, data)
	for i := 0; i < 8; i++ {
		want := uint8(0)
		if i >= 4 {
			want = 255
		}
		wantRGB(t, "one-bit gray", img, sampleX(i, 8), want, want, want)
	}
	// The renderer and the extractor read the same picture.
	m := decodeFixture(t, data)
	for i := 0; i < 8; i++ {
		got := color.GrayModel.Convert(m.At(i, 0)).(color.Gray).Y
		if (i < 4 && got != 0) || (i >= 4 && got != 255) {
			t.Errorf("decoded sample %d = %d", i, got)
		}
	}
}

func TestRenderTwoBitIndexedImage(t *testing.T) {
	// Four two-bit samples, one for each palette entry.
	data := rawImagePage(t, []byte{0x1B},
		"/Width 4 /Height 1 /BitsPerComponent 2 "+
			"/ColorSpace [/Indexed /DeviceRGB 3 <FF0000 00FF00 0000FF FFFFFF>]", plainDo)
	img := renderFixture(t, data)
	for i, want := range [][3]uint8{{255, 0, 0}, {0, 255, 0}, {0, 0, 255}, {255, 255, 255}} {
		wantRGB(t, "two-bit indexed", img, sampleX(i, 4), want[0], want[1], want[2])
	}
}

func TestRenderFourBitGrayImage(t *testing.T) {
	data := rawImagePage(t, []byte{0x0F, 0x5A},
		"/Width 4 /Height 1 /BitsPerComponent 4 /ColorSpace /DeviceGray", plainDo)
	img := renderFixture(t, data)
	for i, v := range []uint8{0, 255, 85, 170} {
		wantRGB(t, "four-bit gray", img, sampleX(i, 4), v, v, v)
	}
}

func TestRenderSixteenBitGrayImage(t *testing.T) {
	data := rawImagePage(t, []byte{0x00, 0x00, 0xFF, 0xFF},
		"/Width 2 /Height 1 /BitsPerComponent 16 /ColorSpace /DeviceGray", plainDo)
	img := renderFixture(t, data)
	wantRGB(t, "sixteen-bit black", img, sampleX(0, 2), 0, 0, 0)
	wantRGB(t, "sixteen-bit white", img, sampleX(1, 2), 255, 255, 255)
}

// A stencil mask has no colour of its own: its set samples are poured
// in the fill colour and the rest of it is not there at all. Scanners
// that split a page into a photograph and a layer of crisp black text
// store the text this way, over the photograph.
func TestRenderStencilMaskPaintsInTheFillColour(t *testing.T) {
	const under = "1 1 0 rg 0 0 612 792 re f " // a yellow page
	for _, c := range []struct {
		name, decode string
		paintedLeft  bool
	}{
		{"default", "", true},
		{"decode 0 1", "/Decode [0 1]", true},
		{"decode 1 0", "/Decode [1 0]", false},
	} {
		data := rawImagePage(t, []byte{0x0F},
			"/Width 8 /Height 1 /BitsPerComponent 1 /ImageMask true "+c.decode,
			under+"0 0 1 rg "+plainDo)
		img := renderFixture(t, data)
		for i := 0; i < 8; i++ {
			painted := (i < 4) == c.paintedLeft
			if painted {
				wantRGB(t, c.name+": painted sample", img, sampleX(i, 8), 0, 0, 255)
			} else {
				wantRGB(t, c.name+": unpainted sample shows the page", img, sampleX(i, 8), 255, 255, 0)
			}
		}
	}
}

// /Decode turns the sample range around: [1 0] on a one-bit gray scan
// is how some scanners write black-on-white with the bits inverted.
func TestImageDecodeArrayIsHonoured(t *testing.T) {
	data := rawImagePage(t, []byte{0x0F},
		"/Width 8 /Height 1 /BitsPerComponent 1 /ColorSpace /DeviceGray /Decode [1 0]", plainDo)
	img := renderFixture(t, data)
	m := decodeFixture(t, data)
	for i := 0; i < 8; i++ {
		want := uint8(255)
		if i >= 4 {
			want = 0
		}
		wantRGB(t, "inverted one-bit gray", img, sampleX(i, 8), want, want, want)
		if got := color.GrayModel.Convert(m.At(i, 0)).(color.Gray).Y; got != want {
			t.Errorf("decoded sample %d = %d, want %d", i, got, want)
		}
	}

	// Eight-bit colour, each component turned around.
	data = rawImagePage(t, []byte{0xFF, 0x00, 0x00, 0x20, 0x80, 0xE0},
		"/Width 2 /Height 1 /BitsPerComponent 8 /ColorSpace /DeviceRGB /Decode [1 0 1 0 1 0]", plainDo)
	img = renderFixture(t, data)
	wantRGB(t, "inverted red", img, sampleX(0, 2), 0, 255, 255)
	wantRGB(t, "inverted mix", img, sampleX(1, 2), 0xDF, 0x7F, 0x1F)

	// A partial range: [0 0.5] halves every value.
	data = rawImagePage(t, []byte{0xFF, 0x80},
		"/Width 2 /Height 1 /BitsPerComponent 8 /ColorSpace /DeviceGray /Decode [0 0.5]", plainDo)
	img = renderFixture(t, data)
	wantRGB(t, "halved white", img, sampleX(0, 2), 127, 127, 127)
	wantRGB(t, "halved grey", img, sampleX(1, 2), 64, 64, 64)

	// An indexed image's /Decode maps indices, not colours; the
	// identity range leaves the palette lookup alone.
	data = rawImagePage(t, []byte{0x1B},
		"/Width 4 /Height 1 /BitsPerComponent 2 /Decode [0 3] "+
			"/ColorSpace [/Indexed /DeviceRGB 3 <FF0000 00FF00 0000FF FFFFFF>]", plainDo)
	img = renderFixture(t, data)
	wantRGB(t, "indexed, identity decode", img, sampleX(0, 4), 255, 0, 0)
	wantRGB(t, "indexed, identity decode", img, sampleX(3, 4), 255, 255, 255)
}

// The same turn-around on a fax image that is not a mask.
func TestDecodeCCITTGrayWithInvertedDecode(t *testing.T) {
	const dict = "/Width 153 /Height 55 /BitsPerComponent 1 /ColorSpace /DeviceGray /Decode [1 0] " +
		"/Filter /CCITTFaxDecode /DecodeParms << /K -1 /Columns 153 /Rows 55 >>"
	ref := gopherPNG(t)
	b := ref.Bounds()
	inverted := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			g := color.GrayModel.Convert(ref.At(b.Min.X+x, b.Min.Y+y)).(color.Gray)
			inverted.SetGray(x, y, color.Gray{Y: 255 - g.Y})
		}
	}
	diffImages(t, decodeFaxPDF(t, "bw-gopher.ccitt_group4", dict), inverted)
}

// A fax stencil over a photograph: the renderer must know it is a
// mask, both to pour it in the fill colour and to read its /Decode.
func TestRenderFaxStencilMask(t *testing.T) {
	fax := mustRead(t, "testdata/ccitt/bw-gopher.ccitt_group4")
	const parms = "/Filter /CCITTFaxDecode /DecodeParms << /K -1 /Columns 153 /Rows 55 >>"
	ref := gopherPNG(t)
	for _, c := range []struct {
		name, decode string
		inkIsPainted bool
	}{
		{"default", "", true},
		{"decode 1 0", "/Decode [1 0]", false},
	} {
		data := rawImagePage(t, fax,
			"/Width 153 /Height 55 /BitsPerComponent 1 /ImageMask true "+c.decode+" "+parms,
			"1 1 0 rg 0 0 612 792 re f 1 0 0 rg "+plainDo)
		img := renderFixture(t, data)
		painted, shown, wrong := 0, 0, 0
		for y := 0; y < 55; y++ {
			for x := 0; x < 153; x++ {
				ink := color.GrayModel.Convert(ref.At(ref.Bounds().Min.X+x, ref.Bounds().Min.Y+y)).(color.Gray).Y < 128
				r, g, bl, _ := at(t, img, depthDPI, 100+float64(x)+0.5, 792-655+float64(y)+0.5)
				isRed := r > 200 && g < 60 && bl < 60
				isYellow := r > 200 && g > 200 && bl < 60
				switch {
				case ink == c.inkIsPainted && isRed:
					painted++
				case ink != c.inkIsPainted && isYellow:
					shown++
				default:
					wrong++
				}
			}
		}
		// Sample edges fall between device pixels, so a fringe of
		// blended pixels is expected; a wrong polarity or an opaque
		// mask gets thousands wrong.
		if wrong > 153*55/8 || painted == 0 || shown == 0 {
			t.Errorf("%s: %d painted, %d showing the page, %d wrong", c.name, painted, shown, wrong)
		}
	}
}

// --- the same description, where redaction decodes an image ---

// truePictureOCR reads its word only from the picture as it really
// is: black on the left, white on the right. A decoder that got the
// bit depth wrong hands it noise, and it reads nothing — which is how
// a check that looks at the wrong pixels passes a document it should
// have held back.
type truePictureOCR struct{ word string }

func (o truePictureOCR) Recognize(m image.Image) ([]OCRWord, error) {
	b := m.Bounds()
	if b.Dx() != 8 {
		return nil, nil
	}
	left := color.GrayModel.Convert(m.At(b.Min.X+1, b.Min.Y)).(color.Gray).Y
	right := color.GrayModel.Convert(m.At(b.Min.X+6, b.Min.Y)).(color.Gray).Y
	if left < 60 && right > 200 {
		return []OCRWord{{Text: o.word, X: 0, Y: 0, W: 3, H: 1, Confidence: 1}}, nil
	}
	return nil, nil
}

// The check that withholds an output walks every image the document
// still reaches, drawn or not. It must see a one-bit image as it is.
func TestOCRVerifyReadsLowDepthImages(t *testing.T) {
	for name, dict := range map[string]string{
		"one-bit gray": "/Width 8 /Height 2 /BitsPerComponent 1 /ColorSpace /DeviceGray",
		"one-bit indexed": "/Width 8 /Height 2 /BitsPerComponent 1 " +
			"/ColorSpace [/Indexed /DeviceGray 1 <00FF>]",
		"inverted gray": "/Width 8 /Height 2 /BitsPerComponent 1 /ColorSpace /DeviceGray /Decode [1 0]",
	} {
		samples := []byte{0x0F, 0x0F}
		if name == "inverted gray" {
			samples = []byte{0xF0, 0xF0}
		}
		// The image sits in the page's resources and is never drawn, so
		// only the final check can find it.
		r, err := NewReader(rawImagePage(t, samples, dict, "q Q"))
		if err != nil {
			t.Fatal(err)
		}
		rd := Redact(r)
		rd.SetOCR(truePictureOCR{word: "Lovelace"})
		rd.Text("Lovelace")
		var buf bytes.Buffer
		if _, err := rd.WriteTo(&buf); err == nil || !strings.Contains(err.Error(), "still be read in an image") {
			t.Errorf("%s: a name readable in an image passed the check (err=%v)", name, err)
		}
		if buf.Len() != 0 {
			t.Errorf("%s: a document that failed its check was written", name)
		}
	}
}

// An area over part of a one-bit picture takes that part and leaves
// the rest of the picture as it was — not a field of noise.
func TestRedactAreaOverLowDepthImageKeepsThePicture(t *testing.T) {
	for _, c := range []struct {
		name, dict string
		samples    []byte
	}{
		{"one-bit gray", "/Width 8 /Height 2 /BitsPerComponent 1 /ColorSpace /DeviceGray", []byte{0x0F, 0x0F}},
		{"inverted gray", "/Width 8 /Height 2 /BitsPerComponent 1 /ColorSpace /DeviceGray /Decode [1 0]", []byte{0xF0, 0xF0}},
		{"two-bit indexed", "/Width 8 /Height 2 /BitsPerComponent 2 " +
			"/ColorSpace [/Indexed /DeviceGray 3 <00 55 AA FF>]", []byte{0x00, 0xFF, 0x00, 0xFF}},
	} {
		r, err := NewReader(rawImagePage(t, c.samples, c.dict, plainDo))
		if err != nil {
			t.Fatal(err)
		}
		rd := Redact(r)
		// The last two samples of the eight: x from 6/8 of the picture.
		rd.Area(0, 100+153*6.0/8, 792-655, 153*2.0/8, 55)
		out := redactTo(t, rd)
		m := decodeFixture(t, out)
		if m.Bounds().Dx() != 8 {
			t.Fatalf("%s: the picture is %v now", c.name, m.Bounds())
		}
		gray := func(x int) uint8 { return color.GrayModel.Convert(m.At(x, 0)).(color.Gray).Y }
		for x := 0; x < 4; x++ {
			if gray(x) > 20 {
				t.Errorf("%s: sample %d was black, is %d", c.name, x, gray(x))
			}
		}
		for x := 4; x < 6; x++ {
			if gray(x) < 235 {
				t.Errorf("%s: sample %d was white and outside the area, is %d", c.name, x, gray(x))
			}
		}
		for x := 6; x < 8; x++ {
			if gray(x) > 20 {
				t.Errorf("%s: sample %d is under the area and still %d", c.name, x, gray(x))
			}
		}
		// What a viewer shows is what the decoder read: a /Decode left
		// on the rewritten image would turn it around a second time.
		r2, _ := NewReader(out)
		imgs, _ := r2.PageImages(0)
		if len(imgs) == 1 {
			if _, has := imgs[0].stream.dict["Decode"]; has {
				t.Errorf("%s: the rewritten image kept its /Decode", c.name)
			}
		}
		// And the page looks the same outside the area.
		page := renderFixture(t, out)
		wantRGB(t, c.name+": page, black sample", page, sampleX(1, 8), 0, 0, 0)
		wantRGB(t, c.name+": page, white sample", page, sampleX(4, 8), 255, 255, 255)
	}
}

// A scrubbed stencil stays a stencil: the area is poured solid in the
// colour the mask is painted with, and where the mask does not paint,
// the page under it still shows. Written back as an ordinary picture
// it would turn opaque and hide the photograph of a scan split into
// picture and text.
func TestRedactAreaOverStencilMaskKeepsItAStencil(t *testing.T) {
	for _, c := range []struct{ name, decode string }{{"default", ""}, {"decode 1 0", "/Decode [1 0]"}} {
		samples := []byte{0x0F, 0x0F} // the left half paints
		if c.decode != "" {
			samples = []byte{0xF0, 0xF0}
		}
		const page = "1 1 0 rg 0 0 612 792 re f 0 0 1 rg " + plainDo // yellow page, blue ink
		r, err := NewReader(rawImagePage(t, samples,
			"/Width 8 /Height 2 /BitsPerComponent 1 /ImageMask true "+c.decode, page))
		if err != nil {
			t.Fatal(err)
		}
		rd := Redact(r)
		// The last two samples of the eight, where nothing is painted.
		rd.Area(0, 100+153*6.0/8, 792-655, 153*2.0/8, 55)
		out := redactTo(t, rd)

		r2, err := NewReader(out)
		if err != nil {
			t.Fatal(err)
		}
		imgs, err := r2.PageImages(0)
		if err != nil || len(imgs) != 1 {
			t.Fatalf("%s: the picture is gone: %v", c.name, err)
		}
		if !imgs[0].IsStencilMask() {
			t.Fatalf("%s: the scrubbed stencil came back as a %s picture", c.name, imgs[0].ColorSpace)
		}
		if _, has := imgs[0].stream.dict["Decode"]; has {
			t.Errorf("%s: the rewritten stencil kept its /Decode", c.name)
		}
		img := renderFixture(t, out)
		for i := 0; i < 4; i++ {
			wantRGB(t, c.name+": painted before, painted still", img, sampleX(i, 8), 0, 0, 255)
		}
		for i := 4; i < 6; i++ {
			wantRGB(t, c.name+": outside the area the page shows through", img, sampleX(i, 8), 255, 255, 0)
		}
		for i := 6; i < 8; i++ {
			gr, gg, gb, _ := at(t, img, depthDPI, sampleX(i, 8), depthMidY)
			if gr > 200 && gg > 200 && gb < 60 {
				t.Errorf("%s: the area still shows the page through sample %d", c.name, i)
			}
		}
	}
}
