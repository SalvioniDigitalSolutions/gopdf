package gopdf

import "math"

// Blend modes.
//
// A paint does not always replace what is under it. A highlighter drawn
// over text is set to Multiply, which darkens rather than covers, and
// drawn normally it is a yellow bar with the words hidden behind it. The
// same goes for a Screen glow, a Darken shadow, and every Overlay a
// designer used to tint a photograph.
//
// The separable modes work on one colour component at a time, so each is
// a function of two numbers and can be checked against its definition.
// The other four — Hue, Saturation, Color and Luminosity — take the
// colour as a whole, borrowing some of its qualities from the backdrop
// and the rest from the source, and need all three components together.

type blendMode int

const (
	blendNormal blendMode = iota
	blendMultiply
	blendScreen
	blendOverlay
	blendDarken
	blendLighten
	blendColorDodge
	blendColorBurn
	blendHardLight
	blendSoftLight
	blendDifference
	blendExclusion
	blendHue
	blendSaturation
	blendColor
	blendLuminosity
)

// separable reports whether a mode works a component at a time.
func (m blendMode) separable() bool { return m < blendHue }

// blendModeByName reads an /ExtGState's /BM.
//
// The entry may be an array of names in order of preference, which is
// how a document offers a fallback to a reader that does not know the
// first one. Since the separable set is complete here, the first
// recognised name wins.
func blendModeByName(r *Reader, v any) (blendMode, bool) {
	switch t := r.resolve(v).(type) {
	case Name:
		return blendByName(t)
	case Array:
		for _, e := range t {
			if n, ok := r.resolve(e).(Name); ok {
				if m, ok := blendByName(n); ok {
					return m, true
				}
			}
		}
	}
	return blendNormal, false
}

func blendByName(n Name) (blendMode, bool) {
	switch n {
	case "Normal", "Compatible":
		return blendNormal, true
	case "Multiply":
		return blendMultiply, true
	case "Screen":
		return blendScreen, true
	case "Overlay":
		return blendOverlay, true
	case "Darken":
		return blendDarken, true
	case "Lighten":
		return blendLighten, true
	case "ColorDodge":
		return blendColorDodge, true
	case "ColorBurn":
		return blendColorBurn, true
	case "HardLight":
		return blendHardLight, true
	case "SoftLight":
		return blendSoftLight, true
	case "Difference":
		return blendDifference, true
	case "Exclusion":
		return blendExclusion, true
	case "Hue":
		return blendHue, true
	case "Saturation":
		return blendSaturation, true
	case "Color":
		return blendColor, true
	case "Luminosity":
		return blendLuminosity, true
	}
	return blendNormal, false
}

// apply combines a backdrop component with a source component, both 0..1.
func (m blendMode) apply(cb, cs float64) float64 {
	switch m {
	case blendMultiply:
		return cb * cs
	case blendScreen:
		return cb + cs - cb*cs
	case blendOverlay:
		return blendHardLight.apply(cs, cb) // Overlay is HardLight reversed
	case blendDarken:
		return math.Min(cb, cs)
	case blendLighten:
		return math.Max(cb, cs)
	case blendColorDodge:
		if cb <= 0 {
			return 0
		}
		if cs >= 1 {
			return 1
		}
		return math.Min(1, cb/(1-cs))
	case blendColorBurn:
		if cb >= 1 {
			return 1
		}
		if cs <= 0 {
			return 0
		}
		return 1 - math.Min(1, (1-cb)/cs)
	case blendHardLight:
		if cs <= 0.5 {
			return cb * 2 * cs
		}
		return blendScreen.apply(cb, 2*cs-1)
	case blendSoftLight:
		if cs <= 0.5 {
			return cb - (1-2*cs)*cb*(1-cb)
		}
		var d float64
		if cb <= 0.25 {
			d = ((16*cb-12)*cb + 4) * cb
		} else {
			d = math.Sqrt(cb)
		}
		return cb + (2*cs-1)*(d-cb)
	case blendDifference:
		return math.Abs(cb - cs)
	case blendExclusion:
		return cb + cs - 2*cb*cs
	}
	return cs
}

