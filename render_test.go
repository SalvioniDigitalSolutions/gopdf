package gopdf

import (
	"image"
	"math"
	"testing"
)

// renderDoc draws a page with fn and returns the rendered picture.
func renderDoc(t *testing.T, opts RenderOpts, fn func(p *Page)) image.Image {
	t.Helper()
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	fn(page)
	r := NewReaderOrFail(t, docBytes(t, doc))
	img, err := r.RenderPage(0, opts)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// at reads a pixel by page coordinates in points, top-left origin.
func at(t *testing.T, img image.Image, dpi, x, y float64) (r, g, b, a uint8) {
	t.Helper()
	s := dpi / 72
	px, py := int(x*s), int(y*s)
	b32 := img.Bounds()
	if px < b32.Min.X || py < b32.Min.Y || px >= b32.Max.X || py >= b32.Max.Y {
		t.Fatalf("(%v,%v) is outside the picture %v", x, y, b32)
	}
	cr, cg, cb, ca := img.At(px, py).RGBA()
	return uint8(cr >> 8), uint8(cg >> 8), uint8(cb >> 8), uint8(ca >> 8)
}

func near(a, b uint8, tol int) bool { return int(a)-int(b) <= tol && int(b)-int(a) <= tol }

func TestRenderPageSize(t *testing.T) {
	img := renderDoc(t, RenderOpts{DPI: 150, IncludeVector: true}, func(p *Page) {})
	b := img.Bounds()
	// A4 at 150 DPI.
	if b.Dx() != 1241 || b.Dy() != 1754 {
		t.Errorf("size = %dx%d, want about 1241x1754", b.Dx(), b.Dy())
	}
	// The default resolution is 150.
	def := renderDoc(t, RenderOpts{IncludeVector: true}, func(p *Page) {})
	if def.Bounds() != b {
		t.Errorf("the default DPI gave %v, want %v", def.Bounds(), b)
	}
}

func TestRenderFill(t *testing.T) {
	img := renderDoc(t, RenderOpts{DPI: 150, IncludeVector: true}, func(p *Page) {
		p.SetFillColor(RGB(30, 90, 200))
		p.Rect(60, 60, 160, 80, Fill)
	})
	r, g, b, a := at(t, img, 150, 140, 100)
	if !near(r, 30, 3) || !near(g, 90, 3) || !near(b, 200, 3) || a != 255 {
		t.Errorf("inside the rectangle = %d,%d,%d,%d", r, g, b, a)
	}
	// Outside it the page is white.
	r, g, b, _ = at(t, img, 150, 400, 400)
	if r != 255 || g != 255 || b != 255 {
		t.Errorf("outside = %d,%d,%d, want white", r, g, b)
	}
}

// TestRenderEvenOdd checks the two winding rules give different answers
// where they should: the hole in a doughnut.
func TestRenderEvenOdd(t *testing.T) {
	draw := func(evenOdd bool) image.Image {
		return renderDoc(t, RenderOpts{DPI: 150, IncludeVector: true}, func(p *Page) {
			p.SetFillColor(Black)
			p.op("100 500 200 200 re")
			p.op("150 550 100 100 re")
			if evenOdd {
				p.op("f*")
			} else {
				p.op("f")
			}
		})
	}
	// The inner square is a hole under even-odd and filled under nonzero,
	// since both rectangles are wound the same way.
	nz := draw(false)
	eo := draw(true)
	// Page coordinates: the rectangles sit near the bottom in PDF space.
	cx, cy := 200.0, 841.89-600
	r1, _, _, _ := at(t, nz, 150, cx, cy)
	r2, _, _, _ := at(t, eo, 150, cx, cy)
	if r1 != 0 {
		t.Errorf("nonzero should fill the middle, got %d", r1)
	}
	if r2 != 255 {
		t.Errorf("even-odd should leave a hole, got %d", r2)
	}
}

func TestRenderStroke(t *testing.T) {
	img := renderDoc(t, RenderOpts{DPI: 150, IncludeVector: true}, func(p *Page) {
		p.SetStrokeColor(RGB(200, 40, 40))
		p.SetLineWidth(10)
		p.Line(100, 200, 400, 200)
	})
	// On the line.
	r, g, b, _ := at(t, img, 150, 250, 200)
	if !near(r, 200, 4) || !near(g, 40, 4) || !near(b, 40, 4) {
		t.Errorf("on the stroke = %d,%d,%d", r, g, b)
	}
	// Just off it, beyond half the width.
	r, _, _, _ = at(t, img, 150, 250, 212)
	if r != 255 {
		t.Errorf("beyond the stroke = %d, want white", r)
	}
}

func TestRenderDash(t *testing.T) {
	img := renderDoc(t, RenderOpts{DPI: 150, IncludeVector: true}, func(p *Page) {
		p.SetStrokeColor(Black)
		p.SetLineWidth(6)
		p.SetDash(20, 20)
		p.Line(60, 300, 460, 300)
	})
	// Walk the line and count the runs of ink: a dashed line alternates.
	s := 150.0 / 72
	y := int(300 * s)
	runs, inInk := 0, false
	for x := int(60 * s); x < int(460*s); x++ {
		cr, _, _, _ := img.At(x, y).RGBA()
		ink := cr>>8 < 128
		if ink && !inInk {
			runs++
		}
		inInk = ink
	}
	if runs < 5 {
		t.Errorf("found %d dashes, want the line broken up", runs)
	}
}

func TestRenderClip(t *testing.T) {
	img := renderDoc(t, RenderOpts{DPI: 150, IncludeVector: true}, func(p *Page) {
		p.Push()
		p.Rect(100, 100, 100, 100, ClipPath)
		p.SetFillColor(Black)
		p.Rect(50, 50, 400, 400, Fill)
		p.Pop()
	})
	// Inside the clip the big rectangle shows.
	if r, _, _, _ := at(t, img, 150, 150, 150); r != 0 {
		t.Errorf("inside the clip = %d, want black", r)
	}
	// Outside it, nothing was painted.
	if r, _, _, _ := at(t, img, 150, 300, 300); r != 255 {
		t.Errorf("outside the clip = %d, want white", r)
	}
	// And the clip ends with the enclosing q/Q.
	img2 := renderDoc(t, RenderOpts{DPI: 150, IncludeVector: true}, func(p *Page) {
		p.Push()
		p.Rect(100, 100, 100, 100, ClipPath)
		p.Pop()
		p.SetFillColor(Black)
		p.Rect(250, 250, 100, 100, Fill)
	})
	if r, _, _, _ := at(t, img2, 150, 300, 300); r != 0 {
		t.Errorf("after the clip was popped = %d, want black", r)
	}
}

func TestRenderTransform(t *testing.T) {
	img := renderDoc(t, RenderOpts{DPI: 150, IncludeVector: true}, func(p *Page) {
		p.Push()
		p.Translate(200, 100)
		p.SetFillColor(Black)
		p.Rect(0, 0, 50, 50, Fill)
		p.Pop()
	})
	if r, _, _, _ := at(t, img, 150, 225, 125); r != 0 {
		t.Errorf("the translated square is missing: %d", r)
	}
	if r, _, _, _ := at(t, img, 150, 25, 25); r != 255 {
		t.Errorf("something was drawn at the untranslated place: %d", r)
	}
}

func TestRenderColorSpaces(t *testing.T) {
	img := renderDoc(t, RenderOpts{DPI: 150, IncludeVector: true}, func(p *Page) {
		p.op("0.5 g 60 700 100 60 re f")      // gray
		p.op("0 1 0 rg 200 700 100 60 re f")  // rgb
		p.op("1 0 0 0 k 340 700 100 60 re f") // cmyk cyan
	})
	y := 841.89 - 730
	if r, g, b, _ := at(t, img, 150, 110, y); !near(r, 128, 2) || !near(g, 128, 2) || !near(b, 128, 2) {
		t.Errorf("gray = %d,%d,%d", r, g, b)
	}
	if r, g, b, _ := at(t, img, 150, 250, y); r != 0 || g != 255 || b != 0 {
		t.Errorf("rgb = %d,%d,%d", r, g, b)
	}
	// Cyan has no red and full green and blue.
	if r, g, b, _ := at(t, img, 150, 390, y); r != 0 || g != 255 || b != 255 {
		t.Errorf("cmyk cyan = %d,%d,%d", r, g, b)
	}
}

func TestRenderAxialShading(t *testing.T) {
	img := renderDoc(t, RenderOpts{DPI: 150, IncludeVector: true}, func(p *Page) {
		p.FillGradientRect(60, 300, 400, 60, GradientHorizontal,
			Stop(0, RGB(255, 0, 0)), Stop(1, RGB(0, 0, 255)))
	})
	left, _, lb, _ := at(t, img, 150, 70, 330)
	rr, _, rb, _ := at(t, img, 150, 450, 330)
	if left < 200 || lb > 60 {
		t.Errorf("the left end should be red, got r=%d b=%d", left, lb)
	}
	if rr > 60 || rb < 200 {
		t.Errorf("the right end should be blue, got r=%d b=%d", rr, rb)
	}
	// And it really is a ramp, not two halves.
	mr, _, mb, _ := at(t, img, 150, 260, 330)
	if mr > 200 || mr < 50 || mb > 200 || mb < 50 {
		t.Errorf("the middle should be mixed, got r=%d b=%d", mr, mb)
	}
}

func TestRenderAlpha(t *testing.T) {
	img := renderDoc(t, RenderOpts{DPI: 150, IncludeVector: true}, func(p *Page) {
		p.SetFillColor(Black)
		p.SetAlpha(0.5, 1)
		p.Rect(100, 100, 200, 200, Fill)
	})
	r, _, _, _ := at(t, img, 150, 200, 200)
	if !near(r, 128, 12) {
		t.Errorf("half-opaque black over white = %d, want about 128", r)
	}
}

func TestRenderTransparentBackground(t *testing.T) {
	img := renderDoc(t, RenderOpts{DPI: 72, IncludeVector: true, Transparent: true},
		func(p *Page) {
			p.SetFillColor(Black)
			p.Rect(100, 100, 50, 50, Fill)
		})
	if _, _, _, a := at(t, img, 72, 300, 300); a != 0 {
		t.Errorf("untouched pixels should be clear, alpha = %d", a)
	}
	if _, _, _, a := at(t, img, 72, 125, 125); a != 255 {
		t.Errorf("painted pixels should be opaque, alpha = %d", a)
	}
}

func TestRenderFormXObject(t *testing.T) {
	inner := New()
	ip := inner.AddPage()
	ip.SetFillColor(Black)
	ip.Rect(20, 20, 60, 60, Fill)
	ir := NewReaderOrFail(t, docBytes(t, inner))

	outer := New()
	outer.Compress = false
	if _, err := outer.ImportPage(ir, 0); err != nil {
		t.Fatal(err)
	}
	r := NewReaderOrFail(t, docBytes(t, outer))
	img, err := r.RenderPage(0, RenderOpts{DPI: 150, IncludeVector: true})
	if err != nil {
		t.Fatal(err)
	}
	if v, _, _, _ := at(t, img, 150, 50, 50); v != 0 {
		t.Errorf("the form's artwork was not drawn: %d", v)
	}
}

func TestRenderTextIsRefused(t *testing.T) {
	doc := New()
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(60, 60, "words")
	r := NewReaderOrFail(t, docBytes(t, doc))
	if _, err := r.RenderPage(0, RenderOpts{IncludeText: true}); err == nil {
		t.Fatal("drawing text should be refused, not silently skipped")
	}
	// And with text off, the glyphs do not appear.
	img, err := r.RenderPage(0, RenderOpts{DPI: 150, IncludeVector: true})
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if cr, _, _, _ := img.At(x, y).RGBA(); cr>>8 != 255 {
				t.Fatalf("text was drawn at (%d,%d)", x, y)
			}
		}
	}
}

