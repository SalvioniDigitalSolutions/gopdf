package gopdf

import "errors"

// JBIG2 symbol dictionaries and text regions.
//
// This is the half of JBIG2 that makes it worth using. A page of text is
// mostly the same few hundred shapes over and over, so an encoder finds
// them, codes each shape once into a symbol dictionary, and then codes
// the page as a list of which symbol goes where. A scanned page of prose
// comes out several times smaller than the same page coded pixel by
// pixel.
//
// It also means the generic-region decoder alone reads almost no real
// scan: the shapes are in a dictionary segment and the page is a text
// region referring to it. Both are here.
//
// Everything is decoded with the same arithmetic coder, through two
// wrappers the specification defines on top of it: one that decodes an
// integer, and one that decodes a symbol's number. Neither is a plain
// binary read — the integer decoder walks a small tree of contexts to
// choose a magnitude range and then reads the value inside it, which is
// why a decoder cannot skip a field it does not care about.

// arithIntCtx is the context set one integer decoder uses. Each field of
// the format — a height difference, a width, a coordinate — has its own,
// so the coder learns each field's distribution separately.
type arithIntCtx struct {
	cx []mqContext
}

func newArithIntCtx() *arithIntCtx {
	return &arithIntCtx{cx: make([]mqContext, 512)}
}

// decodeInt is the integer decoding procedure. It reads a sign, then
// picks a magnitude range from a run of prefix bits, then reads the
// value within that range.
//
// The second result is false for the out-of-band value, which is how the
// format says "no more" — a symbol dictionary ends its height classes
// with one, and a text region ends its strips.
func (a *arithIntCtx) decodeInt(dec *mqDecoder) (int, bool) {
	prev := 1
	bit := func() int {
		b := dec.decode(&a.cx[prev])
		// The context is the bits read so far, capped so the tree stays
		// inside the 512 contexts the field has.
		if prev < 256 {
			prev = prev<<1 | b
		} else {
			prev = (((prev<<1 | b) & 511) | 256)
		}
		return b
	}
	s := bit()
	var v, n int
	switch {
	case bit() == 0:
		n, v = 2, 0
	case bit() == 0:
		n, v = 4, 4
	case bit() == 0:
		n, v = 6, 20
	case bit() == 0:
		n, v = 8, 84
	case bit() == 0:
		n, v = 12, 340
	default:
		n, v = 32, 4436
	}
	val := 0
	for i := 0; i < n; i++ {
		val = val<<1 | bit()
	}
	val += v
	if s == 1 {
		if val == 0 {
			return 0, false // the out-of-band value
		}
		return -val, true
	}
	return val, true
}

// arithIDCtx decodes a symbol's number, which is a plain binary read of
// as many bits as the dictionary's size needs.
type arithIDCtx struct {
	cx      []mqContext
	codeLen int
}

func newArithIDCtx(codeLen int) *arithIDCtx {
	return &arithIDCtx{cx: make([]mqContext, 1<<uint(codeLen+1)), codeLen: codeLen}
}

func (a *arithIDCtx) decodeID(dec *mqDecoder) int {
	prev := 1
	for i := 0; i < a.codeLen; i++ {
		b := dec.decode(&a.cx[prev])
		prev = prev<<1 | b
	}
	return prev - (1 << uint(a.codeLen))
}

// symbolCodeLen is how many bits a symbol number needs.
func symbolCodeLen(n int) int {
	len := 0
	for 1<<uint(len) < n {
		len++
	}
	if len == 0 {
		return 1 // a dictionary of one symbol still spends a bit on it
	}
	return len
}

