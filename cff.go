package gopdf

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Subsetting a CFF font program. Compact Font Format stores PostScript
// outlines in a handful of INDEX structures whose positions are recorded
// as byte offsets in a top-level dictionary, so shrinking the glyph data
// means rewriting those offsets too.
//
// Only the charstrings are reduced: unused glyphs become a bare endchar,
// which keeps every glyph ID where it was — essential for the Identity-H
// encoding this package uses — and avoids having to interpret charstrings
// to discover which subroutines they call. Subroutines and the string
// index are carried over untouched.

var errCFF = errors.New("gopdf: malformed CFF font program")

// cffIndex is a parsed INDEX: the byte range it occupies, and its items.
type cffIndex struct {
	items [][]byte
	end   int // offset just past the INDEX
}

// parseCFFIndex reads the INDEX starting at off.
func parseCFFIndex(data []byte, off int) (*cffIndex, error) {
	if off < 0 || off+2 > len(data) {
		return nil, errCFF
	}
	count := int(binary.BigEndian.Uint16(data[off:]))
	if count == 0 {
		return &cffIndex{end: off + 2}, nil
	}
	if off+3 > len(data) {
		return nil, errCFF
	}
	offSize := int(data[off+2])
	if offSize < 1 || offSize > 4 {
		return nil, errCFF
	}
	base := off + 3
	if base+(count+1)*offSize > len(data) {
		return nil, errCFF
	}
	readOff := func(i int) int {
		v := 0
		for k := 0; k < offSize; k++ {
			v = v<<8 | int(data[base+i*offSize+k])
		}
		return v
	}
	dataStart := base + (count+1)*offSize - 1
	idx := &cffIndex{items: make([][]byte, count)}
	for i := 0; i < count; i++ {
		lo, hi := readOff(i), readOff(i+1)
		if lo < 1 || hi < lo || dataStart+hi > len(data) {
			return nil, errCFF
		}
		idx.items[i] = data[dataStart+lo : dataStart+hi]
	}
	idx.end = dataStart + readOff(count)
	if idx.end > len(data) {
		return nil, errCFF
	}
	return idx, nil
}

// buildCFFIndex serializes items as an INDEX.
func buildCFFIndex(items [][]byte) []byte {
	if len(items) == 0 {
		return []byte{0, 0}
	}
	total := 1
	for _, it := range items {
		total += len(it)
	}
	offSize := 1
	switch {
	case total > 0xFFFFFF:
		offSize = 4
	case total > 0xFFFF:
		offSize = 3
	case total > 0xFF:
		offSize = 2
	}
	out := make([]byte, 0, 3+(len(items)+1)*offSize+total)
	out = binary.BigEndian.AppendUint16(out, uint16(len(items)))
	out = append(out, byte(offSize))
	put := func(v int) {
		for k := offSize - 1; k >= 0; k-- {
			out = append(out, byte(v>>(8*k)))
		}
	}
	pos := 1
	put(pos)
	for _, it := range items {
		pos += len(it)
		put(pos)
	}
	for _, it := range items {
		out = append(out, it...)
	}
	return out
}

// cffDictEntry is one operator of a CFF dictionary with its operands kept
// as raw bytes, so entries this code does not need are preserved exactly.
type cffDictEntry struct {
	op       int // 12xx operators are encoded as 1200+n
	operands []byte
	values   []int // integer operands, where they parsed as integers
}

// parseCFFDict splits a dictionary into its entries.
func parseCFFDict(data []byte) ([]cffDictEntry, error) {
	var out []cffDictEntry
	start := 0
	var values []int
	for i := 0; i < len(data); {
		b := data[i]
		switch {
		case b <= 21: // an operator ends the current entry
			op := int(b)
			size := 1
			if b == 12 {
				if i+1 >= len(data) {
					return nil, errCFF
				}
				op = 1200 + int(data[i+1])
				size = 2
			}
			out = append(out, cffDictEntry{
				op:       op,
				operands: data[start:i],
				values:   values,
			})
			i += size
			start = i
			values = nil
		case b == 28:
			if i+3 > len(data) {
				return nil, errCFF
			}
			values = append(values, int(int16(binary.BigEndian.Uint16(data[i+1:]))))
			i += 3
		case b == 29:
			if i+5 > len(data) {
				return nil, errCFF
			}
			values = append(values, int(int32(binary.BigEndian.Uint32(data[i+1:]))))
			i += 5
		case b == 30: // real number, nibble encoded
			i++
			for i < len(data) {
				n := data[i]
				i++
				if n&0x0F == 0x0F || n>>4 == 0x0F {
					break
				}
			}
			values = append(values, 0)
		case b >= 32 && b <= 246:
			values = append(values, int(b)-139)
			i++
		case b >= 247 && b <= 250:
			if i+2 > len(data) {
				return nil, errCFF
			}
			values = append(values, (int(b)-247)*256+int(data[i+1])+108)
			i += 2
		case b >= 251 && b <= 254:
			if i+2 > len(data) {
				return nil, errCFF
			}
			values = append(values, -(int(b)-251)*256-int(data[i+1])-108)
			i += 2
		default:
			return nil, errCFF
		}
	}
	return out, nil
}

