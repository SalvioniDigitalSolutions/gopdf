package gopdf

import (
	"bytes"
	"errors"
	"fmt"
	"image"

	"github.com/SalvioniDigitalSolutions/gopdf/internal/ccitt"
)

// JBIG2 generic regions, and the framing every kind of region shares.
//
// JBIG2 is what a scanner reaches for when a page is black and white: it
// beats fax coding by a wide margin and is what most scanned PDFs of the
// last twenty years are made of. Undecoded, such a page is a hole — no
// text can be read out of it, no pixel can be scrubbed, and redaction
// has to remove the whole scan rather than a rectangle of it.
//
// A stream is a run of segments. Some carry material for later ones — a
// dictionary of symbols, a dictionary of halftone patterns, a code table
// — and the rest are regions: rectangles of pixels, each coded one of
// four ways and combined onto the page. A generic region is the image
// coded directly, pixel by pixel, which is what is in this file; a text
// region places symbols (image_jbig2_text.go); a halftone region tiles
// patterns (image_jbig2_halftone.go); a refinement region corrects what
// is already there (image_jbig2_refine.go).
//
// Pixels are coded either through the MQ arithmetic coder, which JBIG2
// shares with JPEG 2000, or as MMR — the two-dimensional fax coding.

// decodeJBIG2 decodes an embedded JBIG2 image to one byte per pixel,
// where 1 is black — which is what the format means by a set bit.
func decodeJBIG2(data, globals []byte, width, height int) ([]byte, error) {
	if width <= 0 || height <= 0 {
		return nil, errors.New("gopdf: JBIG2 image has no size")
	}
	if width > 1<<16 || height > 1<<16 || width*height > 1<<28 {
		return nil, errors.New("gopdf: JBIG2 image is implausibly large")
	}
	d := newJBIG2Decoder(newBitmap(width, height))
	d.work += jbig2WorkPerByte * int64(len(data)+len(globals))

	// The globals stream carries the segments shared between images,
	// which for a scanned document is where the symbol dictionary lives:
	// one set of shapes for every page. It is read first for that reason.
	for _, part := range [][]byte{globals, data} {
		segs, err := parseJBIG2Segments(part)
		if err != nil {
			return nil, err
		}
		for _, s := range segs {
			if err := d.segment(s); err != nil {
				return nil, err
			}
		}
	}
	if !d.found {
		return nil, errors.New("gopdf: JBIG2 image has no region to decode")
	}
	return d.page.pix, nil
}

// jbig2Decoder is what the segments of one image share.
type jbig2Decoder struct {
	page *bitmap
	// work is what is left of the budget every loop draws on. The sizes
	// and counts that decide how long decoding takes all come out of the
	// stream, and bounding each against the page is not enough: a
	// thousand page-sized regions are a thousand pages of work. So the
	// image as a whole gets an allowance proportional to its own size —
	// many times what a real scan spends — and a stream that runs
	// through it is refused rather than obeyed.
	work int64

	// Dictionaries are kept by segment number, since a region names the
	// ones it uses and the numbering of its symbols depends on getting
	// exactly those.
	dicts      map[int][]*bitmap
	dictCx     map[int]*symbolContexts
	everything []*bitmap
	tables     map[int]*huffTable
	patterns   map[int][]*bitmap
	// held are the intermediate regions: decoded, not drawn, waiting for
	// the refinement region that names them.
	held  map[int]*bitmap
	found bool
}

func newJBIG2Decoder(page *bitmap) *jbig2Decoder {
	d := &jbig2Decoder{
		page:     page,
		work:     1 << 27,
		dicts:    map[int][]*bitmap{},
		dictCx:   map[int]*symbolContexts{},
		tables:   map[int]*huffTable{},
		patterns: map[int][]*bitmap{},
		held:     map[int]*bitmap{},
	}
	if page != nil {
		d.work = 16*int64(page.w)*int64(page.h) + 1<<20
	}
	return d
}

// jbig2WorkPerByte is the extra work a stream earns per byte of its own
// length. The page-sized allowance is right for what is drawn on the
// page and wrong for a dictionary, which a document scanned in strips
// shares between images much smaller than it. A real dictionary spends
// a hundred or so pixels per byte; this allows several times that, and
// still leaves a stream of forty bytes with nothing to run away on.
const jbig2WorkPerByte = 1024

var errJBIG2Work = errors.New("gopdf: JBIG2 stream asks for far more work " +
	"than an image of its size needs")

