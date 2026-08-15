package gopdf

// Soft masks and tiling patterns.
//
// Both are ways of saying "paint this, but not evenly", and ignoring
// either does not produce a slightly wrong picture — it produces a
// confidently wrong one. A watermark faded behind a page by a soft mask,
// drawn without the mask, is a solid grey slab across the text. A table
// shaded with a light hatch, drawn without the pattern, is whatever
// colour happened to be set last.
//
// So both are drawn. A soft mask is rendered to its own canvas and its
// luminosity folded into the clip, which is exactly what a clip already
// is: a coverage per pixel. A tiling pattern is its cell run again and
// again across the area it fills, clipped to it.

// applySoftMask reads an /SMask from a graphics state dictionary and
// folds it into the clip.
//
// The mask is a form drawn on its own, and how much of it shows through
// is either how bright it came out or how opaque it was. Either way the
// answer is one number per pixel, which is what a clip holds.
func (rn *renderer) applySoftMask(gs *renderState, v any) {
	switch t := rn.r.resolve(v).(type) {
	case Name:
		if t == "None" {
			gs.softMask = nil
			gs.clip = gs.baseClip
		}
		return
	case Dict:
		group, ok := rn.r.resolve(t["G"]).(*rawStream)
		if !ok {
			return
		}
		luminosity := rn.r.resolve(t["S"]) != Name("Alpha")
		mask := rn.renderMaskGroup(group, gs, luminosity)
		if mask == nil {
			return
		}
		gs.softMask = mask
		gs.clip = combineMasks(gs.baseClip, mask)
	}
}

// renderMaskGroup draws the mask's form to a canvas of its own and
// returns what it says about every pixel.
func (rn *renderer) renderMaskGroup(group *rawStream, gs *renderState,
	luminosity bool) *clipMask {
	if rn.maskDepth >= 4 {
		return nil // a mask that masks itself is not worth chasing
	}
	content, err := rn.r.decodeStream(group.dict, group.data)
	if err != nil {
		return nil
	}
	// A luminosity mask starts black, since anything the group does not
	// paint contributes nothing; an alpha mask starts clear.
	sub := &renderer{
		r: rn.r, w: rn.w, h: rn.h, opts: rn.opts,
		dst:       newCanvas(rn.w, rn.h, luminosity),
		maskDepth: rn.maskDepth + 1,
	}
	inner := gs.ctm
	if m := floatArray(rn.r, group.dict["Matrix"]); len(m) == 6 {
		inner = matrix{m[0], m[1], m[2], m[3], m[4], m[5]}.mul(gs.ctm)
	}
	st := newRenderState()
	st.ctm = inner
	sub.run(content, group.dict["Resources"], inner, st, rn.maskDepth+1)

	out := &clipMask{w: rn.w, h: rn.h, a: make([]float32, rn.w*rn.h)}
	pix := sub.dst.Pix
	for i, j := 0, 0; j < len(out.a); i, j = i+4, j+1 {
		if luminosity {
			// Rec. 601 luminance, which is what a viewer uses.
			lum := (0.299*float64(pix[i]) + 0.587*float64(pix[i+1]) +
				0.114*float64(pix[i+2])) / 255
			// An unpainted pixel of a luminosity mask is black, and black
			// hides; the alpha channel says which those were.
			out.a[j] = float32(lum * float64(pix[i+3]) / 255)
		} else {
			out.a[j] = float32(float64(pix[i+3]) / 255)
		}
	}
	return out
}

// combineMasks multiplies a clip by a mask, either of which may be
// absent.
func combineMasks(clip, mask *clipMask) *clipMask {
	if mask == nil {
		return clip
	}
	if clip == nil {
		return mask
	}
	out := &clipMask{w: clip.w, h: clip.h, a: make([]float32, len(clip.a))}
	for i := range out.a {
		out.a[i] = clip.a[i] * mask.a[i]
	}
	return out
}

