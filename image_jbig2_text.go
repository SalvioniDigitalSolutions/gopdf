package gopdf

import (
	"errors"

	"github.com/SalvioniDigitalSolutions/gopdf/internal/ccitt"
)

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
// The numbers both are made of — a height difference, a step along a
// line, which symbol — are coded one of two ways. One is the arithmetic
// coder, through two wrappers the specification defines on top of it:
// one that decodes an integer, and one that decodes a symbol's number.
// Neither is a plain binary read — the integer decoder walks a small
// tree of contexts to choose a magnitude range and then reads the value
// inside it, which is why a decoder cannot skip a field it does not care
// about. The other is Huffman coding (image_jbig2_huffman.go), where the
// same fields are prefix codes out of a table.
//
// Either way a symbol need not be coded from nothing. It can be a
// refinement of another symbol, or of several placed together, and a
// symbol on the page can be a refinement of its dictionary shape
// (image_jbig2_refine.go).

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

// symbolCodeLen is how many bits a symbol number needs: enough to tell
// the symbols apart, which for a single symbol is none at all.
func symbolCodeLen(n int) int {
	len := 0
	for 1<<uint(len) < n {
		len++
	}
	return len
}

// symbolContexts is the adaptive state of a symbol dictionary's bitmap
// coding, which a dictionary may hand on to one that follows it.
type symbolContexts struct {
	template, rTemplate int
	gb, gr              []mqContext
}

// tablePicker hands out the Huffman tables a segment's flags select:
// each field names a standard table or claims the next of the tables
// the segment brought with it.
type tablePicker struct {
	custom []*huffTable
	bad    bool
}

// pick resolves one selection. std lists what each value of the field
// means: a standard table number, 0 for the next custom table, or -1
// for a value the format does not define.
func (tp *tablePicker) pick(sel int, std ...int) *huffTable {
	if sel < 0 || sel >= len(std) || std[sel] < 0 {
		tp.bad = true
		return nil
	}
	if std[sel] > 0 {
		return jbig2StdTable(std[sel])
	}
	if len(tp.custom) == 0 || tp.custom[0] == nil {
		tp.bad = true
		return nil
	}
	t := tp.custom[0]
	tp.custom = tp.custom[1:]
	return t
}

// symbolSegment decodes a symbol dictionary segment and files it.
func (d *jbig2Decoder) symbolSegment(s jbig2Segment) error {
	input, err := d.symbolsFor(s.refers)
	if err != nil {
		return err
	}
	var inherit *symbolContexts
	for i := len(s.refers) - 1; i >= 0 && inherit == nil; i-- {
		inherit = d.dictCx[s.refers[i]]
	}
	got, keep, err := d.symbolDictionary(s.data, input, d.customTables(s.refers), inherit)
	if err != nil {
		return err
	}
	d.dicts[s.num] = got
	if keep != nil {
		d.dictCx[s.num] = keep
	}
	if len(d.everything)+len(got) <= jbig2MaxSymbols {
		d.everything = append(d.everything, got...)
	}
	return nil
}

// decodeSymbolDictionary reads a symbol dictionary segment and returns
// the symbols it exports.
func decodeSymbolDictionary(data []byte, input []*bitmap, page *bitmap) ([]*bitmap, error) {
	got, _, err := newJBIG2Decoder(page).symbolDictionary(data, input, nil, nil)
	return got, err
}