// applyRGB combines a whole backdrop colour with a whole source colour,
// for the four modes that cannot be done a component at a time.
//
// Each takes some qualities from one side and the rest from the other:
// Luminosity keeps the backdrop's colour and the source's brightness,
// Color does the reverse, Hue takes the source's hue with the backdrop's
// strength and brightness, and Saturation takes its strength.
func (m blendMode) applyRGB(cb, cs [3]float64) [3]float64 {
	switch m {
	case blendHue:
		return setLum(setSat(cs, sat(cb)), lum(cb))
	case blendSaturation:
		return setLum(setSat(cb, sat(cs)), lum(cb))
	case blendColor:
		return setLum(cs, lum(cb))
	case blendLuminosity:
		return setLum(cb, lum(cs))
	}
	return cs
}

// lum is a colour's brightness, weighted as the specification defines it.
func lum(c [3]float64) float64 {
	return 0.3*c[0] + 0.59*c[1] + 0.11*c[2]
}

// setLum moves a colour to a given brightness, then pulls it back inside
// the cube if the move took it outside — which it can, since brightness
// and colour are not independent.
func setLum(c [3]float64, l float64) [3]float64 {
	d := l - lum(c)
	return clipColor([3]float64{c[0] + d, c[1] + d, c[2] + d})
}

func clipColor(c [3]float64) [3]float64 {
	l := lum(c)
	lo := math.Min(c[0], math.Min(c[1], c[2]))
	hi := math.Max(c[0], math.Max(c[1], c[2]))
	if lo < 0 && l != lo {
		for i := range c {
			c[i] = l + (c[i]-l)*l/(l-lo)
		}
	}
	if hi > 1 && hi != l {
		for i := range c {
			c[i] = l + (c[i]-l)*(1-l)/(hi-l)
		}
	}
	return c
}

// sat is how far apart a colour's components are, which is what the
// specification means by saturation here.
func sat(c [3]float64) float64 {
	return math.Max(c[0], math.Max(c[1], c[2])) - math.Min(c[0], math.Min(c[1], c[2]))
}

// setSat stretches a colour's components to a given spread, keeping
// their order: the middle one holds its place between the other two.
func setSat(c [3]float64, s float64) [3]float64 {
	// The three are sorted by index rather than by value so the result
	// can be put back in the right places.
	lo, mid, hi := 0, 1, 2
	if c[lo] > c[mid] {
		lo, mid = mid, lo
	}
	if c[mid] > c[hi] {
		mid, hi = hi, mid
	}
	if c[lo] > c[mid] {
		lo, mid = mid, lo
	}
	var out [3]float64
	if c[hi] > c[lo] {
		out[mid] = (c[mid] - c[lo]) * s / (c[hi] - c[lo])
		out[hi] = s
	}
	return out
}

// blended is blend with a mode applied against what is already there.
//
// The source colour is replaced by its blend with the backdrop, weighted
// by how much backdrop there is: over nothing at all a Multiply has
// nothing to multiply with and paints its own colour, which is what
// keeps a blended paint on a transparent page from disappearing.
func (rn *renderer) blended(x, y int, r, g, b uint8, a float64, m blendMode) {
	if m == blendNormal {
		rn.blend(x, y, r, g, b, a)
		return
	}
	i := rn.dst.PixOffset(x, y)
	p := rn.dst.Pix
	backA := float64(p[i+3]) / 255
	cb := [3]float64{float64(p[i]) / 255, float64(p[i+1]) / 255, float64(p[i+2]) / 255}
	cs := [3]float64{float64(r) / 255, float64(g) / 255, float64(b) / 255}

	var mixed [3]float64
	if m.separable() {
		for k := range mixed {
			mixed[k] = m.apply(cb[k], cs[k])
		}
	} else {
		mixed = m.applyRGB(cb, cs)
	}
	// Over nothing at all a blend has nothing to blend with and paints
	// its own colour, which keeps a blended paint on a transparent page
	// from disappearing.
	var out [3]uint8
	for k := range out {
		v := (1-backA)*cs[k] + backA*mixed[k]
		out[k] = uint8(clamp01(v)*255 + 0.5)
	}
	rn.blend(x, y, out[0], out[1], out[2], a)
}
