package gopdf

import (
	"fmt"
	"image"
	"image/color"
	"math"
)

// Drawing a page's artwork.
//
// Some of what a page holds has no other form. Text can be extracted, a
// photograph can be pulled out and re-anchored, but a logo drawn as two
// hundred Bézier curves, a rule, a checkbox, a watermark across the
// diagonal — those are instructions, and the only way to carry them
// anywhere else is to draw them.
//
// So this draws them, and deliberately little else. With text and
// photographs turned off what remains is the vector layer: a picture of
// everything on the page that cannot travel any other way, which can sit
// behind live text rather than replacing it.

// RenderOpts says what to draw and how large.
type RenderOpts struct {
	// DPI is the resolution. Zero means 150, which is where a rule stops
	// looking soft.
	DPI float64
	// IncludeText draws glyphs. It is not supported: rendering type means
	// a font rasterizer, and the point of this is the artwork that has no
	// other form. Setting it reports an error rather than quietly
	// producing a page with holes in it.
	IncludeText bool
	// IncludeRasterImages draws photographs and scans. Off by default:
	// they can be pulled out and placed separately, and leaving them out
	// keeps the layer small.
	IncludeRasterImages bool
	// IncludeVector draws paths — fills, strokes and shadings. This is
	// the point; with everything off the result is blank.
	IncludeVector bool
	// Transparent leaves untouched pixels clear instead of white.
	Transparent bool
}

func (o RenderOpts) dpi() float64 {
	if o.DPI <= 0 {
		return 150
	}
	return o.DPI
}

// RenderPage draws a page and returns the picture.
//
// A page is measured in points and the result in pixels, so the size
// follows from the DPI: an A4 page at 150 comes out 1240 by 1754.
//
// What is drawn is chosen by the options, and nothing is drawn that they
// do not ask for. A malformed content stream is reported for that page
// rather than panicking.
func (r *Reader) RenderPage(page int, opts RenderOpts) (img image.Image, err error) {
	if page < 0 || page >= len(r.pages) {
		return nil, fmt.Errorf("gopdf: page %d out of range (document has %d pages)",
			page, r.NumPages())
	}
	if opts.IncludeText {
		return nil, fmt.Errorf("gopdf: RenderPage cannot draw text; it renders the " +
			"vector layer, and glyphs need a font rasterizer this package does not have")
	}
	defer func() {
		if e := recover(); e != nil {
			img, err = nil, fmt.Errorf("gopdf: page %d could not be drawn: %v", page+1, e)
		}
	}()

	pi := r.pages[page]
	size, err := r.PageSize(page)
	if err != nil {
		return nil, err
	}
	scale := opts.dpi() / 72
	w := int(math.Ceil(size.W * scale))
	h := int(math.Ceil(size.H * scale))
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("gopdf: page %d has no area", page+1)
	}
	const maxSide = 20000
	if w > maxSide || h > maxSide {
		return nil, fmt.Errorf("gopdf: page %d would be %dx%d pixels at %g DPI",
			page+1, w, h, opts.dpi())
	}

	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	if !opts.Transparent {
		fillWhite(dst)
	}
	if !opts.IncludeVector && !opts.IncludeRasterImages {
		return dst, nil
	}

	content, err := r.pageContent(pi.dict)
	if err != nil {
		return nil, fmt.Errorf("gopdf: page %d: %w", page+1, err)
	}

	// PDF space has its origin at the bottom left of the media box and
	// runs upwards; the picture runs downwards from the top left.
	base := matrix{scale, 0, 0, -scale, -pi.mediaBox[0] * scale, pi.mediaBox[3] * scale}
	if pi.rotate != 0 {
		base = rotationMatrix(pi.rotate, size, scale).mul(base)
	}

	rn := &renderer{r: r, dst: dst, w: w, h: h, opts: opts}
	rn.run(content, pi.resources, base, newRenderState(), 0)
	return dst, nil
}

// rotationMatrix turns the page as its /Rotate asks.
func rotationMatrix(deg int, size PageSize, scale float64) matrix {
	deg = ((deg % 360) + 360) % 360
	w, h := size.W*scale, size.H*scale
	switch deg {
	case 90:
		return matrix{0, 1, -1, 0, w, 0}
	case 180:
		return matrix{-1, 0, 0, -1, w, h}
	case 270:
		return matrix{0, -1, 1, 0, 0, h}
	}
	return identityMatrix
}

