package gopdf

import "sync"

// JBIG2 Huffman coding.
//
// Every number a symbol dictionary or a text region holds can be coded
// two ways. One is the arithmetic integer coder; the other is this: a
// prefix code choosing a range of values, then as many plain bits as the
// range needs to say which. It compresses a little worse and decodes a
// great deal faster, which is why the encoders built into scanners and
// multifunction devices favour it.
//
// The specification defines fifteen tables and lets a file bring its
// own. All of them are described the same way — a list of lines, each a
// range of values with the length of its prefix — and the prefixes
// themselves are not stored: they are assigned from the lengths by one
// fixed rule, so a table is fully given by its lengths.

// jbig2Bits reads plain bits, most significant first.
type jbig2Bits struct {
	data []byte
	pos  int // in bits
	// bad is set when a read ran past the end of the data or met a code
	// no table line has. Reads go on returning zeros after that, so a
	// caller checks once per step rather than after every bit.
	bad bool
}

func (b *jbig2Bits) bit() int {
	i := b.pos >> 3
	if i >= len(b.data) {
		b.bad = true
		return 0
	}
	v := int(b.data[i]>>(7-uint(b.pos&7))) & 1
	b.pos++
	return v
}

// read returns the next n bits, n at most 32.
func (b *jbig2Bits) read(n int) uint32 {
	var v uint32
	for i := 0; i < n; i++ {
		v = v<<1 | uint32(b.bit())
	}
	return v
}

// align skips to the next byte boundary.
func (b *jbig2Bits) align() { b.pos = (b.pos + 7) &^ 7 }

// take aligns to a byte boundary, returns the next n bytes and steps
// over them.
func (b *jbig2Bits) take(n int) []byte {
	b.align()
	i := b.pos >> 3
	if n < 0 || i > len(b.data) || n > len(b.data)-i {
		b.bad = true
		return nil
	}
	b.pos += n * 8
	return b.data[i : i+n]
}

// rest aligns to a byte boundary and returns everything after it.
func (b *jbig2Bits) rest() []byte {
	b.align()
	i := b.pos >> 3
	if i >= len(b.data) {
		return nil
	}
	return b.data[i:]
}

// The kinds of line a table has.
const (
	huffNormal = iota // a range of values, read as low + offset
	huffLower         // everything below the table, read as low - offset
	huffUpper         // everything above it, read as low + offset
	huffOOB           // the out-of-band value
)

// huffLine is one line of a table.
type huffLine struct {
	low      int
	prefLen  int
	rangeLen int
	kind     int
}

// huffMaxLen is the longest prefix accepted. The standard tables stop at
// nine bits and a symbol code at thirty-one.
const huffMaxLen = 32

// huffTable is a table ready to decode with.
//
// The rule that assigns prefixes gives the lines of one length
// consecutive codes, in table order, starting from a number that
// depends only on how many shorter codes there are. So decoding needs
// no tree: read a bit, and see whether what has been read so far falls
// inside the run of codes that length has.
type huffTable struct {
	lines  []huffLine
	order  []int               // line indices, by prefix length then table order
	start  [huffMaxLen + 1]int // where each length begins in order
	count  [huffMaxLen + 1]int
	first  [huffMaxLen + 1]uint64
	maxLen int
}

// newHuffTable assigns the prefix codes of a table from its lines.
func newHuffTable(lines []huffLine) (*huffTable, error) {
	t := &huffTable{lines: lines}
	for _, l := range lines {
		if l.prefLen < 0 || l.prefLen > huffMaxLen || l.rangeLen < 0 || l.rangeLen > 32 {
			return nil, errJBIG2
		}
		t.count[l.prefLen]++
		if l.prefLen > t.maxLen {
			t.maxLen = l.prefLen
		}
	}
	// A line with no prefix is a value the table never codes.
	t.count[0] = 0
	if t.maxLen == 0 {
		return nil, errJBIG2
	}
	total := 0
	var next [huffMaxLen + 1]int
	for n := 1; n <= t.maxLen; n++ {
		t.first[n] = (t.first[n-1] + uint64(t.count[n-1])) << 1
		t.start[n] = total
		next[n] = total
		total += t.count[n]
	}
	t.order = make([]int, total)
	for i, l := range lines {
		if l.prefLen > 0 {
			t.order[next[l.prefLen]] = i
			next[l.prefLen]++
		}
	}
	return t, nil
}

// decode reads one value. The second result is false for the
// out-of-band value — and for a code the table does not have, which
// also marks the reader bad.
func (t *huffTable) decode(b *jbig2Bits) (int, bool) {
	var acc uint64
	for n := 1; n <= t.maxLen; n++ {
		acc = acc<<1 | uint64(b.bit())
		if b.bad {
			return 0, false
		}
		if acc < t.first[n] || acc-t.first[n] >= uint64(t.count[n]) {
			continue
		}
		l := t.lines[t.order[t.start[n]+int(acc-t.first[n])]]
		var v int64
		switch l.kind {
		case huffOOB:
			return 0, false
		case huffLower:
			v = int64(l.low) - int64(b.read(32))
		case huffUpper:
			v = int64(l.low) + int64(b.read(32))
		default:
			v = int64(l.low) + int64(b.read(l.rangeLen))
		}
		// Nothing the format codes this way is anywhere near this large:
		// a width, a count, a step across a page.
		if b.bad || v > 1<<30 || v < -(1<<30) {
			b.bad = true
			return 0, false
		}
		return int(v), true
	}
	b.bad = true
	return 0, false
}

