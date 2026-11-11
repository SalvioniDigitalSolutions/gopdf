package gopdf

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/SalvioniDigitalSolutions/gopdf/internal/ccitt"
)

// The encoding side of the rest of JBIG2, written for the tests.
//
// No JBIG2 encoder is installed where these tests are developed, so the
// streams the decoder is checked on are made here: each procedure of the
// format has its encoding half below, and the tests put what they
// produce through this package's decoder and through Poppler's. An
// encoder and a decoder written from one misreading agree with each
// other; they do not both agree with a third party.
//
// Nothing here takes a *testing.T, so the fuzz target can build its
// seeds from the same streams.

// jbBits collects plain bits, most significant first.
type jbBits struct {
	out []byte
	n   uint
}

func (w *jbBits) put(v uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		if w.n == 0 {
			w.out = append(w.out, 0)
		}
		if v>>uint(i)&1 != 0 {
			w.out[len(w.out)-1] |= 0x80 >> w.n
		}
		w.n = (w.n + 1) & 7
	}
}

func (w *jbBits) align() { w.n = 0 }

// raw appends whole bytes at the next byte boundary.
func (w *jbBits) raw(b []byte) {
	w.align()
	w.out = append(w.out, b...)
}

// huffPut writes a value, or the out-of-band value, with a table.
func huffPut(w *jbBits, t *huffTable, v int, oob bool) {
	for i, l := range t.lines {
		if l.prefLen == 0 {
			continue
		}
		var off uint32
		n, hit := 0, false
		switch {
		case l.kind == huffOOB:
			hit = oob
		case oob:
		case l.kind == huffNormal:
			if v >= l.low && int64(v)-int64(l.low) < 1<<uint(l.rangeLen) {
				hit, off, n = true, uint32(v-l.low), l.rangeLen
			}
		case l.kind == huffLower:
			if v <= l.low {
				hit, off, n = true, uint32(l.low-v), 32
			}
		case l.kind == huffUpper:
			if v >= l.low {
				hit, off, n = true, uint32(v-l.low), 32
			}
		}
		if !hit {
			continue
		}
		// Codes of one length run consecutively in table order.
		pos := 0
		for _, m := range t.lines[:i] {
			if m.prefLen == l.prefLen {
				pos++
			}
		}
		w.put(uint32(t.first[l.prefLen])+uint32(pos), l.prefLen)
		w.put(off, n)
		return
	}
	panic(fmt.Sprintf("the table has no code for %d (out of band: %v)", v, oob))
}

func jbPut16(b *bytes.Buffer, v int) { b.Write([]byte{byte(v >> 8), byte(v)}) }
func jbPut32(b *bytes.Buffer, v int) {
	b.Write([]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

// jbRegionInfo is the field a region segment opens with.
func jbRegionInfo(b *bytes.Buffer, r jbig2Region) {
	jbPut32(b, r.w)
	jbPut32(b, r.h)
	jbPut32(b, r.x)
	jbPut32(b, r.y)
	b.WriteByte(byte(r.op))
}

func jbATBytes(b *bytes.Buffer, at [][2]int) {
	for _, a := range at {
		b.Write([]byte{byte(int8(a[0])), byte(int8(a[1]))})
	}
}

// encodeGenericBits codes a bitmap into an encoder in progress, with
// the typical-prediction shortcut and a skip mask if asked.
func encodeGenericBits(enc *mqEncoder, cx []mqContext, b *bitmap, template int,
	at [][2]int, tpgdon bool, skip *bitmap) {
	ltp := false
	for y := 0; y < b.h; y++ {
		if tpgdon {
			same := y > 0 && bytes.Equal(b.pix[y*b.w:(y+1)*b.w], b.pix[(y-1)*b.w:y*b.w])
			if y == 0 {
				// The row above the first is white.
				same = !bytes.Contains(b.pix[:b.w], []byte{1})
			}
			bit := 0
			if same != ltp {
				bit = 1
			}
			enc.encode(&cx[tpgdonContext(template)], bit)
			ltp = same
			if ltp {
				continue
			}
		}
		for x := 0; x < b.w; x++ {
			if skip != nil && skip.at(x, y) != 0 {
				continue
			}
			enc.encode(&cx[genericContext(b, x, y, template, at)], int(b.at(x, y)))
		}
	}
}

// encodeGenericSeg is a generic region segment, arithmetic coded.
func encodeGenericSeg(num, kind int, r jbig2Region, b *bitmap, template int, tpgdon bool) []byte {
	return segmentWrap(num, kind, genericBody(r, b, template, tpgdon))
}

func genericBody(r jbig2Region, b *bitmap, template int, tpgdon bool) []byte {
	at := defaultAT(template)
	enc := newMQEncoder()
	encodeGenericBits(enc, make([]mqContext, 1<<16), b, template, at, tpgdon, nil)
	var body bytes.Buffer
	jbRegionInfo(&body, r)
	flags := template << 1
	if tpgdon {
		flags |= 8
	}
	body.WriteByte(byte(flags))
	jbATBytes(&body, at)
	body.Write(enc.flush())
	return body.Bytes()
}

// encodeGenericMMRSeg is a generic region segment coded as MMR.
func encodeGenericMMRSeg(num, kind int, r jbig2Region, b *bitmap, eofb bool) []byte {
	var body bytes.Buffer
	jbRegionInfo(&body, r)
	body.WriteByte(1) // MMR
	body.Write(ccitt.EncodeMMR(b.pix, b.w, b.h, eofb))
	return segmentWrap(num, kind, body.Bytes())
}

// segmentWrapUnknown is a segment header that does not give its length,
// which only an immediate generic region may have.
func segmentWrapUnknown(num int, body []byte) []byte {
	var b bytes.Buffer
	jbPut32(&b, num)
	b.WriteByte(38)
	b.WriteByte(0) // no referred-to segments
	b.WriteByte(1) // page 1
	b.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF})
	b.Write(body)
	return b.Bytes()
}

