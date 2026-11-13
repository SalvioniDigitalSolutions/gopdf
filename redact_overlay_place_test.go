package gopdf

import (
	"image"
	"testing"
)

// scaledPageDoc is a page as a browser prints one: the whole page is
// scaled and turned over once, at the top of its content, and that
// transform is never restored. open leaves that many saves unclosed at
// the end as well.
func scaledPageDoc(t *testing.T, open int) []byte {
	t.Helper()
	// A quarter-point grid with its origin at the top left, so the
	// line stands at 68, 150 in points from the top-left of the page.
	content := ".25 0 0 -.25 0 842 cm\n" +
		"BT /F1 48 Tf 1 0 0 -1 272 600 Tm (Der Mieter Marco Bianchi wohnt in Bern.) Tj ET\n"
	for i := 0; i < open; i++ {
		content += "q 2 0 0 2 0 0 cm\n"
	}
	return rawPageDoc(t, content)
}

// darkIn reports how much of a box of a rendered page is dark, the box
// given in points from the top-left of the page at 72 points an inch.
func darkIn(img image.Image, x0, y0, x1, y1 float64) float64 {
	dark, total := 0, 0
	for y := int(y0); y < int(y1); y++ {
		for x := int(x0); x < int(x1); x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			total++
			if r < 0x4000 && g < 0x4000 && b < 0x4000 {
				dark++
			}
		}
	}
	if total == 0 {
		return 0
	}
	return float64(dark) / float64(total)
}

// The bar over a removed value stands where the value stood, whatever
// transform the page's content left in force: it was drawn in that
// transform, a quarter of the way in from a corner.
func TestRedactOverlayStandsWhereTheTextStood(t *testing.T) {
	for _, open := range []int{0, 2} {
		r, err := NewReader(scaledPageDoc(t, open))
		if err != nil {
			t.Fatal(err)
		}
		rd := Redact(r)
		rd.Text("Marco Bianchi")
		marks, err := rd.Marks()
		if err != nil || len(marks) != 1 {
			t.Fatalf("marks: %+v, %v", marks, err)
		}
		m := marks[0]
		if m.X < 100 || m.X > 160 || m.Y < 135 || m.Y > 150 {
			t.Fatalf("fixture: the name is reported at %.0f, %.0f", m.X, m.Y)
		}
		out, err := NewReader(redactTo(t, rd))
		if err != nil {
			t.Fatal(err)
		}
		img, err := out.RenderPage(0, RenderOpts{DPI: 72, IncludeVector: true, IncludeText: true})
		if err != nil {
			t.Fatal(err)
		}
		if got := darkIn(img, m.X+1, m.Y+1, m.X+m.W-1, m.Y+m.H-1); got < 0.99 {
			t.Errorf("%d saves left open: %.0f%% of the place the name stood in is under the bar", open, got*100)
		}
		// And nowhere else: the page below the line is blank.
		if got := darkIn(img, 0, 170, 595, 842); got > 0.001 {
			t.Errorf("%d saves left open: something is painted away from the line (%.1f%% of the rest of the page)", open, got*100)
		}
	}
}

func TestClosedOff(t *testing.T) {
	for in, want := range map[string]string{
		"":                    "q\n\nQ\n",
		"1 0 0 1 5 5 cm":      "q\n1 0 0 1 5 5 cm\nQ\n",
		"q q 1 0 0 1 5 5 cm":  "q\nq q 1 0 0 1 5 5 cm\nQ\nQ\nQ\n",
		"q Q":                 "q\nq Q\nQ\n",
		"Q":                   "q\nQ\n",
		"q (a Q in a string)": "q\nq (a Q in a string)\nQ\nQ\n",
	} {
		if got := string(closedOff([]byte(in))); got != want {
			t.Errorf("closedOff(%q) = %q, want %q", in, got, want)
		}
	}
}
