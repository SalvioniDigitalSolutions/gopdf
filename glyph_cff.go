package gopdf

import "strconv"

// CFF glyph outlines.
//
// A CFF glyph is a small stack program — a Type 2 charstring — that draws
// its own shape. The package already walks these programs to work out
// which subroutines a subset has to keep; this runs them properly and
// records where the pen went.
//
// Only the drawing operators matter here. Hints are read and discarded,
// because hinting decides which side of a pixel boundary a stem lands on
// and has nothing to say about the shape. The two operators that are not
// merely decorative are hintmask, whose operand bytes have to be skipped
// by counting the stems declared so far, and the four seac-style endchar
// forms that build an accented letter out of two others.

// cffOutlines draws glyphs from a CFF font program.
type cffOutlines struct {
	charstrings [][]byte
	gsubrs      [][]byte
	lsubrs      [][]byte // the font's own local subroutines
	nominalW    float64
	defaultW    float64

	// A CID-keyed font has one private dictionary per group of glyphs,
	// so which local subroutines apply depends on the glyph.
	fdSelect []uint8    // glyph -> font dict index
	fdSubrs  [][][]byte // font dict index -> local subroutines

	// fontMatrix maps charstring units to text space. It is almost
	// always 1/1000, but a font is free to say otherwise and some do.
	fontMatrix matrix

	// charsetCIDs maps a glyph to the CID it stands for in a CID-keyed
	// font, which is how a Type 0 font addresses it.
	charsetCIDs []uint16
}

// numGlyphs is how many glyphs the font defines.
func (c *cffOutlines) numGlyphs() int { return len(c.charstrings) }

// gidForCID finds the glyph that carries a CID in a CID-keyed font.
func (c *cffOutlines) gidForCID(cid uint16) (uint16, bool) {
	if c.charsetCIDs == nil {
		return cid, true // an identity charset, which is the common case
	}
	for gid, v := range c.charsetCIDs {
		if v == cid {
			return uint16(gid), true
		}
	}
	return 0, false
}

// parseCFFOutlines reads a bare CFF font program.
func parseCFFOutlines(data []byte) (*cffOutlines, error) {
	if len(data) < 4 {
		return nil, errTTF
	}
	hdrSize := int(data[2])
	names, err := parseCFFIndex(data, hdrSize)
	if err != nil {
		return nil, err
	}
	topIdx, err := parseCFFIndex(data, names.end)
	if err != nil {
		return nil, err
	}
	strs, err := parseCFFIndex(data, topIdx.end)
	if err != nil {
		return nil, err
	}
	gsubrIdx, err := parseCFFIndex(data, strs.end)
	if err != nil {
		return nil, err
	}
	if len(topIdx.items) == 0 {
		return nil, errTTF
	}
	top, err := parseCFFDict(topIdx.items[0])
	if err != nil {
		return nil, err
	}

	out := &cffOutlines{
		gsubrs:     gsubrIdx.items,
		fontMatrix: matrix{0.001, 0, 0, 0.001, 0, 0},
	}
	if e := dictEntry(top, 1207); e != nil {
		if o := cffReals(e.operands); len(o) == 6 {
			out.fontMatrix = matrix{o[0], o[1], o[2], o[3], o[4], o[5]}
		}
	}

	cs := dictEntry(top, 17)
	if cs == nil || len(cs.values) != 1 {
		return nil, errTTF
	}
	csIdx, err := parseCFFIndex(data, cs.values[0])
	if err != nil {
		return nil, err
	}
	out.charstrings = csIdx.items

	// The font's own private dictionary, and the local subroutines it
	// points at.
	out.lsubrs, out.defaultW, out.nominalW = readPrivate(data, top)

	// A CID-keyed font distributes its private dictionaries across an
	// FDArray, chosen per glyph by FDSelect.
	if e := dictEntry(top, 1236); e != nil && len(e.values) == 1 { // FDArray
		if fdIdx, err := parseCFFIndex(data, e.values[0]); err == nil {
			for _, item := range fdIdx.items {
				fd, err := parseCFFDict(item)
				if err != nil {
					out.fdSubrs = append(out.fdSubrs, nil)
					continue
				}
				subrs, _, _ := readPrivate(data, fd)
				out.fdSubrs = append(out.fdSubrs, subrs)
			}
		}
	}
	if e := dictEntry(top, 1237); e != nil && len(e.values) == 1 { // FDSelect
		out.fdSelect = parseFDSelect(data, e.values[0], len(out.charstrings))
	}
	// ROS marks a CID-keyed font, whose charset holds CIDs rather than
	// glyph-name identifiers.
	if dictEntry(top, 1230) != nil {
		if e := dictEntry(top, 15); e != nil && len(e.values) == 1 && e.values[0] > 2 {
			if sids, err := parseCharsetSIDs(data, e.values[0], len(out.charstrings)); err == nil {
				out.charsetCIDs = sids
			}
		}
	}
	return out, nil
}