// encodeRefinementBits is the encoding side of the refinement
// procedure.
func encodeRefinementBits(enc *mqEncoder, cx []mqContext, target, ref *bitmap,
	dx, dy, template int, tpgron bool, at [][2]int) {
	ltpCtx := 0x0008
	if template == 0 {
		ltpCtx = 0x0020
	}
	ltp := false
	for y := 0; y < target.h; y++ {
		if tpgron {
			// A row can be coded with prediction when every pixel the
			// prediction covers is predicted right.
			typical := true
			for x := 0; x < target.w; x++ {
				if v, ok := refinementTypical(ref, x-dx, y-dy); ok && v != target.at(x, y) {
					typical = false
					break
				}
			}
			bit := 0
			if typical != ltp {
				bit = 1
			}
			enc.encode(&cx[ltpCtx], bit)
			ltp = typical
		}
		for x := 0; x < target.w; x++ {
			if ltp {
				if _, ok := refinementTypical(ref, x-dx, y-dy); ok {
					continue
				}
			}
			ctx := refinementContext(target, ref, x, y, dx, dy, template, at)
			enc.encode(&cx[ctx], int(target.at(x, y)))
		}
	}
}

var nominalRAT = [][2]int{{-1, -1}, {-1, -1}}

// encodeRefinementSeg is a generic refinement region segment: target
// coded as a correction to ref.
func encodeRefinementSeg(num, kind int, r jbig2Region, target, ref *bitmap,
	template int, tpgron bool, refers ...int) []byte {
	enc := newMQEncoder()
	encodeRefinementBits(enc, make([]mqContext, refinementContexts(template)),
		target, ref, 0, 0, template, tpgron, nominalRAT)
	var body bytes.Buffer
	jbRegionInfo(&body, r)
	flags := template
	if tpgron {
		flags |= 2
	}
	body.WriteByte(byte(flags))
	if template == 0 {
		jbATBytes(&body, nominalRAT)
	}
	body.Write(enc.flush())
	return segmentWrap(num, kind, body.Bytes(), refers...)
}

// jbPlace is one symbol instance: which symbol, and where the top left
// corner of what is drawn lands. refined, when set, is what is drawn
// instead of the symbol — coded as a correction to it.
type jbPlace struct {
	id       int
	x, y     int
	refined  *bitmap
	rdx, rdy int
}

// jbTextOpts is how a text region is to be coded.
type jbTextOpts struct {
	huff, refine bool
	logStrips    int
	refCorner    int
	transposed   bool
	combOp       int
	defPixel     int
	dsOffset     int
	rTemplate    int
	huffFlags    int          // which table each field uses
	custom       []*huffTable // the tables a selection of "custom" claims
	codeLens     []int        // Huffman code length per symbol; nil for uniform
}

