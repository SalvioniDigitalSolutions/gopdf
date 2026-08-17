package gopdf

import (
	"errors"
	"fmt"
	"image"
)

// JBIG2 generic regions.
//
// JBIG2 is what a scanner reaches for when a page is black and white: it
// beats fax coding by a wide margin and is what most scanned PDFs of the
// last twenty years are made of. Undecoded, such a page is a hole — no
// text can be read out of it, no pixel can be scrubbed, and redaction
// has to remove the whole scan rather than a rectangle of it.
//
// The format has two halves. A generic region is the image coded
// directly, pixel by pixel, through an arithmetic coder driven by a
// template of neighbours already decoded — that is what is here, and it
// is what a scanner in its ordinary mode produces. The other half is the
// symbol dictionary, which finds the repeated shapes on a page and codes
// each once; a file using one is reported rather than guessed at.
//
// The arithmetic coder is the MQ coder, shared with JPEG 2000.

// decodeJBIG2 decodes an embedded JBIG2 image to one byte per pixel,
// where 1 is black — which is what the format means by a set bit.
func decodeJBIG2(data, globals []byte, width, height int) ([]byte, error) {
	if width <= 0 || height <= 0 {
		return nil, errors.New("gopdf: JBIG2 image has no size")
	}
	if width > 1<<16 || height > 1<<16 || width*height > 1<<28 {
		return nil, errors.New("gopdf: JBIG2 image is implausibly large")
	}
	page := newBitmap(width, height)
	found := false

	// The globals stream carries segments shared between images; a
	// generic region needs nothing from it, but a file may put its page
	// information there and it costs nothing to read.
	for _, part := range [][]byte{globals, data} {
		segs, err := parseJBIG2Segments(part)
		if err != nil {
			return nil, err
		}
		for _, s := range segs {
			switch s.kind {
			case 36, 38, 39: // immediate generic region, and its variants
				if err := decodeGenericSegment(s.data, page); err != nil {
					return nil, err
				}
				found = true
			case 0, 4, 6, 7: // symbol dictionary and text regions
				return nil, errors.New("gopdf: JBIG2 image uses a symbol " +
					"dictionary, which is not decoded")
			}
		}
	}
	if !found {
		return nil, errors.New("gopdf: JBIG2 image has no generic region")
	}
	return page.pix, nil
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
	kind int
	data []byte
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
		q += count * refSize
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
		if length == -1 || uint32(length) == 0xFFFFFFFF {
			// An unknown-length segment is only allowed for one kind of
			// generic region and needs a scan for its terminator; the
			// rest of the stream is not readable without it.
			return nil, errors.New("gopdf: JBIG2 segment of unknown length")
		}
		if length < 0 || q+length > len(data) {
			return nil, errJBIG2
		}
		out = append(out, jbig2Segment{kind: kind, data: data[q : q+length]})
		p = q + length
		if len(out) > 1<<16 {
			return nil, errJBIG2
		}
	}
	return out, nil
}

var errJBIG2 = errors.New("gopdf: malformed JBIG2 stream")

// decodeGenericSegment decodes one immediate generic region onto a page.
func decodeGenericSegment(data []byte, page *bitmap) error {
	// The region segment information field: position, size and the
	// operator combining it with the page.
	if len(data) < 18 {
		return errJBIG2
	}
	w, h := int(be32(data, 0)), int(be32(data, 4))
	x, y := int(be32(data, 8)), int(be32(data, 12))
	p := 17

	if len(data) <= p {
		return errJBIG2
	}
	flags := data[p]
	p++
	mmr := flags&1 != 0
	template := int(flags>>1) & 3
	tpgdon := flags&8 != 0

	if mmr {
		return errors.New("gopdf: JBIG2 generic region uses MMR coding, " +
			"which is not decoded")
	}
	if w <= 0 || h <= 0 || w > 1<<16 || h > 1<<16 || w*h > 1<<28 {
		return errJBIG2
	}

	// The adaptive template pixels: four for template 0, one otherwise.
	nAT := 1
	if template == 0 {
		nAT = 4
	}
	at := make([][2]int, nAT)
	for i := 0; i < nAT; i++ {
		if p+2 > len(data) {
			return errJBIG2
		}
		at[i] = [2]int{int(int8(data[p])), int(int8(data[p+1]))}
		p += 2
	}

	region := newBitmap(w, h)
	dec := newMQDecoder(data[p:])
	cx := make([]mqContext, 1<<16)
	if err := decodeGenericRegion(region, dec, cx, template, at, tpgdon); err != nil {
		return err
	}
	// Combined onto the page by OR, which is what a scanner's single
	// region uses and the only operator that matters for one.
	for ry := 0; ry < h; ry++ {
		for rx := 0; rx < w; rx++ {
			if region.at(rx, ry) != 0 {
				page.set(x+rx, y+ry, 1)
			}
		}
	}
	return nil
}

// decodeGenericRegion is the arithmetic decoding procedure: each pixel's
// context is the neighbours already decoded, and the coder is asked what
// comes next.
func decodeGenericRegion(b *bitmap, dec *mqDecoder, cx []mqContext,
	template int, at [][2]int, tpgdon bool) error {

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