func TestRenderNothingRequested(t *testing.T) {
	img := renderDoc(t, RenderOpts{DPI: 72}, func(p *Page) {
		p.SetFillColor(Black)
		p.Rect(10, 10, 100, 100, Fill)
	})
	if r, _, _, _ := at(t, img, 72, 50, 50); r != 255 {
		t.Errorf("with nothing switched on the page should be blank, got %d", r)
	}
}

func TestRenderRangeAndLimits(t *testing.T) {
	r := NewReaderOrFail(t, redactFixture(t))
	if _, err := r.RenderPage(-1, RenderOpts{IncludeVector: true}); err == nil {
		t.Error("a negative page should be refused")
	}
	if _, err := r.RenderPage(99, RenderOpts{IncludeVector: true}); err == nil {
		t.Error("a page past the end should be refused")
	}
	if _, err := r.RenderPage(0, RenderOpts{DPI: 100000, IncludeVector: true}); err == nil {
		t.Error("an absurd resolution should be refused, not attempted")
	}
}

func TestRenderRotatedPage(t *testing.T) {
	doc := New()
	doc.Compress = false
	p := doc.AddPage()
	p.SetRotate(90)
	p.SetFillColor(Black)
	p.Rect(0, 0, 100, 50, Fill)
	r := NewReaderOrFail(t, docBytes(t, doc))
	img, err := r.RenderPage(0, RenderOpts{DPI: 72, IncludeVector: true})
	if err != nil {
		t.Fatal(err)
	}
	// A quarter turn swaps the page's sides.
	b := img.Bounds()
	if b.Dx() < b.Dy() {
		t.Errorf("a page turned on its side should be wider than tall: %v", b)
	}
}

