package gopdf

// TrueType glyph outlines.
//
// The parser already reads loca and glyf to embed and subset fonts; this
// turns those bytes into contours. A TrueType contour is a run of points,
// each on or off the curve, describing quadratic segments — and where two
// off-curve points sit next to each other the on-curve point between them
// is left out, to be worked out as their midpoint. That implied point is
// the whole trick of the format and the one thing an outline reader has
// to get right.
//
// Coordinates come out in font units. The caller scales by the units per
// em, which is 1000 for most fonts and 2048 for most of the rest.

// glyphOutline is one glyph's contours in font units.
type glyphOutline struct {
	contours []glyphContour
}

// glyphContour is one closed run of points.
type glyphContour struct {
	pts []glyphPoint
}

type glyphPoint struct {
	x, y float64
	on   bool
}

const maxCompositeDepth = 5

// outline returns a glyph's contours in font units.
func (f *ttfFont) outline(gid uint16) *glyphOutline {
	return f.outlineAt(gid, 0)
}

func (f *ttfFont) outlineAt(gid uint16, depth int) *glyphOutline {
	if depth > maxCompositeDepth {
		return nil
	}
	data := f.glyphData(gid)
	if len(data) < 10 {
		return nil // an empty glyph, such as a space
	}
	if int16(be16(data, 0)) < 0 {
		return f.compositeOutline(data, depth)
	}
	return f.simpleOutline(data)
}

// simpleOutline reads a glyph described by its own points.
func (f *ttfFont) simpleOutline(data []byte) *glyphOutline {
	n := int(be16(data, 0))
	off := 10
	if off+2*n+2 > len(data) {
		return nil
	}
	ends := make([]int, n)
	for i := 0; i < n; i++ {
		ends[i] = int(be16(data, off))
		off += 2
	}
	total := 0
	if n > 0 {
		total = ends[n-1] + 1
	}
	if total <= 0 || total > 10000 {
		return nil
	}
	// Instructions are skipped: hinting changes where a pixel edge lands,
	// not what the shape is.
	insLen := int(be16(data, off))
	off += 2 + insLen
	if off > len(data) {
		return nil
	}

	flags := make([]byte, 0, total)
	for len(flags) < total {
		if off >= len(data) {
			return nil
		}
		fl := data[off]
		off++
		flags = append(flags, fl)
		if fl&0x08 != 0 { // REPEAT
			if off >= len(data) {
				return nil
			}
			r := int(data[off])
			off++
			for i := 0; i < r && len(flags) < total; i++ {
				flags = append(flags, fl)
			}
		}
	}

	read := func(shortBit, sameBit byte) ([]float64, bool) {
		out := make([]float64, total)
		v := 0.0
		for i, fl := range flags {
			switch {
			case fl&shortBit != 0:
				if off >= len(data) {
					return nil, false
				}
				d := float64(data[off])
				off++
				if fl&sameBit == 0 {
					d = -d
				}
				v += d
			case fl&sameBit == 0:
				if off+2 > len(data) {
					return nil, false
				}
				v += float64(int16(be16(data, off)))
				off += 2
			}
			out[i] = v
		}
		return out, true
	}
	xs, ok := read(0x02, 0x10)
	if !ok {
		return nil
	}
	ys, ok := read(0x04, 0x20)
	if !ok {
		return nil
	}

	out := &glyphOutline{}
	start := 0
	for _, end := range ends {
		if end < start || end >= total {
			break
		}
		c := glyphContour{}
		for i := start; i <= end; i++ {
			c.pts = append(c.pts, glyphPoint{x: xs[i], y: ys[i], on: flags[i]&0x01 != 0})
		}
		if len(c.pts) > 0 {
			out.contours = append(out.contours, c)
		}
		start = end + 1
	}
	return out
}