// symbolDictionary is the symbol dictionary decoding procedure.
//
// The symbols come in height classes: the encoder sorts them by height so
// that each class codes one height and then a run of widths, which is
// most of the saving. In the arithmetic form every symbol's bitmap is
// decoded by the generic region procedure, sharing one set of contexts
// across the whole dictionary so the coder keeps learning. In the
// Huffman form a class codes only its widths, and the bitmaps of the
// whole class follow as one strip, side by side.
func (d *jbig2Decoder) symbolDictionary(data []byte, input []*bitmap,
	custom []*huffTable, inherit *symbolContexts) ([]*bitmap, *symbolContexts, error) {

	if len(data) < 2 {
		return nil, nil, errJBIG2
	}
	flags := int(be16(data, 0))
	huff := flags&1 != 0
	refAgg := flags&2 != 0
	ctxUsed := flags&0x100 != 0
	ctxRetained := flags&0x200 != 0
	template := (flags >> 10) & 3
	rTemplate := (flags >> 12) & 1
	p := 2

	var at [][2]int
	if !huff {
		var ok bool
		if at, ok = genericAT(data, &p, template); !ok {
			return nil, nil, errJBIG2
		}
	}
	var rAT [][2]int
	if refAgg {
		// Refinement has its own template pixels: a symbol coded
		// against another symbol rather than from nothing.
		var ok bool
		if rAT, ok = refinementAT(data, &p, rTemplate); !ok {
			return nil, nil, errJBIG2
		}
	}
	if p+8 > len(data) {
		return nil, nil, errJBIG2
	}
	nExported := int(be32(data, p))
	nNew := int(be32(data, p+4))
	p += 8
	if nNew < 0 || nNew > 1<<16 || nExported < 0 || nExported > 2*jbig2MaxSymbols ||
		len(input) > jbig2MaxSymbols {
		return nil, nil, errJBIG2
	}
	codeLen := symbolCodeLen(len(input) + nNew)

	// Where the numbers come from.
	var (
		dec                    *mqDecoder
		bits                   *jbig2Bits
		gbCx                   []mqContext
		iadh, iadw, iaex, iaai *arithIntCtx
		tDH, tDW, tBM, tAgg    *huffTable
		tc                     *textCoder // for symbols made of other symbols
	)
	if huff {
		bits = &jbig2Bits{data: data[p:]}
		tp := &tablePicker{custom: custom}
		tDH = tp.pick((flags>>2)&3, 4, 5, -1, 0)
		tDW = tp.pick((flags>>4)&3, 2, 3, -1, 0)
		tBM = tp.pick((flags>>6)&1, 1, 0)
		tAgg = tp.pick((flags>>7)&1, 1, 0)
		if tp.bad {
			return nil, nil, errJBIG2
		}
	} else {
		dec = newMQDecoder(data[p:])
		gbCx = make([]mqContext, 1<<16)
		if ctxUsed && inherit != nil && inherit.gb != nil && inherit.template == template {
			copy(gbCx, inherit.gb)
		}
		iadh, iadw = newArithIntCtx(), newArithIntCtx()
		iaex, iaai = newArithIntCtx(), newArithIntCtx()
	}
	if refAgg {
		if err := d.spend(int64(2)<<uint(codeLen)/64 + 1); err != nil {
			return nil, nil, err
		}
		tc = &textCoder{codeLen: codeLen, grCx: make([]mqContext, refinementContexts(rTemplate))}
		if ctxUsed && inherit != nil && inherit.gr != nil && inherit.rTemplate == rTemplate {
			copy(tc.grCx, inherit.gr)
		}
		if huff {
			tc.bits = bits
			tc.fs, tc.ds, tc.dt = jbig2StdTable(6), jbig2StdTable(8), jbig2StdTable(11)
			tc.rdw, tc.rdh = jbig2StdTable(15), jbig2StdTable(15)
			tc.rdx, tc.rdy = jbig2StdTable(15), jbig2StdTable(15)
			tc.rsize = jbig2StdTable(1)
		} else {
			tc.arithmetic(dec, codeLen)
		}
	}
	number := func(a *arithIntCtx, t *huffTable) (int, bool) {
		if huff {
			return t.decode(bits)
		}
		return a.decodeInt(dec)
	}
	failed := func() bool { return huff && bits.bad }

	// Every symbol is decoded pixel by pixel, and both the count and the
	// sizes come out of the stream. Bounding each one against the page
	// is not enough on its own — a thousand page-sized symbols are a
	// thousand pages of work — so the dictionary also gets a budget for
	// the whole of it. A page's worth of glyphs, several times over, is
	// far more than any real scan needs and still a bounded amount.
	//
	// The allowance grows with the dictionary's own length as well as
	// with the page. A document scanned in strips shares one dictionary
	// between images far smaller than it, and that dictionary is honest
	// work; what marks a hostile one is a few bytes claiming millions of
	// pixels.
	budget := int64(1<<26) + jbig2WorkPerByte*int64(len(data))
	if d.page != nil {
		budget = 4*int64(d.page.w)*int64(d.page.h) + jbig2WorkPerByte*int64(len(data))
	}
	claim := func(w, h int) error {
		budget -= int64(w) * int64(h)
		if budget < 0 {
			return errJBIG2
		}
		return d.spendArea(w, h)
	}

	// all is every symbol a later one may be built from: the ones that
	// came in, then the new ones in the order they were decoded.
	all := append(make([]*bitmap, 0, len(input)+nNew), input...)
	nDecoded := 0
	height := 0
	for nDecoded < nNew {
		if err := d.spend(1); err != nil {
			return nil, nil, err
		}
		dh, ok := number(iadh, tDH)
		if !ok {
			if failed() {
				return nil, nil, errJBIG2
			}
			break
		}
		height += dh
		if height <= 0 || height > 1<<14 {
			return nil, nil, errJBIG2
		}
		width, totalWidth := 0, 0
		var widths []int // a Huffman class, whose bitmaps come after it
		for {
			if err := d.spend(1); err != nil {
				return nil, nil, err
			}
			dw, ok := number(iadw, tDW)
			if !ok {
				if failed() {
					return nil, nil, errJBIG2
				}
				break // the height class ends
			}
			width += dw
			if width <= 0 || width > 1<<14 || nDecoded >= nNew {
				return nil, nil, errJBIG2
			}
			nDecoded++
			if huff && !refAgg {
				widths = append(widths, width)
				totalWidth += width
				continue
			}
			if !d.sideFits(width, height) {
				return nil, nil, errJBIG2
			}
			if err := claim(width, height); err != nil {
				return nil, nil, err
			}
			sym := newBitmap(width, height)
			if !refAgg {
				if err := decodeGenericRegion(sym, dec, gbCx, template, at, false); err != nil {
					return nil, nil, err
				}
			} else if err := d.aggregateSymbol(sym, all, number, iaai, tAgg, tc,
				rTemplate, rAT); err != nil {
				return nil, nil, err
			}
			all = append(all, sym)
		}
		if !huff || refAgg {
			continue
		}

		// The strip: every bitmap of the class side by side, either as
		// plain rows of bits or as MMR. A class of no symbols still
		// says how long its strip is.
		size, ok := tBM.decode(bits)
		if !ok {
			return nil, nil, errJBIG2
		}
		if len(widths) == 0 {
			if bits.take(size); bits.bad {
				return nil, nil, errJBIG2
			}
			continue
		}
		if !d.sideFits(totalWidth, height) {
			return nil, nil, errJBIG2
		}
		if err := claim(totalWidth, height); err != nil {
			return nil, nil, err
		}
		strip := newBitmap(totalWidth, height)
		if size == 0 {
			rowBytes := (totalWidth + 7) / 8
			raw := bits.take(rowBytes * height)
			if bits.bad {
				return nil, nil, errJBIG2
			}
			for y := 0; y < height; y++ {
				for x := 0; x < totalWidth; x++ {
					strip.pix[y*totalWidth+x] = raw[y*rowBytes+x>>3] >> (7 - uint(x&7)) & 1
				}
			}
		} else {
			raw := bits.take(size)
			if bits.bad {
				return nil, nil, errJBIG2
			}
			pix, _, err := ccitt.DecodeMMR(raw, totalWidth, height)
			if err != nil {
				return nil, nil, jbig2MMRError(err)
			}
			strip.pix = pix
		}
		x0 := 0
		for _, w := range widths {
			sym := newBitmap(w, height)
			for y := 0; y < height; y++ {
				copy(sym.pix[y*w:(y+1)*w], strip.pix[y*totalWidth+x0:y*totalWidth+x0+w])
			}
			all = append(all, sym)
			x0 += w
		}
	}
	newSymbols := all[len(input):]

	// The export flags say which of the input and new symbols this
	// dictionary passes on: alternating runs of skipped and exported.
	var tEx *huffTable
	if huff {
		tEx = jbig2StdTable(1)
	}
	var exported []*bitmap
	for i, cur := 0, false; i < len(all); cur = !cur {
		if err := d.spend(1); err != nil {
			return nil, nil, err
		}
		run, ok := number(iaex, tEx)
		if !ok || run < 0 || run > len(all)-i {
			break
		}
		if cur {
			exported = append(exported, all[i:i+run]...)
		}
		i += run
	}
	if len(exported) == 0 {
		// A dictionary that says nothing about its exports exports what
		// it made, which is what the common encoder means.
		exported = newSymbols
	}

	var keep *symbolContexts
	if ctxRetained && !huff {
		keep = &symbolContexts{template: template, rTemplate: rTemplate, gb: gbCx}
		if tc != nil {
			keep.gr = tc.grCx
		}
	}
	return exported, keep, nil
}