// spend draws on the work budget.
func (d *jbig2Decoder) spend(n int64) error {
	if n < 0 {
		return errJBIG2
	}
	d.work -= n
	if d.work < 0 {
		return errJBIG2Work
	}
	return nil
}

// spendArea is spend for a bitmap about to be decoded or drawn.
func (d *jbig2Decoder) spendArea(w, h int) error {
	if w < 0 || h < 0 {
		return errJBIG2
	}
	return d.spend(int64(w) * int64(h))
}

// sideFits reports whether a bitmap that is not a region — a strip of
// symbols, a row of patterns, a halftone grid — is of a size worth
// allocating. Such a thing may be wider than the page, which a region
// may not, so it is bounded by area: several pages' worth and no more.
func (d *jbig2Decoder) sideFits(w, h int) bool {
	if w <= 0 || h <= 0 {
		return false
	}
	limit := int64(1 << 26)
	if d.page != nil {
		limit = 4*int64(d.page.w)*int64(d.page.h) + 1<<20
	}
	return int64(w)*int64(h) <= limit
}

// segment decodes one segment.
func (d *jbig2Decoder) segment(s jbig2Segment) error {
	switch s.kind {
	case 0: // symbol dictionary
		return d.symbolSegment(s)
	case 4, 6, 7: // text region: intermediate, immediate, immediate lossless
		symbols, err := d.symbolsFor(s.refers)
		if err != nil {
			return err
		}
		if len(symbols) == 0 {
			// A region that names nothing is not conforming, and is
			// common enough that refusing would be worse than offering
			// every dictionary in the file.
			symbols = d.everything
		}
		info, region, err := d.textRegion(s.data, symbols, d.customTables(s.refers))
		if err != nil {
			return err
		}
		return d.place(s, info, region)
	case 16: // pattern dictionary
		return d.patternSegment(s)
	case 20, 22, 23: // halftone region
		return d.halftoneSegment(s)
	case 36, 38, 39: // generic region
		return d.genericSegment(s)
	case 40, 42, 43: // generic refinement region
		return d.refinementSegment(s)
	case 48: // page information
		d.pageInfo(s.data)
	case 53: // code table
		// A table that does not parse is only an error for the segment
		// that goes on to use it.
		t, _ := parseHuffTable(s.data)
		d.tables[s.num] = t
	}
	// End of page, end of stripe, end of file, profiles, extensions and
	// anything not yet invented carry no pixels.
	return nil
}

// jbig2MaxSymbols is the most symbols one segment may draw on. A dense
// page has a few thousand; a whole book's dictionary, tens of thousands.
const jbig2MaxSymbols = 1 << 20

// symbolsFor gathers the symbols of the dictionaries a segment names.
// The list is counted before it is built: a segment may name a
// dictionary any number of times, and a list of a million dictionaries
// of a million symbols is not one to start on.
func (d *jbig2Decoder) symbolsFor(refers []int) ([]*bitmap, error) {
	n := 0
	for _, num := range refers {
		if n += len(d.dicts[num]); n > jbig2MaxSymbols {
			return nil, errJBIG2
		}
	}
	if err := d.spend(int64(n)); err != nil {
		return nil, err
	}
	return inputSymbols(d.dicts, refers), nil
}

// customTables gathers the code tables a segment names, in the order it
// names them, which is the order its fields claim them in.
func (d *jbig2Decoder) customTables(refers []int) []*huffTable {
	var out []*huffTable
	for _, num := range refers {
		if t, ok := d.tables[num]; ok {
			out = append(out, t)
		}
	}
	return out
}

// pageInfo reads the one thing the page information segment says that
// the image dictionary does not: the colour of a page nothing has been
// drawn on yet.
func (d *jbig2Decoder) pageInfo(data []byte) {
	if len(data) < 17 || d.found {
		return
	}
	if data[16]&4 != 0 {
		for i := range d.page.pix {
			d.page.pix[i] = 1
		}
	}
}

// jbig2Region is the field every region segment opens with: where the
// region goes and how it combines with what is there.
type jbig2Region struct {
	w, h, x, y int
	op         int
}

func parseRegionInfo(data []byte) (jbig2Region, bool) {
	if len(data) < 17 {
		return jbig2Region{}, false
	}
	return jbig2Region{
		w: int(be32(data, 0)), h: int(be32(data, 4)),
		x: int(int32(be32(data, 8))), y: int(int32(be32(data, 12))),
		op: int(data[16] & 7),
	}, true
}