// --- the pieces underneath ---

func TestFlattenCubicIsSmooth(t *testing.T) {
	var p rasterPath
	p.moveTo(point{0, 0})
	p.curveTo(point{0, 100}, point{100, 100}, point{100, 0})
	if len(p.subs) != 1 {
		t.Fatalf("got %d subpaths", len(p.subs))
	}
	pts := p.subs[0].pts
	if len(pts) < 8 {
		t.Errorf("a curve flattened to %d points, too few to look smooth", len(pts))
	}
	if last := pts[len(pts)-1]; math.Abs(last.x-100) > 0.01 || math.Abs(last.y) > 0.01 {
		t.Errorf("the curve did not end where it should: %+v", last)
	}
	// Every point sits inside the hull of the control points.
	for _, pt := range pts {
		if pt.x < -0.5 || pt.x > 100.5 || pt.y < -0.5 || pt.y > 100.5 {
			t.Errorf("a flattened point escaped the hull: %+v", pt)
		}
	}
}

func TestAddSpanCoverage(t *testing.T) {
	cov := make([]float64, 10)
	// Exactly two whole pixels.
	addSpan(cov, 10, 2, 4, 1)
	if cov[2] != 1 || cov[3] != 1 || cov[1] != 0 || cov[4] != 0 {
		t.Errorf("whole pixels = %v", cov[:6])
	}
	// A half pixel at each end.
	cov = make([]float64, 10)
	addSpan(cov, 10, 2.5, 4.5, 1)
	if !almost(cov[2], 0.5) || !almost(cov[3], 1) || !almost(cov[4], 0.5) {
		t.Errorf("fractional ends = %v", cov[:6])
	}
	// Inside one pixel.
	cov = make([]float64, 10)
	addSpan(cov, 10, 3.25, 3.75, 1)
	if !almost(cov[3], 0.5) {
		t.Errorf("sub-pixel span = %v", cov[:6])
	}
	// Clipped to the row.
	cov = make([]float64, 10)
	addSpan(cov, 10, -5, 2, 1)
	if !almost(cov[0], 1) || !almost(cov[1], 1) {
		t.Errorf("clipped span = %v", cov[:4])
	}
	if addSpan(cov, 10, 5, 5, 1) {
		t.Error("an empty span should paint nothing")
	}
}

