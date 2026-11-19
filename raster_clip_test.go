package gopdf

import (
	"image"
	"math/rand"
	"testing"
)

// denseIntersect is intersectClip as it was: a mask of the whole page,
// every pixel the path covers filled through fillPath. The masks of
// boxes, rectangles and the clip left as it was must give, pixel for
// pixel, what it gives.
func denseIntersect(w, h int, old *clipMask, path *rasterPath, evenOdd bool) []float32 {
	a := make([]float32, w*h)
	fillPath(path, w, h, evenOdd, func(y, x0, x1 int, cov []float64) {
		for x := x0; x < x1; x++ {
			c := cov[x]
			if c <= 0 {
				continue
			}
			if c > 1 {
				c = 1
			}
			a[y*w+x] = float32(c * old.at(x, y))
		}
	})
	return a
}

func rectPath(x0, y0, x1, y1 float64) *rasterPath {
	p := &rasterPath{}
	p.moveTo(point{x0, y0})
	p.lineTo(point{x1, y0})
	p.lineTo(point{x1, y1})
	p.lineTo(point{x0, y1})
	p.close()
	return p
}

func polyPath(pts ...point) *rasterPath {
	p := &rasterPath{}
	p.moveTo(pts[0])
	for _, q := range pts[1:] {
		p.lineTo(q)
	}
	p.close()
	return p
}

func sameAsDense(t *testing.T, what string, w, h int, m *clipMask, want []float32) {
	t.Helper()
	for y := -1; y <= h; y++ {
		for x := -1; x <= w; x++ {
			var d float32
			if x >= 0 && y >= 0 && x < w && y < h {
				d = want[y*w+x]
			}
			if got := float32(m.at(x, y)); got != d {
				t.Fatalf("%s: at %d,%d %v, as it was %v", what, x, y, got, d)
			}
		}
	}
}

// Clips narrowed one after another — rectangles whose sides fall inside
// pixels and outside the page, a page clip a quarter pixel short, a
// rectangle that covers what it clips, a triangle, a rectangle turned
// so it is no rectangle on the grid — hold, pixel for pixel, the
// coverage a whole-page mask held.
func TestClipMasksHoldWhatAWholePageMaskHeld(t *testing.T) {
	w, h := 61, 47
	rn := &renderer{w: w, h: h}
	paths := map[string]*rasterPath{
		"page a quarter short":         rectPath(0, 0, 60.75, 46.25),
		"inside, fractional":           rectPath(3.3, 4.7, 40.2, 30.9),
		"past the page":                rectPath(-10, -5, 80, 60),
		"thin":                         rectPath(10.1, 10.2, 10.6, 40.4),
		"one row":                      rectPath(5, 20.1, 50, 20.3),
		"covers what it clips":         rectPath(-1, -1, 62, 48),
		"upside down":                  rectPath(40.2, 30.9, 3.3, 4.7),
		"triangle":                     polyPath(point{5, 5}, point{55, 9}, point{20, 44}),
		"turned":                       polyPath(point{30, 2}, point{58, 23}, point{30, 44}, point{2, 23}),
		"on sub-scanlines":             rectPath(5, 10.125, 50, 20.375),
		"a quarter short at the right": rectPath(0, -1, 60.75, 48),
		"outside the page":             rectPath(70, 50, 90, 60),
		"rectangle, five points":       polyPath(point{8, 8}, point{50, 8}, point{50, 40}, point{8, 40}, point{8, 8}),
	}
	names := []string{"page a quarter short", "inside, fractional", "past the page", "thin", "one row",
		"covers what it clips", "upside down", "triangle", "turned", "outside the page", "rectangle, five points",
		"on sub-scanlines", "a quarter short at the right"}
	for _, first := range names {
		for _, second := range names {
			for _, evenOdd := range []bool{false, true} {
				m1 := rn.intersectClip(nil, paths[first], evenOdd)
				sameAsDense(t, first, w, h, m1, denseIntersect(w, h, nil, paths[first], evenOdd))
				m2 := rn.intersectClip(m1, paths[second], evenOdd)
				sameAsDense(t, first+", then "+second, w, h, m2, denseIntersect(w, h, m1, paths[second], evenOdd))
			}
		}
	}
}