// decodeSymbolDictionary reads a symbol dictionary segment and returns
// the symbols it exports.
//
// The symbols come in height classes: the encoder sorts them by height so
// that each class codes one height and then a run of widths, which is
// most of the saving. Every symbol's bitmap is decoded by the generic
// region procedure, sharing one set of contexts across the whole
// dictionary so the coder keeps learning.
func decodeSymbolDictionary(data []byte, input []*bitmap) ([]*bitmap, error) {
	if len(data) < 2 {
		return nil, errJBIG2
	}
	flags := int(be16(data, 0))
	huff := flags&1 != 0
	refAgg := flags&2 != 0
	template := (flags >> 10) & 3
	p := 2

	if huff {
		return nil, errors.New("gopdf: JBIG2 symbol dictionary is Huffman-coded, " +
			"which is not decoded")
	}
	nAT := 1
	if template == 0 {
		nAT = 4
	}
	at := make([][2]int, nAT)
	for i := 0; i < nAT; i++ {
		if p+2 > len(data) {
			return nil, errJBIG2
		}
		at[i] = [2]int{int(int8(data[p])), int(int8(data[p+1]))}
		p += 2
	}
	if refAgg {
		// Refinement needs its own template pixels, and a symbol coded
		// against another symbol rather than from nothing.
		p += 4
	}
	if p+8 > len(data) {
		return nil, errJBIG2
	}
	nExported := int(be32(data, p))
	nNew := int(be32(data, p+4))
	p += 8
	if nNew < 0 || nNew > 1<<16 || nExported < 0 || nExported > 1<<16 {
		return nil, errJBIG2
	}

	dec := newMQDecoder(data[p:])
	cx := make([]mqContext, 1<<16)
	iadh, iadw := newArithIntCtx(), newArithIntCtx()
	iaex, iaai := newArithIntCtx(), newArithIntCtx()

	newSymbols := make([]*bitmap, 0, nNew)
	height := 0
	for len(newSymbols) < nNew {
		dh, ok := iadh.decodeInt(dec)
		if !ok {
			break
		}
		height += dh
		if height <= 0 || height > 1<<14 {
			return nil, errJBIG2
		}
		width := 0
		for {
			dw, ok := iadw.decodeInt(dec)
			if !ok {
				break // the height class ends
			}
			width += dw
			if width <= 0 || width > 1<<14 || len(newSymbols) >= nNew {
				return nil, errJBIG2
			}
			if refAgg {
				// A symbol coded as a refinement of others; the count is
				// read so the stream stays aligned, and the shape is not
				// recoverable without the refinement decoder.
				if _, ok := iaai.decodeInt(dec); !ok {
					return nil, errJBIG2
				}
				return nil, errors.New("gopdf: JBIG2 symbol dictionary uses " +
					"refinement coding, which is not decoded")
			}
			sym := newBitmap(width, height)
			if err := decodeGenericRegion(sym, dec, cx, template, at, false); err != nil {
				return nil, err
			}
			newSymbols = append(newSymbols, sym)
		}
	}

	// The export flags say which of the input and new symbols this
	// dictionary passes on: alternating runs of skipped and exported.
	all := append(append([]*bitmap(nil), input...), newSymbols...)
	var exported []*bitmap
	i, cur := 0, false
	for i < len(all) && len(exported) <= nExported {
		run, ok := iaex.decodeInt(dec)
		if !ok || run < 0 {
			break
		}
		if cur {
			for j := 0; j < run && i < len(all); j++ {
				exported = append(exported, all[i])
				i++
			}
		} else {
			i += run
		}
		cur = !cur
	}
	if len(exported) == 0 {
		// A dictionary that says nothing about its exports exports what
		// it made, which is what the common encoder means.
		exported = newSymbols
	}
	return exported, nil
}