// The standard tables, B.1 to B.15, as the specification prints them:
// the ordinary lines, then the lower range, the upper range and the
// out-of-band line where a table has them. The order matters, since it
// is the order codes of equal length are handed out in.
func hn(low, pref, rng int) huffLine { return huffLine{low, pref, rng, huffNormal} }
func hlo(low, pref int) huffLine     { return huffLine{low, pref, 32, huffLower} }
func hup(low, pref int) huffLine     { return huffLine{low, pref, 32, huffUpper} }
func hoob(pref int) huffLine         { return huffLine{0, pref, 0, huffOOB} }

var jbig2StdLines = [16][]huffLine{
	1: {hn(0, 1, 4), hn(16, 2, 8), hn(272, 3, 16), hup(65808, 3)},
	2: {hn(0, 1, 0), hn(1, 2, 0), hn(2, 3, 0), hn(3, 4, 3), hn(11, 5, 6),
		hup(75, 6), hoob(6)},
	3: {hn(-256, 8, 8), hn(0, 1, 0), hn(1, 2, 0), hn(2, 3, 0), hn(3, 4, 3),
		hn(11, 5, 6), hlo(-257, 8), hup(75, 7), hoob(6)},
	4: {hn(1, 1, 0), hn(2, 2, 0), hn(3, 3, 0), hn(4, 4, 3), hn(12, 5, 6),
		hup(76, 5)},
	5: {hn(-255, 7, 8), hn(1, 1, 0), hn(2, 2, 0), hn(3, 3, 0), hn(4, 4, 3),
		hn(12, 5, 6), hlo(-256, 7), hup(76, 6)},
	6: {hn(-2048, 5, 10), hn(-1024, 4, 9), hn(-512, 4, 8), hn(-256, 4, 7),
		hn(-128, 5, 6), hn(-64, 5, 5), hn(-32, 4, 5), hn(0, 2, 7), hn(128, 3, 7),
		hn(256, 3, 8), hn(512, 4, 9), hn(1024, 4, 10), hlo(-2049, 6), hup(2048, 6)},
	7: {hn(-1024, 4, 9), hn(-512, 3, 8), hn(-256, 4, 7), hn(-128, 5, 6),
		hn(-64, 5, 5), hn(-32, 4, 5), hn(0, 4, 5), hn(32, 5, 5), hn(64, 5, 6),
		hn(128, 4, 7), hn(256, 3, 8), hn(512, 3, 9), hn(1024, 3, 10),
		hlo(-1025, 5), hup(2048, 5)},
	8: {hn(-15, 8, 3), hn(-7, 9, 1), hn(-5, 8, 1), hn(-3, 9, 0), hn(-2, 7, 0),
		hn(-1, 4, 0), hn(0, 2, 1), hn(2, 5, 0), hn(3, 6, 0), hn(4, 3, 4),
		hn(20, 6, 1), hn(22, 4, 4), hn(38, 4, 5), hn(70, 5, 6), hn(134, 5, 7),
		hn(262, 6, 7), hn(390, 7, 8), hn(646, 6, 10), hlo(-16, 9), hup(1670, 9),
		hoob(2)},
	9: {hn(-31, 8, 4), hn(-15, 9, 2), hn(-11, 8, 2), hn(-7, 9, 1), hn(-5, 7, 1),
		hn(-3, 4, 1), hn(-1, 3, 1), hn(1, 3, 1), hn(3, 5, 1), hn(5, 6, 1),
		hn(7, 3, 5), hn(39, 6, 2), hn(43, 4, 5), hn(75, 4, 6), hn(139, 5, 7),
		hn(267, 5, 8), hn(523, 6, 8), hn(779, 7, 9), hn(1291, 6, 11),
		hlo(-32, 9), hup(3339, 9), hoob(2)},
	10: {hn(-21, 7, 4), hn(-5, 8, 0), hn(-4, 7, 0), hn(-3, 5, 0), hn(-2, 2, 2),
		hn(2, 5, 0), hn(3, 6, 0), hn(4, 7, 0), hn(5, 8, 0), hn(6, 2, 6),
		hn(70, 5, 5), hn(102, 6, 5), hn(134, 6, 6), hn(198, 6, 7), hn(326, 6, 8),
		hn(582, 6, 9), hn(1094, 6, 10), hn(2118, 7, 11), hlo(-22, 8),
		hup(4166, 8), hoob(2)},
	11: {hn(1, 1, 0), hn(2, 2, 1), hn(4, 4, 0), hn(5, 4, 1), hn(7, 5, 1),
		hn(9, 5, 2), hn(13, 6, 2), hn(17, 7, 2), hn(21, 7, 3), hn(29, 7, 4),
		hn(45, 7, 5), hn(77, 7, 6), hup(141, 7)},
	12: {hn(1, 1, 0), hn(2, 2, 0), hn(3, 3, 1), hn(5, 5, 0), hn(6, 5, 1),
		hn(8, 6, 1), hn(10, 7, 0), hn(11, 7, 1), hn(13, 7, 2), hn(17, 7, 3),
		hn(25, 7, 4), hn(41, 8, 5), hup(73, 8)},
	13: {hn(1, 1, 0), hn(2, 3, 0), hn(3, 4, 0), hn(4, 5, 0), hn(5, 4, 1),
		hn(7, 3, 3), hn(15, 6, 1), hn(17, 6, 2), hn(21, 6, 3), hn(29, 6, 4),
		hn(45, 6, 5), hn(77, 7, 6), hup(141, 7)},
	14: {hn(-2, 3, 0), hn(-1, 3, 0), hn(0, 1, 0), hn(1, 3, 0), hn(2, 3, 0)},
	15: {hn(-24, 7, 4), hn(-8, 6, 2), hn(-4, 5, 1), hn(-2, 4, 0), hn(-1, 3, 0),
		hn(0, 1, 0), hn(1, 3, 0), hn(2, 4, 0), hn(3, 5, 1), hn(5, 6, 2),
		hn(9, 7, 4), hlo(-25, 7), hup(25, 7)},
}

