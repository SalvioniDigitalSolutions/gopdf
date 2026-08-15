package gopdf

import "math"

// Drawing annotations.
//
// Much of what a reader sees on a page is not in the page's content
// stream at all. A filled form field, a signature block, a stamp, a
// highlight, a sticky note — each is an annotation carrying its own
// appearance stream, and the page says only where to put it.
//
// So a render that draws the content and stops is not a picture of the
// page; it is a picture of the page with the form empty. The appearances
// are drawn the way the specification says to place them: the stream's
// bounding box is transformed by its matrix, and the box that results is
// fitted to the rectangle the annotation occupies.

// drawAnnotations paints the appearance of every annotation on a page.
func (rn *renderer) drawAnnotations(page Dict, base matrix, depth int) {
	annots, ok := rn.r.resolve(page["Annots"]).(Array)
	if !ok {
		return
	}
	for _, entry := range annots {
		a, ok := rn.r.resolve(entry).(Dict)
		if !ok {
			continue
		}
		if rn.annotationSkipped(a) {
			continue
		}
		stm := rn.appearanceStream(a)
		if stm == nil || rn.layerHidden(a["OC"]) {
			continue
		}
		rect := floatArray(rn.r, a["Rect"])
		if len(rect) != 4 {
			continue
		}
		normalizeRect(rect)
		content, err := rn.r.decodeStream(stm.dict, stm.data)
		if err != nil {
			continue
		}
		m, ok := appearanceMatrix(rn.r, stm.dict, rect)
		if !ok {
			continue
		}
		gs := newRenderState()
		gs.ctm = m.mul(base)
		// The bounding box clips the appearance, which is what keeps a
		// stamp inside its own frame.
		if bb := floatArray(rn.r, stm.dict["BBox"]); len(bb) == 4 {
			gs.baseClip = rn.intersectClip(nil, boxPath(bb, gs.ctm), false)
			gs.clip = gs.baseClip
		}
		rn.run(content, stm.dict["Resources"], gs.ctm, gs, depth+1)
	}
}

// annotationSkipped reports the annotations that are never drawn: the
// ones marked hidden, the ones marked not for the screen, and the popup
// windows that only open when a note is clicked.
func (rn *renderer) annotationSkipped(a Dict) bool {
	if rn.r.resolve(a["Subtype"]) == Name("Popup") {
		return true
	}
	if rn.r.resolve(a["Subtype"]) == Name("Link") {
		// A link's appearance is its border, which viewers do not paint.
		return true
	}
	flags, _ := toInt(rn.r.resolve(a["F"]))
	const (
		hidden = 1 << 1
		noView = 1 << 5
	)
	return flags&hidden != 0 || flags&noView != 0
}

// appearanceStream picks the appearance to draw.
//
// /AP /N is either a stream or a set of them, one per state — a checkbox
// has an /Off and an /On — and /AS says which state the annotation is
// in. A set with no /AS and exactly one entry is unambiguous enough to
// draw anyway, which is what a viewer does.
func (rn *renderer) appearanceStream(a Dict) *rawStream {
	ap, ok := rn.r.resolve(a["AP"]).(Dict)
	if !ok {
		return nil
	}
	switch n := rn.r.resolve(ap["N"]).(type) {
	case *rawStream:
		return n
	case Dict:
		if as, ok := rn.r.resolve(a["AS"]).(Name); ok {
			stm, _ := rn.r.resolve(n[as]).(*rawStream)
			return stm
		}
		if len(n) == 1 {
			for _, v := range n {
				stm, _ := rn.r.resolve(v).(*rawStream)
				return stm
			}
		}
	}
	return nil
}

// appearanceMatrix works out where an appearance stream goes.
//
// The stream draws in its own space. Its /BBox is transformed by its
// /Matrix, and the box that results — the smallest one containing the
// four corners — is scaled and shifted to fit the annotation's /Rect.
// Skipping this and drawing at the rectangle's corner puts a stamp
// designed at the origin in the wrong place and at the wrong size.
func appearanceMatrix(r *Reader, d Dict, rect []float64) (matrix, bool) {
	form := matrix{1, 0, 0, 1, 0, 0}
	if m := floatArray(r, d["Matrix"]); len(m) == 6 {
		form = matrix{m[0], m[1], m[2], m[3], m[4], m[5]}
	}
	bb := floatArray(r, d["BBox"])
	if len(bb) != 4 {
		// With no bounding box there is nothing to fit; the form's own
		// matrix, placed at the rectangle, is the best available guess.
		return matrix{1, 0, 0, 1, rect[0], rect[1]}.mulLeft(form), true
	}
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, c := range [][2]float64{{bb[0], bb[1]}, {bb[2], bb[1]}, {bb[2], bb[3]}, {bb[0], bb[3]}} {
		x, y := form.apply(c[0], c[1])
		minX, maxX = math.Min(minX, x), math.Max(maxX, x)
		minY, maxY = math.Min(minY, y), math.Max(maxY, y)
	}
	sx, sy := 1.0, 1.0
	if w := maxX - minX; w > 1e-9 {
		sx = (rect[2] - rect[0]) / w
	}
	if h := maxY - minY; h > 1e-9 {
		sy = (rect[3] - rect[1]) / h
	}
	if sx == 0 || sy == 0 {
		return matrix{}, false
	}
	fit := matrix{sx, 0, 0, sy, rect[0] - minX*sx, rect[1] - minY*sy}
	return form.mul(fit), true
}

// mulLeft is m applied after other, for the case where only the order
// reads more clearly that way round.
func (m matrix) mulLeft(other matrix) matrix { return other.mul(m) }

// normalizeRect puts a rectangle's corners in ascending order, which a
// document is not obliged to do.
func normalizeRect(r []float64) {
	if r[0] > r[2] {
		r[0], r[2] = r[2], r[0]
	}
	if r[1] > r[3] {
		r[1], r[3] = r[3], r[1]
	}
}

// boxPath is a rectangle in user space as a device-space path.
func boxPath(bb []float64, m matrix) *rasterPath {
	var p rasterPath
	for i, c := range [][2]float64{{bb[0], bb[1]}, {bb[2], bb[1]}, {bb[2], bb[3]}, {bb[0], bb[3]}} {
		x, y := m.apply(c[0], c[1])
		if i == 0 {
			p.moveTo(point{x, y})
		} else {
			p.lineTo(point{x, y})
		}
	}
	p.close()
	return &p
}
