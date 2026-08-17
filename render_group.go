package gopdf

import "image"

// Transparency groups.
//
// A group is a set of drawing operations composited as one thing rather
// than one at a time. Most of the time that makes no difference and
// drawing straight onto the page gives the same picture. Two flags make
// it matter.
//
// An isolated group starts from nothing rather than from what is already
// on the page. Its own overlaps are resolved among themselves and only
// the result reaches the page, so a Multiply inside it multiplies with
// the group's own contents and not with the page beneath — and a group
// meant to darken only itself does not darken the whole document.
//
// A knockout group lets each operation cover the ones before it rather
// than blend with them, which is how a diagram of overlapping shapes
// keeps its shapes opaque to one another while the group as a whole is
// half transparent.
//
// Both are drawn by giving the group a canvas of its own and compositing
// the result. That costs a surface per group, so it is done only when
// one of the flags is set: an ordinary group is drawn straight onto the
// page, which is both faster and identical.

// groupKind says how a form's transparency group has to be drawn.
type groupKind struct {
	isolated bool
	knockout bool
}

// needsOwnSurface reports whether the group cannot simply be drawn onto
// the page.
func (g groupKind) needsOwnSurface() bool { return g.isolated || g.knockout }

// transparencyGroup reads a form's group dictionary.
func (rn *renderer) transparencyGroup(d Dict) (groupKind, bool) {
	grp, ok := rn.r.resolve(d["Group"]).(Dict)
	if !ok || rn.r.resolve(grp["S"]) != Name("Transparency") {
		return groupKind{}, false
	}
	var g groupKind
	g.isolated, _ = rn.r.resolve(grp["I"]).(bool)
	g.knockout, _ = rn.r.resolve(grp["K"]).(bool)
	return g, true
}

// drawGroup renders a form onto a surface of its own and composites the
// result onto the page.
//
// The group's alpha is applied once, to the composite, rather than to
// every operation inside it. That is the visible difference: two
// overlapping shapes in a half-transparent group show the page through
// the pair of them, not through each in turn with a darker seam where
// they cross.
func (rn *renderer) drawGroup(content []byte, resources any, inner matrix,
	gs renderState, depth int, kind groupKind) {

	if rn.groupDepth >= 6 {
		// A group inside a group inside a group is real; a hundred of
		// them is a file trying to make a renderer allocate for ever.
		rn.run(content, resources, inner, gs, depth+1)
		return
	}
	sub := &renderer{
		r: rn.r, w: rn.w, h: rn.h, opts: rn.opts,
		// An isolated group starts from nothing. A knockout group that
		// is not isolated starts from the page, which is what it knocks
		// out of; the copy is what lets each operation cover the last.
		dst:        rn.groupCanvas(kind),
		baseCTM:    rn.baseCTM,
		maskDepth:  rn.maskDepth,
		groupDepth: rn.groupDepth + 1,
		hidden:     rn.hidden,
		text:       textRenderer{fonts: rn.text.fonts, info: rn.text.info},
	}
	// Inside the group the alpha is spent once on the way out, so the
	// contents are drawn at full strength.
	st := gs
	st.fillAlpha, st.strokeAlpha = 1, 1
	if kind.knockout {
		// The backdrop is frozen: everything the group draws composites
		// against this copy, so the last shape covers the first instead
		// of showing through it.
		base := image.NewNRGBA(image.Rect(0, 0, rn.w, rn.h))
		copy(base.Pix, sub.dst.Pix)
		sub.knockoutBase = base
	}
	sub.run(content, resources, inner, st, depth+1)

	rn.text.drawn += sub.text.drawn
	rn.text.missing += sub.text.missing
	rn.compositeGroup(sub.dst, gs, kind)
}

// groupCanvas is the surface a group draws on.
func (rn *renderer) groupCanvas(kind groupKind) *image.NRGBA {
	if kind.isolated {
		return image.NewNRGBA(image.Rect(0, 0, rn.w, rn.h))
	}
	// A non-isolated group sees the page it sits on: the backdrop is
	// copied in so that what it blends with is what is there.
	m := image.NewNRGBA(image.Rect(0, 0, rn.w, rn.h))
	copy(m.Pix, rn.dst.Pix)
	return m
}

// compositeGroup puts a finished group onto the page.
func (rn *renderer) compositeGroup(src *image.NRGBA, gs renderState, kind groupKind) {
	alpha := gs.fillAlpha
	if alpha <= 0 {
		return
	}
	for y := 0; y < rn.h; y++ {
		for x := 0; x < rn.w; x++ {
			i := src.PixOffset(x, y)
			sa := float64(src.Pix[i+3]) / 255
			if sa <= 0 {
				continue
			}
			// A non-isolated group started from the page, so where it
			// changed nothing it must change nothing now.
			if !kind.isolated {
				j := rn.dst.PixOffset(x, y)
				if src.Pix[i] == rn.dst.Pix[j] && src.Pix[i+1] == rn.dst.Pix[j+1] &&
					src.Pix[i+2] == rn.dst.Pix[j+2] && src.Pix[i+3] == rn.dst.Pix[j+3] {
					continue
				}
			}
			a := sa * alpha * gs.clip.at(x, y)
			if a <= 0 {
				continue
			}
			rn.blended(x, y, src.Pix[i], src.Pix[i+1], src.Pix[i+2], a, gs.mode)
		}
	}
}