// readPrivate reads a private dictionary's local subroutines and widths.
func readPrivate(data []byte, owner []cffDictEntry) (subrs [][]byte, defaultW, nominalW float64) {
	e := dictEntry(owner, 18)
	if e == nil || len(e.values) != 2 {
		return nil, 0, 0
	}
	size, off := e.values[0], e.values[1]
	if off < 0 || size < 0 || off+size > len(data) {
		return nil, 0, 0
	}
	priv, err := parseCFFDict(data[off : off+size])
	if err != nil {
		return nil, 0, 0
	}
	if w := dictEntry(priv, 20); w != nil && len(w.values) == 1 {
		defaultW = float64(w.values[0])
	}
	if w := dictEntry(priv, 21); w != nil && len(w.values) == 1 {
		nominalW = float64(w.values[0])
	}
	if s := dictEntry(priv, 19); s != nil && len(s.values) == 1 {
		if idx, err := parseCFFIndex(data, off+s.values[0]); err == nil {
			subrs = idx.items
		}
	}
	return subrs, defaultW, nominalW
}

// parseFDSelect reads which font dictionary each glyph belongs to.
func parseFDSelect(data []byte, off, nGlyphs int) []uint8 {
	if off < 0 || off >= len(data) || nGlyphs <= 0 {
		return nil
	}
	out := make([]uint8, nGlyphs)
	switch data[off] {
	case 0:
		if off+1+nGlyphs > len(data) {
			return nil
		}
		copy(out, data[off+1:off+1+nGlyphs])
	case 3:
		if off+5 > len(data) {
			return nil
		}
		nRanges := int(be16(data, off+1))
		p := off + 3
		if p+nRanges*3+2 > len(data) {
			return nil
		}
		for i := 0; i < nRanges; i++ {
			first := int(be16(data, p))
			fd := data[p+2]
			next := int(be16(data, p+3))
			for g := first; g < next && g < nGlyphs; g++ {
				if g >= 0 {
					out[g] = fd
				}
			}
			p += 3
		}
	default:
		return nil
	}
	return out
}

// outline runs a glyph's charstring and returns the shape it draws.
func (c *cffOutlines) outline(gid uint16) *glyphOutline {
	if int(gid) >= len(c.charstrings) {
		return nil
	}
	local := c.lsubrs
	if c.fdSelect != nil && int(gid) < len(c.fdSelect) {
		if fd := int(c.fdSelect[gid]); fd < len(c.fdSubrs) {
			local = c.fdSubrs[fd]
		}
	}
	r := &type2Run{font: c, local: local}
	r.exec(c.charstrings[gid], 0)
	r.closeContour()
	if len(r.out.contours) == 0 {
		return nil
	}
	return &r.out
}

// type2Run is one charstring being interpreted.
type type2Run struct {
	font  *cffOutlines
	local [][]byte

	stack   []float64
	out     glyphOutline
	cur     glyphContour
	x, y    float64
	nStems  int
	haveW   bool
	inSeac  bool
	trans   []float64 // the transient array used by put/get
	started bool
}

const maxCharstringDepth = 10

