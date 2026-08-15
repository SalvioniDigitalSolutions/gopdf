package gopdf

// Mesh shadings.
//
// The four kinds a gradient cannot express: triangles with a colour at
// each corner (types 4 and 5), and patches bounded by four Bézier curves
// with a colour at each corner (types 6 and 7). They are how a designer
// draws a shape that shades in two directions at once — a folded ribbon,
// a metallic sweep, the shading on a map.
//
// Painted as one flat colour, which is what happens when a renderer only
// knows the two gradient kinds, a mesh is a coloured blob where the
// artwork was. Drawing it properly needs no more than the triangle: a
// patch is subdivided into triangles and each is filled with its corners
// interpolated across it, which is what the shape is.
//
// The data is packed into a bit stream whose field widths the shading
// dictionary declares, and the coordinates and colours are integers to
// be mapped through a /Decode array. That packing is most of the work.

// meshVertex is one corner: where it is, and what colour it is there.
type meshVertex struct {
	x, y float64
	rgb  [3]float64
}

// shadeMesh paints a mesh shading, and reports whether it could.
func (rn *renderer) shadeMesh(gs *renderState, d Dict, stm *rawStream,
	kind int, space *colorSpace, fn pdfFunction) bool {

	if stm == nil {
		return false
	}
	data, err := rn.r.decodeStream(stm.dict, stm.data)
	if err != nil {
		return false
	}
	m := &meshReader{
		bits:    &sampleBits{data: data},
		coord:   intOr(rn.r, d["BitsPerCoordinate"], 0),
		comp:    intOr(rn.r, d["BitsPerComponent"], 0),
		flag:    intOr(rn.r, d["BitsPerFlag"], 0),
		decode:  floatArray(rn.r, d["Decode"]),
		space:   space,
		fn:      fn,
		nCompIn: 1,
	}
	if fn == nil {
		m.nCompIn = space.components()
	}
	if m.coord <= 0 || m.comp <= 0 || len(m.decode) < 4+2*m.nCompIn {
		return false
	}

	var tris [][3]meshVertex
	switch kind {
	case 4:
		if m.flag <= 0 {
			return false
		}
		tris = m.freeTriangles()
	case 5:
		perRow := intOr(rn.r, d["VerticesPerRow"], 0)
		if perRow < 2 {
			return false
		}
		tris = m.latticeTriangles(perRow)
	case 6, 7:
		if m.flag <= 0 {
			return false
		}
		tris = m.patches(kind)
	default:
		return false
	}
	if len(tris) == 0 {
		return false
	}
	for _, t := range tris {
		rn.fillTriangle(gs, t)
	}
	return true
}

func intOr(r *Reader, v any, def int) int {
	if n, ok := toInt(r.resolve(v)); ok {
		return n
	}
	return def
}

// meshReader pulls vertices out of the packed stream.
type meshReader struct {
	bits              *sampleBits
	coord, comp, flag int
	decode            []float64
	space             *colorSpace
	fn                pdfFunction
	nCompIn           int
	done              bool
}

// maxMeshTriangles bounds the work a single mesh can ask for. A real
// illustration reaches into the tens of thousands — the apple in the
// corpus draws 65,000 for its body alone — so the bound is set well
// above that, and is there for a malformed stream rather than a
// detailed one.
const maxMeshTriangles = 1 << 20

// value reads one packed field and maps it through its decode range.
func (m *meshReader) value(bits int, lo, hi float64) float64 {
	v, ok := m.bits.read(bits)
	if !ok {
		m.done = true
		return 0
	}
	max := float64(uint64(1)<<uint(bits) - 1)
	if bits == 32 {
		max = float64(uint64(1)<<32 - 1)
	}
	return lo + float64(v)/max*(hi-lo)
}

// vertex reads a position and a colour.
func (m *meshReader) vertex() meshVertex {
	var v meshVertex
	v.x = m.value(m.coord, m.decode[0], m.decode[1])
	v.y = m.value(m.coord, m.decode[2], m.decode[3])
	v.rgb = m.colour()
	return v
}

// colour reads a vertex's colour components and converts them to RGB.
func (m *meshReader) colour() [3]float64 {
	comps := make([]float64, m.nCompIn)
	for i := range comps {
		comps[i] = m.value(m.comp, m.decode[4+2*i], m.decode[5+2*i])
	}
	if m.fn != nil {
		comps = m.fn.eval(comps[0])
	}
	return m.space.toRGB(comps, [3]float64{0, 0, 0})
}

// align moves to the next byte, which each vertex row of a free-form
// mesh starts on.
func (m *meshReader) align() {
	if r := m.bits.pos % 8; r != 0 {
		m.bits.pos += 8 - r
	}
}

