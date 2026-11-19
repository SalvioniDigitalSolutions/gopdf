package gopdf

import (
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"strings"
	"testing"
)

// Two CFF programs made for this test (fontTools), each of five glyphs:
// .notdef, space, "equal" (two bars), "H11001" (one block, a name only a
// math font's maker knows) and "bullet" (a square). The first reaches
// them through an encoding of its own — equal at 1, H11001 at 2, bullet
// at 3 — the second through the predefined Standard encoding.
const (
	cffOwnEncoding      = "01000401000101010b546573744d61746850690001010115f81c028b8bf8baf88805cf0fd8108bf72912de11000201010711483131303031546573744d61746850690000000001001e018700740102200001020005010106091e2c3af8888b160ef78e0ef8ecbdf75c15f888effc8806ef04f888effc88060ef8ecbdf78e15f888f75cfc88060ef888eff75c15f7c0f7c0fbc0060e"
	cffStandardEncoding = "01000401000101010b546573744d61746850690001010113f81c028b8bf8baf88805cd0f8bf72112d611000201010711483131303031546573744d61746850690000000001001e018700740005010106091e2c3af8888b160ef78e0ef8ecbdf75c15f888effc8806ef04f888effc88060ef8ecbdf78e15f888f75cfc88060ef888eff75c15f7c0f7c0fbc0060e"
)

// A simple font with a bare CFF program is drawn with its own glyphs,
// found by name: the name its /Differences gives a code, the name the
// program's own encoding gives it, or — where the dictionary names a
// base encoding — the glyph of the character that encoding says. It was
// drawn by a stand-in found by character, and a math font's operators,
// which name no character a stand-in has, were left blank: a page read
// from its rendering lost them, and a page whose text could not be read
// could not be told from one whose fonts drew nothing.
func TestABareCFFFontIsDrawnWithItsOwnGlyphs(t *testing.T) {
	own, err := hex.DecodeString(cffOwnEncoding)
	if err != nil {
		t.Fatal(err)
	}
	std, err := hex.DecodeString(cffStandardEncoding)
	if err != nil {
		t.Fatal(err)
	}
	desc := func(file int) string {
		return fmt.Sprintf(`<< /Type /FontDescriptor /FontName /TestMathPi /Flags 4 /FontBBox [0 0 600 800] /ItalicAngle 0 /Ascent 800 /Descent -200 /CapHeight 700 /StemV 80 /FontFile3 %d 0 R >>`, file)
	}
	// One glyph per line, 100 points tall, at x = 100: the bars of
	// "equal" stand 20 to 30 and 40 to 50 points above the baseline,
	// the block of "H11001" 25 to 45, the bullet 20 to 50.
	content := `BT /F1 100 Tf 1 0 0 1 100 700 Tm (\001) Tj ET
BT /F1 100 Tf 1 0 0 1 100 560 Tm (\002) Tj ET
BT /F2 100 Tf 1 0 0 1 100 420 Tm (\001) Tj ET
BT /F3 100 Tf 1 0 0 1 100 280 Tm (=) Tj ET
BT /F3 100 Tf 1 0 0 1 100 140 Tm (\225) Tj ET
BT /F4 100 Tf 1 0 0 1 300 700 Tm (\001) Tj ET
`
	data := onePage(content, []string{
		`<< /Type /Font /Subtype /Type1 /BaseFont /TestMathPi /FirstChar 1 /LastChar 3 /Widths [600 600 600] /FontDescriptor 20 0 R >>`,
		`<< /Type /Font /Subtype /Type1 /BaseFont /TestMathPi /FirstChar 1 /LastChar 1 /Widths [600] /FontDescriptor 20 0 R /Encoding << /Differences [1 /H11001] >> >>`,
		`<< /Type /Font /Subtype /Type1 /BaseFont /TestMathPiStd /FirstChar 32 /LastChar 255 /Widths [] /FontDescriptor 22 0 R /Encoding /WinAnsiEncoding >>`,
		// A name the program lacks draws nothing.
		`<< /Type /Font /Subtype /Type1 /BaseFont /TestMathPi /FirstChar 1 /LastChar 1 /Widths [600] /FontDescriptor 20 0 R /Encoding << /Differences [1 /plusminus] >> >>`,
	}, map[int]string{
		20: desc(21),
		21: streamObj("/Subtype /Type1C", own),
		22: strings.Replace(desc(23), "/Flags 4", "/Flags 32", 1),
		23: streamObj("/Subtype /Type1C", std),
	})
	r, err := NewReader(data)
	if err != nil {
		t.Fatal(err)
	}
	img, rep, err := r.RenderPageDetail(0, RenderOpts{DPI: 72, IncludeText: true})
	if err != nil {
		t.Fatal(err)
	}
	ink := func(x, baseline, up float64) bool {
		p := image.Pt(int(x), int(792-baseline-up))
		return color.GrayModel.Convert(img.At(p.X, p.Y)).(color.Gray).Y < 128
	}
	checks := []struct {
		what        string
		x, baseline float64
		up          float64
		want        bool
	}{
		{"own encoding: equal's lower bar", 130, 700, 25, true},
		{"own encoding: equal's gap", 130, 700, 35, false},
		{"own encoding: equal's upper bar", 130, 700, 45, true},
		{"own encoding: H11001's block", 130, 560, 35, true},
		{"Differences: H11001's block", 130, 420, 35, true},
		{"WinAnsi '=': equal's lower bar", 130, 280, 25, true},
		{"WinAnsi '=': equal's gap", 130, 280, 35, false},
		{"WinAnsi bullet", 125, 140, 35, true},
		{"a name the program lacks", 330, 700, 35, false},
	}
	for _, c := range checks {
		if got := ink(c.x, c.baseline, c.up); got != c.want {
			t.Errorf("%s: ink %v, want %v", c.what, got, c.want)
		}
	}
	if rep.Missing != 1 {
		t.Errorf("missing glyphs: %d, want 1 (the name the program lacks)", rep.Missing)
	}
}