var (
	jbig2StdOnce   sync.Once
	jbig2StdTables [16]*huffTable
)

// jbig2StdTable returns standard table B.n.
func jbig2StdTable(n int) *huffTable {
	jbig2StdOnce.Do(func() {
		for i := 1; i <= 15; i++ {
			t, err := newHuffTable(jbig2StdLines[i])
			if err != nil {
				panic("gopdf: standard JBIG2 table " + string(rune('A'+i-1)) + " does not build")
			}
			jbig2StdTables[i] = t
		}
	})
	return jbig2StdTables[n]
}

// parseHuffTable reads a code table segment: a table a file brings
// along instead of using a standard one.
func parseHuffTable(data []byte) (*huffTable, error) {
	if len(data) < 9 {
		return nil, errJBIG2
	}
	flags := data[0]
	hasOOB := flags&1 != 0
	prefBits := int(flags>>1)&7 + 1
	rangeBits := int(flags>>4)&7 + 1
	low := int64(int32(be32(data, 1)))
	high := int64(int32(be32(data, 5)))
	if low > high {
		return nil, errJBIG2
	}
	b := &jbig2Bits{data: data[9:]}
	var lines []huffLine
	for cur := low; cur < high; {
		pl := int(b.read(prefBits))
		rl := int(b.read(rangeBits))
		if b.bad || rl > 32 || len(lines) >= 1<<16 {
			return nil, errJBIG2
		}
		lines = append(lines, huffLine{int(cur), pl, rl, huffNormal})
		cur += 1 << uint(rl)
	}
	lines = append(lines, huffLine{int(low - 1), int(b.read(prefBits)), 32, huffLower})
	lines = append(lines, huffLine{int(high), int(b.read(prefBits)), 32, huffUpper})
	if hasOOB {
		lines = append(lines, huffLine{0, int(b.read(prefBits)), 0, huffOOB})
	}
	if b.bad {
		return nil, errJBIG2
	}
	return newHuffTable(lines)
}

// readSymbolIDTable reads the table a Huffman-coded text region opens
// with: the code of each symbol it can place.
//
// The table is itself compressed. Thirty-five run codes come first, each
// a four-bit length; with those, one length per symbol is read, where a
// run code is either a length or an instruction to repeat one.
func readSymbolIDTable(b *jbig2Bits, nSyms int) (*huffTable, error) {
	runLines := make([]huffLine, 35)
	for i := range runLines {
		runLines[i] = huffLine{i, int(b.read(4)), 0, huffNormal}
	}
	if b.bad {
		return nil, errJBIG2
	}
	runs, err := newHuffTable(runLines)
	if err != nil {
		return nil, err
	}
	lines := make([]huffLine, nSyms)
	for i := 0; i < nSyms; {
		code, ok := runs.decode(b)
		if !ok {
			return nil, errJBIG2
		}
		if code < 32 {
			lines[i] = huffLine{i, code, 0, huffNormal}
			i++
			continue
		}
		length, n := 0, 0
		switch code {
		case 32:
			if i == 0 {
				return nil, errJBIG2
			}
			length, n = lines[i-1].prefLen, int(b.read(2))+3
		case 33:
			n = int(b.read(3)) + 3
		default:
			n = int(b.read(7)) + 11
		}
		if b.bad || n > nSyms-i {
			return nil, errJBIG2
		}
		for ; n > 0; n-- {
			lines[i] = huffLine{i, length, 0, huffNormal}
			i++
		}
	}
	b.align()
	return newHuffTable(lines)
}