// jbTextEnc is the encoder's mirror of textCoder.
type jbTextEnc struct {
	enc                          *mqEncoder
	iadt, iafs, iads, iait, iari *arithIntCtx
	iardw, iardh, iardx, iardy   *arithIntCtx
	iaid                         *arithIDCtx
	bits                         *jbBits
	fs, ds, dt                   *huffTable
	rdw, rdh, rdx, rdy, rsize    *huffTable
	symCodes                     *huffTable
	codeLen                      int
	grCx                         []mqContext
}

func (e *jbTextEnc) arithmetic(enc *mqEncoder, codeLen int) {
	e.enc = enc
	e.iadt, e.iafs, e.iads = newArithIntCtx(), newArithIntCtx(), newArithIntCtx()
	e.iait, e.iari = newArithIntCtx(), newArithIntCtx()
	e.iardw, e.iardh = newArithIntCtx(), newArithIntCtx()
	e.iardx, e.iardy = newArithIntCtx(), newArithIntCtx()
	e.iaid = newArithIDCtx(codeLen)
}

func (e *jbTextEnc) number(a *arithIntCtx, t *huffTable, v int, oob bool) {
	if e.bits != nil {
		huffPut(e.bits, t, v, oob)
		return
	}
	a.encodeInt(e.enc, v, oob)
}

func (e *jbTextEnc) symbolID(id int) {
	switch {
	case e.bits == nil:
		e.iaid.encodeID(e.enc, id)
	case e.symCodes == nil:
		e.bits.put(uint32(id), e.codeLen)
	default:
		huffPut(e.bits, e.symCodes, id, false)
	}
}