// aggregateSymbol decodes a symbol that is made out of others: either a
// refinement of one, which is the common case and has a short form, or
// several placed like a small text region and refined together.
func (d *jbig2Decoder) aggregateSymbol(sym *bitmap, all []*bitmap,
	number func(*arithIntCtx, *huffTable) (int, bool), iaai *arithIntCtx,
	tAgg *huffTable, tc *textCoder, rTemplate int, rAT [][2]int) error {

	n, ok := number(iaai, tAgg)
	if !ok || n < 1 || len(all) == 0 {
		return errJBIG2
	}
	if n > 1 {
		return d.textInstances(sym, all, &textParams{
			huff: tc.bits != nil, refine: true, refCorner: 1,
			rTemplate: rTemplate, rAT: rAT, nInstances: n,
		}, tc)
	}
	id := tc.symbolID()
	rdx, ok1 := tc.number(tc.iardx, tc.rdx)
	rdy, ok2 := tc.number(tc.iardy, tc.rdy)
	if !ok1 || !ok2 || tc.failed() || id < 0 || id >= len(all) {
		return errJBIG2
	}
	dec := tc.dec
	if tc.bits != nil {
		// In the Huffman form the refinement is still arithmetic: a
		// length, then that many bytes with a coder of their own.
		size, ok := tc.rsize.decode(tc.bits)
		raw := tc.bits.take(size)
		if !ok || tc.bits.bad {
			return errJBIG2
		}
		dec = newMQDecoder(raw)
	}
	decodeRefinement(sym, dec, tc.grCx, rTemplate, all[id], rdx, rdy, false, rAT)
	return nil
}

