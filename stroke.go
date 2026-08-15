package gopdf

import "math"

// Turning a stroke into something that can be filled.
//
// There is one rasterizer, and it fills. A stroke is therefore converted
// into the region it covers — a quadrilateral per segment, a wedge at
// every corner, a shape at each end — and that region is filled with the
// nonzero rule.
//
// Filling the pieces together rather than computing a true outline is the
// point: the pieces overlap, and nonzero winding takes their union as
// long as each is convex and wound the same way, which is exactly what
// this builds. A real outline would have to solve the self-intersections
// that a doubling-back path creates, and would look no different.

// strokeStyle is the part of the graphics state a stroke depends on.
type strokeStyle struct {
	width      float64
	cap        int // 0 butt, 1 round, 2 square
	join       int // 0 miter, 1 round, 2 bevel
	miterLimit float64
	dash       []float64
	dashPhase  float64
}

// strokeOutline returns the region a stroke covers, ready to be filled
// with the nonzero rule.
//
// The path arrives already flattened and in device space, so the width
// has been scaled with it.
func strokeOutline(p *rasterPath, st strokeStyle) *rasterPath {
	w := st.width
	if w <= 0 {
		// A zero width means the thinnest line the device can draw.
		w = 0.8
	}
	r := w / 2
	out := &rasterPath{}
	for _, s := range applyDash(p, st) {
		strokeSubpath(out, s, r, st)
	}
	return out
}

// strokeSubpath adds the region covered by one run of points.
func strokeSubpath(out *rasterPath, s subpath, r float64, st strokeStyle) {
	pts := dedupe(s.pts)
	if len(pts) == 1 {
		// A lone point shows only under a round cap, which the
		// specification is explicit about.
		if st.cap == 1 {
			addCircle(out, pts[0], r)
		} else if st.cap == 2 {
			addQuad(out, point{pts[0].x - r, pts[0].y - r}, point{pts[0].x + r, pts[0].y - r},
				point{pts[0].x + r, pts[0].y + r}, point{pts[0].x - r, pts[0].y + r})
		}
		return
	}
	if len(pts) < 2 {
		return
	}
	if s.closed && (pts[0] != pts[len(pts)-1]) {
		pts = append(pts, pts[0])
	}

	for i := 1; i < len(pts); i++ {
		a, b := pts[i-1], pts[i]
		nx, ny, ok := normal(a, b, r)
		if !ok {
			continue
		}
		addQuad(out,
			point{a.x + nx, a.y + ny}, point{b.x + nx, b.y + ny},
			point{b.x - nx, b.y - ny}, point{a.x - nx, a.y - ny})
	}

	// A wedge at every interior corner, so the segments meet cleanly.
	for i := 1; i < len(pts)-1; i++ {
		addJoin(out, pts[i-1], pts[i], pts[i+1], r, st)
	}
	if s.closed && len(pts) > 2 {
		addJoin(out, pts[len(pts)-2], pts[0], pts[1], r, st)
	} else {
		addCap(out, pts[1], pts[0], r, st.cap)
		addCap(out, pts[len(pts)-2], pts[len(pts)-1], r, st.cap)
	}
}

// normal returns the offset vector perpendicular to a segment.
func normal(a, b point, r float64) (float64, float64, bool) {
	dx, dy := b.x-a.x, b.y-a.y
	l := math.Hypot(dx, dy)
	if l == 0 {
		return 0, 0, false
	}
	return -dy / l * r, dx / l * r, true
}

// addJoin fills the gap on the outside of a corner.
func addJoin(out *rasterPath, a, b, c point, r float64, st strokeStyle) {
	switch st.join {
	case 1:
		addCircle(out, b, r)
		return
	case 0:
		if pt, ok := miterPoint(a, b, c, r, st.miterLimit); ok {
			n1x, n1y, ok1 := normal(a, b, r)
			n2x, n2y, ok2 := normal(b, c, r)
			if ok1 && ok2 {
				// The miter is the tip beyond the two offset corners.
				side := 1.0
				if cross(a, b, c) > 0 {
					side = -1
				}
				addTriangle(out, b,
					point{b.x + side*n1x, b.y + side*n1y}, pt)
				addTriangle(out, b, pt,
					point{b.x + side*n2x, b.y + side*n2y})
				return
			}
		}
	}
	// Bevel, and the fallback when a miter runs past its limit.
	n1x, n1y, ok1 := normal(a, b, r)
	n2x, n2y, ok2 := normal(b, c, r)
	if !ok1 || !ok2 {
		return
	}
	side := 1.0
	if cross(a, b, c) > 0 {
		side = -1
	}
	addTriangle(out, b,
		point{b.x + side*n1x, b.y + side*n1y},
		point{b.x + side*n2x, b.y + side*n2y})
}

// miterPoint returns where the two offset edges of a corner meet, and
// whether the corner is sharp enough for the limit to allow it.
func miterPoint(a, b, c point, r, limit float64) (point, bool) {
	if limit <= 0 {
		limit = 10
	}
	d1x, d1y := b.x-a.x, b.y-a.y
	d2x, d2y := c.x-b.x, c.y-b.y
	l1, l2 := math.Hypot(d1x, d1y), math.Hypot(d2x, d2y)
	if l1 == 0 || l2 == 0 {
		return point{}, false
	}
	d1x, d1y = d1x/l1, d1y/l1
	d2x, d2y = d2x/l2, d2y/l2
	// The bisector of the outer angle, and how far along it the tip sits.
	bx, by := d1x-d2x, d1y-d2y
	bl := math.Hypot(bx, by)
	if bl == 0 {
		return point{}, false // straight on: no join needed
	}
	cosHalf := math.Sqrt(math.Max(0, (1+(d1x*d2x+d1y*d2y))/2))
	if cosHalf < 1e-6 {
		return point{}, false
	}
	ratio := 1 / cosHalf
	if ratio > limit {
		return point{}, false
	}
	return point{b.x + bx/bl*r*ratio, b.y + by/bl*r*ratio}, true
}