// place finishes a region segment: an immediate region goes onto the
// page, an intermediate one is held for the refinement that names it.
func (d *jbig2Decoder) place(s jbig2Segment, info jbig2Region, region *bitmap) error {
	switch s.kind {
	case 4, 20, 36, 40:
		d.held[s.num] = region
		return nil
	}
	if err := d.spendArea(region.w, region.h); err != nil {
		return err
	}
	composeBitmap(d.page, region, info.x, info.y, info.op)
	d.found = true
	return nil
}

// The combination operators.
const (
	jbig2OR = iota
	jbig2AND
	jbig2XOR
	jbig2XNOR
	jbig2REPLACE
)

// composeBitmap combines src onto dst with its top left corner at
// (x0, y0), clipping what falls outside.
func composeBitmap(dst, src *bitmap, x0, y0, op int) {
	for sy := 0; sy < src.h; sy++ {
		y := y0 + sy
		if y < 0 || y >= dst.h {
			continue
		}
		row := src.pix[sy*src.w : (sy+1)*src.w]
		out := dst.pix[y*dst.w : (y+1)*dst.w]
		for sx, v := range row {
			x := x0 + sx
			if x < 0 || x >= dst.w {
				continue
			}
			switch op {
			case jbig2AND:
				out[x] &= v
			case jbig2XOR:
				out[x] ^= v
			case jbig2XNOR:
				out[x] = 1 ^ out[x] ^ v
			case jbig2REPLACE:
				out[x] = v
			default:
				out[x] |= v
			}
		}
	}
}

// inputSymbols gathers the symbols of the dictionaries a segment names,
// in the order it names them, which is the order their identifiers run
// in.
func inputSymbols(dicts map[int][]*bitmap, refers []int) []*bitmap {
	var out []*bitmap
	for _, num := range refers {
		out = append(out, dicts[num]...)
	}
	return out
}

// bitmap is one byte per pixel, 1 for black.
type bitmap struct {
	w, h int
	pix  []byte
}

func newBitmap(w, h int) *bitmap {
	return &bitmap{w: w, h: h, pix: make([]byte, w*h)}
}

// at reads a pixel, treating everything outside the bitmap as white,
// which is what the decoding procedure asks for.
func (b *bitmap) at(x, y int) byte {
	if x < 0 || y < 0 || x >= b.w || y >= b.h {
		return 0
	}
	return b.pix[y*b.w+x]
}

func (b *bitmap) set(x, y int, v byte) {
	if x < 0 || y < 0 || x >= b.w || y >= b.h {
		return
	}
	b.pix[y*b.w+x] = v
}

// jbig2Segment is one segment of the embedded stream.
type jbig2Segment struct {
	num int
	// refers lists the segments this one draws on. A text region names
	// the symbol dictionaries whose shapes it uses, and using anything
	// else numbers the shapes differently: the symbol identifiers are
	// indices into exactly the dictionaries named, so a region given one
	// dictionary too many reads every identifier at the wrong width.
	refers []int
	kind   int
	data   []byte
	// unknownLength marks a generic region whose header gave no length:
	// its data ends with a row count the header's height stands in for.
	unknownLength bool
}

