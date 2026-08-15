package gopdf

import "math"

// Colour, and the gradient that has to be asked for it.
//
// A page names its colours in whatever space it likes and the picture
// needs red, green and blue. The three device spaces convert directly.
// The rest — an ICC profile, a separation, an indexed palette — are
// converted by the number of components they carry, which is what their
// alternate space would have done for the common cases and is close
// enough for artwork that is nearly always already grey or RGB.

// colorSpace is how many components a colour has and how to read them.
type colorSpace struct {
	n int
	// indexed holds a palette, when the space is one.
	indexed []byte
	// base is the palette's own space.
	base *colorSpace
	// separation converts a tint through a function into its alternate.
	tint pdfFunction
	alt  *colorSpace
}

// lookupSpace resolves a colour space named by cs or CS.
func (rn *renderer) lookupSpace(res Dict, operands []contentToken) *colorSpace {
	if len(operands) == 0 {
		return nil
	}
	name, ok := operands[0].val.(Name)
	if !ok {
		return nil
	}
	switch name {
	case "DeviceGray", "G", "CalGray":
		return &colorSpace{n: 1}
	case "DeviceRGB", "RGB", "CalRGB", "Lab":
		return &colorSpace{n: 3}
	case "DeviceCMYK", "CMYK":
		return &colorSpace{n: 4}
	case "Pattern":
		return &colorSpace{n: 1}
	}
	spaces, ok := rn.r.resolve(res["ColorSpace"]).(Dict)
	if !ok {
		return nil
	}
	return rn.readSpace(spaces[name], 0)
}

// readSpace works out a colour space from its object.
func (rn *renderer) readSpace(v any, depth int) *colorSpace {
	if depth > 8 {
		return nil
	}
	switch t := rn.r.resolve(v).(type) {
	case Name:
		switch t {
		case "DeviceGray", "G", "CalGray":
			return &colorSpace{n: 1}
		case "DeviceRGB", "RGB", "CalRGB", "Lab":
			return &colorSpace{n: 3}
		case "DeviceCMYK", "CMYK":
			return &colorSpace{n: 4}
		}
	case Array:
		if len(t) == 0 {
			return nil
		}
		family, _ := rn.r.resolve(t[0]).(Name)
		switch family {
		case "ICCBased":
			if len(t) >= 2 {
				if stm, ok := rn.r.resolve(t[1]).(*rawStream); ok {
					n, _ := toInt(rn.r.resolve(stm.dict["N"]))
					if n == 1 || n == 3 || n == 4 {
						return &colorSpace{n: n}
					}
				}
			}
			return &colorSpace{n: 3}
		case "CalRGB", "Lab":
			return &colorSpace{n: 3}
		case "CalGray":
			return &colorSpace{n: 1}
		case "Indexed", "I":
			if len(t) < 4 {
				return nil
			}
			base := rn.readSpace(t[1], depth+1)
			if base == nil {
				return nil
			}
			var table []byte
			switch lookup := rn.r.resolve(t[3]).(type) {
			case String:
				table = []byte(lookup)
			case *rawStream:
				if data, err := rn.r.decodeStream(lookup.dict, lookup.data); err == nil {
					table = data
				}
			}
			return &colorSpace{n: 1, indexed: table, base: base}
		case "Separation", "DeviceN":
			// The tint runs through a function into an alternate space.
			at := 2
			n := 1
			if family == "DeviceN" {
				if names, ok := rn.r.resolve(t[1]).(Array); ok {
					n = len(names)
				}
			}
			if len(t) <= at {
				return &colorSpace{n: n}
			}
			cs := &colorSpace{n: n, alt: rn.readSpace(t[at], depth+1)}
			if len(t) > at+1 {
				cs.tint = loadFunction(rn.r, t[at+1])
			}
			return cs
		case "Pattern":
			return &colorSpace{n: 1}
		}
	}
	return nil
}

// initial is the colour a space starts at, which is black everywhere.
func (c *colorSpace) initial() [3]float64 {
	return [3]float64{0, 0, 0}
}

// toRGB converts components in this space. Unknown shapes fall back to
// the previous colour rather than to an arbitrary one, since a wrong
// colour is more visible than an unchanged one.
func (c *colorSpace) toRGB(v []float64, prev [3]float64) [3]float64 {
	if len(v) == 0 {
		return prev
	}
	if c == nil {
		return componentsToRGB(v, prev)
	}
	if c.indexed != nil && c.base != nil {
		i := int(v[len(v)-1])
		n := c.base.components()
		if i < 0 || (i+1)*n > len(c.indexed) {
			return prev
		}
		comps := make([]float64, n)
		for k := 0; k < n; k++ {
			comps[k] = float64(c.indexed[i*n+k]) / 255
		}
		return c.base.toRGB(comps, prev)
	}
	if c.tint != nil && c.alt != nil {
		return c.alt.toRGB(c.tint.eval(v[0]), prev)
	}
	if c.tint == nil && c.alt != nil && len(v) == 1 {
		// A separation with no usable function: the tint is how dark it is.
		return grayRGB(1 - clamp01(v[0]))
	}
	return componentsToRGB(v, prev)
}