// textParams is how a text region lays its symbols out.
type textParams struct {
	huff, refine bool
	logStrips    int
	refCorner    int
	transposed   bool
	combOp       int
	dsOffset     int
	rTemplate    int
	rAT          [][2]int
	nInstances   int
}

// textCoder is where a text region's numbers come from: the arithmetic
// coder with a context set per field, or a bit reader with a table per
// field. A symbol dictionary that builds symbols out of others keeps one
// for its whole length.
type textCoder struct {
	dec                          *mqDecoder
	iadt, iafs, iads, iait, iari *arithIntCtx
	iardw, iardh, iardx, iardy   *arithIntCtx
	iaid                         *arithIDCtx
	bits                         *jbig2Bits
	fs, ds, dt                   *huffTable
	rdw, rdh, rdx, rdy, rsize    *huffTable
	symCodes                     *huffTable // nil where symbol numbers are plain
	codeLen                      int
	grCx                         []mqContext
}

// arithmetic sets the coder up for the arithmetic form.
func (c *textCoder) arithmetic(dec *mqDecoder, codeLen int) {
	c.dec = dec
	c.iadt, c.iafs, c.iads = newArithIntCtx(), newArithIntCtx(), newArithIntCtx()
	c.iait, c.iari = newArithIntCtx(), newArithIntCtx()
	c.iardw, c.iardh = newArithIntCtx(), newArithIntCtx()
	c.iardx, c.iardy = newArithIntCtx(), newArithIntCtx()
	c.iaid = newArithIDCtx(codeLen)
}

