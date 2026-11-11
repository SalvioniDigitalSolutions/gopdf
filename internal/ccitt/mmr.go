package ccitt

import "errors"

// MMR — the two-dimensional fax coding of ITU-T T.6 — as JBIG2 uses it.
//
// The reader in this package decodes a whole fax image from an io.Reader
// and says nothing about where it stopped. JBIG2 needs exactly that: a
// symbol dictionary and a halftone region hold several MMR bitmaps one
// after the other, each beginning where the last one ended. So this is a
// second, smaller decoder over a byte slice, built on the same code
// tables, which reports how many bytes it consumed.
//
// It differs from a fax page in three ways the JBIG2 specification
// lists: a set pixel is black, the end-of-block code may be missing when
// the length of the data is known from outside, and the data is padded
// to a whole byte.

var (
	errMMRTruncated = errors.New("ccitt: MMR data ends inside a code")
	errMMRInvalid   = errors.New("ccitt: invalid MMR code")
	errMMRBounds    = errors.New("ccitt: invalid MMR bounds")
)

// mmrBits reads bits most significant first from a slice.
type mmrBits struct {
	data []byte
	pos  int // in bits
}

func (b *mmrBits) bit() (int, error) {
	i := b.pos >> 3
	if i >= len(b.data) {
		return 0, errMMRTruncated
	}
	v := int(b.data[i]>>(7-uint(b.pos&7))) & 1
	b.pos++
	return v, nil
}

// peek24 returns the next 24 bits without consuming them, and false when
// fewer than 24 remain.
func (b *mmrBits) peek24() (uint32, bool) {
	if b.pos+24 > len(b.data)*8 {
		return 0, false
	}
	var v uint32
	for i := 0; i < 24; i++ {
		p := b.pos + i
		v = v<<1 | uint32(b.data[p>>3]>>(7-uint(p&7)))&1
	}
	return v, true
}

// walk follows one of the package's decode trees to a leaf.
func (b *mmrBits) walk(table [][2]int16) (int, error) {
	state := int16(1)
	for {
		bit, err := b.bit()
		if err != nil {
			return 0, err
		}
		state = table[state][bit]
		if state < 0 {
			return int(^state), nil
		}
		if state == 0 {
			return 0, errMMRInvalid
		}
	}
}

// run reads one run length: any number of make-up codes and the
// terminating code that ends them.
func (b *mmrBits) run(black bool, limit int) (int, error) {
	table := whiteDecodeTable[:]
	if black {
		table = blackDecodeTable[:]
	}
	total := 0
	for {
		n, err := b.walk(table)
		if err != nil {
			return 0, err
		}
		total += n
		if total > limit {
			return 0, errMMRBounds
		}
		if n < 64 {
			return total, nil
		}
	}
}

const mmrEOFB = 0x001001 // two end-of-line codes

// DecodeMMR decodes height rows of width pixels of T.6 data, one byte
// per pixel with 1 for black, and reports how many bytes of data the
// bitmap occupied — including an end-of-block code if one follows it,
// and the padding to a whole byte.
func DecodeMMR(data []byte, width, height int) (pix []byte, consumed int, err error) {
	if width <= 0 || height <= 0 || width > maxWidth || height > maxWidth ||
		width*height > 1<<28 {
		return nil, 0, errMMRBounds
	}
	pix = make([]byte, width*height)
	b := &mmrBits{data: data}
	ref := make([]byte, width) // the line above the first is white

	// changing reports the first changing element on the reference line
	// at or after from: a pixel differing from the one to its left.
	changing := func(from int) int {
		for i := from; i < width; i++ {
			left := byte(0)
			if i > 0 {
				left = ref[i-1]
			}
			if ref[i] != left {
				return i
			}
		}
		return width
	}

rows:
	for y := 0; y < height; y++ {
		cur := pix[y*width : (y+1)*width]
		a0, color := -1, byte(0)
		fill := func(to int, c byte) {
			from := a0
			if from < 0 {
				from = 0
			}
			for i := from; i < to; i++ {
				cur[i] = c
			}
		}
		// b1 is the first changing element right of a0 whose colour is
		// the opposite of the one being run; b2 is the next after it.
		// They are looked for only by the modes that use them, so the
		// scanning a row costs stays in step with the progress made.
		findB := func() (b1, b2 int) {
			b1 = changing(a0 + 1)
			for b1 < width && ref[b1] == color {
				b1 = changing(b1 + 1)
			}
			b2 = width
			if b1 < width {
				b2 = changing(b1 + 1)
			}
			return b1, b2
		}
		for a0 < width {
			if v, ok := b.peek24(); ok && v == mmrEOFB {
				// The block ends early; what was not coded is white.
				break rows
			}
			mode, err := b.walk(modeDecodeTable[:])
			if err != nil {
				return nil, 0, err
			}
			// Every code has to move the position on. One that does not
			// is malformed, and accepting it would let the length of
			// the data, not the size of the bitmap, decide the work.
			before := a0
			switch mode {
			case modePass:
				_, b2 := findB()
				fill(b2, color)
				a0 = b2
			case modeH:
				start := a0
				if start < 0 {
					start = 0
				}
				r1, err := b.run(color == 1, width-start)
				if err != nil {
					return nil, 0, err
				}
				r2, err := b.run(color == 0, width-start-r1)
				if err != nil {
					return nil, 0, err
				}
				fill(start+r1, color)
				a0 = start + r1
				fill(a0+r2, 1-color)
				a0 += r2
			case modeV0, modeVR1, modeVR2, modeVR3, modeVL1, modeVL2, modeVL3:
				delta := 0
				switch mode {
				case modeVR1:
					delta = 1
				case modeVR2:
					delta = 2
				case modeVR3:
					delta = 3
				case modeVL1:
					delta = -1
				case modeVL2:
					delta = -2
				case modeVL3:
					delta = -3
				}
				b1, _ := findB()
				a1 := b1 + delta
				if a1 > width || a1 < 0 {
					return nil, 0, errMMRBounds
				}
				fill(a1, color)
				a0 = a1
				color = 1 - color
			default:
				return nil, 0, errMMRInvalid
			}
			if a0 <= before {
				return nil, 0, errMMRInvalid
			}
		}
		ref = cur
	}
	if v, ok := b.peek24(); ok && v == mmrEOFB {
		b.pos += 24
	}
	return pix, (b.pos + 7) / 8, nil
}