// cffLongInt encodes v in the fixed five-byte integer form, so rewriting
// an offset never changes the size of the dictionary around it.
func cffLongInt(v int) []byte {
	out := []byte{29, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(out[1:], uint32(int32(v)))
	return out
}

// buildCFFDict serializes dictionary entries.
func buildCFFDict(entries []cffDictEntry) []byte {
	var out []byte
	for _, e := range entries {
		out = append(out, e.operands...)
		if e.op >= 1200 {
			out = append(out, 12, byte(e.op-1200))
		} else {
			out = append(out, byte(e.op))
		}
	}
	return out
}

// CFF top dictionary operators this code needs to understand.
const (
	cffOpCharset     = 15
	cffOpEncoding    = 16
	cffOpCharStrings = 17
	cffOpPrivate     = 18
	cffOpSubrs       = 19
	cffOpROS         = 1230
	cffOpFDArray     = 1236
	cffOpFDSelect    = 1237
)

func dictEntry(entries []cffDictEntry, op int) *cffDictEntry {
	for i := range entries {
		if entries[i].op == op {
			return &entries[i]
		}
	}
	return nil
}

// charsetLength measures the charset table starting at off, which stores
// one entry per glyph after .notdef.
func charsetLength(data []byte, off, nGlyphs int) (int, error) {
	if off < 0 || off >= len(data) {
		return 0, errCFF
	}
	switch format := data[off]; format {
	case 0:
		n := 1 + (nGlyphs-1)*2
		if off+n > len(data) {
			return 0, errCFF
		}
		return n, nil
	case 1, 2:
		step := 3
		if format == 2 {
			step = 4
		}
		covered, n := 1, 1
		for covered < nGlyphs {
			if off+n+step > len(data) {
				return 0, errCFF
			}
			var left int
			if format == 1 {
				left = int(data[off+n+2])
			} else {
				left = int(binary.BigEndian.Uint16(data[off+n+2:]))
			}
			covered += left + 1
			n += step
		}
		return n, nil
	default:
		return 0, fmt.Errorf("gopdf: unsupported CFF charset format %d", format)
	}
}

// subsetCFF rebuilds a CFF font program keeping only the charstrings of
// the given glyphs. Glyph IDs are preserved: dropped glyphs are replaced
// by an empty outline rather than removed.
//
// CID-keyed fonts return an error; their glyph data is spread across an
// FDArray of private dictionaries, which this code does not rewrite.
func subsetCFF(cff []byte, keep map[uint16]bool, nGlyphs int) ([]byte, error) {
	if len(cff) < 4 {
		return nil, errCFF
	}
	hdrSize := int(cff[2])
	if hdrSize < 4 || hdrSize > len(cff) {
		return nil, errCFF
	}
	nameIdx, err := parseCFFIndex(cff, hdrSize)
	if err != nil {
		return nil, err
	}
	topIdx, err := parseCFFIndex(cff, nameIdx.end)
	if err != nil {
		return nil, err
	}
	if len(topIdx.items) == 0 {
		return nil, errCFF
	}
	stringIdx, err := parseCFFIndex(cff, topIdx.end)
	if err != nil {
		return nil, err
	}
	gsubrIdx, err := parseCFFIndex(cff, stringIdx.end)
	if err != nil {
		return nil, err
	}

	top, err := parseCFFDict(topIdx.items[0])
	if err != nil {
		return nil, err
	}
	if dictEntry(top, cffOpROS) != nil || dictEntry(top, cffOpFDArray) != nil {
		return nil, errors.New("gopdf: CID-keyed CFF fonts are embedded without subsetting")
	}

	csEntry := dictEntry(top, cffOpCharStrings)
	if csEntry == nil || len(csEntry.values) != 1 {
		return nil, errCFF
	}
	charStrings, err := parseCFFIndex(cff, csEntry.values[0])
	if err != nil {
		return nil, err
	}
	if len(charStrings.items) != nGlyphs {
		return nil, errCFF
	}

	// Replace unused outlines with a bare endchar.
	newCharStrings := make([][]byte, len(charStrings.items))
	for gid := range charStrings.items {
		if keep[uint16(gid)] || gid == 0 {
			newCharStrings[gid] = charStrings.items[gid]
		} else {
			newCharStrings[gid] = []byte{14} // endchar
		}
	}

	// The charset maps glyph IDs to names; it is copied unchanged.
	var charsetData []byte
	charsetPredefined := 0
	if e := dictEntry(top, cffOpCharset); e != nil && len(e.values) == 1 {
		if v := e.values[0]; v > 2 {
			n, err := charsetLength(cff, v, nGlyphs)
			if err != nil {
				return nil, err
			}
			charsetData = cff[v : v+n]
		} else {
			charsetPredefined = v
		}
	}

	// The private dictionary and its local subroutines are copied as one
	// block, so the Subrs offset stored inside it stays valid.
	var privateBlock []byte
	privateSize := 0
	if e := dictEntry(top, cffOpPrivate); e != nil && len(e.values) == 2 {
		privateSize = e.values[0]
		privOff := e.values[1]
		if privOff < 0 || privateSize < 0 || privOff+privateSize > len(cff) {
			return nil, errCFF
		}
		blockEnd := privOff + privateSize
		priv, err := parseCFFDict(cff[privOff:blockEnd])
		if err != nil {
			return nil, err
		}
		if s := dictEntry(priv, cffOpSubrs); s != nil && len(s.values) == 1 {
			subrsOff := privOff + s.values[0]
			subrs, err := parseCFFIndex(cff, subrsOff)
			if err != nil {
				return nil, err
			}
			if subrs.end > blockEnd {
				blockEnd = subrs.end
			}
		}
		privateBlock = cff[privOff:blockEnd]
	}

	// Assemble. The offsets in the top dictionary are written in the
	// fixed five-byte form, so the dictionary's size does not depend on
	// the values and one sizing pass is enough.
	rebuild := func(charsetOff, charStringsOff, privateOff int) []byte {
		entries := make([]cffDictEntry, 0, len(top))
		for _, e := range top {
			switch e.op {
			case cffOpEncoding:
				// Glyphs are addressed by index, so the encoding is
				// irrelevant and the default applies.
				continue
			case cffOpCharset:
				if charsetData == nil {
					entries = append(entries, cffDictEntry{
						op: e.op, operands: cffLongInt(charsetPredefined)})
				} else {
					entries = append(entries, cffDictEntry{
						op: e.op, operands: cffLongInt(charsetOff)})
				}
			case cffOpCharStrings:
				entries = append(entries, cffDictEntry{
					op: e.op, operands: cffLongInt(charStringsOff)})
			case cffOpPrivate:
				operands := append(cffLongInt(privateSize), cffLongInt(privateOff)...)
				entries = append(entries, cffDictEntry{op: e.op, operands: operands})
			default:
				entries = append(entries, e)
			}
		}
		topData := buildCFFDict(entries)

		out := make([]byte, 0, len(cff)/2)
		out = append(out, cff[:hdrSize]...)
		out = append(out, buildCFFIndex(nameIdx.items)...)
		out = append(out, buildCFFIndex([][]byte{topData})...)
		out = append(out, buildCFFIndex(stringIdx.items)...)
		out = append(out, buildCFFIndex(gsubrIdx.items)...)
		out = append(out, charsetData...)
		out = append(out, buildCFFIndex(newCharStrings)...)
		out = append(out, privateBlock...)
		return out
	}

	// Sizing pass: the top dictionary's size is independent of the offset
	// values, so measuring it with placeholders gives the real layout.
	prefix := hdrSize +
		len(buildCFFIndex(nameIdx.items)) +
		len(buildCFFIndex([][]byte{buildCFFDict(topDictShape(top))})) +
		len(buildCFFIndex(stringIdx.items)) +
		len(buildCFFIndex(gsubrIdx.items))
	charsetOff := prefix
	charStringsOff := charsetOff + len(charsetData)
	privateOff := charStringsOff + len(buildCFFIndex(newCharStrings))

	out := rebuild(charsetOff, charStringsOff, privateOff)
	// The sizing pass must have predicted the layout exactly.
	if len(out) != privateOff+len(privateBlock) {
		return nil, errors.New("gopdf: CFF subset layout did not converge")
	}
	return out, nil
}

// topDictShape builds a top dictionary with the same entries and sizes the
// final one will have, for measuring the prefix before the tables it
// points at.
func topDictShape(top []cffDictEntry) []cffDictEntry {
	entries := make([]cffDictEntry, 0, len(top))
	for _, e := range top {
		switch e.op {
		case cffOpEncoding:
			continue
		case cffOpCharset, cffOpCharStrings:
			entries = append(entries, cffDictEntry{op: e.op, operands: cffLongInt(0)})
		case cffOpPrivate:
			entries = append(entries, cffDictEntry{
				op: e.op, operands: append(cffLongInt(0), cffLongInt(0)...)})
		default:
			entries = append(entries, e)
		}
	}
	return entries
}