func fillWhite(m *image.NRGBA) {
	for i := 0; i < len(m.Pix); i += 4 {
		m.Pix[i], m.Pix[i+1], m.Pix[i+2], m.Pix[i+3] = 0xFF, 0xFF, 0xFF, 0xFF
	}
}

// renderState is the part of the graphics state that drawing depends on.
type renderState struct {
	ctm         matrix
	fill        [3]float64
	stroke      [3]float64
	fillAlpha   float64
	strokeAlpha float64
	line        strokeStyle
	clip        *clipMask
	fillSpace   *colorSpace
	strokeSpace *colorSpace
}

func newRenderState() renderState {
	return renderState{
		fillAlpha:   1,
		strokeAlpha: 1,
		line:        strokeStyle{width: 1, miterLimit: 10},
	}
}

// clipMask is the coverage every later paint is multiplied by. A nil mask
// means nothing is clipped, which costs nothing to carry.
type clipMask struct {
	w, h int
	a    []float32
}

func (c *clipMask) at(x, y int) float64 {
	if c == nil {
		return 1
	}
	if x < 0 || y < 0 || x >= c.w || y >= c.h {
		return 0
	}
	return float64(c.a[y*c.w+x])
}

// renderer walks a content stream and paints what it says.
type renderer struct {
	r     *Reader
	dst   *image.NRGBA
	w, h  int
	opts  RenderOpts
	depth int
}

const maxRenderDepth = 12

