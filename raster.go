package gopdf

import "math"

// Turning paths into coverage.
//
// A PDF page describes its artwork as paths, and a picture of that page
// needs to know, for every pixel, how much of it a path covers. This is
// that: flatten the curves into line segments, walk the scanlines, and
// accumulate how much of each pixel falls inside.
//
// Coverage is sampled on several sub-scanlines per pixel row and measured
// exactly across each row, so an edge at any angle comes out smooth. The
// two winding rules the specification defines are decided in the same
// walk, since both are questions about the same crossings.

// subSamples is how many sub-scanlines are taken per pixel row. Four is
// the point where a diagonal edge stops looking like a staircase; more
// costs time and shows in nothing.
const subSamples = 4

// point is a position in device space, in pixels.
type point struct{ x, y float64 }

// subpath is one connected run of points, already flattened.
type subpath struct {
	pts    []point
	closed bool
}

// rasterPath is a path ready to be filled: curves gone, coordinates in
// device space.
type rasterPath struct {
	subs []subpath
}

func (p *rasterPath) moveTo(pt point) {
	p.subs = append(p.subs, subpath{pts: []point{pt}})
}

func (p *rasterPath) lineTo(pt point) {
	if len(p.subs) == 0 {
		p.moveTo(pt)
		return
	}
	s := &p.subs[len(p.subs)-1]
	s.pts = append(s.pts, pt)
}

func (p *rasterPath) close() {
	if len(p.subs) > 0 {
		p.subs[len(p.subs)-1].closed = true
	}
}

func (p *rasterPath) current() (point, bool) {
	if len(p.subs) == 0 {
		return point{}, false
	}
	s := p.subs[len(p.subs)-1]
	if len(s.pts) == 0 {
		return point{}, false
	}
	return s.pts[len(s.pts)-1], true
}

func (p *rasterPath) empty() bool {
	for _, s := range p.subs {
		if len(s.pts) > 1 {
			return false
		}
	}
	return true
}

// curveTo appends a cubic Bézier, flattened into line segments.
func (p *rasterPath) curveTo(c1, c2, to point) {
	from, ok := p.current()
	if !ok {
		p.moveTo(to)
		return
	}
	flattenCubic(from, c1, c2, to, 0, func(pt point) { p.lineTo(pt) })
}

// flattenCubic subdivides a cubic until each piece is flat enough to be a
// line, emitting the far end of every piece.
//
// Flatness is judged by how far the control points stray from the chord,
// which is the usual measure and needs no square roots in the common case
// of a curve that is already nearly straight.
func flattenCubic(p0, p1, p2, p3 point, depth int, emit func(point)) {
	const tolerance = 0.12 // pixels
	if depth >= 16 || cubicIsFlat(p0, p1, p2, p3, tolerance) {
		emit(p3)
		return
	}
	// de Casteljau at the midpoint.
	p01 := midpoint(p0, p1)
	p12 := midpoint(p1, p2)
	p23 := midpoint(p2, p3)
	p012 := midpoint(p01, p12)
	p123 := midpoint(p12, p23)
	mid := midpoint(p012, p123)
	flattenCubic(p0, p01, p012, mid, depth+1, emit)
	flattenCubic(mid, p123, p23, p3, depth+1, emit)
}

func midpoint(a, b point) point {
	return point{(a.x + b.x) / 2, (a.y + b.y) / 2}
}

// cubicIsFlat reports whether the control points sit close enough to the
// chord for a straight line to stand in for the curve.
func cubicIsFlat(p0, p1, p2, p3 point, tol float64) bool {
	dx, dy := p3.x-p0.x, p3.y-p0.y
	d1 := math.Abs((p1.x-p3.x)*dy - (p1.y-p3.y)*dx)
	d2 := math.Abs((p2.x-p3.x)*dy - (p2.y-p3.y)*dx)
	dd := d1 + d2
	return dd*dd <= tol*(dx*dx+dy*dy)
}

// bounds returns the path's extent in pixels.
func (p *rasterPath) bounds() (minX, minY, maxX, maxY float64, ok bool) {
	first := true
	for _, s := range p.subs {
		for _, pt := range s.pts {
			if first {
				minX, minY, maxX, maxY, first = pt.x, pt.y, pt.x, pt.y, false
				continue
			}
			minX, maxX = math.Min(minX, pt.x), math.Max(maxX, pt.x)
			minY, maxY = math.Min(minY, pt.y), math.Max(maxY, pt.y)
		}
	}
	return minX, minY, maxX, maxY, !first
}

// edge is one line segment of a path, prepared for scanning.
type edge struct {
	x0, y0, x1, y1 float64 // y0 < y1 always
	dir            int     // +1 if the original ran downwards, -1 if up
	dxdy           float64
}