// compositeOutline assembles a glyph built from others, such as an
// accented letter made of a letter and an accent.
func (f *ttfFont) compositeOutline(data []byte, depth int) *glyphOutline {
	out := &glyphOutline{}
	for off := 10; off+4 <= len(data); {
		flags := be16(data, off)
		component := be16(data, off+2)
		off += 4

		var dx, dy float64
		if flags&0x0001 != 0 { // ARG_1_AND_2_ARE_WORDS
			if off+4 > len(data) {
				return out
			}
			dx, dy = float64(int16(be16(data, off))), float64(int16(be16(data, off+2)))
			off += 4
		} else {
			if off+2 > len(data) {
				return out
			}
			dx, dy = float64(int8(data[off])), float64(int8(data[off+1]))
			off += 2
		}
		// Arguments that are point numbers rather than an offset are
		// rare and are treated as no offset at all, which is closer than
		// treating a point index as a distance.
		if flags&0x0002 == 0 {
			dx, dy = 0, 0
		}

		a, b, c, d := 1.0, 0.0, 0.0, 1.0
		f2dot14 := func(o int) float64 { return float64(int16(be16(data, o))) / 16384 }
		switch {
		case flags&0x0008 != 0: // WE_HAVE_A_SCALE
			if off+2 > len(data) {
				return out
			}
			a = f2dot14(off)
			d = a
			off += 2
		case flags&0x0040 != 0: // X_AND_Y_SCALE
			if off+4 > len(data) {
				return out
			}
			a, d = f2dot14(off), f2dot14(off+2)
			off += 4
		case flags&0x0080 != 0: // TWO_BY_TWO
			if off+8 > len(data) {
				return out
			}
			a, b, c, d = f2dot14(off), f2dot14(off+2), f2dot14(off+4), f2dot14(off+6)
			off += 8
		}

		if sub := f.outlineAt(component, depth+1); sub != nil {
			for _, ct := range sub.contours {
				moved := glyphContour{pts: make([]glyphPoint, len(ct.pts))}
				for i, pt := range ct.pts {
					moved.pts[i] = glyphPoint{
						x:  a*pt.x + c*pt.y + dx,
						y:  b*pt.x + d*pt.y + dy,
						on: pt.on,
					}
				}
				out.contours = append(out.contours, moved)
			}
		}
		if flags&0x0020 == 0 { // no MORE_COMPONENTS
			break
		}
	}
	return out
}

// appendTo flattens an outline into a path, mapping font units through m.
//
// The quadratic segments become cubics, which the path already knows how
// to flatten, and the implied on-curve point between two off-curve points
// is put back where the format left it out.
func (g *glyphOutline) appendTo(p *rasterPath, m matrix) {
	if g == nil {
		return
	}
	at := func(pt glyphPoint) point {
		x, y := m.apply(pt.x, pt.y)
		return point{x, y}
	}
	for _, c := range g.contours {
		pts := c.pts
		if len(pts) == 0 {
			continue
		}
		// The contour has to begin on the curve. If it does not, an
		// on-curve point is borrowed from the end, or invented halfway
		// between the two off-curve neighbours.
		startIdx, start := 0, glyphPoint{}
		switch {
		case pts[0].on:
			start, startIdx = pts[0], 1
		case pts[len(pts)-1].on:
			start, startIdx = pts[len(pts)-1], 0
		default:
			start = glyphPoint{
				x:  (pts[0].x + pts[len(pts)-1].x) / 2,
				y:  (pts[0].y + pts[len(pts)-1].y) / 2,
				on: true,
			}
			startIdx = 0
		}
		p.moveTo(at(start))
		cur := start

		var ctrl *glyphPoint
		emit := func(to glyphPoint) {
			if ctrl == nil {
				p.lineTo(at(to))
			} else {
				p.curveTo(quadAsCubic(at(cur), at(*ctrl), at(to)))
				ctrl = nil
			}
			cur = to
		}
		for i := 0; i < len(pts); i++ {
			pt := pts[(startIdx+i)%len(pts)]
			if pt.on {
				emit(pt)
				continue
			}
			if ctrl != nil {
				// Two controls in a row: the point between them was left
				// out of the file and is their midpoint.
				mid := glyphPoint{x: (ctrl.x + pt.x) / 2, y: (ctrl.y + pt.y) / 2, on: true}
				emit(mid)
			}
			c := pt
			ctrl = &c
		}
		emit(start)
		p.close()
	}
}

// quadAsCubic raises a quadratic segment to the cubic the path draws.
func quadAsCubic(from, ctrl, to point) (point, point, point) {
	c1 := point{from.x + 2.0/3.0*(ctrl.x-from.x), from.y + 2.0/3.0*(ctrl.y-from.y)}
	c2 := point{to.x + 2.0/3.0*(ctrl.x-to.x), to.y + 2.0/3.0*(ctrl.y-to.y)}
	return c1, c2, to
}
