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
	// IncludeText draws glyphs, using the outlines the document's own
	// fonts carry. Fonts whose glyphs are addressed by name through the
	// built-in encodings are the exception and are left undrawn;
	// RenderPageDetail reports how many glyphs that came to.
	//
	// Text that clips is followed whether or not this is set, because
	// what a text clip removes is part of the artwork.
	IncludeText bool
	// IncludeRasterImages draws photographs and scans. Off by default:
	// they can be pulled out and placed separately, and leaving them out
	// keeps the layer small.
	IncludeRasterImages bool
	// IncludeVector draws paths — fills, strokes and shadings. This is
	// the point; with everything off the result is blank.
	IncludeVector bool
	// IncludeAnnotations draws the appearance streams of the page's
	// annotations: a filled form field, a signature block, a stamp, a
	// highlight, a sticky note. Much of what a reader sees is not in the
	// content stream at all, so a render of a form without this is a
	// render of an empty form.
	IncludeAnnotations bool
	// Transparent leaves untouched pixels clear instead of white.
	Transparent bool
	// SubstituteFont supplies a font program for a font the document
	// names but does not embed, and for the Type 1 programs this package
	// does not read. It is asked for TrueType or OpenType bytes and may
	// return nil, in which case that font's glyphs are left undrawn.
	//
	// A substitute provides shapes only: every advance still comes from
	// the widths in the document, so the text lands where the document
	// says. SystemFonts returns one built on the machine's own fonts.
	SubstituteFont func(FontRequest) []byte
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
func (r *Reader) RenderPage(page int, opts RenderOpts) (image.Image, error) {
	img, _, err := r.RenderPageDetail(page, opts)
	return img, err
}

// RenderReport says what a render managed.
//
// Glyphs is how many were drawn and Missing how many were asked for and
// could not be: a font whose program is not embedded, or one whose
// glyphs are addressed by name through the built-in encodings this
// package does not carry. A page that reports missing glyphs is a page
// with holes in it, and the count is the only way to tell that apart
// from a page that simply had little text.
type RenderReport struct {
	Glyphs  int
	Missing int
}

// RenderPageDetail draws a page and says what it managed, which matters
// when text is switched on: a font this package cannot read leaves holes,
// and silence about them would be the wrong answer.
func (r *Reader) RenderPageDetail(page int, opts RenderOpts) (img image.Image, rep RenderReport, err error) {
	if page < 0 || page >= len(r.pages) {
		return nil, rep, fmt.Errorf("gopdf: page %d out of range (document has %d pages)",
			page, r.NumPages())
	}
	defer func() {
		if e := recover(); e != nil {
			img, err = nil, fmt.Errorf("gopdf: page %d could not be drawn: %v", page+1, e)
		}
	}()

	pi := r.pages[page]
	size, err := r.PageSize(page)
	if err != nil {
		return nil, rep, err
	}
	scale := opts.dpi() / 72
	w := int(math.Ceil(size.W * scale))
	h := int(math.Ceil(size.H * scale))
	if w <= 0 || h <= 0 {
		return nil, rep, fmt.Errorf("gopdf: page %d has no area", page+1)
	}
	const maxSide = 20000
	if w > maxSide || h > maxSide {
		return nil, rep, fmt.Errorf("gopdf: page %d would be %dx%d pixels at %g DPI",
			page+1, w, h, opts.dpi())
	}

	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	if !opts.Transparent {
		fillWhite(dst)
	}
	if !opts.IncludeVector && !opts.IncludeRasterImages && !opts.IncludeText &&
		!opts.IncludeAnnotations {
		return dst, rep, nil
	}

	content, err := r.pageContent(pi.dict)
	if err != nil {
		return nil, rep, fmt.Errorf("gopdf: page %d: %w", page+1, err)
	}

	// PDF space has its origin at the bottom left of the media box and
	// runs upwards; the picture runs downwards from the top left.
	base := matrix{scale, 0, 0, -scale, -pi.mediaBox[0] * scale, pi.mediaBox[3] * scale}
	if pi.rotate != 0 {
		base = rotationMatrix(pi.rotate, size, scale).mul(base)
	}

	rn := &renderer{r: r, dst: dst, w: w, h: h, opts: opts, baseCTM: base,
		hidden: hiddenLayers(r)}
	rn.run(content, pi.resources, base, newRenderState(), 0)
	if opts.IncludeAnnotations {
		rn.drawAnnotations(pi.dict, base, 0)
	}
	rep = RenderReport{Glyphs: rn.text.drawn, Missing: rn.text.missing}
	return dst, rep, nil
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
	// baseClip is the clip without any soft mask, so a later /SMask
	// replaces the mask rather than compounding with the last one.
	baseClip *clipMask
	softMask *clipMask
	// fillPattern names the tiling or shading pattern a fill uses, when
	// the fill colour space is /Pattern.
	fillPattern Name
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
	r    *Reader
	dst  *image.NRGBA
	w, h int
	opts RenderOpts
	// baseCTM is the transform the page began with, which is the space a
	// pattern is placed in however deep it is used.
	baseCTM   matrix
	maskDepth int
	// hidden holds the optional-content groups the document switches
	// off, by object number.
	hidden map[int]bool
	// text carries the font cache and the pending text clip across a
	// content stream.
	text textRenderer
}