// fillWithPattern paints a path with a tiling pattern: the pattern's cell
// run again and again over the area, clipped to it.
//
// The alternative is to paint the area in whatever colour was set last,
// which for a light hatch over white can be solid black.
func (rn *renderer) fillWithPattern(path *rasterPath, gs *renderState,
	evenOdd bool, res Dict, name Name) bool {
	patterns, ok := rn.r.resolve(res["Pattern"]).(Dict)
	if !ok {
		return false
	}
	stm, ok := rn.r.resolve(patterns[name]).(*rawStream)
	if !ok {
		// A shading pattern has no stream: paint the shading through the
		// path instead.
		if d, ok := rn.r.resolve(patterns[name]).(Dict); ok {
			return rn.fillWithShadingPattern(path, gs, evenOdd, d)
		}
		return false
	}
	if kind, _ := toInt(rn.r.resolve(stm.dict["PatternType"])); kind != 1 {
		return false
	}
	content, err := rn.r.decodeStream(stm.dict, stm.data)
	if err != nil {
		return false
	}
	xstep, _ := toFloat(rn.r.resolve(stm.dict["XStep"]))
	ystep, _ := toFloat(rn.r.resolve(stm.dict["YStep"]))
	bbox := floatArray(rn.r, stm.dict["BBox"])
	if len(bbox) != 4 {
		return false
	}
	if xstep == 0 {
		xstep = bbox[2] - bbox[0]
	}
	if ystep == 0 {
		ystep = bbox[3] - bbox[1]
	}
	if xstep == 0 || ystep == 0 {
		return false
	}

	// A pattern is placed in the space the page was in when the content
	// stream began, not the space in force where it is used.
	base := rn.baseCTM
	if m := floatArray(rn.r, stm.dict["Matrix"]); len(m) == 6 {
		base = matrix{m[0], m[1], m[2], m[3], m[4], m[5]}.mul(base)
	}
	inv, ok := invert(base)
	if !ok {
		return false
	}

	// Only the cells that fall inside the path are worth drawing.
	minX, minY, maxX, maxY, ok := path.bounds()
	if !ok {
		return false
	}
	lo, hi := patternRange(inv, minX, minY, maxX, maxY, xstep, ystep)
	const maxCells = 4096
	if (hi[0]-lo[0]+1)*(hi[1]-lo[1]+1) > maxCells {
		return false // too fine to tile pixel by pixel; the caller falls back
	}

	clip := combineMasks(gs.clip, rn.pathMask(path, evenOdd))
	sub := newRenderState()
	sub.clip = clip
	sub.baseClip = clip
	sub.fillAlpha, sub.strokeAlpha = gs.fillAlpha, gs.strokeAlpha
	for iy := lo[1]; iy <= hi[1]; iy++ {
		for ix := lo[0]; ix <= hi[0]; ix++ {
			cell := matrix{1, 0, 0, 1, float64(ix) * xstep, float64(iy) * ystep}.mul(base)
			st := sub
			st.ctm = cell
			rn.run(content, stm.dict["Resources"], cell, st, rn.maskDepth+1)
		}
	}
	return true
}

// fillWithShadingPattern paints a shading through a path.
func (rn *renderer) fillWithShadingPattern(path *rasterPath, gs *renderState,
	evenOdd bool, d Dict) bool {
	sh := rn.r.resolve(d["Shading"])
	if sh == nil {
		return false
	}
	clip := combineMasks(gs.clip, rn.pathMask(path, evenOdd))
	st := *gs
	st.clip = clip
	if m := floatArray(rn.r, d["Matrix"]); len(m) == 6 {
		st.ctm = matrix{m[0], m[1], m[2], m[3], m[4], m[5]}.mul(rn.baseCTM)
	}
	rn.shadeDict(sh, &st)
	return true
}

// pathMask turns a path into the coverage it covers, for use as a clip.
func (rn *renderer) pathMask(path *rasterPath, evenOdd bool) *clipMask {
	m := &clipMask{w: rn.w, h: rn.h, a: make([]float32, rn.w*rn.h)}
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
			m.a[row+x] = float32(c)
		}
	})
	return m
}

// patternRange returns which cells of a pattern can reach a box on the
// page, in the pattern's own space.
func patternRange(inv matrix, minX, minY, maxX, maxY, xstep, ystep float64) (lo, hi [2]int) {
	first := true
	var x0, y0, x1, y1 float64
	for _, c := range [][2]float64{{minX, minY}, {maxX, minY}, {minX, maxY}, {maxX, maxY}} {
		ux, uy := inv.apply(c[0], c[1])
		if first {
			x0, y0, x1, y1, first = ux, uy, ux, uy, false
			continue
		}
		x0, x1 = minF(x0, ux), maxF(x1, ux)
		y0, y1 = minF(y0, uy), maxF(y1, uy)
	}
	return [2]int{int(x0/xstep) - 1, int(y0/ystep) - 1},
		[2]int{int(x1/xstep) + 1, int(y1/ystep) + 1}
}
