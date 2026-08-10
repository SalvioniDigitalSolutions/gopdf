package gopdf

// Removing vector artwork and inline images.
//
// Text is the obvious thing to redact, but a diagram, a signature drawn
// as a path, or a picture written straight into the content stream is
// content too. This pass walks the drawing operators, tracks the current
// transform, and deletes any path or inline image that falls entirely
// inside a redacted area.
//
// A path that only partly overlaps is left alone and reported: trimming
// one would need a real clipper, and painting over it is what the overlay
// box already does. A path that establishes a clip is never removed —
// dropping it would change how everything after it is drawn.

// pathRedactor finds drawing operations to delete from one content
// stream.
type pathRedactor struct {
	areas []rect
	// height is the page height, for turning content-stream coordinates
	// into the top-left system the API uses.
	height float64
	// origin is the media box's lower-left corner.
	origin [2]float64

	removed []RedactionMark
	splices []splice
	partial int
}

// scan walks a content stream and records what to remove.
func (pr *pathRedactor) scan(data []byte, base matrix) {
	tokens := tokenizeContent(data)

	ctm := base
	var ctmStack []matrix

	// Path state: where the construction started, and its bounds.
	pathStart := -1
	var bounds rect
	havePath := false
	clipped := false

	var operands []contentToken
	opStart := -1

	num := func(i int) float64 {
		if i >= len(operands) {
			return 0
		}
		v, _ := toFloat(operands[i].val)
		return v
	}
	// add extends the path's bounds with a point in user space.
	add := func(x, y float64) {
		px, py := ctm.apply(x, y)
		// Content-stream coordinates run bottom-up.
		ty := pr.height - (py - pr.origin[1])
		tx := px - pr.origin[0]
		if !havePath {
			bounds = rect{tx, ty, tx, ty}
			havePath = true
			return
		}
		bounds.x0, bounds.x1 = minF(bounds.x0, tx), maxF(bounds.x1, tx)
		bounds.y0, bounds.y1 = minF(bounds.y0, ty), maxF(bounds.y1, ty)
	}
	reset := func() {
		pathStart, havePath, clipped = -1, false, false
		bounds = rect{}
	}
	begin := func(start int) {
		if pathStart < 0 {
			pathStart = start
		}
	}

	// finish handles a painting operator: the path is deleted when it is
	// wholly inside a redacted area.
	finish := func(end int) {
		defer reset()
		if !havePath || clipped || pathStart < 0 {
			return
		}
		// A zero-area path still has a stroke; give it a little body so
		// a hairline is judged on where it is, not on being empty.
		box := bounds
		if box.x1 == box.x0 {
			box.x1 = box.x0 + 0.01
		}
		if box.y1 == box.y0 {
			box.y1 = box.y0 + 0.01
		}
		for _, a := range pr.areas {
			if a.contains(box) {
				pr.splices = append(pr.splices, splice{pathStart, end, nil})
				pr.removed = append(pr.removed, RedactionMark{
					Kind: RedactPath,
					X:    box.x0, Y: box.y0,
					W: box.x1 - box.x0, H: box.y1 - box.y0,
				})
				return
			}
			if a.intersects(box) {
				pr.partial++
				return
			}
		}
	}

	for _, tok := range tokens {
		op, isOp := tok.val.(opKeyword)
		if !isOp {
			if len(operands) == 0 {
				opStart = tok.start
			}
			if len(operands) < 32 {
				operands = append(operands, tok)
			}
			continue
		}
		if len(operands) == 0 {
			opStart = tok.start
		}
		switch string(op) {
		case "q":
			if len(ctmStack) < 64 {
				ctmStack = append(ctmStack, ctm)
			}
		case "Q":
			if n := len(ctmStack); n > 0 {
				ctm = ctmStack[n-1]
				ctmStack = ctmStack[:n-1]
			}
		case "cm":
			if len(operands) >= 6 {
				var m matrix
				for i := 0; i < 6; i++ {
					m[i] = num(i)
				}
				ctm = m.mul(ctm)
			}

		case "m", "l":
			begin(opStart)
			add(num(0), num(1))
		case "c":
			begin(opStart)
			add(num(0), num(1))
			add(num(2), num(3))
			add(num(4), num(5))
		case "v", "y":
			begin(opStart)
			add(num(0), num(1))
			add(num(2), num(3))
		case "re":
			begin(opStart)
			x, y, w, h := num(0), num(1), num(2), num(3)
			add(x, y)
			add(x+w, y+h)
		case "h":
			// Closing adds no new point.

		case "W", "W*":
			clipped = true

		case "S", "s", "f", "F", "f*", "B", "B*", "b", "b*", "n":
			finish(tok.end)

		case "BI":
			// An inline image's own extent is the unit square under the
			// current transform.
			begin(opStart)
			add(0, 0)
			add(1, 1)
		case "EI":
			finish(tok.end)

		case "sh":
			// A shading paints the current clip, whose extent is not
			// tracked here; report it rather than guess.
			pr.partial++
			reset()

		case "BT":
			// Text is handled by the run scanner; keep out of its way.
			reset()
		}
		operands = operands[:0]
		opStart = -1
	}
}

// planPaths removes vector artwork and inline images inside the redacted
// areas of one page.
func (rd *Redactor) planPaths(page int, target *editTarget, areas []rect, box [4]float64) {
	if len(areas) == 0 {
		return
	}
	pr := &pathRedactor{
		areas:  areas,
		height: box[3] - box[1],
		origin: [2]float64{box[0], box[1]},
	}
	pr.scan(target.content, identityMatrix)
	for _, m := range pr.removed {
		m.Page = page
		rd.marks = append(rd.marks, m)
	}
	target.splices = append(target.splices, pr.splices...)
	rd.partialPaths += pr.partial
}

// PartialArtwork reports how many pieces of vector artwork or shading
// straddle the edge of a redacted area on the last plan. Such a piece is
// covered by the overlay box but not deleted, because trimming a path to
// a rectangle needs a clipper this package does not have. When the count
// is not zero and the artwork itself is sensitive, redact a larger area
// so it falls wholly inside.
func (rd *Redactor) PartialArtwork() (int, error) {
	if err := rd.plan(); err != nil {
		return 0, err
	}
	return rd.partialPaths, nil
}