func (r *type2Run) closeContour() {
	if len(r.cur.pts) > 0 {
		r.out.contours = append(r.out.contours, r.cur)
		r.cur = glyphContour{}
	}
}

func (r *type2Run) moveTo(x, y float64) {
	r.closeContour()
	r.x, r.y = x, y
	r.cur.pts = append(r.cur.pts, glyphPoint{x: x, y: y, on: true})
	r.started = true
}

func (r *type2Run) lineTo(x, y float64) {
	r.x, r.y = x, y
	r.cur.pts = append(r.cur.pts, glyphPoint{x: x, y: y, on: true})
}

// curveTo records a cubic. Contours here hold points that are all on the
// curve, so the cubic is flattened as it is added.
func (r *type2Run) curveTo(x1, y1, x2, y2, x3, y3 float64) {
	const steps = 8
	x0, y0 := r.x, r.y
	for i := 1; i <= steps; i++ {
		t := float64(i) / steps
		u := 1 - t
		x := u*u*u*x0 + 3*u*u*t*x1 + 3*u*t*t*x2 + t*t*t*x3
		y := u*u*u*y0 + 3*u*u*t*y1 + 3*u*t*t*y2 + t*t*t*y3
		r.cur.pts = append(r.cur.pts, glyphPoint{x: x, y: y, on: true})
	}
	r.x, r.y = x3, y3
}

// takeWidth drops the optional leading width argument. A charstring may
// begin with its own advance width, told apart from the drawing operands
// only by the argument count being odd where the operator expects even.
func (r *type2Run) takeWidth(even bool) {
	if r.haveW {
		return
	}
	r.haveW = true
	odd := len(r.stack)%2 == 1
	if (even && odd) || (!even && !odd && len(r.stack) > 0) {
		r.stack = r.stack[1:]
	}
}