func almost(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestStrokeOutlineCoversTheLine(t *testing.T) {
	var p rasterPath
	p.moveTo(point{10, 50})
	p.lineTo(point{90, 50})
	out := strokeOutline(&p, strokeStyle{width: 10, miterLimit: 10})
	if len(out.subs) == 0 {
		t.Fatal("a stroke produced no outline")
	}
	minX, minY, maxX, maxY, ok := out.bounds()
	if !ok {
		t.Fatal("the outline has no extent")
	}
	if minY > 45.1 || maxY < 54.9 {
		t.Errorf("the outline is %v..%v tall, want about 45..55", minY, maxY)
	}
	if minX > 10.1 || maxX < 89.9 {
		t.Errorf("the outline runs %v..%v, want about 10..90", minX, maxX)
	}
}

func TestDashSplitsAPath(t *testing.T) {
	var p rasterPath
	p.moveTo(point{0, 0})
	p.lineTo(point{100, 0})
	got := applyDash(&p, strokeStyle{dash: []float64{10, 10}})
	if len(got) != 5 {
		t.Errorf("a 100-unit line dashed 10 on 10 off gave %d pieces, want 5", len(got))
	}
	// A pattern of nothing leaves the path whole.
	if got := applyDash(&p, strokeStyle{}); len(got) != 1 {
		t.Errorf("no dash gave %d pieces", len(got))
	}
	if got := applyDash(&p, strokeStyle{dash: []float64{0, 0}}); len(got) != 1 {
		t.Errorf("a zero pattern gave %d pieces", len(got))
	}
}

func TestCMYKToRGB(t *testing.T) {
	cases := []struct {
		c, m, y, k float64
		want       [3]float64
	}{
		{0, 0, 0, 0, [3]float64{1, 1, 1}},
		{0, 0, 0, 1, [3]float64{0, 0, 0}},
		{1, 0, 0, 0, [3]float64{0, 1, 1}},
		{0, 1, 0, 0, [3]float64{1, 0, 1}},
	}
	for _, c := range cases {
		got := cmykRGB(c.c, c.m, c.y, c.k)
		for i := range got {
			if math.Abs(got[i]-c.want[i]) > 1e-9 {
				t.Errorf("cmyk(%v,%v,%v,%v) = %v, want %v", c.c, c.m, c.y, c.k, got, c.want)
				break
			}
		}
	}
}

func TestInvertMatrix(t *testing.T) {
	m := matrix{2, 0, 0, 3, 5, 7}
	inv, ok := invert(m)
	if !ok {
		t.Fatal("a scale should be invertible")
	}
	x, y := m.apply(4, 6)
	bx, by := inv.apply(x, y)
	if math.Abs(bx-4) > 1e-9 || math.Abs(by-6) > 1e-9 {
		t.Errorf("round trip gave %v,%v", bx, by)
	}
	if _, ok := invert(matrix{0, 0, 0, 0, 0, 0}); ok {
		t.Error("a degenerate matrix should not invert")
	}
}

func TestExponentialFunction(t *testing.T) {
	r := &Reader{}
	f := loadExponential(r, Dict{
		"C0": Array{0.0, 0.0, 0.0},
		"C1": Array{1.0, 0.5, 0.0},
		"N":  1.0,
	}, nil)
	if f == nil {
		t.Fatal("the function did not load")
	}
	mid := f.eval(0.5)
	if len(mid) != 3 || math.Abs(mid[0]-0.5) > 1e-9 || math.Abs(mid[1]-0.25) > 1e-9 {
		t.Errorf("halfway = %v", mid)
	}
	if end := f.eval(1); math.Abs(end[0]-1) > 1e-9 {
		t.Errorf("the end = %v", end)
	}
	// Outside the domain it clamps rather than running away.
	if lo := f.eval(-5); math.Abs(lo[0]) > 1e-9 {
		t.Errorf("below the domain = %v", lo)
	}
}

func TestStitchingFunction(t *testing.T) {
	r := &Reader{}
	f := loadFunction(r, Dict{
		"FunctionType": int64(3),
		"Domain":       Array{0.0, 1.0},
		"Bounds":       Array{0.5},
		"Encode":       Array{0.0, 1.0, 0.0, 1.0},
		"Functions": Array{
			Dict{"FunctionType": int64(2), "C0": Array{0.0}, "C1": Array{1.0}, "N": 1.0},
			Dict{"FunctionType": int64(2), "C0": Array{1.0}, "C1": Array{0.0}, "N": 1.0},
		},
	})
	if f == nil {
		t.Fatal("the function did not load")
	}
	// Up then down: a triangle.
	if v := f.eval(0.25); math.Abs(v[0]-0.5) > 1e-9 {
		t.Errorf("a quarter along = %v, want 0.5", v)
	}
	if v := f.eval(0.75); math.Abs(v[0]-0.5) > 1e-9 {
		t.Errorf("three quarters along = %v, want 0.5", v)
	}
}

func TestUnsupportedFunctionIsNil(t *testing.T) {
	r := &Reader{}
	// Type 4 is a PostScript program, which is not evaluated.
	if f := loadFunction(r, Dict{"FunctionType": int64(4)}); f != nil {
		t.Error("a calculator function should not claim to work")
	}
}

// --- the paths the first round of tests did not reach ---

func TestRenderStrokeJoinsAndCaps(t *testing.T) {
	// Each join and cap must draw, and none may throw the corner away.
	for _, join := range []LineJoin{JoinMiter, JoinRound, JoinBevel} {
		for _, cp := range []LineCap{CapButt, CapRound, CapSquare} {
			img := renderDoc(t, RenderOpts{DPI: 100, IncludeVector: true}, func(p *Page) {
				p.SetStrokeColor(Black)
				p.SetLineWidth(12)
				p.SetLineJoin(join)
				p.SetLineCap(cp)
				p.Polygon(Stroke, 100, 200, 200, 100, 300, 200)
			})
			// The apex of the corner is inked whichever join is used.
			if v, _, _, _ := at(t, img, 100, 200, 104); v > 200 {
				t.Errorf("join %v cap %v left the corner unpainted (%d)", join, cp, v)
			}
		}
	}
}

// A miter past its limit falls back to a bevel rather than shooting off
// the page.
func TestRenderMiterLimit(t *testing.T) {
	img := renderDoc(t, RenderOpts{DPI: 100, IncludeVector: true}, func(p *Page) {
		p.SetStrokeColor(Black)
		p.SetLineWidth(10)
		p.op("2 M")     // a very tight limit
		p.op("1 j 0 j") // miter
		p.op("100 600 m 300 605 l 100 610 l S")
	})
	// Nothing should be painted far from the sliver of a corner.
	if v, _, _, _ := at(t, img, 100, 500, 230); v != 255 {
		t.Errorf("a miter ran past its limit: %d", v)
	}
}

func TestMiterPointGeometry(t *testing.T) {
	// A right angle: the miter tip sits out along the bisector.
	pt, ok := miterPoint(point{0, 10}, point{0, 0}, point{10, 0}, 5, 10)
	if !ok {
		t.Fatal("a right angle should miter")
	}
	if math.Abs(math.Hypot(pt.x, pt.y)-5*math.Sqrt2) > 0.01 {
		t.Errorf("the tip is at %+v, want %v from the corner", pt, 5*math.Sqrt2)
	}
	// A hairpin exceeds any sane limit.
	if _, ok := miterPoint(point{0, 0}, point{10, 0}, point{0, 0.001}, 5, 2); ok {
		t.Error("a hairpin should be refused by the limit")
	}
	// Straight on needs no join at all.
	if _, ok := miterPoint(point{0, 0}, point{5, 0}, point{10, 0}, 5, 10); ok {
		t.Error("a straight run should not produce a miter")
	}
}

func TestCrossAndWinding(t *testing.T) {
	if cross(point{0, 0}, point{1, 0}, point{1, 1}) <= 0 {
		t.Error("a left turn should be positive")
	}
	if cross(point{0, 0}, point{1, 0}, point{1, -1}) >= 0 {
		t.Error("a right turn should be negative")
	}
	// Shapes are wound the same way whichever order they arrive in.
	var a, b rasterPath
	addTriangle(&a, point{0, 0}, point{10, 0}, point{0, 10})
	addTriangle(&b, point{0, 10}, point{10, 0}, point{0, 0})
	if (signedArea(a.subs[0].pts) < 0) != (signedArea(b.subs[0].pts) < 0) {
		t.Error("triangles were not wound consistently")
	}
	var c rasterPath
	addCircle(&c, point{5, 5}, 4)
	if len(c.subs) != 1 || len(c.subs[0].pts) < 8 {
		t.Errorf("a disc came out as %d points", len(c.subs[0].pts))
	}
	if signedArea(c.subs[0].pts) < 0 {
		t.Error("a disc was wound the wrong way")
	}
}

func TestDashStartPhase(t *testing.T) {
	pattern := []float64{10, 5}
	idx, on, left := dashStart(pattern, 0)
	if idx != 0 || !on || left != 10 {
		t.Errorf("no phase = %d,%v,%v", idx, on, left)
	}
	// Part way through the first dash.
	if idx, on, left = dashStart(pattern, 4); idx != 0 || !on || left != 6 {
		t.Errorf("phase 4 = %d,%v,%v", idx, on, left)
	}
	// Into the gap.
	if idx, on, left = dashStart(pattern, 12); idx != 1 || on || left != 3 {
		t.Errorf("phase 12 = %d,%v,%v", idx, on, left)
	}
	// Round the pattern more than once.
	if idx, on, _ = dashStart(pattern, 30); idx != 0 || !on {
		t.Errorf("phase 30 = %d,%v", idx, on)
	}
}

func TestRenderIndexedAndSeparation(t *testing.T) {
	// An indexed palette, and a separation whose tint runs through a
	// function into a device space.
	img := renderDoc(t, RenderOpts{DPI: 100, IncludeVector: true}, func(p *Page) {
		p.ownResources = Dict{"ColorSpace": Dict{
			"Pal": Array{Name("Indexed"), Name("DeviceRGB"), int64(1),
				String([]byte{0xFF, 0x00, 0x00, 0x00, 0x00, 0xFF})},
			"Spot": Array{Name("Separation"), Name("Ink"), Name("DeviceRGB"),
				Dict{"FunctionType": int64(2), "Domain": Array{0.0, 1.0},
					"C0": Array{1.0, 1.0, 1.0}, "C1": Array{0.0, 0.5, 0.0}, "N": 1.0}},
		}}
		p.op("/Pal cs 1 sc 60 700 80 60 re f")
		p.op("/Spot cs 1 scn 200 700 80 60 re f")
	})
	y := 841.89 - 730
	if r, g, b, _ := at(t, img, 100, 100, y); r != 0 || g != 0 || b != 255 {
		t.Errorf("indexed entry 1 = %d,%d,%d, want blue", r, g, b)
	}
	if r, g, b, _ := at(t, img, 100, 240, y); r != 0 || !near(g, 128, 2) || b != 0 {
		t.Errorf("separation at full tint = %d,%d,%d, want dark green", r, g, b)
	}
}

func TestColorSpaceHelpers(t *testing.T) {
	if (&colorSpace{n: 4}).components() != 4 {
		t.Error("component count is wrong")
	}
	if (*colorSpace)(nil).components() != 3 {
		t.Error("an unknown space should assume three components")
	}
	if (&colorSpace{indexed: []byte{1}}).components() != 1 {
		t.Error("an indexed space takes one component")
	}
	if got := (&colorSpace{n: 3}).initial(); got != [3]float64{0, 0, 0} {
		t.Errorf("a space should start at black, got %v", got)
	}
	// An unreadable colour leaves the previous one alone.
	prev := [3]float64{0.2, 0.4, 0.6}
	if got := (&colorSpace{n: 3}).toRGB(nil, prev); got != prev {
		t.Errorf("no components should keep the colour, got %v", got)
	}
	if got := componentsToRGB([]float64{1, 2}, prev); got != prev {
		t.Errorf("an odd component count should keep the colour, got %v", got)
	}
}

func TestSampledFunction(t *testing.T) {
	// Four 8-bit samples of one output, ramping 0 to 1.
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	_ = page
	src := docBytes(t, doc)
	r := NewReaderOrFail(t, src)
	stm := &rawStream{
		dict: Dict{
			"FunctionType":  int64(0),
			"Domain":        Array{0.0, 1.0},
			"Range":         Array{0.0, 1.0},
			"Size":          Array{int64(4)},
			"BitsPerSample": int64(8),
		},
		data: []byte{0, 85, 170, 255},
	}
	f := loadFunctionDict(r, stm.dict, stm)
	if f == nil {
		t.Fatal("a sampled function did not load")
	}
	if v := f.eval(0); math.Abs(v[0]) > 0.01 {
		t.Errorf("the start = %v", v)
	}
	if v := f.eval(1); math.Abs(v[0]-1) > 0.01 {
		t.Errorf("the end = %v", v)
	}
	if v := f.eval(0.5); math.Abs(v[0]-0.5) > 0.05 {
		t.Errorf("halfway = %v", v)
	}
}

func TestSampleBitsReader(t *testing.T) {
	b := &sampleBits{data: []byte{0xF0, 0x0F}}
	for _, want := range []uint64{0xF, 0x0, 0x0, 0xF} {
		got, ok := b.read(4)
		if !ok || got != want {
			t.Errorf("read 4 bits = %v,%v want %v", got, ok, want)
		}
	}
	if _, ok := b.read(4); ok {
		t.Error("reading past the end should fail")
	}
	if _, ok := (&sampleBits{}).read(0); ok {
		t.Error("a zero width should fail")
	}
}

func TestRenderRasterImage(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	m := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for i := 0; i < 4; i++ {
		m.Pix[i*4], m.Pix[i*4+1], m.Pix[i*4+2], m.Pix[i*4+3] = 220, 30, 30, 255
	}
	img, err := doc.AddImage(m)
	if err != nil {
		t.Fatal(err)
	}
	page.DrawImage(img, 100, 100, 120, 80)
	r := NewReaderOrFail(t, docBytes(t, doc))

	// Off by default.
	off, err := r.RenderPage(0, RenderOpts{DPI: 100, IncludeVector: true})
	if err != nil {
		t.Fatal(err)
	}
	if v, _, _, _ := at(t, off, 100, 160, 140); v != 255 {
		t.Errorf("images should be left out unless asked for, got %d", v)
	}
	// And drawn when asked for.
	on, err := r.RenderPage(0, RenderOpts{DPI: 100, IncludeVector: true,
		IncludeRasterImages: true})
	if err != nil {
		t.Fatal(err)
	}
	rr, gg, bb, _ := at(t, on, 100, 160, 140)
	if !near(rr, 220, 6) || !near(gg, 30, 6) || !near(bb, 30, 6) {
		t.Errorf("the image = %d,%d,%d, want the red it holds", rr, gg, bb)
	}
}

func TestRenderShadingFallsBackToSolid(t *testing.T) {
	// A radial shading is not drawn as a gradient; the clip is filled
	// with a colour from the middle of its ramp rather than left blank.
	img := renderDoc(t, RenderOpts{DPI: 100, IncludeVector: true}, func(p *Page) {
		p.ownResources = Dict{"Shading": Dict{"Sh": Dict{
			"ShadingType": int64(3),
			"ColorSpace":  Name("DeviceRGB"),
			"Coords":      Array{100.0, 400.0, 0.0, 100.0, 400.0, 50.0},
			"Function": Dict{"FunctionType": int64(2), "Domain": Array{0.0, 1.0},
				"C0": Array{1.0, 0.0, 0.0}, "C1": Array{1.0, 0.0, 0.0}, "N": 1.0},
		}}}
		p.Push()
		p.Rect(60, 380, 100, 60, ClipPath)
		p.op("/Sh sh")
		p.Pop()
	})
	if r, g, b, _ := at(t, img, 100, 110, 410); r < 200 || g > 60 || b > 60 {
		t.Errorf("the fallback should paint the ramp's colour, got %d,%d,%d", r, g, b)
	}
	// And it stays inside the clip.
	if r, _, _, _ := at(t, img, 100, 300, 410); r != 255 {
		t.Errorf("the fallback escaped the clip: %d", r)
	}
}
