package gopdf

import (
	"bytes"
	"image"
	"testing"
)

// More than one area on a page.
//
// An area is a promise about a rectangle, and a second area is a second
// promise, not a footnote to the first. These tests exist because for
// images and for artwork it once was: whichever area came first settled
// the matter, and a later one got its bar painted over content that was
// still in the file.

// redAt returns the red channel, 0 to 255, of the pixel a fraction of
// the way across and down an image.
func redAt(m image.Image, fx, fy float64) uint32 {
	b := m.Bounds()
	r, _, _, _ := m.At(b.Min.X+int(fx*float64(b.Dx())), b.Min.Y+int(fy*float64(b.Dy()))).RGBA()
	return r >> 8
}

// TestRedactEveryAreaOverAnImage: two areas on one picture, and both
// rectangles of pixels must be gone from the picture itself.
func TestRedactEveryAreaOverAnImage(t *testing.T) {
	r, _ := NewReader(scanDoc(t, 400, 200))
	page, _ := r.PageSize(0)
	rd := Redact(r)
	rd.Area(0, 0.10*page.W, 0.10*page.H, 0.10*page.W, 0.10*page.H)
	rd.Area(0, 0.70*page.W, 0.70*page.H, 0.10*page.W, 0.10*page.H)
	rd.Area(0, 0.40*page.W, 0.80*page.H, 0.05*page.W, 0.05*page.H)

	marks, err := rd.Marks()
	if err != nil {
		t.Fatal(err)
	}
	images := 0
	for _, m := range marks {
		if m.Kind == RedactImage {
			images++
			if !m.Partial {
				t.Errorf("a mark over part of the image says it took all of it: %+v", m)
			}
		}
	}
	if images != 3 {
		t.Errorf("got %d image marks, want one for each area: %+v", images, marks)
	}

	out := redactTo(t, rd)
	r2, _ := NewReader(out)
	imgs, err := r2.PageImages(0)
	if err != nil || len(imgs) != 1 {
		t.Fatalf("the image is gone: %v", err)
	}
	m, err := imgs[0].Decode()
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range [][2]float64{{0.15, 0.15}, {0.75, 0.75}, {0.42, 0.82}} {
		if red := redAt(m, at[0], at[1]); red > 20 {
			t.Errorf("the pixels under the area at %v are still in the image (red=%d)", at, red)
		}
	}
	for _, at := range [][2]float64{{0.45, 0.45}, {0.15, 0.75}, {0.75, 0.15}} {
		if red := redAt(m, at[0], at[1]); red < 200 {
			t.Errorf("the image was scrubbed at %v, outside every area (red=%d)", at, red)
		}
	}
}

// TestRedactSecondAreaIsKnownToTheOCRPlan: the plan for the words in an
// image has to know about every area over it, or it reads words that
// will not be there.
func TestRedactSecondAreaIsKnownToTheOCRPlan(t *testing.T) {
	const w, h = 400, 200
	words := line(10, "Frau", "Li", "Stempel", "Na", "schreibt")
	left, err := saveAndRead(t, w, h, words, func(rd *Redactor, page PageSize) {
		rd.Text("Li Na")
		// An area over nothing in particular, and then the one that
		// takes the word between the two halves of the name.
		rd.Area(0, 0.10*page.W, 0.70*page.H, 0.10*page.W, 0.10*page.H)
		u := words[2]
		rd.Area(0, float64(u.X)/w*page.W, float64(u.Y)/h*page.H,
			float64(u.W)/w*page.W, float64(u.H)/h*page.H)
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "Frau schreibt"; left != want {
		t.Errorf("still readable %q, want %q", left, want)
	}
}

// TestRedactArtworkHeldBySecondArea: one area clips the corner of a
// rectangle and another holds all of it. The rectangle goes, whichever
// area was drawn first.
func TestRedactArtworkHeldBySecondArea(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFillColor(RGB(200, 30, 30))
	page.Rect(100, 100, 60, 40, Fill)
	src := docBytes(t, doc)

	corner := [4]float64{90, 90, 20, 20}  // touches the top-left corner
	whole := [4]float64{80, 80, 120, 100} // holds all of it
	for name, order := range map[string][][4]float64{
		"corner first": {corner, whole},
		"whole first":  {whole, corner},
	} {
		r, _ := NewReader(src)
		rd := Redact(r)
		for _, a := range order {
			rd.Area(0, a[0], a[1], a[2], a[3])
		}
		rd.SetOverlay(false)
		out := redactTo(t, rd)

		if n, err := rd.PartialArtwork(); err != nil || n != 0 {
			t.Errorf("%s: %d pieces reported as straddling an edge (%v), want 0", name, n, err)
		}
		r2, _ := NewReader(out)
		content, err := r2.pageContent(r2.pages[0].dict)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(content, []byte("100 701.89 60 40 re")) {
			t.Errorf("%s: the rectangle inside an area survived", name)
		}
	}
}