// addCap closes the end of an open stroke.
func addCap(out *rasterPath, from, at point, r float64, kind int) {
	switch kind {
	case 1:
		addCircle(out, at, r)
	case 2:
		nx, ny, ok := normal(from, at, r)
		if !ok {
			return
		}
		// Out along the direction of travel by the half width.
		dx, dy := at.x-from.x, at.y-from.y
		l := math.Hypot(dx, dy)
		ex, ey := dx/l*r, dy/l*r
		addQuad(out,
			point{at.x + nx, at.y + ny}, point{at.x + nx + ex, at.y + ny + ey},
			point{at.x - nx + ex, at.y - ny + ey}, point{at.x - nx, at.y - ny})
	}
}

// cross gives the turn direction at a corner.
func cross(a, b, c point) float64 {
	return (b.x-a.x)*(c.y-b.y) - (b.y-a.y)*(c.x-b.x)
}

// addQuad appends a quadrilateral wound anticlockwise, so that pieces
// overlapping under the nonzero rule add rather than cancel.
func addQuad(out *rasterPath, a, b, c, d point) {
	if signedArea([]point{a, b, c, d}) < 0 {
		a, b, c, d = d, c, b, a
	}
	out.subs = append(out.subs, subpath{pts: []point{a, b, c, d}, closed: true})
}

func addTriangle(out *rasterPath, a, b, c point) {
	if signedArea([]point{a, b, c}) < 0 {
		a, c = c, a
	}
	out.subs = append(out.subs, subpath{pts: []point{a, b, c}, closed: true})
}

// addCircle appends a disc, as a polygon fine enough that its edge does
// not show at the sizes a page uses.
func addCircle(out *rasterPath, at point, r float64) {
	steps := 8 + int(r*2)
	if steps > 64 {
		steps = 64
	}
	pts := make([]point, 0, steps)
	for i := 0; i < steps; i++ {
		a := 2 * math.Pi * float64(i) / float64(steps)
		pts = append(pts, point{at.x + r*math.Cos(a), at.y + r*math.Sin(a)})
	}
	if signedArea(pts) < 0 {
		for i, j := 0, len(pts)-1; i < j; i, j = i+1, j-1 {
			pts[i], pts[j] = pts[j], pts[i]
		}
	}
	out.subs = append(out.subs, subpath{pts: pts, closed: true})
}

func signedArea(pts []point) float64 {
	a := 0.0
	for i := range pts {
		j := (i + 1) % len(pts)
		a += pts[i].x*pts[j].y - pts[j].x*pts[i].y
	}
	return a / 2
}

// dedupe drops points a path repeats, which produce zero-length segments
// with no direction to offset along.
func dedupe(pts []point) []point {
	out := pts[:0:0]
	for i, p := range pts {
		if i > 0 && math.Abs(p.x-out[len(out)-1].x) < 1e-9 &&
			math.Abs(p.y-out[len(out)-1].y) < 1e-9 {
			continue
		}
		out = append(out, p)
	}
	return out
}

// applyDash cuts a path into the pieces a dash pattern leaves drawn.
func applyDash(p *rasterPath, st strokeStyle) []subpath {
	pattern := positiveDash(st.dash)
	if len(pattern) == 0 {
		return p.subs
	}
	total := 0.0
	for _, d := range pattern {
		total += d
	}
	if total <= 0 {
		return p.subs
	}

	var out []subpath
	for _, s := range p.subs {
		pts := dedupe(s.pts)
		if s.closed && len(pts) > 1 && pts[0] != pts[len(pts)-1] {
			pts = append(pts, pts[0])
		}
		if len(pts) < 2 {
			continue
		}
		idx, on, left := dashStart(pattern, st.dashPhase)
		var cur []point
		if on {
			cur = []point{pts[0]}
		}
		for i := 1; i < len(pts); i++ {
			a, b := pts[i-1], pts[i]
			segLen := math.Hypot(b.x-a.x, b.y-a.y)
			pos := 0.0
			for segLen-pos > 1e-9 {
				step := math.Min(left, segLen-pos)
				pos += step
				left -= step
				at := point{a.x + (b.x-a.x)*pos/segLen, a.y + (b.y-a.y)*pos/segLen}
				if on {
					cur = append(cur, at)
				}
				if left > 1e-9 {
					continue
				}
				// The pattern moves on.
				if on && len(cur) > 1 {
					out = append(out, subpath{pts: cur})
				}
				on = !on
				idx = (idx + 1) % len(pattern)
				left = pattern[idx]
				if on {
					cur = []point{at}
				} else {
					cur = nil
				}
			}
		}
		if on && len(cur) > 1 {
			out = append(out, subpath{pts: cur})
		}
	}
	return out
}

// positiveDash drops a pattern that would never advance.
func positiveDash(dash []float64) []float64 {
	any := false
	for _, d := range dash {
		if d < 0 {
			return nil
		}
		if d > 0 {
			any = true
		}
	}
	if !any {
		return nil
	}
	return dash
}

// dashStart works out where in the pattern the phase leaves us.
func dashStart(pattern []float64, phase float64) (idx int, on bool, left float64) {
	on = true
	idx = 0
	left = pattern[0]
	if phase <= 0 {
		return idx, on, left
	}
	guard := 0
	for phase > 0 && guard < 10000 {
		guard++
		if phase < left {
			left -= phase
			return idx, on, left
		}
		phase -= left
		idx = (idx + 1) % len(pattern)
		left = pattern[idx]
		on = !on
	}
	return idx, on, left
}