func (c *textCoder) number(a *arithIntCtx, t *huffTable) (int, bool) {
	if c.bits != nil {
		return t.decode(c.bits)
	}
	return a.decodeInt(c.dec)
}

// closeStrip reads the step that ends the last strip. Running out of
// data there is not an error: nothing was owed after the last symbol.
func (c *textCoder) closeStrip() {
	if c.bits == nil {
		c.iads.decodeInt(c.dec)
		return
	}
	if c.bits.bad {
		return
	}
	pos := c.bits.pos
	if c.ds.decode(c.bits); c.bits.bad {
		c.bits.pos, c.bits.bad = pos, false
	}
}

// failed reports whether the Huffman reader has run off its data or met
// a code no table has. The arithmetic coder cannot tell.
func (c *textCoder) failed() bool { return c.bits != nil && c.bits.bad }

func (c *textCoder) symbolID() int {
	if c.bits == nil {
		return c.iaid.decodeID(c.dec)
	}
	if c.symCodes == nil {
		return int(c.bits.read(c.codeLen))
	}
	id, ok := c.symCodes.decode(c.bits)
	if !ok {
		c.bits.bad = true
		return -1
	}
	return id
}

// decodeTextRegion reads a text region segment and draws its symbols onto
// a bitmap.
func decodeTextRegion(data []byte, symbols []*bitmap, page *bitmap) error {
	info, region, err := newJBIG2Decoder(page).textRegion(data, symbols, nil)
	if err != nil {
		return err
	}
	composeBitmap(page, region, info.x, info.y, info.op)
	return nil
}