// parseJBIG2Segments reads the segment headers of an embedded stream.
//
// The embedded format has no file header and no page association
// beyond what the segments say, so this is the whole of the framing.
func parseJBIG2Segments(data []byte) ([]jbig2Segment, error) {
	var out []jbig2Segment
	for p := 0; p+11 <= len(data); {
		// Segment number, then the flags byte whose low bits are the type.
		flags := data[p+4]
		kind := int(flags & 0x3F)
		pageAssoc4 := flags&0x40 != 0
		q := p + 5

		// The referred-to segments: a short form for up to four, and a
		// long form with a count and a retain bitmap.
		rt := data[q]
		count := int(rt >> 5)
		longForm := count == 7
		if count == 7 {
			if q+4 > len(data) {
				return nil, errJBIG2
			}
			count = int(be32(data, q)) & 0x1FFFFFFF
			if count < 0 || count > 1<<20 {
				return nil, errJBIG2
			}
			q += 4 + (count+8)/8
		} else {
			q++
		}
		// Each reference is one, two or four bytes, by segment number.
		segNum := be32(data, p)
		refSize := 1
		switch {
		case segNum > 65536:
			refSize = 4
		case segNum > 256:
			refSize = 2
		}
		refStart := q
		q += count * refSize
		refers := make([]int, 0, count)
		for i := 0; i < count; i++ {
			at := refStart + i*refSize
			if at+refSize > len(data) {
				return nil, errJBIG2
			}
			switch refSize {
			case 1:
				refers = append(refers, int(data[at]))
			case 2:
				refers = append(refers, int(be16(data, at)))
			default:
				refers = append(refers, int(be32(data, at)))
			}
		}
		_ = longForm
		if pageAssoc4 {
			q += 4
		} else {
			q++
		}
		if q+4 > len(data) {
			return nil, errJBIG2
		}
		length := int(be32(data, q))
		q += 4
		unknown := false
		if uint32(length) == 0xFFFFFFFF {
			// A length nobody wrote down is allowed for one kind of
			// segment, the immediate generic region, which an encoder
			// may start before it knows how many rows are coming. Its
			// end is found by looking for the bytes its coding finishes
			// with; four bytes of row count follow them.
			if kind != 38 {
				return nil, errors.New("gopdf: JBIG2 segment of unknown length")
			}
			if q+18 > len(data) {
				return nil, errJBIG2
			}
			// The search starts where the coded data does: past the
			// template pixels an arithmetic region lists first.
			end, from := []byte{0xFF, 0xAC}, 18+2
			switch gflags := data[q+17]; {
			case gflags&1 != 0:
				end, from = []byte{0x00, 0x00}, 18 // MMR
			case gflags&6 == 0:
				from = 18 + 8 // template 0 has four pixels
			}
			if q+from > len(data) {
				return nil, errJBIG2
			}
			at := bytes.Index(data[q+from:], end)
			if at < 0 || q+from+at+6 > len(data) {
				return nil, errJBIG2
			}
			length = from + at + 6
			unknown = true
		}
		if length < 0 || q+length > len(data) {
			return nil, errJBIG2
		}
		out = append(out, jbig2Segment{
			num: int(segNum), refers: refers, kind: kind, data: data[q : q+length],
			unknownLength: unknown,
		})
		p = q + length
		if len(out) > 1<<16 {
			return nil, errJBIG2
		}
	}
	return out, nil
}

var errJBIG2 = errors.New("gopdf: malformed JBIG2 stream")

// regionFits reports whether a region's declared size is one worth
// decoding onto this page.
//
// The size comes out of the stream and the decoder then walks every
// pixel of it, so a number nobody checked is a number that decides how
// long this takes. An absolute cap alone is too loose to help: at 268
// million pixels — which is what a cap of 1<<28 allows — forty-three
// bytes of input kept the decoder busy for thirteen seconds and then
// returned an error.
//
// The page is the real bound. It is the image's own /Width and /Height,
// which the caller has from the document, and a region larger than the
// image it is being composited onto has nothing to contribute: whatever
// hangs over the edge is clipped. So a region is allowed to be as large
// as the page and no larger.
func regionFits(w, h int, page *bitmap) bool {
	if w <= 0 || h <= 0 {
		return false
	}
	if page == nil {
		return w <= 1<<16 && h <= 1<<16 && w*h <= 1<<26
	}
	return w <= page.w && h <= page.h
}

// genericSegment decodes a generic region segment.
func (d *jbig2Decoder) genericSegment(s jbig2Segment) error {
	data := s.data
	info, ok := parseRegionInfo(data)
	if !ok || len(data) < 18 {
		return errJBIG2
	}
	flags := data[17]
	p := 18
	mmr := flags&1 != 0
	template := int(flags>>1) & 3
	tpgdon := flags&8 != 0

	if s.unknownLength {
		// The rows actually coded, which the header could only guess.
		if len(data) < p+4 {
			return errJBIG2
		}
		if rows := int(be32(data, len(data)-4)); rows < info.h {
			info.h = rows
		}
		data = data[:len(data)-4]
	}
	if !regionFits(info.w, info.h, d.page) {
		return errJBIG2
	}
	if err := d.spendArea(info.w, info.h); err != nil {
		return err
	}

	var region *bitmap
	if mmr {
		pix, _, err := ccitt.DecodeMMR(data[p:], info.w, info.h)
		if err != nil {
			return jbig2MMRError(err)
		}
		region = &bitmap{w: info.w, h: info.h, pix: pix}
	} else {
		if flags&0x10 != 0 {
			return errors.New("gopdf: JBIG2 generic region uses the extended " +
				"template, which is not decoded")
		}
		at, ok := genericAT(data, &p, template)
		if !ok {
			return errJBIG2
		}
		region = newBitmap(info.w, info.h)
		dec := newMQDecoder(data[p:])
		cx := make([]mqContext, 1<<16)
		if err := decodeGenericRegion(region, dec, cx, template, at, tpgdon); err != nil {
			return err
		}
	}
	return d.place(s, info, region)
}