// freeTriangles reads a type 4 mesh: a run of vertices, each saying by
// its flag whether it starts a new triangle or continues the last.
func (m *meshReader) freeTriangles() [][3]meshVertex {
	var out [][3]meshVertex
	var va, vb, vc meshVertex
	have := 0
	for !m.done && len(out) < maxMeshTriangles {
		f, ok := m.bits.read(m.flag)
		if !ok {
			break
		}
		v := m.vertex()
		m.align()
		if m.done {
			break
		}
		switch {
		case f == 0 || have < 3:
			if f == 0 && have >= 3 {
				have = 0
			}
			switch have {
			case 0:
				va, have = v, 1
			case 1:
				vb, have = v, 2
			default:
				vc, have = v, 3
				out = append(out, [3]meshVertex{va, vb, vc})
			}
		case f == 1: // the last two corners and this one
			va, vb, vc = vb, vc, v
			out = append(out, [3]meshVertex{va, vb, vc})
		default: // flag 2: the first and last corners and this one
			vb, vc = vc, v
			out = append(out, [3]meshVertex{va, vb, vc})
		}
	}
	return out
}

// latticeTriangles reads a type 5 mesh: rows of vertices of a fixed
// length, each pair of rows making a strip of triangles.
func (m *meshReader) latticeTriangles(perRow int) [][3]meshVertex {
	var rows [][]meshVertex
	for !m.done && len(rows)*perRow < maxMeshTriangles {
		row := make([]meshVertex, 0, perRow)
		for i := 0; i < perRow; i++ {
			v := m.vertex()
			if m.done {
				break
			}
			row = append(row, v)
		}
		if len(row) < perRow {
			break
		}
		rows = append(rows, row)
	}
	var out [][3]meshVertex
	for r := 1; r < len(rows); r++ {
		for c := 1; c < perRow; c++ {
			a, b := rows[r-1][c-1], rows[r-1][c]
			d, e := rows[r][c-1], rows[r][c]
			out = append(out, [3]meshVertex{a, b, d}, [3]meshVertex{b, e, d})
		}
	}
	return out
}

// patches reads a type 6 or 7 mesh and cuts each patch into triangles.
//
// A patch is twelve control points around its edge — sixteen for a
// tensor patch, the four extra being interior points that only refine
// the middle — and a colour at each of its four corners. The edges are
// Bézier curves, so the patch is evaluated on a grid using the Coons
// surface the specification defines and the grid is cut into triangles.
func (m *meshReader) patches(kind int) [][3]meshVertex {
	var out [][3]meshVertex
	var prev [12]meshVertex // the previous patch's points, for shared edges
	var prevCol [4][3]float64
	first := true

	for !m.done && len(out) < maxMeshTriangles {
		f, ok := m.bits.read(m.flag)
		if !ok {
			break
		}
		nPts, nCols := 12, 4
		if kind == 7 {
			nPts = 16
		}
		if f != 0 {
			nPts -= 4 // the shared edge comes from the previous patch
			nCols = 2
		}
		if f != 0 && first {
			break // nothing to share with
		}
		pts := make([]meshVertex, 0, 16)
		for i := 0; i < nPts; i++ {
			var v meshVertex
			v.x = m.value(m.coord, m.decode[0], m.decode[1])
			v.y = m.value(m.coord, m.decode[2], m.decode[3])
			pts = append(pts, v)
		}
		cols := make([][3]float64, 0, 4)
		for i := 0; i < nCols; i++ {
			cols = append(cols, m.colour())
		}
		m.align()
		if m.done {
			break
		}

		var p [12]meshVertex
		var c [4][3]float64
		if f == 0 {
			copy(p[:], pts[:12])
			copy(c[:], cols)
		} else {
			// The shared edge depends on which side of the last patch
			// this one joins, which is what the flag says.
			edge, ec := sharedEdge(prev, prevCol, int(f))
			copy(p[:4], edge[:])
			copy(p[4:], pts[:8])
			c[0], c[1] = ec[0], ec[1]
			c[2], c[3] = cols[0], cols[1]
		}
		out = append(out, patchTriangles(p, c)...)
		prev, prevCol, first = p, c, false
	}
	return out
}

// sharedEdge returns the four control points and two colours a new patch
// takes from the last one, chosen by the flag.
func sharedEdge(p [12]meshVertex, c [4][3]float64, flag int) ([4]meshVertex, [2][3]float64) {
	switch flag {
	case 1:
		return [4]meshVertex{p[3], p[4], p[5], p[6]}, [2][3]float64{c[1], c[2]}
	case 2:
		return [4]meshVertex{p[6], p[7], p[8], p[9]}, [2][3]float64{c[2], c[3]}
	default: // 3
		return [4]meshVertex{p[9], p[10], p[11], p[0]}, [2][3]float64{c[3], c[0]}
	}
}