func (r *type2Run) exec(cs []byte, depth int) {
	if depth > maxCharstringDepth {
		return
	}
	for i := 0; i < len(cs); {
		b := cs[i]
		if b >= 32 || b == 28 {
			var v float64
			switch {
			case b == 28:
				if i+3 > len(cs) {
					return
				}
				v = float64(int16(be16(cs, i+1)))
				i += 3
			case b <= 246:
				v = float64(int(b) - 139)
				i++
			case b <= 250:
				if i+2 > len(cs) {
					return
				}
				v = float64((int(b)-247)*256 + int(cs[i+1]) + 108)
				i += 2
			case b <= 254:
				if i+2 > len(cs) {
					return
				}
				v = float64(-(int(b)-251)*256 - int(cs[i+1]) - 108)
				i += 2
			default: // 255: a 16.16 fixed-point number
				if i+5 > len(cs) {
					return
				}
				v = float64(int32(be32(cs, i+1))) / 65536
				i += 5
			}
			if len(r.stack) < 48 {
				r.stack = append(r.stack, v)
			}
			continue
		}

		op := int(b)
		i++
		if op == 12 {
			if i >= len(cs) {
				return
			}
			op = 0xC00 | int(cs[i])
			i++
		}

		switch op {
		case 1, 3, 18, 23: // hstem, vstem, hstemhm, vstemhm
			r.takeWidth(true)
			r.nStems += len(r.stack) / 2
			r.stack = r.stack[:0]

		case 19, 20: // hintmask, cntrmask
			r.takeWidth(true)
			r.nStems += len(r.stack) / 2
			r.stack = r.stack[:0]
			i += (r.nStems + 7) / 8
			if i > len(cs) {
				return
			}

		case 21: // rmoveto
			r.takeWidth(true)
			if len(r.stack) >= 2 {
				n := len(r.stack)
				r.moveTo(r.x+r.stack[n-2], r.y+r.stack[n-1])
			}
			r.stack = r.stack[:0]

		case 22: // hmoveto
			r.takeWidth(false)
			if len(r.stack) >= 1 {
				r.moveTo(r.x+r.stack[len(r.stack)-1], r.y)
			}
			r.stack = r.stack[:0]

		case 4: // vmoveto
			r.takeWidth(false)
			if len(r.stack) >= 1 {
				r.moveTo(r.x, r.y+r.stack[len(r.stack)-1])
			}
			r.stack = r.stack[:0]

		case 5: // rlineto
			for j := 0; j+1 < len(r.stack); j += 2 {
				r.lineTo(r.x+r.stack[j], r.y+r.stack[j+1])
			}
			r.stack = r.stack[:0]

		case 6, 7: // hlineto, vlineto
			horiz := op == 6
			for _, d := range r.stack {
				if horiz {
					r.lineTo(r.x+d, r.y)
				} else {
					r.lineTo(r.x, r.y+d)
				}
				horiz = !horiz
			}
			r.stack = r.stack[:0]

		case 8: // rrcurveto
			for j := 0; j+5 < len(r.stack); j += 6 {
				r.relCurve(r.stack[j : j+6])
			}
			r.stack = r.stack[:0]

		case 24: // rcurveline
			j := 0
			for ; j+5 < len(r.stack)-2; j += 6 {
				r.relCurve(r.stack[j : j+6])
			}
			if j+1 < len(r.stack) {
				r.lineTo(r.x+r.stack[j], r.y+r.stack[j+1])
			}
			r.stack = r.stack[:0]

		case 25: // rlinecurve
			j := 0
			for ; len(r.stack)-j > 6; j += 2 {
				r.lineTo(r.x+r.stack[j], r.y+r.stack[j+1])
			}
			if j+5 < len(r.stack) {
				r.relCurve(r.stack[j : j+6])
			}
			r.stack = r.stack[:0]

		case 26, 27: // vvcurveto, hhcurveto
			j := 0
			d1 := 0.0
			if len(r.stack)%4 == 1 {
				d1 = r.stack[0]
				j = 1
			}
			for ; j+3 < len(r.stack); j += 4 {
				if op == 26 {
					r.relCurve([]float64{d1, r.stack[j], r.stack[j+1], r.stack[j+2], 0, r.stack[j+3]})
				} else {
					r.relCurve([]float64{r.stack[j], d1, r.stack[j+1], r.stack[j+2], r.stack[j+3], 0})
				}
				d1 = 0
			}
			r.stack = r.stack[:0]

		case 30, 31: // vhcurveto, hvcurveto
			horiz := op == 31
			j := 0
			for j+3 < len(r.stack) {
				last := j+8 > len(r.stack)
				extra := 0.0
				if last && j+5 == len(r.stack) {
					extra = r.stack[j+4]
				}
				if horiz {
					r.relCurve([]float64{r.stack[j], 0, r.stack[j+1], r.stack[j+2], extra, r.stack[j+3]})
				} else {
					r.relCurve([]float64{0, r.stack[j], r.stack[j+1], r.stack[j+2], r.stack[j+3], extra})
				}
				horiz = !horiz
				j += 4
			}
			r.stack = r.stack[:0]

		case 10: // callsubr
			if len(r.stack) == 0 {
				break
			}
			idx := int(r.stack[len(r.stack)-1]) + subrBias(len(r.local))
			r.stack = r.stack[:len(r.stack)-1]
			if idx >= 0 && idx < len(r.local) {
				r.exec(r.local[idx], depth+1)
			}

		case 29: // callgsubr
			if len(r.stack) == 0 {
				break
			}
			idx := int(r.stack[len(r.stack)-1]) + subrBias(len(r.font.gsubrs))
			r.stack = r.stack[:len(r.stack)-1]
			if idx >= 0 && idx < len(r.font.gsubrs) {
				r.exec(r.font.gsubrs[idx], depth+1)
			}

		case 11: // return
			return

		case 14: // endchar
			r.takeWidth(true)
			r.closeContour()
			return

		case 0xC23: // flex
			if len(r.stack) >= 13 {
				r.relCurve(r.stack[0:6])
				r.relCurve(r.stack[6:12])
			}
			r.stack = r.stack[:0]

		case 0xC22: // hflex
			if len(r.stack) >= 7 {
				y0 := r.y
				r.relCurve([]float64{r.stack[0], 0, r.stack[1], r.stack[2], r.stack[3], 0})
				r.relCurve([]float64{r.stack[4], 0, r.stack[5], y0 - r.y, r.stack[6], 0})
				r.y = y0
			}
			r.stack = r.stack[:0]

		case 0xC24: // hflex1
			if len(r.stack) >= 9 {
				y0 := r.y
				r.relCurve([]float64{r.stack[0], r.stack[1], r.stack[2], r.stack[3], r.stack[4], 0})
				r.relCurve([]float64{r.stack[5], 0, r.stack[6], r.stack[7], r.stack[8], y0 - r.y -
					r.stack[1] - r.stack[3] - r.stack[7]})
			}
			r.stack = r.stack[:0]

		case 0xC25: // flex1
			if len(r.stack) >= 11 {
				var dx, dy float64
				for j := 0; j < 10; j += 2 {
					dx += r.stack[j]
					dy += r.stack[j+1]
				}
				x0, y0 := r.x, r.y
				r.relCurve(r.stack[0:6])
				r.relCurve([]float64{r.stack[6], r.stack[7], r.stack[8], r.stack[9],
					x0 + dx + r.stack[10] - r.x - r.stack[6] - r.stack[8],
					y0 + dy - r.y - r.stack[7] - r.stack[9]})
			}
			r.stack = r.stack[:0]

		default:
			// Arithmetic and storage operators appear in almost no real
			// font; clearing the stack keeps a charstring that uses one
			// from drawing nonsense.
			r.stack = r.stack[:0]
		}
	}
}