// newCanvas makes a surface for a mask group: black where a luminosity
// mask has drawn nothing, clear where an alpha mask has.
func newCanvas(w, h int, opaqueBlack bool) *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	if opaqueBlack {
		for i := 3; i < len(m.Pix); i += 4 {
			m.Pix[i] = 0xFF
		}
	}
	return m
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
	ts := newGlyphState()
	var tsStack []glyphState
	// mcDepth counts marked-content nesting; hideFrom is the depth a
	// hidden layer began at, and everything until its EMC is skipped.
	mcDepth, hideFrom := 0, 0

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
		// A layer switched off is not drawn, but its state still has to
		// be followed: what it leaves behind applies to what comes after.
		if hideFrom != 0 {
			switch string(op) {
			case "BDC", "BMC":
				mcDepth++
				operands = operands[:0]
				continue
			case "EMC":
				if hideFrom == mcDepth {
					hideFrom = 0
				}
				if mcDepth > 0 {
					mcDepth--
				}
				operands = operands[:0]
				continue
			case "f", "F", "f*", "S", "s", "B", "B*", "b", "b*", "sh", "Do",
				"Tj", "TJ", "'", "\"", "EI":
				// The painting operators are the ones to drop. A path
				// operator that also ends a path still has to end it.
				switch string(op) {
				case "f", "F", "f*", "S", "s", "B", "B*", "b", "b*":
					rn.endPath(&path, &gs, &pendingClip)
				}
				operands = operands[:0]
				continue
			}
		}
		switch string(op) {
		case "q":
			if len(stack) < 64 {
				stack = append(stack, gs)
				tsStack = append(tsStack, ts)
			}
		case "Q":
			if n := len(stack); n > 0 {
				gs = stack[n-1]
				stack = stack[:n-1]
				ts = tsStack[n-1]
				tsStack = tsStack[:n-1]
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
			rn.fillArt(&path, &gs, string(op) == "f*", res)
			rn.endPath(&path, &gs, &pendingClip)
		case "S":
			rn.strokeArt(&path, &gs)
			rn.endPath(&path, &gs, &pendingClip)
		case "s":
			path.close()
			rn.strokeArt(&path, &gs)
			rn.endPath(&path, &gs, &pendingClip)
		case "B", "B*":
			rn.fillArt(&path, &gs, string(op) == "B*", res)
			rn.strokeArt(&path, &gs)
			rn.endPath(&path, &gs, &pendingClip)
		case "b", "b*":
			path.close()
			rn.fillArt(&path, &gs, string(op) == "b*", res)
			rn.strokeArt(&path, &gs)
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
			gs.fillSpace, gs.fillPattern = nil, ""
			gs.fill = grayRGB(num(0))
		case "G":
			gs.strokeSpace = nil
			gs.stroke = grayRGB(num(0))
		case "rg":
			gs.fillSpace, gs.fillPattern = nil, ""
			gs.fill = [3]float64{clamp01(num(0)), clamp01(num(1)), clamp01(num(2))}
		case "RG":
			gs.strokeSpace = nil
			gs.stroke = [3]float64{clamp01(num(0)), clamp01(num(1)), clamp01(num(2))}
		case "k":
			gs.fillSpace, gs.fillPattern = nil, ""
			gs.fill = cmykRGB(num(0), num(1), num(2), num(3))
		case "K":
			gs.strokeSpace = nil
			gs.stroke = cmykRGB(num(0), num(1), num(2), num(3))
		case "cs":
			gs.fillSpace, gs.fillPattern = rn.lookupSpace(res, operands), ""
			gs.fill = gs.fillSpace.initial()
		case "CS":
			gs.strokeSpace = rn.lookupSpace(res, operands)
			gs.stroke = gs.strokeSpace.initial()
		case "sc", "scn":
			gs.fillPattern = ""
			if len(operands) > 0 {
				if n, ok := operands[len(operands)-1].val.(Name); ok {
					gs.fillPattern = n
				}
			}
			if gs.fillPattern == "" {
				gs.fill = gs.fillSpace.toRGB(nums(), gs.fill)
			}
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

		// --- marked content ---
		case "BDC", "BMC":
			mcDepth++
			if hideFrom == 0 && len(operands) >= 2 &&
				operands[0].val == Name("OC") && rn.ocHidden(res, operands[1].val) {
				hideFrom = mcDepth
			}
		case "EMC":
			if hideFrom == mcDepth {
				hideFrom = 0
			}
			if mcDepth > 0 {
				mcDepth--
			}

		// --- text ---
		case "BT":
			ts.tm, ts.tlm = identity(), identity()
		case "ET":
			rn.endTextObject(&gs)
		case "Tf":
			if len(operands) >= 2 {
				if n, ok := operands[0].val.(Name); ok {
					rn.setFont(res, n, num(1), &ts)
				}
			}
		case "Td":
			ts.tlm = matrix{1, 0, 0, 1, num(0), num(1)}.mul(ts.tlm)
			ts.tm = ts.tlm
		case "TD":
			ts.leading = -num(1)
			ts.tlm = matrix{1, 0, 0, 1, num(0), num(1)}.mul(ts.tlm)
			ts.tm = ts.tlm
		case "Tm":
			if len(operands) >= 6 {
				ts.tlm = matrix{num(0), num(1), num(2), num(3), num(4), num(5)}
				ts.tm = ts.tlm
			}
		case "T*":
			ts.nextLine()
		case "TL":
			ts.leading = num(0)
		case "Tc":
			ts.charSp = num(0)
		case "Tw":
			ts.wordSp = num(0)
		case "Tz":
			ts.hScale = num(0) / 100
		case "Ts":
			ts.rise = num(0)
		case "Tr":
			ts.mode = int(num(0))
		case "Tj":
			if len(operands) >= 1 {
				if str, ok := operands[0].val.(String); ok {
					rn.showText([]byte(str), &gs, &ts, res)
				}
			}
		case "'":
			ts.nextLine()
			if len(operands) >= 1 {
				if str, ok := operands[0].val.(String); ok {
					rn.showText([]byte(str), &gs, &ts, res)
				}
			}
		case "\"":
			if len(operands) >= 3 {
				ts.wordSp, ts.charSp = num(0), num(1)
				ts.nextLine()
				if str, ok := operands[2].val.(String); ok {
					rn.showText([]byte(str), &gs, &ts, res)
				}
			}
		case "TJ":
			if len(operands) >= 1 {
				if arr, ok := operands[0].val.(Array); ok {
					rn.showAdjusted(arr, &gs, &ts, res)
				}
			}
		}
		_ = start
		operands = operands[:0]
	}
}