// textRegion reads a text region segment into a bitmap of its own.
//
// The page is coded as strips: a vertical position, then a run of symbols
// along it, each with its own horizontal step. That is the whole idea —
// the shapes are already known, so the page is only a list of where they
// go.
func (d *jbig2Decoder) textRegion(data []byte, symbols []*bitmap,
	custom []*huffTable) (jbig2Region, *bitmap, error) {

	info, ok := parseRegionInfo(data)
	if !ok || len(data) < 19 {
		return info, nil, errJBIG2
	}
	p := 17
	flags := int(be16(data, p))
	p += 2
	tp := &textParams{
		huff:       flags&1 != 0,
		refine:     flags&2 != 0,
		logStrips:  (flags >> 2) & 3,
		refCorner:  (flags >> 4) & 3,
		transposed: (flags>>6)&1 != 0,
		combOp:     (flags >> 7) & 3,
		dsOffset:   (flags >> 10) & 0x1F,
		rTemplate:  (flags >> 15) & 1,
	}
	defPixel := byte((flags >> 9) & 1)
	if tp.dsOffset > 15 {
		tp.dsOffset -= 32
	}
	huffFlags := 0
	if tp.huff {
		if p+2 > len(data) {
			return info, nil, errJBIG2
		}
		huffFlags = int(be16(data, p))
		p += 2
	}
	if tp.refine {
		if tp.rAT, ok = refinementAT(data, &p, tp.rTemplate); !ok {
			return info, nil, errJBIG2
		}
	}
	if p+4 > len(data) {
		return info, nil, errJBIG2
	}
	tp.nInstances = int(be32(data, p))
	p += 4
	if tp.nInstances < 0 || tp.nInstances > 1<<22 {
		return info, nil, errJBIG2
	}
	if len(symbols) == 0 {
		return info, nil, errors.New("gopdf: JBIG2 text region refers to no symbols")
	}
	if !regionFits(info.w, info.h, d.page) {
		return info, nil, errJBIG2
	}
	codeLen := symbolCodeLen(len(symbols))
	if err := d.spend(int64(info.w)*int64(info.h) + int64(2)<<uint(codeLen)/64); err != nil {
		return info, nil, err
	}

	region := newBitmap(info.w, info.h)
	if defPixel != 0 {
		for i := range region.pix {
			region.pix[i] = 1
		}
	}

	c := &textCoder{codeLen: codeLen}
	if tp.huff {
		c.bits = &jbig2Bits{data: data[p:]}
		pick := &tablePicker{custom: custom}
		c.fs = pick.pick(huffFlags&3, 6, 7, -1, 0)
		c.ds = pick.pick((huffFlags>>2)&3, 8, 9, 10, 0)
		c.dt = pick.pick((huffFlags>>4)&3, 11, 12, 13, 0)
		c.rdw = pick.pick((huffFlags>>6)&3, 14, 15, -1, 0)
		c.rdh = pick.pick((huffFlags>>8)&3, 14, 15, -1, 0)
		c.rdx = pick.pick((huffFlags>>10)&3, 14, 15, -1, 0)
		c.rdy = pick.pick((huffFlags>>12)&3, 14, 15, -1, 0)
		c.rsize = pick.pick((huffFlags>>14)&1, 1, 0)
		if pick.bad {
			return info, nil, errJBIG2
		}
		// The region opens with the codes of the symbols it places.
		if err := d.spend(int64(len(symbols))); err != nil {
			return info, nil, err
		}
		var err error
		if c.symCodes, err = readSymbolIDTable(c.bits, len(symbols)); err != nil {
			return info, nil, err
		}
	} else {
		c.arithmetic(newMQDecoder(data[p:]), codeLen)
	}
	if tp.refine {
		c.grCx = make([]mqContext, refinementContexts(tp.rTemplate))
	}
	if err := d.textInstances(region, symbols, tp, c); err != nil {
		return info, nil, err
	}
	return info, region, nil
}

// textInstances is the text region decoding procedure: it places symbol
// instances on a bitmap, strip by strip.
func (d *jbig2Decoder) textInstances(region *bitmap, symbols []*bitmap,
	tp *textParams, c *textCoder) error {

	strips := 1 << uint(tp.logStrips)

	// The first strip's position, counted in strips rather than pixels.
	stripT, ok := c.number(c.iadt, c.dt)
	if !ok {
		return errJBIG2
	}
	stripT = -stripT * strips
	firstS, placed := 0, 0

	for placed < tp.nInstances {
		if err := d.spend(1); err != nil {
			return err
		}
		dt, ok := c.number(c.iadt, c.dt)
		if !ok {
			break
		}
		stripT += dt * strips

		// The first symbol of the strip gives its own position; the rest
		// step along from it.
		dfs, ok := c.number(c.iafs, c.fs)
		if !ok {
			break
		}
		firstS += dfs
		curS := firstS

		for {
			curT := stripT
			if strips > 1 {
				if c.bits != nil {
					curT += int(c.bits.read(tp.logStrips))
				} else {
					t, ok := c.iait.decodeInt(c.dec)
					if !ok {
						return errJBIG2
					}
					curT += t
				}
			}
			id := c.symbolID()
			if c.failed() || id < 0 || id >= len(symbols) {
				return errJBIG2
			}
			sym := symbols[id]
			if tp.refine {
				refined := 0
				if c.bits != nil {
					refined = c.bits.bit()
				} else if refined, ok = c.iari.decodeInt(c.dec); !ok {
					return errJBIG2
				}
				if refined != 0 {
					var err error
					if sym, err = d.refinedSymbol(sym, tp, c); err != nil {
						return err
					}
				}
			}
			if err := d.spend(int64(sym.w)*int64(sym.h) + 1); err != nil {
				return err
			}
			drawSymbolOp(region, sym, curS, curT, tp.refCorner, tp.transposed, tp.combOp)
			if tp.transposed {
				curS += sym.h - 1
			} else {
				curS += sym.w - 1
			}
			placed++
			if placed >= tp.nInstances {
				// The last strip is closed like any other, and the
				// closing has to be read: where symbols are built from
				// other symbols, the next symbol's numbers come straight
				// after it in the same coded data. A region that simply
				// stops instead is let off.
				c.closeStrip()
				break
			}

			ds, ok := c.number(c.iads, c.ds)
			if !ok {
				break // the strip ends
			}
			curS += ds + tp.dsOffset
		}
		if c.failed() {
			return errJBIG2
		}
	}
	if c.failed() {
		return errJBIG2
	}
	return nil
}

