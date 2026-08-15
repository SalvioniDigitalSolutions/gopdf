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
// The separable modes are all here: each works on one colour component
// at a time, so the mode is a function of two numbers. The non-separable
// four — Hue, Saturation, Color and Luminosity — work on the colour as a
// whole and are treated as Normal, which is what they degrade to.

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
)

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
	case "Hue", "Saturation", "Color", "Luminosity":
		// Not separable: these need all three components at once and a
		// colour model to take luminosity from. Normal is what they fall
		// back to, and it is closer than any of the separable modes.
		return blendNormal, true
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
	mix := func(cs float64, back uint8) uint8 {
		cb := float64(back) / 255
		v := (1-backA)*cs + backA*m.apply(cb, cs)
		return uint8(clamp01(v)*255 + 0.5)
	}
	rn.blend(x, y, mix(float64(r)/255, p[i]), mix(float64(g)/255, p[i+1]),
		mix(float64(b)/255, p[i+2]), a)
}