// patchTriangles evaluates a Coons patch on a grid and cuts it up.
func patchTriangles(p [12]meshVertex, c [4][3]float64) [][3]meshVertex {
	const n = 6 // a grid this fine is smooth at any sensible resolution
	var grid [n + 1][n + 1]meshVertex
	for i := 0; i <= n; i++ {
		u := float64(i) / n
		for j := 0; j <= n; j++ {
			v := float64(j) / n
			x, y := coonsPoint(p, u, v)
			grid[i][j] = meshVertex{x: x, y: y, rgb: bilinear(c, u, v)}
		}
	}
	out := make([][3]meshVertex, 0, n*n*2)
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			a, b := grid[i][j], grid[i+1][j]
			d, e := grid[i][j+1], grid[i+1][j+1]
			out = append(out, [3]meshVertex{a, b, d}, [3]meshVertex{b, e, d})
		}
	}
	return out
}

// coonsPoint is the Coons surface: the two pairs of opposite edges
// blended together, less the bilinear surface of the corners, which
// would otherwise be counted twice.
func coonsPoint(p [12]meshVertex, u, v float64) (float64, float64) {
	// The twelve points run anticlockwise from the first corner: the
	// edges are D1 (p0..p3), C2 (p3..p6), D2 reversed (p6..p9) and C1
	// reversed (p9..p0).
	cx := func(i int) float64 { return p[i].x }
	cy := func(i int) float64 { return p[i].y }

	bez := func(get func(int) float64, a, b, c, d int, t float64) float64 {
		s := 1 - t
		return s*s*s*get(a) + 3*s*s*t*get(b) + 3*s*t*t*get(c) + t*t*t*get(d)
	}
	surf := func(get func(int) float64) float64 {
		top := bez(get, 0, 1, 2, 3, v)    // the left edge, in v
		bottom := bez(get, 9, 8, 7, 6, v) // the right edge, in v
		left := bez(get, 0, 11, 10, 9, u) // the bottom edge, in u
		right := bez(get, 3, 4, 5, 6, u)  // the top edge, in u
		sU := (1-u)*top + u*bottom
		sV := (1-v)*left + v*right
		sB := (1-u)*(1-v)*get(0) + (1-u)*v*get(3) + u*(1-v)*get(9) + u*v*get(6)
		return sU + sV - sB
	}
	return surf(cx), surf(cy)
}

// bilinear interpolates the four corner colours across the patch.
func bilinear(c [4][3]float64, u, v float64) [3]float64 {
	var out [3]float64
	for k := range out {
		out[k] = (1-u)*(1-v)*c[0][k] + (1-u)*v*c[1][k] + u*v*c[2][k] + u*(1-v)*c[3][k]
	}
	return out
}

// fillTriangle paints one triangle with its corner colours spread across
// it, which is what makes a mesh a mesh rather than a set of flat facets.
func (rn *renderer) fillTriangle(gs *renderState, t [3]meshVertex) {
	// The corners are in shading space; the paint happens in device space.
	var dev [3]meshVertex
	for i, v := range t {
		x, y := gs.ctm.apply(v.x, v.y)
		dev[i] = meshVertex{x: x, y: y, rgb: v.rgb}
	}
	minX, minY := dev[0].x, dev[0].y
	maxX, maxY := minX, minY
	for _, v := range dev[1:] {
		minX, maxX = minF(minX, v.x), maxF(maxX, v.x)
		minY, maxY = minF(minY, v.y), maxF(maxY, v.y)
	}
	x0, y0 := int(minX)-1, int(minY)-1
	x1, y1 := int(maxX)+2, int(maxY)+2
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x1 > rn.w {
		x1 = rn.w
	}
	if y1 > rn.h {
		y1 = rn.h
	}

	// The barycentric coordinates of a pixel say both whether it is
	// inside and how much of each corner's colour it takes.
	ax, ay := dev[0].x, dev[0].y
	bx, by := dev[1].x, dev[1].y
	cx, cy := dev[2].x, dev[2].y
	den := (by-cy)*(ax-cx) + (cx-bx)*(ay-cy)
	if den == 0 {
		return // a degenerate triangle covers nothing
	}
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			l0 := ((by-cy)*(px-cx) + (cx-bx)*(py-cy)) / den
			l1 := ((cy-ay)*(px-cx) + (ax-cx)*(py-cy)) / den
			l2 := 1 - l0 - l1
			// A small tolerance keeps the seam between two triangles
			// from showing as a line of unpainted pixels.
			const eps = -0.002
			if l0 < eps || l1 < eps || l2 < eps {
				continue
			}
			a := gs.clip.at(x, y) * gs.fillAlpha
			if a <= 0 {
				continue
			}
			var col [3]float64
			for k := range col {
				col[k] = l0*dev[0].rgb[k] + l1*dev[1].rgb[k] + l2*dev[2].rgb[k]
			}
			rn.blended(x, y, uint8(clamp01(col[0])*255+0.5),
				uint8(clamp01(col[1])*255+0.5), uint8(clamp01(col[2])*255+0.5),
				a, gs.mode)
		}
	}
}