// decodeTextRegion reads a text region segment and draws its symbols onto
// a bitmap.
//
// The page is coded as strips: a vertical position, then a run of symbols
// along it, each with its own horizontal step. That is the whole idea —
// the shapes are already known, so the page is only a list of where they
// go.
func decodeTextRegion(data []byte, symbols []*bitmap, page *bitmap) error {
	if len(data) < 17 {
		return errJBIG2
	}
	w, h := int(be32(data, 0)), int(be32(data, 4))
	x0, y0 := int(be32(data, 8)), int(be32(data, 12))
	p := 17

	if p+2 > len(data) {
		return errJBIG2
	}
	flags := int(be16(data, p))
	p += 2
	huff := flags&1 != 0
	refine := flags&2 != 0
	logStrips := (flags >> 2) & 3
	strips := 1 << uint(logStrips)
	refCorner := (flags >> 4) & 3
	transposed := (flags>>6)&1 != 0
	combOp := (flags >> 7) & 3
	defPixel := (flags >> 9) & 1
	dsOffset := (flags >> 10) & 0x1F
	if dsOffset > 15 {
		dsOffset -= 32
	}
	rTemplate := (flags >> 15) & 1
	_ = combOp

	if huff {
		return errors.New("gopdf: JBIG2 text region is Huffman-coded, " +
			"which is not decoded")
	}
	if refine {
		if rTemplate == 0 {
			p += 4 // the refinement template pixels
		}
	}
	if p+4 > len(data) {
		return errJBIG2
	}
	nInstances := int(be32(data, p))
	p += 4
	if nInstances < 0 || nInstances > 1<<22 {
		return errJBIG2
	}
	if len(symbols) == 0 {
		return errors.New("gopdf: JBIG2 text region refers to no symbols")
	}
	if w <= 0 || h <= 0 || w > 1<<16 || h > 1<<16 || w*h > 1<<28 {
		return errJBIG2
	}

	region := newBitmap(w, h)
	if defPixel != 0 {
		for i := range region.pix {
			region.pix[i] = 1
		}
	}

	dec := newMQDecoder(data[p:])
	iadt, iafs := newArithIntCtx(), newArithIntCtx()
	iads, iait := newArithIntCtx(), newArithIntCtx()
	iari := newArithIntCtx()
	iaid := newArithIDCtx(symbolCodeLen(len(symbols)))

	// The first strip's position, counted in strips rather than pixels.
	stripT, ok := iadt.decodeInt(dec)
	if !ok {
		return errJBIG2
	}
	stripT = -stripT * strips
	firstS, placed := 0, 0

	for placed < nInstances {
		dt, ok := iadt.decodeInt(dec)
		if !ok {
			break
		}
		stripT += dt * strips

		// The first symbol of the strip gives its own position; the rest
		// step along from it.
		dfs, ok := iafs.decodeInt(dec)
		if !ok {
			break
		}
		firstS += dfs
		curS := firstS

		for {
			curT := stripT
			if strips > 1 {
				t, ok := iait.decodeInt(dec)
				if !ok {
					return errJBIG2
				}
				curT += t
			}
			id := iaid.decodeID(dec)
			if id < 0 || id >= len(symbols) {
				return errJBIG2
			}
			sym := symbols[id]
			if refine {
				ri, ok := iari.decodeInt(dec)
				if !ok {
					return errJBIG2
				}
				if ri != 0 {
					return errors.New("gopdf: JBIG2 text region refines a " +
						"symbol, which is not decoded")
				}
			}
			drawSymbol(region, sym, curS, curT, refCorner, transposed)
			if transposed {
				curS += sym.h - 1
			} else {
				curS += sym.w - 1
			}
			placed++
			if placed >= nInstances {
				break
			}

			ds, ok := iads.decodeInt(dec)
			if !ok {
				break // the strip ends
			}
			curS += ds + dsOffset
		}
	}

	// Onto the page by OR, as a scanner's region is.
	for ry := 0; ry < h; ry++ {
		for rx := 0; rx < w; rx++ {
			if region.at(rx, ry) != 0 {
				page.set(x0+rx, y0+ry, 1)
			}
		}
	}
	return nil
}

// drawSymbol places one symbol, by whichever of its corners the region
// said it counts positions from.
func drawSymbol(region, sym *bitmap, s, t, refCorner int, transposed bool) {
	var x, y int
	if transposed {
		x, y = t, s
		switch refCorner {
		case 0: // bottom left
		case 1: // top left
		case 2: // bottom right
			x -= sym.w - 1
		case 3: // top right
			x -= sym.w - 1
		}
	} else {
		x, y = s, t
		switch refCorner {
		case 0: // bottom left
			y -= sym.h - 1
		case 2: // bottom right
			y -= sym.h - 1
		}
	}
	for sy := 0; sy < sym.h; sy++ {
		for sx := 0; sx < sym.w; sx++ {
			if sym.at(sx, sy) != 0 {
				region.set(x+sx, y+sy, 1)
			}
		}
	}
}