// jbig2MMRError reports MMR data that does not decode.
func jbig2MMRError(err error) error {
	return fmt.Errorf("%w (%v)", errJBIG2, err)
}

// genericAT reads the adaptive template pixels of a generic region:
// four for template 0, one otherwise.
func genericAT(data []byte, p *int, template int) ([][2]int, bool) {
	n := 1
	if template == 0 {
		n = 4
	}
	if *p+2*n > len(data) {
		return nil, false
	}
	at := make([][2]int, n)
	for i := range at {
		at[i] = [2]int{int(int8(data[*p])), int(int8(data[*p+1]))}
		*p += 2
	}
	return at, true
}

// refinementSegment decodes a generic refinement region segment: a
// correction to a region decoded earlier, or to the page as it stands.
func (d *jbig2Decoder) refinementSegment(s jbig2Segment) error {
	data := s.data
	info, ok := parseRegionInfo(data)
	if !ok || len(data) < 18 {
		return errJBIG2
	}
	flags := data[17]
	p := 18
	template := int(flags & 1)
	tpgron := flags&2 != 0
	at, ok := refinementAT(data, &p, template)
	if !ok || !regionFits(info.w, info.h, d.page) {
		return errJBIG2
	}
	if err := d.spend(2 * int64(info.w) * int64(info.h)); err != nil {
		return err
	}

	// What is refined is the region the segment names, if it names one.
	var ref *bitmap
	for _, num := range s.refers {
		if held, ok := d.held[num]; ok {
			ref = held
			delete(d.held, num)
			break
		}
	}
	if ref == nil {
		// Otherwise it is the page itself, under this region.
		ref = newBitmap(info.w, info.h)
		for y := 0; y < info.h; y++ {
			for x := 0; x < info.w; x++ {
				ref.pix[y*info.w+x] = d.page.at(info.x+x, info.y+y)
			}
		}
	} else if ref.w != info.w || ref.h != info.h {
		return errJBIG2
	}

	region := newBitmap(info.w, info.h)
	dec := newMQDecoder(data[p:])
	cx := make([]mqContext, refinementContexts(template))
	decodeRefinement(region, dec, cx, template, ref, 0, 0, tpgron, at)
	return d.place(s, info, region)
}

// decodeGenericRegion is the arithmetic decoding procedure: each pixel's
// context is the neighbours already decoded, and the coder is asked what
// comes next.
func decodeGenericRegion(b *bitmap, dec *mqDecoder, cx []mqContext,
	template int, at [][2]int, tpgdon bool) error {
	return decodeGenericRegionSkip(b, dec, cx, template, at, tpgdon, nil)
}

// decodeGenericRegionSkip is decodeGenericRegion with a mask of pixels
// that are not coded at all and stay white, which a halftone region
// uses for the parts of its grid that fall outside it.
func decodeGenericRegionSkip(b *bitmap, dec *mqDecoder, cx []mqContext,
	template int, at [][2]int, tpgdon bool, skip *bitmap) error {

	ltp := false
	for y := 0; y < b.h; y++ {
		if tpgdon {
			// A row identical to the one above is coded as a single bit,
			// which is most of why the format is small: a scan is mostly
			// white and mostly repeats.
			ctx := tpgdonContext(template)
			if dec.decode(&cx[ctx]) == 1 {
				ltp = !ltp
			}
			if ltp {
				if y > 0 {
					copy(b.pix[y*b.w:(y+1)*b.w], b.pix[(y-1)*b.w:y*b.w])
				}
				continue
			}
		}
		for x := 0; x < b.w; x++ {
			if skip != nil && skip.at(x, y) != 0 {
				continue
			}
			ctx := genericContext(b, x, y, template, at)
			b.set(x, y, byte(dec.decode(&cx[ctx])))
		}
	}
	return nil
}

// tpgdonContext is the fixed context the typical-prediction bit uses.
func tpgdonContext(template int) int {
	switch template {
	case 0:
		return 0x9B25
	case 1:
		return 0x0795
	case 2:
		return 0x00E5
	default:
		return 0x0195
	}
}