// buildEdges turns a path into the edge list a scan needs, closing every
// subpath because filling always treats them as closed.
func buildEdges(p *rasterPath) []edge {
	var out []edge
	add := func(a, b point) {
		if a.y == b.y {
			return // horizontal edges never cross a scanline
		}
		e := edge{x0: a.x, y0: a.y, x1: b.x, y1: b.y, dir: 1}
		if a.y > b.y {
			e = edge{x0: b.x, y0: b.y, x1: a.x, y1: a.y, dir: -1}
		}
		e.dxdy = (e.x1 - e.x0) / (e.y1 - e.y0)
		out = append(out, e)
	}
	for _, s := range p.subs {
		if len(s.pts) < 2 {
			continue
		}
		for i := 1; i < len(s.pts); i++ {
			add(s.pts[i-1], s.pts[i])
		}
		add(s.pts[len(s.pts)-1], s.pts[0])
	}
	return out
}

// spanFunc receives one row of coverage, values from 0 to 1, for the
// pixels from x0 up to but not including x1 of row y.
type spanFunc func(y, x0, x1 int, cov []float64)

// fillPath computes coverage for a path and hands it out row by row.
//
// Nothing is allocated per row: one buffer is reused and cleared, so a
// page of many small fills costs no more memory than a page of one.
func fillPath(p *rasterPath, w, h int, evenOdd bool, fn spanFunc) {
	minX, minY, maxX, maxY, ok := p.bounds()
	if !ok {
		return
	}
	y0 := clampInt(int(math.Floor(minY)), 0, h)
	y1 := clampInt(int(math.Ceil(maxY))+1, 0, h)
	x0 := clampInt(int(math.Floor(minX)), 0, w)
	x1 := clampInt(int(math.Ceil(maxX))+1, 0, w)
	if y0 >= y1 || x0 >= x1 {
		return
	}
	edges := buildEdges(p)
	if len(edges) == 0 {
		return
	}

	cov := make([]float64, w)
	var xs []crossing
	weight := 1.0 / subSamples
	for y := y0; y < y1; y++ {
		touched := false
		for s := 0; s < subSamples; s++ {
			sy := float64(y) + (float64(s)+0.5)/subSamples
			xs = crossingsAt(edges, sy, xs[:0])
			if len(xs) < 2 {
				continue
			}
			if spansAt(xs, evenOdd, weight, cov, w) {
				touched = true
			}
		}
		if !touched {
			continue
		}
		fn(y, x0, x1, cov)
		for i := x0; i < x1; i++ {
			cov[i] = 0
		}
	}
}

// crossing is where one edge meets a sub-scanline.
type crossing struct {
	x   float64
	dir int
}

// crossingsAt collects and orders the edges crossing a sub-scanline.
func crossingsAt(edges []edge, y float64, out []crossing) []crossing {
	for _, e := range edges {
		if y < e.y0 || y >= e.y1 {
			continue
		}
		out = append(out, crossing{x: e.x0 + (y-e.y0)*e.dxdy, dir: e.dir})
	}
	// Insertion sort: crossings per scanline are few, and they arrive
	// nearly ordered on the sort of artwork a page holds.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].x < out[j-1].x; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// spansAt walks the crossings and adds the inside runs to the row.
func spansAt(xs []crossing, evenOdd bool, weight float64, cov []float64, w int) bool {
	touched := false
	winding := 0
	for i := 0; i+1 <= len(xs)-1; i++ {
		if evenOdd {
			winding ^= 1
		} else {
			winding += xs[i].dir
		}
		inside := winding != 0
		if evenOdd {
			inside = winding&1 != 0
		}
		if !inside {
			continue
		}
		if addSpan(cov, w, xs[i].x, xs[i+1].x, weight) {
			touched = true
		}
	}
	return touched
}

// addSpan adds coverage over [xa,xb) of one row, giving the two end
// pixels only the fraction the span actually covers.
func addSpan(cov []float64, w int, xa, xb, weight float64) bool {
	if xb <= xa {
		return false
	}
	if xa < 0 {
		xa = 0
	}
	if xb > float64(w) {
		xb = float64(w)
	}
	if xb <= xa {
		return false
	}
	ia, ib := int(xa), int(xb)
	if ia == ib {
		cov[ia] += (xb - xa) * weight
		return true
	}
	cov[ia] += (float64(ia+1) - xa) * weight
	for i := ia + 1; i < ib && i < w; i++ {
		cov[i] += weight
	}
	if ib < w {
		cov[ib] += (xb - float64(ib)) * weight
	}
	return true
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