// run interprets one content stream.
func (rn *renderer) run(content []byte, resources any, base matrix, gs renderState, depth int) {
	if depth > maxRenderDepth {
		return
	}
	gs.ctm = base
	res, _ := rn.r.resolve(resources).(Dict)

	var stack []renderState
	var path rasterPath
	var start point
	pendingClip := 0 // 0 none, 1 nonzero, 2 even-odd
	inText := false

	tokens := tokenizeContent(content)
	var operands []contentToken
	num := func(i int) float64 {
		if i >= len(operands) {
			return 0
		}
		v, _ := toFloat(operands[i].val)
		return v
	}
	nums := func() []float64 {
		out := make([]float64, len(operands))
		for i := range operands {
			out[i] = num(i)
		}
		return out
	}
	dev := func(x, y float64) point {
		px, py := gs.ctm.apply(x, y)
		return point{px, py}
	}

	for _, tok := range tokens {
		op, isOp := tok.val.(opKeyword)
		if !isOp {
			if len(operands) < 64 {
				operands = append(operands, tok)
			}
			continue
		}
		switch string(op) {
		case "q":
			if len(stack) < 64 {
				stack = append(stack, gs)
			}
		case "Q":
			if n := len(stack); n > 0 {
				gs = stack[n-1]
				stack = stack[:n-1]
			}
		case "cm":
			if len(operands) >= 6 {
				var m matrix
				for i := 0; i < 6; i++ {
					m[i] = num(i)
				}
				gs.ctm = m.mul(gs.ctm)
			}

		// Path construction.
		case "m":
			start = dev(num(0), num(1))
			path.moveTo(start)
		case "l":
			path.lineTo(dev(num(0), num(1)))
		case "c":
			path.curveTo(dev(num(0), num(1)), dev(num(2), num(3)), dev(num(4), num(5)))
		case "v":
			if cur, ok := path.current(); ok {
				path.curveTo(cur, dev(num(0), num(1)), dev(num(2), num(3)))
			}
		case "y":
			to := dev(num(2), num(3))
			path.curveTo(dev(num(0), num(1)), to, to)
		case "h":
			path.close()
		case "re":
			x, y, rw, rh := num(0), num(1), num(2), num(3)
			path.moveTo(dev(x, y))
			path.lineTo(dev(x+rw, y))
			path.lineTo(dev(x+rw, y+rh))
			path.lineTo(dev(x, y+rh))
			path.close()

		// Painting.
		case "n":
			rn.endPath(&path, &gs, &pendingClip)
		case "f", "F", "f*":
			rn.fill(&path, &gs, string(op) == "f*")
			rn.endPath(&path, &gs, &pendingClip)
		case "S":
			rn.stroke(&path, &gs)
			rn.endPath(&path, &gs, &pendingClip)
		case "s":
			path.close()
			rn.stroke(&path, &gs)
			rn.endPath(&path, &gs, &pendingClip)
		case "B", "B*":
			rn.fill(&path, &gs, string(op) == "B*")
			rn.stroke(&path, &gs)
			rn.endPath(&path, &gs, &pendingClip)
		case "b", "b*":
			path.close()
			rn.fill(&path, &gs, string(op) == "b*")
			rn.stroke(&path, &gs)
			rn.endPath(&path, &gs, &pendingClip)
		case "W":
			pendingClip = 1
		case "W*":
			pendingClip = 2

		// Line style.
		case "w":
			gs.line.width = num(0)
		case "J":
			gs.line.cap = int(num(0))
		case "j":
			gs.line.join = int(num(0))
		case "M":
			gs.line.miterLimit = num(0)
		case "d":
			if len(operands) >= 2 {
				if arr, ok := operands[0].val.(Array); ok {
					gs.line.dash = nil
					for _, e := range arr {
						if f, ok := toFloat(e); ok {
							gs.line.dash = append(gs.line.dash, f)
						}
					}
					gs.line.dashPhase = num(1)
				}
			}
		case "gs":
			if len(operands) >= 1 {
				if n, ok := operands[0].val.(Name); ok {
					rn.applyExtGState(&gs, res, n)
				}
			}

		// Colour.
		case "g":
			gs.fillSpace = nil
			gs.fill = grayRGB(num(0))
		case "G":
			gs.strokeSpace = nil
			gs.stroke = grayRGB(num(0))
		case "rg":
			gs.fillSpace = nil
			gs.fill = [3]float64{clamp01(num(0)), clamp01(num(1)), clamp01(num(2))}
		case "RG":
			gs.strokeSpace = nil
			gs.stroke = [3]float64{clamp01(num(0)), clamp01(num(1)), clamp01(num(2))}
		case "k":
			gs.fillSpace = nil
			gs.fill = cmykRGB(num(0), num(1), num(2), num(3))
		case "K":
			gs.strokeSpace = nil
			gs.stroke = cmykRGB(num(0), num(1), num(2), num(3))
		case "cs":
			gs.fillSpace = rn.lookupSpace(res, operands)
			gs.fill = gs.fillSpace.initial()
		case "CS":
			gs.strokeSpace = rn.lookupSpace(res, operands)
			gs.stroke = gs.strokeSpace.initial()
		case "sc", "scn":
			gs.fill = gs.fillSpace.toRGB(nums(), gs.fill)
		case "SC", "SCN":
			gs.stroke = gs.strokeSpace.toRGB(nums(), gs.stroke)

		// Shading, forms and images.
		case "sh":
			if len(operands) >= 1 && rn.opts.IncludeVector {
				if n, ok := operands[0].val.(Name); ok {
					rn.shade(res, n, &gs)
				}
			}
		case "Do":
			if len(operands) >= 1 {
				if n, ok := operands[0].val.(Name); ok {
					rn.doXObject(res, n, gs, depth)
				}
			}

		// Text is walked so its state does not leak, and not drawn.
		case "BT":
			inText = true
		case "ET":
			inText = false
		}
		_ = inText
		_ = start
		operands = operands[:0]
	}
}

// endPath clears the current path and applies any clip it established.
func (rn *renderer) endPath(path *rasterPath, gs *renderState, pending *int) {
	if *pending != 0 && !path.empty() {
		gs.clip = rn.intersectClip(gs.clip, path, *pending == 2)
	}
	*pending = 0
	*path = rasterPath{}
}

// intersectClip narrows a clip by a path.
func (rn *renderer) intersectClip(old *clipMask, path *rasterPath, evenOdd bool) *clipMask {
	next := &clipMask{w: rn.w, h: rn.h, a: make([]float32, rn.w*rn.h)}
	fillPath(path, rn.w, rn.h, evenOdd, func(y, x0, x1 int, cov []float64) {
		row := y * rn.w
		for x := x0; x < x1; x++ {
			c := cov[x]
			if c <= 0 {
				continue
			}
			if c > 1 {
				c = 1
			}
			next.a[row+x] = float32(c * old.at(x, y))
		}
	})
	return next
}