func (c *colorSpace) components() int {
	if c == nil {
		return 3
	}
	if c.indexed != nil {
		return 1
	}
	if c.n <= 0 {
		return 3
	}
	return c.n
}

// componentsToRGB reads a colour by how many numbers it has, which is how
// the device spaces differ and is the best guess for anything else.
func componentsToRGB(v []float64, prev [3]float64) [3]float64 {
	switch len(v) {
	case 1:
		return grayRGB(v[0])
	case 3:
		return [3]float64{clamp01(v[0]), clamp01(v[1]), clamp01(v[2])}
	case 4:
		return cmykRGB(v[0], v[1], v[2], v[3])
	}
	return prev
}

func grayRGB(g float64) [3]float64 {
	g = clamp01(g)
	return [3]float64{g, g, g}
}

// cmykRGB converts without a profile, which is what a viewer without one
// does too.
func cmykRGB(c, m, y, k float64) [3]float64 {
	c, m, y, k = clamp01(c), clamp01(m), clamp01(y), clamp01(k)
	return [3]float64{
		clamp01((1 - c) * (1 - k)),
		clamp01((1 - m) * (1 - k)),
		clamp01((1 - y) * (1 - k)),
	}
}

// shade paints a shading over the clip, which is what sh means.
func (rn *renderer) shade(res Dict, name Name, gs *renderState) {
	shadings, ok := rn.r.resolve(res["Shading"]).(Dict)
	if !ok {
		return
	}
	var d Dict
	switch t := rn.r.resolve(shadings[name]).(type) {
	case Dict:
		d = t
	case *rawStream:
		d = t.dict
	default:
		return
	}
	kind, _ := toInt(rn.r.resolve(d["ShadingType"]))
	space := rn.readSpace(d["ColorSpace"], 0)
	fn := loadFunction(rn.r, d["Function"])

	if kind != 2 || fn == nil {
		// Anything else is painted as the one colour nearest to it: the
		// middle of its own ramp where there is one, mid grey otherwise.
		rn.shadeSolid(gs, space, fn)
		return
	}
	coords := floatArray(rn.r, d["Coords"])
	if len(coords) < 4 {
		rn.shadeSolid(gs, space, fn)
		return
	}
	ext := [2]bool{false, false}
	if e, ok := rn.r.resolve(d["Extend"]).(Array); ok && len(e) == 2 {
		ext[0], _ = rn.r.resolve(e[0]).(bool)
		ext[1], _ = rn.r.resolve(e[1]).(bool)
	}
	rn.shadeAxial(gs, space, fn, coords, ext)
}

// shadeAxial paints a linear gradient across the clipped area.
func (rn *renderer) shadeAxial(gs *renderState, space *colorSpace, fn pdfFunction,
	coords []float64, ext [2]bool) {
	inv, ok := invert(gs.ctm)
	if !ok {
		return
	}
	x0, y0, x1, y1 := coords[0], coords[1], coords[2], coords[3]
	dx, dy := x1-x0, y1-y0
	den := dx*dx + dy*dy
	if den == 0 {
		rn.shadeSolid(gs, space, fn)
		return
	}
	// The colours are sampled once into a ramp, since a function call per
	// pixel is the difference between quick and unusable.
	const rampSize = 256
	var ramp [rampSize][3]float64
	for i := 0; i < rampSize; i++ {
		ramp[i] = space.toRGB(fn.eval(float64(i)/(rampSize-1)), [3]float64{0, 0, 0})
	}
	for y := 0; y < rn.h; y++ {
		for x := 0; x < rn.w; x++ {
			a := gs.clip.at(x, y) * gs.fillAlpha
			if a <= 0 {
				continue
			}
			ux, uy := inv.apply(float64(x)+0.5, float64(y)+0.5)
			t := ((ux-x0)*dx + (uy-y0)*dy) / den
			if t < 0 {
				if !ext[0] {
					continue
				}
				t = 0
			}
			if t > 1 {
				if !ext[1] {
					continue
				}
				t = 1
			}
			c := ramp[int(t*(rampSize-1)+0.5)]
			rn.blend(x, y, uint8(clamp01(c[0])*255+0.5), uint8(clamp01(c[1])*255+0.5),
				uint8(clamp01(c[2])*255+0.5), a)
		}
	}
}

// shadeSolid paints the clipped area in one colour, for the shading kinds
// this does not draw. Something of about the right colour in about the
// right place carries a page better than a hole in it.
func (rn *renderer) shadeSolid(gs *renderState, space *colorSpace, fn pdfFunction) {
	if gs.clip == nil {
		return // an unbounded solid would cover the page
	}
	rgb := [3]float64{0.5, 0.5, 0.5}
	if fn != nil {
		rgb = space.toRGB(fn.eval(0.5), rgb)
	}
	r8 := uint8(clamp01(rgb[0])*255 + 0.5)
	g8 := uint8(clamp01(rgb[1])*255 + 0.5)
	b8 := uint8(clamp01(rgb[2])*255 + 0.5)
	for y := 0; y < rn.h; y++ {
		for x := 0; x < rn.w; x++ {
			if a := gs.clip.at(x, y) * gs.fillAlpha; a > 0 {
				rn.blend(x, y, r8, g8, b8, a)
			}
		}
	}
}

var _ = math.Abs