// endPath clears the current path and applies any clip it established.
func (rn *renderer) endPath(path *rasterPath, gs *renderState, pending *int) {
	if *pending != 0 && !path.empty() {
		gs.baseClip = rn.intersectClip(gs.baseClip, path, *pending == 2)
		gs.clip = combineMasks(gs.baseClip, gs.softMask)
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
// fillArt and strokeArt are the path operators' way in, and are the one
// place the artwork switch is read. A glyph goes to fill directly,
// because it is drawn on the strength of IncludeText instead.
func (rn *renderer) fillArt(path *rasterPath, gs *renderState, evenOdd bool, res Dict) {
	if rn.opts.IncludeVector {
		rn.fill(path, gs, evenOdd, res)
	}
}

func (rn *renderer) strokeArt(path *rasterPath, gs *renderState) {
	if rn.opts.IncludeVector {
		rn.stroke(path, gs)
	}
}

// fill paints a path. Whether the caller was allowed to draw is the
// caller's business: a glyph is filled because text was asked for, and a
// rectangle because artwork was.
func (rn *renderer) fill(path *rasterPath, gs *renderState, evenOdd bool, res Dict) {
	if path.empty() {
		return
	}
	if gs.fillPattern != "" {
		if rn.fillWithPattern(path, gs, evenOdd, res, gs.fillPattern) {
			return
		}
		// Too fine to tile, or a kind not drawn: a mid grey says
		// something is there without claiming to be it.
		rn.paint(path, evenOdd, [3]float64{0.6, 0.6, 0.6}, gs.fillAlpha, gs.clip)
		return
	}
	rn.paint(path, evenOdd, gs.fill, gs.fillAlpha, gs.clip)
}

// stroke paints the current path's outline.
func (rn *renderer) stroke(path *rasterPath, gs *renderState) {
	if len(path.subs) == 0 {
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
	if sm, has := d["SMask"]; has {
		rn.applySoftMask(gs, sm)
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
	if rn.layerHidden(stm.dict["OC"]) {
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
		sub := gs
		// A transparency group is composited as a whole and the soft
		// mask in force applies to that composite, so the group cannot
		// undo it from the inside. Illustrator writes exactly that:
		// /GS0 gs sets a mask, the form it applies to sets /SMask /None
		// on its first line, and a renderer that treats the mask as
		// ordinary state throws it away and paints the artwork solid.
		// Folding the mask into the group's base clip makes it immovable
		// for everything the group draws.
		if grp, ok := rn.r.resolve(stm.dict["Group"]).(Dict); ok &&
			rn.r.resolve(grp["S"]) == Name("Transparency") && sub.softMask != nil {
			sub.baseClip = combineMasks(sub.baseClip, sub.softMask)
			sub.softMask = nil
			sub.clip = sub.baseClip
		}
		// A form's bounding box clips what it draws.
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
			sub.baseClip = rn.intersectClip(sub.baseClip, &box, false)
			sub.clip = combineMasks(sub.baseClip, sub.softMask)
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