// genericContext builds a pixel's context from its already-decoded
// neighbours, in the order the templates define.
func genericContext(b *bitmap, x, y, template int, at [][2]int) int {
	px := func(dx, dy int) int { return int(b.at(x+dx, y+dy)) }
	a := func(i int) int { return int(b.at(x+at[i][0], y+at[i][1])) }

	switch template {
	case 0:
		return px(-1, -2)<<15 | px(0, -2)<<14 | px(1, -2)<<13 |
			px(-2, -1)<<12 | px(-1, -1)<<11 | px(0, -1)<<10 |
			px(1, -1)<<9 | px(2, -1)<<8 |
			px(-4, 0)<<7 | px(-3, 0)<<6 | px(-2, 0)<<5 | px(-1, 0)<<4 |
			a(0)<<3 | a(1)<<2 | a(2)<<1 | a(3)
	case 1:
		return px(-1, -2)<<12 | px(0, -2)<<11 | px(1, -2)<<10 | px(2, -2)<<9 |
			px(-2, -1)<<8 | px(-1, -1)<<7 | px(0, -1)<<6 | px(1, -1)<<5 |
			px(2, -1)<<4 |
			px(-3, 0)<<3 | px(-2, 0)<<2 | px(-1, 0)<<1 | a(0)
	case 2:
		return px(-1, -2)<<9 | px(0, -2)<<8 | px(1, -2)<<7 |
			px(-2, -1)<<6 | px(-1, -1)<<5 | px(0, -1)<<4 | px(1, -1)<<3 |
			px(-2, 0)<<2 | px(-1, 0)<<1 | a(0)
	default:
		return px(-3, -1)<<9 | px(-2, -1)<<8 | px(-1, -1)<<7 | px(0, -1)<<6 |
			px(1, -1)<<5 |
			px(-4, 0)<<4 | px(-3, 0)<<3 | px(-2, 0)<<2 | px(-1, 0)<<1 | a(0)
	}
}

// --- the MQ arithmetic decoder ---

// mqContext is one adaptive probability state.
type mqContext struct {
	index uint8
	mps   uint8
}

// mqState is the probability estimation table the coder is defined by:
// the estimate, where to go on a more or less probable symbol, and
// whether the sense switches.
type mqState struct {
	qe         uint32
	nmps, nlps uint8
	sw         uint8
}

var mqStates = [47]mqState{
	{0x5601, 1, 1, 1}, {0x3401, 2, 6, 0}, {0x1801, 3, 9, 0}, {0x0AC1, 4, 12, 0},
	{0x0521, 5, 29, 0}, {0x0221, 38, 33, 0}, {0x5601, 7, 6, 1}, {0x5401, 8, 14, 0},
	{0x4801, 9, 14, 0}, {0x3801, 10, 14, 0}, {0x3001, 11, 17, 0}, {0x2401, 12, 18, 0},
	{0x1C01, 13, 20, 0}, {0x1601, 29, 21, 0}, {0x5601, 15, 14, 1}, {0x5401, 16, 14, 0},
	{0x5101, 17, 15, 0}, {0x4801, 18, 16, 0}, {0x3801, 19, 17, 0}, {0x3401, 20, 18, 0},
	{0x3001, 21, 19, 0}, {0x2801, 22, 19, 0}, {0x2401, 23, 20, 0}, {0x2201, 24, 21, 0},
	{0x1C01, 25, 22, 0}, {0x1801, 26, 23, 0}, {0x1601, 27, 24, 0}, {0x1401, 28, 25, 0},
	{0x1201, 29, 26, 0}, {0x1101, 30, 27, 0}, {0x0AC1, 31, 28, 0}, {0x09C1, 32, 29, 0},
	{0x08A1, 33, 30, 0}, {0x0521, 34, 31, 0}, {0x0441, 35, 32, 0}, {0x02A1, 36, 33, 0},
	{0x0221, 37, 34, 0}, {0x0141, 38, 35, 0}, {0x0111, 39, 36, 0}, {0x0085, 40, 37, 0},
	{0x0049, 41, 38, 0}, {0x0025, 42, 39, 0}, {0x0015, 43, 40, 0}, {0x0009, 44, 41, 0},
	{0x0005, 45, 42, 0}, {0x0001, 45, 43, 0}, {0x5601, 46, 46, 0},
}