// fill paints the current path.
func (rn *renderer) fill(path *rasterPath, gs *renderState, evenOdd bool) {
	if !rn.opts.IncludeVector || path.empty() {
		return
	}
	rn.paint(path, evenOdd, gs.fill, gs.fillAlpha, gs.clip)
}

// stroke paints the current path's outline.
func (rn *renderer) stroke(path *rasterPath, gs *renderState) {
	if !rn.opts.IncludeVector || len(path.subs) == 0 {
		return
	}
	st := gs.line
	// A width is given in user space and drawn in device space.
	st.width = gs.line.width * matrixScale(gs.ctm)
	if len(st.dash) > 0 {
		scaled := make([]float64, len(st.dash))
		for i, d := range st.dash {
			scaled[i] = d * matrixScale(gs.ctm)
		}
		st.dash = scaled
		st.dashPhase = gs.line.dashPhase * matrixScale(gs.ctm)
	}
	outline := strokeOutline(path, st)
	rn.paint(outline, false, gs.stroke, gs.strokeAlpha, gs.clip)
}

// matrixScale is how much a transform magnifies, as one number.
func matrixScale(m matrix) float64 {
	det := math.Abs(m[0]*m[3] - m[1]*m[2])
	return math.Sqrt(det)
}

// paint composites a path's coverage in one colour.
func (rn *renderer) paint(path *rasterPath, evenOdd bool, rgb [3]float64,
	alpha float64, clip *clipMask) {
	if alpha <= 0 {
		return
	}
	cr := uint8(clamp01(rgb[0])*255 + 0.5)
	cg := uint8(clamp01(rgb[1])*255 + 0.5)
	cb := uint8(clamp01(rgb[2])*255 + 0.5)
	fillPath(path, rn.w, rn.h, evenOdd, func(y, x0, x1 int, cov []float64) {
		for x := x0; x < x1; x++ {
			a := cov[x]
			if a <= 0 {
				continue
			}
			if a > 1 {
				a = 1
			}
			a *= alpha * clip.at(x, y)
			if a <= 0 {
				continue
			}
			rn.blend(x, y, cr, cg, cb, a)
		}
	})
}

// blend puts one colour over what is already there.
func (rn *renderer) blend(x, y int, r, g, b uint8, a float64) {
	i := rn.dst.PixOffset(x, y)
	p := rn.dst.Pix
	dstA := float64(p[i+3]) / 255
	outA := a + dstA*(1-a)
	if outA <= 0 {
		p[i], p[i+1], p[i+2], p[i+3] = 0, 0, 0, 0
		return
	}
	mix := func(src, dst uint8) uint8 {
		v := (float64(src)*a + float64(dst)*dstA*(1-a)) / outA
		if v < 0 {
			v = 0
		}
		if v > 255 {
			v = 255
		}
		return uint8(v + 0.5)
	}
	p[i] = mix(r, p[i])
	p[i+1] = mix(g, p[i+1])
	p[i+2] = mix(b, p[i+2])
	p[i+3] = uint8(clamp01(outA)*255 + 0.5)
}

// applyExtGState reads the entries of a graphics state dictionary that
// change how later paint lands.
func (rn *renderer) applyExtGState(gs *renderState, res Dict, name Name) {
	states, ok := rn.r.resolve(res["ExtGState"]).(Dict)
	if !ok {
		return
	}
	d, ok := rn.r.resolve(states[name]).(Dict)
	if !ok {
		return
	}
	if v, ok := toFloat(rn.r.resolve(d["ca"])); ok {
		gs.fillAlpha = clamp01(v)
	}
	if v, ok := toFloat(rn.r.resolve(d["CA"])); ok {
		gs.strokeAlpha = clamp01(v)
	}
	if v, ok := toFloat(rn.r.resolve(d["LW"])); ok {
		gs.line.width = v
	}
}

