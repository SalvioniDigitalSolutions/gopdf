package gopdf

import (
	"encoding/hex"
	"strings"
	"testing"
)

// The fixtures of render_image_depth_test.go draw into 153×55 pt at
// (100, 600); these put an inline picture in the same place, so the
// same probes apply.
const inlineAt = "q 153 0 0 55 100 600 cm "

func inlinePage(t *testing.T, content string) []byte {
	t.Helper()
	// rawImagePage wants a picture object; the page does not draw it.
	return rawImagePage(t, []byte{0xFF}, "/Width 1 /Height 1 /BitsPerComponent 8 /ColorSpace /DeviceGray", content)
}

func TestRenderInlineImage_HexGray(t *testing.T) {
	// Four grey samples in a row, written out in hexadecimal, with every
	// entry by its short name.
	data := inlinePage(t, inlineAt+"BI /W 4 /H 1 /CS /G /BPC 8 /F /AHx ID\n00FF55AA>\nEI Q")
	img := renderFixture(t, data)
	for i, v := range []uint8{0, 255, 85, 170} {
		wantRGB(t, "inline hex grey", img, sampleX(i, 4), v, v, v)
	}
}

func TestRenderInlineImage_RawOneBit(t *testing.T) {
	// Eight one-bit samples a row, two rows, not packed: the data's
	// length follows from the description. Its bytes are chosen to hold
	// what a careless reader takes for the end ("EI" with white space
	// about it) and for an operator.
	data := inlinePage(t, inlineAt+"BI /W 8 /H 2 /CS /G /BPC 1 ID \x0F\x0F\nEI Q 1 0 0 rg 300 300 40 40 re f")
	img := renderFixture(t, data)
	for i := 0; i < 8; i++ {
		want := uint8(0)
		if i >= 4 {
			want = 255
		}
		wantRGB(t, "inline one-bit", img, sampleX(i, 8), want, want, want)
	}
	// What follows EI is drawn: the red square.
	r, g, b, _ := at(t, img, depthDPI, 320, 792-320)
	if r < 200 || g > 40 || b > 40 {
		t.Errorf("the instructions after the picture were not run: %d,%d,%d", r, g, b)
	}
}

func TestRenderInlineImage_DataIsNotRunAsInstructions(t *testing.T) {
	// Sixteen bytes of raw grey that read as instructions: "q", a fill
	// colour, a rectangle over the page, "f". Run as such they would
	// paint the page black.
	raw := "0 g 0 0 9 9 re f"
	data := inlinePage(t, "q 16 0 0 1 100 600 cm BI /W 16 /H 1 /CS /G /BPC 8 ID "+raw+"\nEI Q")
	img := renderFixture(t, data)
	if r, g, b, _ := at(t, img, depthDPI, 4, 792-4); r < 250 || g < 250 || b < 250 {
		t.Errorf("the picture's data was run as instructions: the page's corner is %d,%d,%d", r, g, b)
	}
}

func TestRenderInlineImage_StencilAndPalette(t *testing.T) {
	// A stencil poured in red.
	data := inlinePage(t, "1 0 0 rg "+inlineAt+"BI /W 8 /H 1 /IM true /BPC 1 ID \x0F\nEI Q")
	img := renderFixture(t, data)
	wantRGB(t, "inline stencil, painted", img, sampleX(1, 8), 255, 0, 0)
	wantRGB(t, "inline stencil, open", img, sampleX(6, 8), 255, 255, 255)

	// Two colours of its own, the space written in short names.
	data = inlinePage(t, inlineAt+"BI /W 8 /H 1 /CS [/I /RGB 1 <0000FF FFFF00>] /BPC 1 ID \x0F\nEI Q")
	img = renderFixture(t, data)
	wantRGB(t, "inline palette, first", img, sampleX(1, 8), 0, 0, 255)
	wantRGB(t, "inline palette, second", img, sampleX(6, 8), 255, 255, 0)
}

func TestRenderInlineImage_NotDrawnWithoutPictures(t *testing.T) {
	data := inlinePage(t, inlineAt+"BI /W 1 /H 1 /CS /G /BPC 8 /F /AHx ID\n00>\nEI Q")
	r, err := NewReader(data)
	if err != nil {
		t.Fatal(err)
	}
	img, err := r.RenderPage(0, RenderOpts{DPI: depthDPI, IncludeVector: true})
	if err != nil {
		t.Fatal(err)
	}
	if rr, g, b, _ := at(t, img, depthDPI, sampleX(0, 1), depthMidY); rr < 250 || g < 250 || b < 250 {
		t.Errorf("drawn with pictures switched off: %d,%d,%d", rr, g, b)
	}
}

func TestInlineImageData_FindsTheEnd(t *testing.T) {
	d := Dict{"Width": int64(4), "Height": int64(1), "BitsPerComponent": int64(8), "ColorSpace": Name("DeviceGray")}
	// The data itself holds " EI ": the length decides, not the look.
	content := []byte("ID a EI \nEI Q")
	to, after := inlineImageData(&Reader{}, content, 3, d)
	if string(content[3:to]) != "a EI" || string(content[after:]) != " Q" {
		t.Errorf("data %q, rest %q", content[3:to], content[after:])
	}
	// Packed data: the first EI that stands alone.
	d["Filter"] = Name("ASCIIHexDecode")
	content = []byte("ID\n" + strings.ToUpper(hex.EncodeToString([]byte("EIEI"))) + ">\nEI\nQ")
	to, after = inlineImageData(&Reader{}, content, 3, d)
	if string(content[3:to]) != "45494549>" || string(content[after:]) != "\nQ" {
		t.Errorf("data %q, rest %q", content[3:to], content[after:])
	}
}