func floorDiv(a, b int) int {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// instances is the encoding side of the text region procedure.
func (e *jbTextEnc) instances(symbols []*bitmap, places []jbPlace, o *jbTextOpts) {
	strips := 1 << uint(o.logStrips)
	type item struct {
		p    jbPlace
		s, t int
		bm   *bitmap
	}
	items := make([]item, len(places))
	for i, p := range places {
		bm := symbols[p.id]
		if p.refined != nil {
			bm = p.refined
		}
		// From where the instance is drawn to the coordinates that put
		// it there: s runs along the line, t across it, and t names the
		// corner the region counts from.
		it := item{p: p, bm: bm}
		if o.transposed {
			it.s, it.t = p.y, p.x
			if o.refCorner == 2 || o.refCorner == 3 {
				it.t += bm.w - 1
			}
		} else {
			it.s, it.t = p.x, p.y
			if o.refCorner == 0 || o.refCorner == 2 {
				it.t += bm.h - 1
			}
		}
		items[i] = it
	}
	sort.SliceStable(items, func(a, b int) bool {
		sa, sb := floorDiv(items[a].t, strips), floorDiv(items[b].t, strips)
		if sa != sb {
			return sa < sb
		}
		return items[a].s < items[b].s
	})

	// The first strip position is written as one strip above the top:
	// the Huffman tables for it have no code for zero.
	e.number(e.iadt, e.dt, 1, false)
	strip, firstS := -1, 0
	for i := 0; i < len(items); {
		idx := floorDiv(items[i].t, strips)
		e.number(e.iadt, e.dt, idx-strip, false)
		strip = idx
		e.number(e.iafs, e.fs, items[i].s-firstS, false)
		firstS = items[i].s
		curS := firstS
		for {
			it := items[i]
			if strips > 1 {
				within := it.t - idx*strips
				if e.bits != nil {
					e.bits.put(uint32(within), o.logStrips)
				} else {
					e.iait.encodeInt(e.enc, within, false)
				}
			}
			e.symbolID(it.p.id)
			if o.refine {
				r := 0
				if it.p.refined != nil {
					r = 1
				}
				if e.bits != nil {
					e.bits.put(uint32(r), 1)
				} else {
					e.iari.encodeInt(e.enc, r, false)
				}
				if r == 1 {
					e.refinement(symbols[it.p.id], it.p, o)
				}
			}
			if o.transposed {
				curS += it.bm.h - 1
			} else {
				curS += it.bm.w - 1
			}
			i++
			if i >= len(items) || floorDiv(items[i].t, strips) != idx {
				break
			}
			e.number(e.iads, e.ds, items[i].s-curS-o.dsOffset, false)
			curS = items[i].s
		}
		e.number(e.iads, e.ds, 0, true) // the strip ends
	}
}

// refinement codes one instance as a correction to its symbol.
func (e *jbTextEnc) refinement(base *bitmap, p jbPlace, o *jbTextOpts) {
	rdw, rdh := p.refined.w-base.w, p.refined.h-base.h
	e.number(e.iardw, e.rdw, rdw, false)
	e.number(e.iardh, e.rdh, rdh, false)
	e.number(e.iardx, e.rdx, p.rdx, false)
	e.number(e.iardy, e.rdy, p.rdy, false)
	dx, dy := floorDiv(rdw, 2)+p.rdx, floorDiv(rdh, 2)+p.rdy
	if e.bits == nil {
		encodeRefinementBits(e.enc, e.grCx, p.refined, base, dx, dy, o.rTemplate, false, nominalRAT)
		return
	}
	// The Huffman form: a coder of its own, its length written first.
	sub := newMQEncoder()
	encodeRefinementBits(sub, e.grCx, p.refined, base, dx, dy, o.rTemplate, false, nominalRAT)
	data := sub.flush()
	huffPut(e.bits, e.rsize, len(data), false)
	e.bits.raw(data)
}

// uniformCodeLens gives every symbol a code of the same length.
func uniformCodeLens(n int) []int {
	l := symbolCodeLen(n)
	if l == 0 {
		l = 1
	}
	out := make([]int, n)
	for i := range out {
		out[i] = l
	}
	return out
}

// writeSymbolIDTable is the encoding side of readSymbolIDTable: the
// code lengths, run-length coded, and the table that codes the runs.
func writeSymbolIDTable(w *jbBits, lens []int) *huffTable {
	type run struct{ code, extra, extraBits int }
	var runs []run
	for i := 0; i < len(lens); {
		j := i
		for j < len(lens) && lens[j] == lens[i] {
			j++
		}
		n := j - i
		switch {
		case lens[i] == 0 && n >= 11:
			if n > 138 {
				n = 138
			}
			runs = append(runs, run{34, n - 11, 7})
		case lens[i] == 0 && n >= 3:
			if n > 10 {
				n = 10
			}
			runs = append(runs, run{33, n - 3, 3})
		case lens[i] != 0 && n >= 4:
			// The length itself, then "repeat the previous" for the
			// rest of the run.
			runs = append(runs, run{lens[i], 0, 0})
			n--
			if n > 6 {
				n = 6
			}
			runs = append(runs, run{32, n - 3, 2})
			n++
		default:
			runs = append(runs, run{lens[i], 0, 0})
			n = 1
		}
		i += n
	}
	// Every run code that occurs gets a code of the same length.
	used := map[int]bool{}
	for _, r := range runs {
		used[r.code] = true
	}
	runLen := 1
	for 1<<uint(runLen) < len(used) {
		runLen++
	}
	runLines := make([]huffLine, 35)
	for i := range runLines {
		runLines[i] = huffLine{low: i}
		if used[i] {
			runLines[i].prefLen = runLen
		}
		w.put(uint32(runLines[i].prefLen), 4)
	}
	runTable, err := newHuffTable(runLines)
	if err != nil {
		panic(err)
	}
	for _, r := range runs {
		huffPut(w, runTable, r.code, false)
		w.put(uint32(r.extra), r.extraBits)
	}
	w.align()

	lines := make([]huffLine, len(lens))
	for i, l := range lens {
		lines[i] = huffLine{low: i, prefLen: l}
	}
	t, err := newHuffTable(lines)
	if err != nil {
		panic(err)
	}
	return t
}

// encodeTextSeg is a text region segment.
func encodeTextSeg(num, kind int, r jbig2Region, symbols []*bitmap, places []jbPlace,
	o jbTextOpts, refers ...int) []byte {

	var body bytes.Buffer
	jbRegionInfo(&body, r)
	flags := o.logStrips<<2 | o.refCorner<<4 | o.combOp<<7 | o.defPixel<<9 |
		(o.dsOffset&0x1F)<<10 | o.rTemplate<<15
	if o.huff {
		flags |= 1
	}
	if o.refine {
		flags |= 2
	}
	if o.transposed {
		flags |= 1 << 6
	}
	jbPut16(&body, flags)
	if o.huff {
		jbPut16(&body, o.huffFlags)
	}
	if o.refine && o.rTemplate == 0 {
		jbATBytes(&body, nominalRAT)
	}
	jbPut32(&body, len(places))

	e := &jbTextEnc{}
	if o.refine {
		e.grCx = make([]mqContext, refinementContexts(o.rTemplate))
	}
	if o.huff {
		e.bits = &jbBits{}
		pick := &tablePicker{custom: o.custom}
		hf := o.huffFlags
		e.fs = pick.pick(hf&3, 6, 7, -1, 0)
		e.ds = pick.pick((hf>>2)&3, 8, 9, 10, 0)
		e.dt = pick.pick((hf>>4)&3, 11, 12, 13, 0)
		e.rdw = pick.pick((hf>>6)&3, 14, 15, -1, 0)
		e.rdh = pick.pick((hf>>8)&3, 14, 15, -1, 0)
		e.rdx = pick.pick((hf>>10)&3, 14, 15, -1, 0)
		e.rdy = pick.pick((hf>>12)&3, 14, 15, -1, 0)
		e.rsize = pick.pick((hf>>14)&1, 1, 0)
		if pick.bad {
			panic("the text region names a table it was not given")
		}
		lens := o.codeLens
		if lens == nil {
			lens = uniformCodeLens(len(symbols))
		}
		e.symCodes = writeSymbolIDTable(e.bits, lens)
		e.instances(symbols, places, &o)
		body.Write(e.bits.out)
	} else {
		e.arithmetic(newMQEncoder(), symbolCodeLen(len(symbols)))
		e.instances(symbols, places, &o)
		body.Write(e.enc.flush())
	}
	return segmentWrap(num, kind, body.Bytes(), refers...)
}

// heightClasses groups symbols by height, keeping their order, and
// requires the heights to rise: the order a dictionary codes them in.
func heightClasses(symbols []*bitmap) [][]*bitmap {
	var out [][]*bitmap
	for _, s := range symbols {
		if n := len(out); n > 0 && out[n-1][0].h == s.h {
			out[n-1] = append(out[n-1], s)
			continue
		}
		if n := len(out); n > 0 && out[n-1][0].h > s.h {
			panic("symbols must be given in order of height")
		}
		out = append(out, []*bitmap{s})
	}
	return out
}

// jbHuffDict is how a Huffman-coded symbol dictionary is to be coded.
type jbHuffDict struct {
	dhSel, dwSel  int // 0 or 1 for the standard tables, 3 for a custom one
	bmSel, aggSel int // 0 for the standard table, 1 for a custom one
	mmr           bool
	custom        []*huffTable
}

// encodeHuffSymbolDict is a symbol dictionary in the Huffman form: each
// height class codes its widths and then one strip holding every
// bitmap of the class.
func encodeHuffSymbolDict(num int, symbols []*bitmap, o jbHuffDict, refers ...int) []byte {
	pick := &tablePicker{custom: o.custom}
	tDH := pick.pick(o.dhSel, 4, 5, -1, 0)
	tDW := pick.pick(o.dwSel, 2, 3, -1, 0)
	tBM := pick.pick(o.bmSel, 1, 0)
	pick.pick(o.aggSel, 1, 0)
	if pick.bad {
		panic("the dictionary names a table it was not given")
	}
	w := &jbBits{}
	height := 0
	for _, class := range heightClasses(symbols) {
		huffPut(w, tDH, class[0].h-height, false)
		height = class[0].h
		width, total := 0, 0
		for _, s := range class {
			huffPut(w, tDW, s.w-width, false)
			width = s.w
			total += s.w
		}
		huffPut(w, tDW, 0, true)

		strip := newBitmap(total, height)
		x0 := 0
		for _, s := range class {
			composeBitmap(strip, s, x0, 0, jbig2REPLACE)
			x0 += s.w
		}
		if o.mmr {
			coded := ccitt.EncodeMMR(strip.pix, total, height, false)
			huffPut(w, tBM, len(coded), false)
			w.raw(coded)
			continue
		}
		// A length of zero means the strip is stored as it is.
		huffPut(w, tBM, 0, false)
		w.align()
		for y := 0; y < height; y++ {
			for x := 0; x < total; x++ {
				w.put(uint32(strip.at(x, y)), 1)
			}
			w.align()
		}
	}
	// Export every new symbol: skip none, then take them all.
	std1 := jbig2StdTable(1)
	huffPut(w, std1, 0, false)
	huffPut(w, std1, len(symbols), false)

	var body bytes.Buffer
	jbPut16(&body, 1|o.dhSel<<2|o.dwSel<<4|o.bmSel<<6|o.aggSel<<7)
	jbPut32(&body, len(symbols))
	jbPut32(&body, len(symbols))
	body.Write(w.out)
	return segmentWrap(num, 0, body.Bytes(), refers...)
}

// jbAggSym is a symbol coded out of other symbols: a correction to one
// of them (of, rdx, rdy), or — when places is set — several placed
// together, each of which may itself be corrected.
type jbAggSym struct {
	bm       *bitmap
	of       int
	rdx, rdy int
	places   []jbPlace
}

// encodeAggSymbolDict is a symbol dictionary whose symbols are all
// built from others: those of the dictionaries it refers to, given as
// input, and its own earlier ones.
func encodeAggSymbolDict(num int, input []*bitmap, syms []jbAggSym, huff bool,
	rTemplate int, exportInput bool, refers ...int) []byte {

	const template = 0
	all := append([]*bitmap(nil), input...)
	codeLen := symbolCodeLen(len(input) + len(syms))
	opts := &jbTextOpts{huff: huff, refine: true, refCorner: 1, rTemplate: rTemplate}

	te := &jbTextEnc{codeLen: codeLen, grCx: make([]mqContext, refinementContexts(rTemplate))}
	var (
		enc                    *mqEncoder
		bits                   *jbBits
		iadh, iadw, iaex, iaai *arithIntCtx
		tDH, tDW, tOne         *huffTable
	)
	if huff {
		bits = &jbBits{}
		te.bits = bits
		te.fs, te.ds, te.dt = jbig2StdTable(6), jbig2StdTable(8), jbig2StdTable(11)
		te.rdw, te.rdh = jbig2StdTable(15), jbig2StdTable(15)
		te.rdx, te.rdy = jbig2StdTable(15), jbig2StdTable(15)
		te.rsize = jbig2StdTable(1)
		// The signed tables, so heights and widths can come in any
		// order.
		tDH, tDW, tOne = jbig2StdTable(5), jbig2StdTable(3), jbig2StdTable(1)
	} else {
		enc = newMQEncoder()
		te.arithmetic(enc, codeLen)
		iadh, iadw = newArithIntCtx(), newArithIntCtx()
		iaex, iaai = newArithIntCtx(), newArithIntCtx()
	}
	number := func(a *arithIntCtx, t *huffTable, v int, oob bool) {
		if huff {
			huffPut(bits, t, v, oob)
			return
		}
		a.encodeInt(enc, v, oob)
	}

	height := 0
	for i := 0; i < len(syms); {
		number(iadh, tDH, syms[i].bm.h-height, false)
		height = syms[i].bm.h
		width := 0
		for ; i < len(syms) && syms[i].bm.h == height; i++ {
			s := syms[i]
			number(iadw, tDW, s.bm.w-width, false)
			width = s.bm.w
			if s.places != nil {
				number(iaai, tOne, len(s.places), false)
				te.instances(all, s.places, opts)
			} else {
				number(iaai, tOne, 1, false)
				te.symbolID(s.of)
				te.number(te.iardx, te.rdx, s.rdx, false)
				te.number(te.iardy, te.rdy, s.rdy, false)
				if huff {
					sub := newMQEncoder()
					encodeRefinementBits(sub, te.grCx, s.bm, all[s.of], s.rdx, s.rdy,
						rTemplate, false, nominalRAT)
					data := sub.flush()
					huffPut(bits, tOne, len(data), false)
					bits.raw(data)
				} else {
					encodeRefinementBits(enc, te.grCx, s.bm, all[s.of], s.rdx, s.rdy,
						rTemplate, false, nominalRAT)
				}
			}
			all = append(all, s.bm)
		}
		number(iadw, tDW, 0, true)
	}
	nExported := len(syms)
	if exportInput {
		number(iaex, tOne, 0, false)
		number(iaex, tOne, len(all), false)
		nExported = len(all)
	} else {
		number(iaex, tOne, len(input), false)
		number(iaex, tOne, len(syms), false)
	}

	var body bytes.Buffer
	flags := 2 | template<<10 | rTemplate<<12
	if huff {
		// The signed tables for heights and widths.
		flags |= 1 | 1<<2 | 1<<4
	}
	jbPut16(&body, flags)
	if !huff {
		jbATBytes(&body, defaultAT(template))
	}
	if rTemplate == 0 {
		jbATBytes(&body, nominalRAT)
	}
	jbPut32(&body, nExported)
	jbPut32(&body, len(syms))
	if huff {
		body.Write(bits.out)
	} else {
		body.Write(enc.flush())
	}
	return segmentWrap(num, 0, body.Bytes(), refers...)
}

// encodeArithSymbolDict is the plain arithmetic dictionary, with the
// two flags that pass the coder's state from one dictionary to the
// next. It returns the state it finished in, for the next one to start
// from.
func encodeArithSymbolDict(num int, symbols []*bitmap, template, nInput int,
	start []mqContext, retain bool, refers ...int) ([]byte, []mqContext) {

	at := defaultAT(template)
	enc := newMQEncoder()
	cx := make([]mqContext, 1<<16)
	copy(cx, start)
	iadh, iadw, iaex := newArithIntCtx(), newArithIntCtx(), newArithIntCtx()
	height := 0
	for _, class := range heightClasses(symbols) {
		iadh.encodeInt(enc, class[0].h-height, false)
		height = class[0].h
		width := 0
		for _, s := range class {
			iadw.encodeInt(enc, s.w-width, false)
			width = s.w
			encodeGenericBits(enc, cx, s, template, at, false, nil)
		}
		iadw.encodeInt(enc, 0, true)
	}
	// Export the new symbols and none of the ones that came in.
	iaex.encodeInt(enc, nInput, false)
	iaex.encodeInt(enc, len(symbols), false)

	var body bytes.Buffer
	flags := template << 10
	if start != nil {
		flags |= 0x100
	}
	if retain {
		flags |= 0x200
	}
	jbPut16(&body, flags)
	jbATBytes(&body, at)
	jbPut32(&body, len(symbols))
	jbPut32(&body, len(symbols))
	body.Write(enc.flush())
	return segmentWrap(num, 0, body.Bytes(), refers...), cx
}

// encodePatternDict is a pattern dictionary: the patterns side by side,
// coded as one generic region.
func encodePatternDict(num int, patterns []*bitmap, template int, mmr bool) []byte {
	pw, ph := patterns[0].w, patterns[0].h
	all := newBitmap(pw*len(patterns), ph)
	for i, p := range patterns {
		composeBitmap(all, p, i*pw, 0, jbig2REPLACE)
	}
	var body bytes.Buffer
	flags := template << 1
	if mmr {
		flags |= 1
	}
	body.WriteByte(byte(flags))
	body.WriteByte(byte(pw))
	body.WriteByte(byte(ph))
	jbPut32(&body, len(patterns)-1)
	if mmr {
		body.Write(ccitt.EncodeMMR(all.pix, all.w, all.h, false))
	} else {
		at := [][2]int{{-pw, 0}, {-3, -1}, {2, -2}, {-2, -2}}
		if template != 0 {
			at = at[:1]
		}
		enc := newMQEncoder()
		encodeGenericBits(enc, make([]mqContext, 1<<16), all, template, at, false, nil)
		body.Write(enc.flush())
	}
	return segmentWrap(num, 16, body.Bytes())
}

// jbHalftone describes a halftone region: a grid of grey values and
// where the grid sits.
type jbHalftone struct {
	gw, gh         int
	gx, gy, rx, ry int // in 256ths of a pixel
	vals           []int
	template       int
	mmr, skip      bool
	combOp         int
	defPixel       int
}

// cell is where a grid cell's pattern goes.
func (h *jbHalftone) cell(m, n int) (x, y int) {
	return (h.gx + m*h.ry + n*h.rx) >> 8, (h.gy + m*h.rx - n*h.ry) >> 8
}

// encodeHalftoneSeg is a halftone region segment.
func encodeHalftoneSeg(num, kind int, r jbig2Region, h jbHalftone, patterns []*bitmap,
	refers ...int) []byte {

	pw, ph := patterns[0].w, patterns[0].h
	bpp := 0
	for 1<<uint(bpp) < len(patterns) {
		bpp++
	}
	var skip *bitmap
	if h.skip && !h.mmr {
		skip = newBitmap(h.gw, h.gh)
		for m := 0; m < h.gh; m++ {
			for n := 0; n < h.gw; n++ {
				x, y := h.cell(m, n)
				if x+pw <= 0 || x >= r.w || y+ph <= 0 || y >= r.h {
					skip.pix[m*h.gw+n] = 1
				}
			}
		}
	}

	var body bytes.Buffer
	jbRegionInfo(&body, r)
	flags := h.template<<1 | h.combOp<<4 | h.defPixel<<7
	if h.mmr {
		flags |= 1
	}
	if h.skip {
		flags |= 8
	}
	body.WriteByte(byte(flags))
	jbPut32(&body, h.gw)
	jbPut32(&body, h.gh)
	jbPut32(&body, h.gx)
	jbPut32(&body, h.gy)
	jbPut16(&body, h.rx)
	jbPut16(&body, h.ry)

	// The bit planes, most significant first, each the difference from
	// the one above it.
	at := [][2]int{{3, -1}, {-3, -1}, {2, -2}, {-2, -2}}
	if h.template > 1 {
		at[0][0] = 2
	}
	if h.template != 0 {
		at = at[:1]
	}
	enc := newMQEncoder()
	cx := make([]mqContext, 1<<16)
	for j := bpp - 1; j >= 0; j-- {
		plane := newBitmap(h.gw, h.gh)
		for i, v := range h.vals {
			bit := v >> uint(j) & 1
			if j < bpp-1 {
				bit ^= v >> uint(j+1) & 1
			}
			plane.pix[i] = byte(bit)
		}
		if h.mmr {
			body.Write(ccitt.EncodeMMR(plane.pix, h.gw, h.gh, true))
			continue
		}
		if skip != nil {
			// A skipped cell is not coded and reads back as zero, so the
			// encoder has to see it as zero too.
			for i, s := range skip.pix {
				if s != 0 {
					plane.pix[i] = 0
				}
			}
		}
		encodeGenericBits(enc, cx, plane, h.template, at, false, skip)
	}
	if !h.mmr {
		body.Write(enc.flush())
	}
	return segmentWrap(num, kind, body.Bytes(), refers...)
}

// encodeTableSeg is a code table segment: lines must be the ordinary
// lines of the table, rising and with no gaps.
func encodeTableSeg(num int, lines []huffLine, lowerLen, upperLen, oobLen int) []byte {
	maxPref, maxRange := lowerLen, 0
	if upperLen > maxPref {
		maxPref = upperLen
	}
	if oobLen > maxPref {
		maxPref = oobLen
	}
	for _, l := range lines {
		if l.prefLen > maxPref {
			maxPref = l.prefLen
		}
		if l.rangeLen > maxRange {
			maxRange = l.rangeLen
		}
	}
	prefBits, rangeBits := 1, 1
	for 1<<uint(prefBits) <= maxPref {
		prefBits++
	}
	for 1<<uint(rangeBits) <= maxRange {
		rangeBits++
	}
	last := lines[len(lines)-1]
	low, high := lines[0].low, last.low+1<<uint(last.rangeLen)

	var body bytes.Buffer
	flags := (prefBits-1)<<1 | (rangeBits-1)<<4
	if oobLen >= 0 {
		flags |= 1
	}
	body.WriteByte(byte(flags))
	jbPut32(&body, low)
	jbPut32(&body, high)
	w := &jbBits{}
	for _, l := range lines {
		w.put(uint32(l.prefLen), prefBits)
		w.put(uint32(l.rangeLen), rangeBits)
	}
	w.put(uint32(lowerLen), prefBits)
	w.put(uint32(upperLen), prefBits)
	if oobLen >= 0 {
		w.put(uint32(oobLen), prefBits)
	}
	body.Write(w.out)
	return segmentWrap(num, 53, body.Bytes())
}

// jbPageInfoFlags is the page information segment with its flags byte
// given, for a page that starts out black.
func jbPageInfoFlags(w, h, flags int) []byte {
	var b bytes.Buffer
	jbPut32(&b, w)
	jbPut32(&b, h)
	jbPut32(&b, 0)
	jbPut32(&b, 0)
	b.WriteByte(byte(flags))
	b.Write([]byte{0, 0})
	return segmentWrap(0, 48, b.Bytes())
}

// joinSegs strings segments together.
func joinSegs(segs ...[]byte) []byte {
	var out []byte
	for _, s := range segs {
		out = append(out, s...)
	}
	return out
}