// doXObject draws a form or, where asked, an image.
func (rn *renderer) doXObject(res Dict, name Name, gs renderState, depth int) {
	xobjects, ok := rn.r.resolve(res["XObject"]).(Dict)
	if !ok {
		return
	}
	entry := xobjects[name]
	stm, ok := rn.r.resolve(entry).(*rawStream)
	if !ok {
		return
	}
	switch rn.r.resolve(stm.dict["Subtype"]) {
	case Name("Form"):
		content, err := rn.r.decodeStream(stm.dict, stm.data)
		if err != nil {
			return
		}
		inner := gs.ctm
		if m := floatArray(rn.r, stm.dict["Matrix"]); len(m) == 6 {
			inner = matrix{m[0], m[1], m[2], m[3], m[4], m[5]}.mul(gs.ctm)
		}
		// A form's bounding box clips what it draws.
		sub := gs
		if bb := floatArray(rn.r, stm.dict["BBox"]); len(bb) == 4 {
			var box rasterPath
			corners := [][2]float64{{bb[0], bb[1]}, {bb[2], bb[1]}, {bb[2], bb[3]}, {bb[0], bb[3]}}
			for i, c := range corners {
				x, y := inner.apply(c[0], c[1])
				if i == 0 {
					box.moveTo(point{x, y})
				} else {
					box.lineTo(point{x, y})
				}
			}
			box.close()
			sub.clip = rn.intersectClip(gs.clip, &box, false)
		}
		formRes := stm.dict["Resources"]
		if formRes == nil {
			formRes = res
		}
		rn.run(content, formRes, inner, sub, depth+1)
	case Name("Image"):
		if rn.opts.IncludeRasterImages {
			rn.drawImage(stm, entry, gs)
		}
	}
}

// drawImage paints a raster image into the unit square of the transform.
func (rn *renderer) drawImage(stm *rawStream, entry any, gs renderState) {
	img := ImageRef{r: rn.r, stream: stm}
	if ref, ok := entry.(Ref); ok {
		img.ref = ref
	}
	img.Width, _ = toInt(rn.r.resolve(stm.dict["Width"]))
	img.Height, _ = toInt(rn.r.resolve(stm.dict["Height"]))
	src, err := img.Decode()
	if err != nil || img.Width <= 0 || img.Height <= 0 {
		return
	}
	inv, ok := invert(gs.ctm)
	if !ok {
		return
	}
	// The image fills the unit square, so every pixel of the box the
	// square maps to is looked up back through the transform.
	var box rasterPath
	for i, c := range [][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 1}} {
		x, y := gs.ctm.apply(c[0], c[1])
		if i == 0 {
			box.moveTo(point{x, y})
		} else {
			box.lineTo(point{x, y})
		}
	}
	box.close()
	b := src.Bounds()
	fillPath(&box, rn.w, rn.h, false, func(y, x0, x1 int, cov []float64) {
		for x := x0; x < x1; x++ {
			a := cov[x]
			if a <= 0 {
				continue
			}
			if a > 1 {
				a = 1
			}
			a *= gs.fillAlpha * gs.clip.at(x, y)
			if a <= 0 {
				continue
			}
			u, v := inv.apply(float64(x)+0.5, float64(y)+0.5)
			sx := b.Min.X + int(u*float64(b.Dx()))
			sy := b.Min.Y + int((1-v)*float64(b.Dy()))
			if sx < b.Min.X || sy < b.Min.Y || sx >= b.Max.X || sy >= b.Max.Y {
				continue
			}
			cr, cg, cb, ca := src.At(sx, sy).RGBA()
			if ca == 0 {
				continue
			}
			rn.blend(x, y, uint8(cr>>8), uint8(cg>>8), uint8(cb>>8), a*float64(ca)/65535)
		}
	})
}

// invert returns the inverse of a transform.
func invert(m matrix) (matrix, bool) {
	det := m[0]*m[3] - m[1]*m[2]
	if math.Abs(det) < 1e-12 {
		return matrix{}, false
	}
	return matrix{
		m[3] / det, -m[1] / det,
		-m[2] / det, m[0] / det,
		(m[2]*m[5] - m[3]*m[4]) / det,
		(m[1]*m[4] - m[0]*m[5]) / det,
	}, true
}

var _ = color.RGBA{}