// Rectangles behind rectangles deeper than a mask reads through are
// worked out and held, and still hold what they held.
func TestDeepRectangleClipsAreHeld(t *testing.T) {
	w, h := 50, 40
	rn := &renderer{w: w, h: h}
	var m *clipMask
	var dense []float32
	for i := 0; i < 3*maxRectDepth; i++ {
		f := float64(i) * 0.37
		p := rectPath(1.3+f, 0.6+f/2, 48.8-f, 39.1-f/3)
		dense = denseIntersect(w, h, m, p, false)
		m = rn.intersectClip(m, p, false)
		if m.rect != nil && m.rect.depth > maxRectDepth {
			t.Fatalf("clip %d reads through %d rectangles", i, m.rect.depth)
		}
	}
	sameAsDense(t, "deep", w, h, m, dense)
}

// Masks of boxes put together hold what masks of the whole page put
// together held.
func TestCombinedMasksOfBoxes(t *testing.T) {
	w, h := 40, 30
	rn := &renderer{w: w, h: h}
	a := rn.intersectClip(nil, rectPath(2.5, 3.25, 30.5, 25.75), false)
	b := rn.pathMask(polyPath(point{10, 1}, point{39, 15}, point{12, 29}), false)
	full := rn.intersectClip(nil, rectPath(-1, -1, 41, 31), false)
	for _, c := range []struct {
		name string
		x, y *clipMask
	}{{"rectangle and triangle", a, b}, {"triangle and rectangle", b, a}, {"whole and rectangle", full, a}, {"whole and whole", full, full}} {
		got := combineMasks(c.x, c.y)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				if want := c.x.raw(x, y) * c.y.raw(x, y); got.raw(x, y) != want {
					t.Fatalf("%s: at %d,%d %v, want %v", c.name, x, y, got.raw(x, y), want)
				}
			}
		}
	}
}

// The crossings of edges begun and not ended are the crossings of all
// edges, in the same order, on every sub-scanline: paths of many edges,
// ties at one x, edges that begin and end between two sub-scanlines.
func TestEdgeScanGivesTheCrossingsOfAllEdges(t *testing.T) {
	rnd := rand.New(rand.NewSource(7))
	for n := 0; n < 200; n++ {
		p := &rasterPath{}
		for s := 0; s < 1+rnd.Intn(4); s++ {
			p.moveTo(point{rnd.Float64() * 100, rnd.Float64() * 80})
			for k := 0; k < 2+rnd.Intn(60); k++ {
				x, y := rnd.Float64()*100, rnd.Float64()*80
				if rnd.Intn(5) == 0 {
					x = float64(rnd.Intn(10)) * 10 // ties at one x
				}
				if rnd.Intn(7) == 0 {
					y = float64(rnd.Intn(80)) + 0.125 // on a sub-scanline
				}
				p.lineTo(point{x, y})
			}
			p.close()
		}
		edges := buildEdges(p)
		scan := newEdgeScan(edges)
		for y := 0; y < 80; y++ {
			for s := 0; s < subSamples; s++ {
				sy := subScanline(y, s)
				want := crossingsAt(edges, sy, nil)
				got := scan.crossingsAt(sy, nil)
				if len(got) != len(want) {
					t.Fatalf("path %d, sub-scanline %v: %d crossings, all edges give %d", n, sy, len(got), len(want))
				}
				for i := range got {
					if got[i] != want[i] {
						t.Fatalf("path %d, sub-scanline %v, crossing %d: %v, all edges give %v", n, sy, i, got[i], want[i])
					}
				}
			}
		}
	}
}

// A rectangle is four corners each once, its sides along the grid.
func TestAxisRect(t *testing.T) {
	for _, c := range []struct {
		name string
		p    *rasterPath
		ok   bool
	}{
		{"rectangle", rectPath(1, 2, 3, 4), true},
		{"five points, back at the first", polyPath(point{1, 2}, point{3, 2}, point{3, 4}, point{1, 4}, point{1, 2}), true},
		{"comes back on itself", polyPath(point{0, 0}, point{10, 0}, point{10, 10}, point{10, 0}), false},
		{"turned", polyPath(point{5, 0}, point{10, 5}, point{5, 10}, point{0, 5}), false},
		{"two rectangles", func() *rasterPath {
			p := rectPath(0, 0, 1, 1)
			q := rectPath(2, 2, 3, 3)
			p.subs = append(p.subs, q.subs...)
			return p
		}(), false},
		{"a line", polyPath(point{0, 0}, point{10, 0}, point{10, 0}, point{0, 0}), false},
	} {
		if _, ok := c.p.axisRect(); ok != c.ok {
			t.Errorf("%s: rectangle %v", c.name, ok)
		}
	}
	if b := fillBox(rectPath(-5, -5, 3.5, 2.25), 10, 10); b != image.Rect(0, 0, 5, 4) {
		t.Errorf("fillBox %v", b)
	}
}