// relCurve draws a cubic given as three relative point offsets.
func (r *type2Run) relCurve(d []float64) {
	x1, y1 := r.x+d[0], r.y+d[1]
	x2, y2 := x1+d[2], y1+d[3]
	r.curveTo(x1, y1, x2, y2, x2+d[4], y2+d[5])
}

// cffReals reads a dictionary's operands as numbers, including the
// nibble-encoded reals the subsetting parser has no use for and skips.
// A font matrix is written in reals, so without them every CFF font
// would be assumed to be the usual thousandth of an em — which most are,
// and the ones that are not would come out at the wrong size entirely.
func cffReals(operands []byte) []float64 {
	var out []float64
	for i := 0; i < len(operands); {
		b := operands[i]
		switch {
		case b == 28:
			if i+3 > len(operands) {
				return out
			}
			out = append(out, float64(int16(be16(operands, i+1))))
			i += 3
		case b == 29:
			if i+5 > len(operands) {
				return out
			}
			out = append(out, float64(int32(be32(operands, i+1))))
			i += 5
		case b == 30:
			v, n := cffReal(operands[i+1:])
			out = append(out, v)
			i += 1 + n
		case b >= 32 && b <= 246:
			out = append(out, float64(int(b)-139))
			i++
		case b >= 247 && b <= 250:
			if i+2 > len(operands) {
				return out
			}
			out = append(out, float64((int(b)-247)*256+int(operands[i+1])+108))
			i += 2
		case b >= 251 && b <= 254:
			if i+2 > len(operands) {
				return out
			}
			out = append(out, float64(-(int(b)-251)*256-int(operands[i+1])-108))
			i += 2
		default:
			return out
		}
	}
	return out
}

// cffReal decodes one nibble-encoded real and reports how many bytes it
// took. The nibbles spell the number out as text: digits, a point, an
// exponent marker, a minus sign, and 0xF to end.
func cffReal(b []byte) (float64, int) {
	var s []byte
	for i := 0; i < len(b); i++ {
		for _, n := range [2]byte{b[i] >> 4, b[i] & 0x0F} {
			switch {
			case n <= 9:
				s = append(s, '0'+n)
			case n == 0x0A:
				s = append(s, '.')
			case n == 0x0B:
				s = append(s, 'E')
			case n == 0x0C:
				s = append(s, 'E', '-')
			case n == 0x0E:
				s = append(s, '-')
			case n == 0x0F:
				v, err := strconv.ParseFloat(string(s), 64)
				if err != nil {
					return 0, i + 1
				}
				return v, i + 1
			}
		}
	}
	return 0, len(b)
}