// refinedSymbol decodes a symbol instance coded as a correction to its
// dictionary shape: four numbers saying how the size and position
// differ, then the refinement itself.
func (d *jbig2Decoder) refinedSymbol(base *bitmap, tp *textParams, c *textCoder) (*bitmap, error) {
	rdw, ok1 := c.number(c.iardw, c.rdw)
	rdh, ok2 := c.number(c.iardh, c.rdh)
	rdx, ok3 := c.number(c.iardx, c.rdx)
	rdy, ok4 := c.number(c.iardy, c.rdy)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return nil, errJBIG2
	}
	const far = 1 << 16
	if rdw < -far || rdw > far || rdh < -far || rdh > far ||
		rdx < -far || rdx > far || rdy < -far || rdy > far {
		return nil, errJBIG2
	}
	w, h := base.w+rdw, base.h+rdh
	if !regionFits(w, h, d.page) {
		return nil, errJBIG2
	}
	if err := d.spendArea(w, h); err != nil {
		return nil, err
	}
	dec := c.dec
	if c.bits != nil {
		// In the Huffman form the refinement is still arithmetic: a
		// length, then that many bytes with a coder of their own.
		size, ok := c.rsize.decode(c.bits)
		raw := c.bits.take(size)
		if !ok || c.bits.bad {
			return nil, errJBIG2
		}
		dec = newMQDecoder(raw)
	}
	out := newBitmap(w, h)
	// Halving rounds down, for a negative difference too.
	decodeRefinement(out, dec, c.grCx, tp.rTemplate, base,
		rdw>>1+rdx, rdh>>1+rdy, false, tp.rAT)
	return out, nil
}

// drawSymbol places one symbol, by whichever of its corners the region
// said it counts positions from.
func drawSymbol(region, sym *bitmap, s, t, refCorner int, transposed bool) {
	drawSymbolOp(region, sym, s, t, refCorner, transposed, jbig2OR)
}

// drawSymbolOp is drawSymbol with the operator the region combines its
// symbols by.
//
// s is always the near edge along the line and t the position across
// it: the format moves s past a symbol before placing it when positions
// count from the far corner and after when they count from the near
// one, which comes to the same edge either way.
func drawSymbolOp(region, sym *bitmap, s, t, refCorner int, transposed bool, op int) {
	var x, y int
	if transposed {
		x, y = t, s
		switch refCorner {
		case 2, 3: // bottom right, top right
			x -= sym.w - 1
		}
	} else {
		x, y = s, t
		switch refCorner {
		case 0, 2: // bottom left, bottom right
			y -= sym.h - 1
		}
	}
	composeBitmap(region, sym, x, y, op)
}