// mqDecoder is the arithmetic decoder JBIG2 and JPEG 2000 share.
type mqDecoder struct {
	data []byte
	bp   int
	c    uint32
	a    uint32
	ct   int
}

func newMQDecoder(data []byte) *mqDecoder {
	d := &mqDecoder{data: data}
	d.c = uint32(d.byteAt(0)) << 16
	d.byteIn()
	d.c <<= 7
	d.ct -= 7
	d.a = 0x8000
	return d
}

// byteAt reads a byte of the coded data, and 0xFF past the end — which
// is how the coder is defined to run off the end of its own stream.
func (d *mqDecoder) byteAt(i int) byte {
	if i < 0 || i >= len(d.data) {
		return 0xFF
	}
	return d.data[i]
}

func (d *mqDecoder) byteIn() {
	if d.byteAt(d.bp) == 0xFF {
		if d.byteAt(d.bp+1) > 0x8F {
			d.c += 0xFF00
			d.ct = 8
		} else {
			d.bp++
			d.c += uint32(d.byteAt(d.bp)) << 9
			d.ct = 7
		}
	} else {
		d.bp++
		d.c += uint32(d.byteAt(d.bp)) << 8
		d.ct = 8
	}
}

// decode returns the next bit under a context.
func (d *mqDecoder) decode(cx *mqContext) int {
	st := &mqStates[cx.index]
	qe := st.qe
	d.a -= qe

	var bit int
	if (d.c >> 16) < qe {
		// The less probable path.
		if d.a < qe {
			d.a = qe
			bit = int(cx.mps)
			cx.index = st.nmps
		} else {
			d.a = qe
			bit = int(1 - cx.mps)
			if st.sw == 1 {
				cx.mps = 1 - cx.mps
			}
			cx.index = st.nlps
		}
	} else {
		d.c -= qe << 16
		if d.a&0x8000 != 0 {
			return int(cx.mps)
		}
		if d.a < qe {
			bit = int(1 - cx.mps)
			if st.sw == 1 {
				cx.mps = 1 - cx.mps
			}
			cx.index = st.nlps
		} else {
			bit = int(cx.mps)
			cx.index = st.nmps
		}
	}
	// Renormalize.
	for {
		if d.ct == 0 {
			d.byteIn()
		}
		d.a <<= 1
		d.c <<= 1
		d.ct--
		if d.a&0x8000 != 0 {
			break
		}
	}
	return bit
}

// jbig2Globals reads the /JBIG2Globals stream a decode-parms dictionary
// may point at.
func jbig2Globals(r *Reader, parms any) []byte {
	switch t := r.resolve(parms).(type) {
	case Dict:
		if stm, ok := r.resolve(t["JBIG2Globals"]).(*rawStream); ok {
			data, err := r.decodeStream(stm.dict, stm.data)
			if err == nil {
				return data
			}
		}
	case Array:
		for _, e := range t {
			if g := jbig2Globals(r, e); g != nil {
				return g
			}
		}
	}
	return nil
}

// jbig2Error wraps a decoding failure with what was being read.
func jbig2Error(err error) error {
	return fmt.Errorf("gopdf: JBIG2: %w", err)
}

// decodeJBIG2Image turns an embedded JBIG2 stream into a picture.
//
// The format's set bit means black, which is the opposite of the grey
// this package hands back, so the sense is inverted on the way out —
// unless the image's own /Decode array already asks for that, in which
// case the two cancel.
func (im ImageRef) decodeJBIG2Image(filters []Name, parms []any) (image.Image, error) {
	data, err := im.unwrapOuter(filters)
	if err != nil {
		return nil, err
	}
	var parm any
	if i := len(filters) - 1; i < len(parms) {
		parm = parms[i]
	}
	globals := jbig2Globals(im.r, parm)

	pix, err := decodeJBIG2(data, globals, im.Width, im.Height)
	if err != nil {
		return nil, jbig2Error(err)
	}
	inverted := false
	if dec := floatArray(im.r, im.stream.dict["Decode"]); len(dec) == 2 && dec[0] == 1 {
		inverted = true
	}
	out := image.NewGray(image.Rect(0, 0, im.Width, im.Height))
	for i, v := range pix {
		black := v != 0
		if inverted {
			black = !black
		}
		if black {
			out.Pix[i] = 0
		} else {
			out.Pix[i] = 0xFF
		}
	}
	return out, nil
}