// mmrWriter collects bits most significant first.
type mmrWriter struct {
	out  []byte
	nbit uint
}

func (w *mmrWriter) put(s bitString) {
	for i := int(s.nBits) - 1; i >= 0; i-- {
		if w.nbit == 0 {
			w.out = append(w.out, 0)
		}
		if s.bits>>uint(i)&1 != 0 {
			w.out[len(w.out)-1] |= 0x80 >> w.nbit
		}
		w.nbit = (w.nbit + 1) & 7
	}
}

func (w *mmrWriter) run(n int, black bool) {
	t2, t3 := whiteEncodeTable2[:], whiteEncodeTable3[:]
	if black {
		t2, t3 = blackEncodeTable2[:], blackEncodeTable3[:]
	}
	for n >= 2560 {
		w.put(t3[39])
		n -= 2560
	}
	if n >= 64 {
		w.put(t3[n/64-1])
		n %= 64
	}
	w.put(t2[n])
}

// EncodeMMR codes a bitmap — one byte per pixel, 1 for black — as T.6
// data, with or without the end-of-block code, padded to a whole byte.
// It is the inverse of DecodeMMR and exists so that decoder can be
// tested against data it did not itself define the meaning of: the
// output is what any Group 4 reader accepts.
func EncodeMMR(pix []byte, width, height int, eofb bool) []byte {
	w := &mmrWriter{}
	ref := make([]byte, width)
	for y := 0; y < height; y++ {
		cur := pix[y*width : (y+1)*width]
		changingRef := func(from int) int {
			for i := from; i < width; i++ {
				left := byte(0)
				if i > 0 {
					left = ref[i-1]
				}
				if ref[i] != left {
					return i
				}
			}
			return width
		}
		// next reports the first pixel at or after from that is not c.
		next := func(from int, c byte) int {
			for i := from; i < width; i++ {
				if cur[i] != c {
					return i
				}
			}
			return width
		}
		a0, color := -1, byte(0)
		for a0 < width {
			start := a0
			if start < 0 {
				start = 0
			}
			a1 := next(start, color)
			b1 := changingRef(a0 + 1)
			for b1 < width && ref[b1] == color {
				b1 = changingRef(b1 + 1)
			}
			b2 := width
			if b1 < width {
				b2 = changingRef(b1 + 1)
			}
			switch d := a1 - b1; {
			case b2 < a1:
				w.put(modeEncodeTable[modePass])
				a0 = b2
			case d >= -3 && d <= 3:
				mode := modeV0
				switch d {
				case 1:
					mode = modeVR1
				case 2:
					mode = modeVR2
				case 3:
					mode = modeVR3
				case -1:
					mode = modeVL1
				case -2:
					mode = modeVL2
				case -3:
					mode = modeVL3
				}
				w.put(modeEncodeTable[mode])
				a0 = a1
				color = 1 - color
			default:
				a2 := next(a1, 1-color)
				w.put(modeEncodeTable[modeH])
				w.run(a1-start, color == 1)
				w.run(a2-a1, color == 0)
				a0 = a2
			}
		}
		ref = cur
	}
	if eofb {
		w.put(bitString{mmrEOFB, 24})
	}
	return w.out
}
