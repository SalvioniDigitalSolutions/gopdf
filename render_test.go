package gopdf

import (
	"bytes"
	"image"
	"math"
	"os"
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

// TestRenderTextIsOffByDefault: text is drawn only when asked for, and
// with it off the page carries the artwork and nothing else.
func TestRenderTextIsOffByDefault(t *testing.T) {
	doc := New()
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(60, 60, "words")
	r := NewReaderOrFail(t, docBytes(t, doc))

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

// TestRenderTextWithoutAFontLeavesHoles: one of the standard fourteen is
// not embedded, so without a substitute there is nothing to draw with.
// The count is the point — a page with holes should say so.
func TestRenderTextWithoutAFontLeavesHoles(t *testing.T) {
	doc := New()
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(60, 60, "words")
	r := NewReaderOrFail(t, docBytes(t, doc))

	img, rep, err := r.RenderPageDetail(0, RenderOpts{DPI: 150, IncludeText: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Glyphs != 0 {
		t.Errorf("drew %d glyphs from a font that is not in the file", rep.Glyphs)
	}
	if rep.Missing != 5 {
		t.Errorf("missing = %d, want the 5 letters of the word", rep.Missing)
	}
	if inkFraction(img) != 0 {
		t.Error("something was painted with no font to paint it with")
	}
}

// TestRenderEmbeddedText draws a page set in a font the document carries,
// which is the case that needs no help from anywhere.
func TestRenderEmbeddedText(t *testing.T) {
	font, err := LoadFont(testFontPath(t))
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	p := doc.AddPage()
	p.SetFont(font, 48)
	p.Text(60, 700, "Hamburgefonstiv")
	r := NewReaderOrFail(t, docBytes(t, doc))

	img, rep, err := r.RenderPageDetail(0, RenderOpts{DPI: 100, IncludeText: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Missing != 0 {
		t.Errorf("%d glyphs of an embedded font could not be drawn", rep.Missing)
	}
	if rep.Glyphs != 15 {
		t.Errorf("drew %d glyphs, want 15", rep.Glyphs)
	}
	ink := inkFraction(img)
	if ink < 0.005 {
		t.Errorf("the page is %.4f%% ink; the text was not drawn", ink*100)
	}
	// The ink must be where the text is, not spread over the page: a
	// glyph drawn at the wrong scale is the failure this catches. Text
	// places its baseline from the top of the page, so at 100 DPI a
	// baseline 700 points down lands near row 972.
	minX, minY, maxX, maxY := inkBounds(img)
	if minY < 900 || maxY > 1000 {
		t.Errorf("text ink runs from y=%d to y=%d, want one line near y=972",
			minY, maxY)
	}
	if maxY-minY > 70 {
		t.Errorf("the text is %d pixels tall, which is more than one line", maxY-minY)
	}
	if minX < 75 || maxX > 700 {
		t.Errorf("text ink runs from x=%d to x=%d, want it to start near x=83",
			minX, maxX)
	}
}

// TestRenderTextModes covers what the eight rendering modes do: fill,
// stroke, both, and the invisible mode that scanned pages use.
func TestRenderTextModes(t *testing.T) {
	font, err := LoadFont(testFontPath(t))
	if err != nil {
		t.Fatal(err)
	}
	ink := func(mode int) float64 {
		doc := New()
		doc.Compress = false
		p := doc.AddPage()
		p.SetFont(font, 60)
		p.op("%d Tr", mode)
		p.Text(60, 200, "MMMM")
		r := NewReaderOrFail(t, docBytes(t, doc))
		img, err := r.RenderPage(0, RenderOpts{DPI: 100, IncludeText: true})
		if err != nil {
			t.Fatal(err)
		}
		return inkFraction(img)
	}
	filled := ink(0)
	if filled <= 0 {
		t.Fatal("mode 0 drew nothing")
	}
	if got := ink(3); got != 0 {
		t.Errorf("mode 3 is invisible but painted %.4f of the page", got)
	}
	// A stroked outline is lighter than a solid fill of the same letters.
	if stroked := ink(1); stroked <= 0 || stroked >= filled {
		t.Errorf("mode 1 ink %.4f against mode 0 ink %.4f", stroked, filled)
	}
}

// TestRenderTextClip is the case that makes text matter to a renderer
// that does not otherwise draw it: a headline used as a clip, with a
// shading painted through the letters. Ignore the clip and the shading
// covers the page.
func TestRenderTextClip(t *testing.T) {
	font, err := LoadFont(testFontPath(t))
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	doc.Compress = false
	p := doc.AddPage()
	p.SetFont(font, 72)
	// Text writes its own BT and ET, so the mode is set around it; text
	// state belongs to the graphics state and carries in.
	p.op("q 7 Tr")
	p.Text(60, 200, "CLIP")
	p.op("0 0 0 rg 0 500 600 200 re f Q")
	r := NewReaderOrFail(t, docBytes(t, doc))

	img, err := r.RenderPage(0, RenderOpts{DPI: 100, IncludeVector: true})
	if err != nil {
		t.Fatal(err)
	}
	ink := inkFraction(img)
	if ink == 0 {
		t.Fatal("the clip removed everything")
	}
	// The rectangle covers about a fifth of the page. Through the
	// letters it must cover a great deal less.
	if ink > 0.05 {
		t.Errorf("%.3f of the page is painted; the text clip was ignored", ink)
	}
	// And what is painted has to sit on the line of text, which a
	// baseline 200 points from the top puts near row 278.
	_, minY, _, maxY := inkBounds(img)
	if minY < 200 || maxY > 300 {
		t.Errorf("ink runs from y=%d to y=%d, not along the headline", minY, maxY)
	}
}

// TestRenderTextClipWithNoFontHidesRatherThanFloods: a clip built from a
// font that cannot be read holds nothing, and nothing is what shows
// through. Treating the clip as absent would paint the fill over the
// whole page, which is the worse of the two mistakes by far.
func TestRenderTextClipWithNoFontHidesRatherThanFloods(t *testing.T) {
	doc := New()
	doc.Compress = false
	p := doc.AddPage()
	p.SetFont(Helvetica, 72) // not embedded, so no outlines
	p.op("q 7 Tr")
	p.Text(60, 200, "CLIP")
	p.op("0 0 0 rg 0 500 600 200 re f Q")
	r := NewReaderOrFail(t, docBytes(t, doc))

	img, err := r.RenderPage(0, RenderOpts{DPI: 100, IncludeVector: true})
	if err != nil {
		t.Fatal(err)
	}
	if ink := inkFraction(img); ink != 0 {
		t.Errorf("%.3f of the page was painted through a clip that could not be built", ink)
	}
}

// TestRenderSubstituteFont: the document names a font it does not carry,
// and the caller supplies one.
func TestRenderSubstituteFont(t *testing.T) {
	data, err := os.ReadFile(testFontPath(t))
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	p := doc.AddPage()
	p.SetFont(Helvetica, 36)
	p.Text(60, 700, "substituted")
	r := NewReaderOrFail(t, docBytes(t, doc))

	var asked FontRequest
	img, rep, err := r.RenderPageDetail(0, RenderOpts{
		DPI: 100, IncludeText: true,
		SubstituteFont: func(req FontRequest) []byte {
			asked = req
			return data
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if asked.BaseFont != "Helvetica" {
		t.Errorf("asked for %q, want Helvetica", asked.BaseFont)
	}
	if rep.Missing != 0 || rep.Glyphs != 11 {
		t.Errorf("drew %d glyphs and missed %d, want 11 and 0", rep.Glyphs, rep.Missing)
	}
	if inkFraction(img) == 0 {
		t.Error("the substitute drew nothing")
	}

	// Returning nothing must leave the page blank rather than break it.
	img2, rep2, err := r.RenderPageDetail(0, RenderOpts{
		DPI: 100, IncludeText: true,
		SubstituteFont: func(FontRequest) []byte { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Glyphs != 0 || inkFraction(img2) != 0 {
		t.Error("a substitute that declined still drew something")
	}
}

// TestSubstituteUsesDocumentWidths: the stand-in supplies shapes, not
// metrics. Text set in a substitute must occupy the width the document
// says it does, or every line ends in the wrong place.
func TestSubstituteUsesDocumentWidths(t *testing.T) {
	data, err := os.ReadFile(testFontPath(t))
	if err != nil {
		t.Fatal(err)
	}
	// Courier is fixed-pitch at 600/1000 em, which no proportional
	// substitute matches, so the advance can only be right if it came
	// from the document.
	doc := New()
	doc.Compress = false
	p := doc.AddPage()
	p.SetFont(Courier, 50)
	p.Text(60, 700, "iiii")
	r := NewReaderOrFail(t, docBytes(t, doc))

	img, _, err := r.RenderPageDetail(0, RenderOpts{
		DPI: 72, IncludeText: true,
		SubstituteFont: func(FontRequest) []byte { return data },
	})
	if err != nil {
		t.Fatal(err)
	}
	minX, _, maxX, _ := inkBounds(img)
	// Four characters at 600/1000 of 50pt is 120pt from the first pen
	// position; the last glyph's ink stops a little short of that.
	if maxX-minX < 80 || maxX-minX > 125 {
		t.Errorf("four fixed-pitch characters span %d points, want about 100",
			maxX-minX)
	}
}

// inkBounds is the box containing everything painted.
func inkBounds(img image.Image) (minX, minY, maxX, maxY int) {
	b := img.Bounds()
	minX, minY = b.Max.X, b.Max.Y
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			if cr>>8 > 245 && cg>>8 > 245 && cb>>8 > 245 {
				continue
			}
			if x < minX {
				minX = x
			}
			if x > maxX {
				maxX = x
			}
			if y < minY {
				minY = y
			}
			if y > maxY {
				maxY = y
			}
		}
	}
	return minX, minY, maxX, maxY
}

// inkFraction is how much of a page was painted.
func inkFraction(img image.Image) float64 {
	b := img.Bounds()
	ink, total := 0, 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			total++
			cr, cg, cb, _ := img.At(x, y).RGBA()
			if cr>>8 < 250 || cg>>8 < 250 || cb>>8 < 250 {
				ink++
			}
		}
	}
	if total == 0 {
		return 0
	}
	return float64(ink) / float64(total)
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

// TestRenderSoftMask covers a shape faded by a luminosity mask. Drawn
// without the mask it is a solid slab, which is how a watermark ruins a
// page.
func TestRenderSoftMask(t *testing.T) {
	// The mask is a form painting mid grey over the left half. A stream
	// has to be an indirect object, so the page is built and then the
	// objects added to it.
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.op("/GSm gs 0 0 0 rg 60 600 400 100 re f")
	src := withResources(t, docBytes(t, doc), func(u *Updater) Dict {
		g := u.add(&rawStream{
			dict: Dict{
				"Type": Name("XObject"), "Subtype": Name("Form"),
				"BBox":  Array{0.0, 0.0, 600.0, 850.0},
				"Group": Dict{"S": Name("Transparency")},
			},
			data: []byte("0.5 g 0 0 250 850 re f\n"),
		})
		return Dict{"ExtGState": Dict{"GSm": Dict{
			"Type":  Name("ExtGState"),
			"SMask": Dict{"S": Name("Luminosity"), "G": Ref{Num: g}},
		}}}
	})

	r := NewReaderOrFail(t, src)
	img, err := r.RenderPage(0, RenderOpts{DPI: 100, IncludeVector: true})
	if err != nil {
		t.Fatal(err)
	}
	y := 841.89 - 650
	// Under the grey half the black shows at about half strength.
	masked, _, _, _ := at(t, img, 100, 150, y)
	if masked < 100 || masked > 160 {
		t.Errorf("under a mid-grey mask = %d, want about 128", masked)
	}
	// Beyond the mask nothing was painted: the mask is black there.
	clear, _, _, _ := at(t, img, 100, 400, y)
	if clear != 255 {
		t.Errorf("outside the mask = %d, want the page untouched", clear)
	}
}

// withResources rebuilds a document with extra entries merged into the
// first page's resources, adding whatever indirect objects they need.
func withResources(t *testing.T, src []byte, build func(*Updater) Dict) []byte {
	t.Helper()
	r := NewReaderOrFail(t, src)
	u := Update(r)
	extra := build(u)
	pi := r.pages[0]
	res, _ := r.resolve(pi.resources).(Dict)
	merged := cloneDict(res)
	for k, v := range extra {
		if existing, ok := r.resolve(merged[k]).(Dict); ok {
			combined := cloneDict(existing)
			if add, ok := v.(Dict); ok {
				for ak, av := range add {
					combined[ak] = av
				}
			}
			merged[k] = combined
			continue
		}
		merged[k] = v
	}
	pd := cloneDict(pi.dict)
	pd["Resources"] = merged
	num, _ := r.pageObjectNumber(0)
	u.set(num, pd)
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestRenderSoftMaskNoneClearsIt(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.op("/GSm gs /GSn gs 0 0 0 rg 60 600 200 100 re f")
	src := withResources(t, docBytes(t, doc), func(u *Updater) Dict {
		g := u.add(&rawStream{
			dict: Dict{"Type": Name("XObject"), "Subtype": Name("Form"),
				"BBox": Array{0.0, 0.0, 600.0, 850.0}},
			data: []byte("0 g 0 0 100 100 re f\n"),
		})
		return Dict{"ExtGState": Dict{
			"GSm": Dict{"SMask": Dict{"S": Name("Luminosity"), "G": Ref{Num: g}}},
			"GSn": Dict{"SMask": Name("None")},
		}}
	})
	r := NewReaderOrFail(t, src)
	img, err := r.RenderPage(0, RenderOpts{DPI: 100, IncludeVector: true})
	if err != nil {
		t.Fatal(err)
	}
	if v, _, _, _ := at(t, img, 100, 150, 841.89-650); v != 0 {
		t.Errorf("after /SMask /None the fill should be solid, got %d", v)
	}
}

// TestRenderTilingPattern covers a hatch. Painted as a flat colour it is
// whatever was set last, which for a light hatch can be solid black.
func TestRenderTilingPattern(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.op("/Pattern cs /P1 scn 60 600 200 120 re f")
	src := withResources(t, docBytes(t, doc), func(u *Updater) Dict {
		p := u.add(&rawStream{
			dict: Dict{
				"Type": Name("Pattern"), "PatternType": int64(1),
				"PaintType": int64(1), "TilingType": int64(1),
				"BBox":  Array{0.0, 0.0, 20.0, 20.0},
				"XStep": 20.0, "YStep": 20.0,
				"Resources": Dict{},
			},
			// A small black square in a mostly empty cell.
			data: []byte("0 g 0 0 8 8 re f\n"),
		})
		return Dict{"Pattern": Dict{"P1": Ref{Num: p}}}
	})
	r := NewReaderOrFail(t, src)
	img, err := r.RenderPage(0, RenderOpts{DPI: 100, IncludeVector: true})
	if err != nil {
		t.Fatal(err)
	}
	// The area is neither blank nor solid: count how much ink landed.
	s := 100.0 / 72
	ink, tot := 0, 0
	for y := int((841.89 - 720) * s); y < int((841.89-600)*s); y++ {
		for x := int(60 * s); x < int(260*s); x++ {
			cr, _, _, _ := img.At(x, y).RGBA()
			tot++
			if cr>>8 < 128 {
				ink++
			}
		}
	}
	frac := float64(ink) / float64(tot)
	if frac < 0.05 {
		t.Errorf("the pattern painted almost nothing (%.3f)", frac)
	}
	if frac > 0.60 {
		t.Errorf("the pattern painted almost everything (%.3f); a hatch is mostly gaps", frac)
	}
	// And it stayed inside the path.
	if v, _, _, _ := at(t, img, 100, 400, 841.89-650); v != 255 {
		t.Errorf("the pattern escaped its path: %d", v)
	}
}

func TestRenderShadingPattern(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.op("/Pattern cs /S1 scn 60 600 400 100 re f")
	src := withResources(t, docBytes(t, doc), func(u *Updater) Dict {
		return Dict{"Pattern": Dict{"S1": Dict{
			"Type": Name("Pattern"), "PatternType": int64(2),
			"Shading": Dict{
				"ShadingType": int64(2),
				"ColorSpace":  Name("DeviceRGB"),
				"Coords":      Array{60.0, 0.0, 460.0, 0.0},
				"Extend":      Array{true, true},
				"Function": Dict{"FunctionType": int64(2), "Domain": Array{0.0, 1.0},
					"C0": Array{1.0, 0.0, 0.0}, "C1": Array{0.0, 0.0, 1.0}, "N": 1.0},
			},
		}}}
	})
	r := NewReaderOrFail(t, src)
	img, err := r.RenderPage(0, RenderOpts{DPI: 100, IncludeVector: true})
	if err != nil {
		t.Fatal(err)
	}
	y := 841.89 - 650
	lr, _, lb, _ := at(t, img, 100, 80, y)
	rr, _, rb, _ := at(t, img, 100, 440, y)
	if lr < 180 || lb > 80 {
		t.Errorf("the left of the shading pattern = r%d b%d, want red", lr, lb)
	}
	if rr > 80 || rb < 180 {
		t.Errorf("the right = r%d b%d, want blue", rr, rb)
	}
}

func TestCombineMasks(t *testing.T) {
	a := &clipMask{w: 2, h: 1, a: []float32{1, 0.5}}
	b := &clipMask{w: 2, h: 1, a: []float32{0.5, 0.5}}
	if got := combineMasks(nil, b); got != b {
		t.Error("no clip should give back the mask")
	}
	if got := combineMasks(a, nil); got != a {
		t.Error("no mask should give back the clip")
	}
	got := combineMasks(a, b)
	if got.a[0] != 0.5 || got.a[1] != 0.25 {
		t.Errorf("combined = %v", got.a)
	}
	// Neither original was disturbed.
	if a.a[0] != 1 || b.a[0] != 0.5 {
		t.Error("combining changed an input")
	}
}

func TestPatternRange(t *testing.T) {
	// An identity placement, a 10-unit step, a box from 0 to 25.
	lo, hi := patternRange(identityMatrix, 0, 0, 25, 25, 10, 10)
	if lo[0] > 0 || lo[1] > 0 || hi[0] < 2 || hi[1] < 2 {
		t.Errorf("range %v..%v does not cover the box", lo, hi)
	}
}

// TestSoftMaskSurvivesAGroupThatClearsIt is the shape Illustrator writes
// and the reason a faded logo came out as a solid slab: the outer state
// sets a soft mask, and the very first thing the form it applies to does
// is set /SMask /None. A transparency group is composited as a whole and
// the mask applies to that composite, so the group cannot undo it from
// the inside.
func TestSoftMaskSurvivesAGroupThatClearsIt(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.op("q /GSm gs /Fm0 Do Q")
	src := withResources(t, docBytes(t, doc), func(u *Updater) Dict {
		mask := u.add(&rawStream{
			dict: Dict{
				"Type": Name("XObject"), "Subtype": Name("Form"),
				"BBox":  Array{0.0, 0.0, 600.0, 850.0},
				"Group": Dict{"S": Name("Transparency")},
			},
			// Mid grey over the left half, black elsewhere.
			data: []byte("0.5 g 0 0 250 850 re f\n"),
		})
		none := u.add(Dict{"Type": Name("ExtGState"), "SMask": Name("None")})
		// The artwork: a transparency group that clears the soft mask on
		// its first line and then paints black across the page.
		art := u.add(&rawStream{
			dict: Dict{
				"Type": Name("XObject"), "Subtype": Name("Form"),
				"BBox":      Array{0.0, 0.0, 600.0, 850.0},
				"Group":     Dict{"S": Name("Transparency")},
				"Resources": Dict{"ExtGState": Dict{"GS0": Ref{Num: none}}},
			},
			data: []byte("/GS0 gs 0 0 0 rg 60 600 400 100 re f\n"),
		})
		return Dict{
			"XObject": Dict{"Fm0": Ref{Num: art}},
			"ExtGState": Dict{"GSm": Dict{
				"Type":  Name("ExtGState"),
				"SMask": Dict{"S": Name("Luminosity"), "G": Ref{Num: mask}},
			}},
		}
	})

	r := NewReaderOrFail(t, src)
	img, err := r.RenderPage(0, RenderOpts{DPI: 100, IncludeVector: true})
	if err != nil {
		t.Fatal(err)
	}
	y := 841.89 - 650
	if masked, _, _, _ := at(t, img, 100, 150, y); masked < 100 || masked > 160 {
		t.Errorf("under the mid-grey half of the mask = %d, want about 128", masked)
	}
	// The half the mask hides must stay hidden, however the group asked.
	if clear, _, _, _ := at(t, img, 100, 400, y); clear != 255 {
		t.Errorf("the group cleared the mask and painted %d, want 255", clear)
	}
}

// TestSoftMaskIsClearableOutsideAGroup is the other half of the rule: a
// form with no transparency group paints straight into the page, so
// /SMask /None inside it does clear the mask, as it does anywhere else.
func TestSoftMaskIsClearableOutsideAGroup(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.op("q /GSm gs /Fm0 Do Q")
	src := withResources(t, docBytes(t, doc), func(u *Updater) Dict {
		mask := u.add(&rawStream{
			dict: Dict{
				"Type": Name("XObject"), "Subtype": Name("Form"),
				"BBox":  Array{0.0, 0.0, 600.0, 850.0},
				"Group": Dict{"S": Name("Transparency")},
			},
			data: []byte("0.5 g 0 0 250 850 re f\n"),
		})
		none := u.add(Dict{"Type": Name("ExtGState"), "SMask": Name("None")})
		art := u.add(&rawStream{
			dict: Dict{
				"Type": Name("XObject"), "Subtype": Name("Form"),
				"BBox":      Array{0.0, 0.0, 600.0, 850.0},
				"Resources": Dict{"ExtGState": Dict{"GS0": Ref{Num: none}}},
			},
			data: []byte("/GS0 gs 0 0 0 rg 60 600 400 100 re f\n"),
		})
		return Dict{
			"XObject": Dict{"Fm0": Ref{Num: art}},
			"ExtGState": Dict{"GSm": Dict{
				"Type":  Name("ExtGState"),
				"SMask": Dict{"S": Name("Luminosity"), "G": Ref{Num: mask}},
			}},
		}
	})

	r := NewReaderOrFail(t, src)
	img, err := r.RenderPage(0, RenderOpts{DPI: 100, IncludeVector: true})
	if err != nil {
		t.Fatal(err)
	}
	y := 841.89 - 650
	if v, _, _, _ := at(t, img, 100, 400, y); v > 40 {
		t.Errorf("outside a group the mask should have been cleared, got %d", v)
	}
}
