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
	// A CID-keyed font is organised differently — numbers for names, and
	// a set of private dictionaries rather than one — and is reduced by
	// its own subsetter.
	if dictEntry(top, cffOpROS) != nil || dictEntry(top, cffOpFDArray) != nil {
		return subsetCIDCFF(cff, keep, nGlyphs)
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

	// The charset names every glyph; a subset only needs names for the
	// glyphs it keeps, which lets most of the string index go too.
	var charsetData []byte
	charsetPredefined := 0
	keptStrings := stringIdx.items
	if e := dictEntry(top, cffOpCharset); e != nil && len(e.values) == 1 {
		if v := e.values[0]; v > 2 {
			sids, err := parseCharsetSIDs(cff, v, nGlyphs)
			if err != nil {
				return nil, err
			}
			var newSids []uint16
			newSids, keptStrings = pruneGlyphNames(sids, keep, stringIdx.items)
			charsetData = buildCharset(newSids)
		} else {
			charsetPredefined = v
			// Nothing references the string index once the descriptive
			// top dictionary entries are dropped.
			keptStrings = nil
		}
	} else {
		keptStrings = nil
	}

	// The private dictionary is copied verbatim; its local subroutines,
	// which follow it, are rebuilt so unused ones cost almost nothing.
	var privateDict, localSubrsData []byte
	var localSubrs [][]byte
	privateSize, localSubrsRel := 0, 0
	if e := dictEntry(top, cffOpPrivate); e != nil && len(e.values) == 2 {
		privateSize = e.values[0]
		privOff := e.values[1]
		if privOff < 0 || privateSize < 0 || privOff+privateSize > len(cff) {
			return nil, errCFF
		}
		privateDict = cff[privOff : privOff+privateSize]
		priv, err := parseCFFDict(privateDict)
		if err != nil {
			return nil, err
		}
		if sub := dictEntry(priv, cffOpSubrs); sub != nil && len(sub.values) == 1 {
			localSubrsRel = sub.values[0]
			idx, err := parseCFFIndex(cff, privOff+localSubrsRel)
			if err != nil {
				return nil, err
			}
			localSubrs = idx.items
		}
	}

	// Follow the kept charstrings to see which subroutines they still
	// need; anything unreachable becomes a bare return.
	usedLocal, usedGlobal := reachableSubrs(charStrings.items, localSubrs, gsubrIdx.items, keep)
	newLocalSubrs := pruneSubrs(localSubrs, usedLocal)
	newGlobalSubrs := pruneSubrs(gsubrIdx.items, usedGlobal)
	if localSubrs != nil {
		localSubrsData = buildCFFIndex(newLocalSubrs)
	}

	// The Subrs offset inside the private dictionary is relative to the
	// dictionary's own start, so the rebuilt index has to sit exactly
	// that far along.
	privateBlockBytes := append([]byte(nil), privateDict...)
	if localSubrsData != nil {
		for len(privateBlockBytes) < localSubrsRel {
			privateBlockBytes = append(privateBlockBytes, 0)
		}
		privateBlockBytes = privateBlockBytes[:localSubrsRel]
		privateBlockBytes = append(privateBlockBytes, localSubrsData...)
	}

	// Assemble. The offsets in the top dictionary are written in the
	// fixed five-byte form, so the dictionary's size does not depend on
	// the values and one sizing pass is enough.
	rebuild := func(charsetOff, charStringsOff, privateOff int) []byte {
		entries := make([]cffDictEntry, 0, len(top))
		for _, e := range top {
			if cffSIDOperators[e.op] {
				continue
			}
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
		out = append(out, buildCFFIndex(keptStrings)...)
		out = append(out, buildCFFIndex(newGlobalSubrs)...)
		out = append(out, charsetData...)
		out = append(out, buildCFFIndex(newCharStrings)...)
		out = append(out, privateBlockBytes...)
		return out
	}

	// Sizing pass: the top dictionary's size is independent of the offset
	// values, so measuring it with placeholders gives the real layout.
	prefix := hdrSize +
		len(buildCFFIndex(nameIdx.items)) +
		len(buildCFFIndex([][]byte{buildCFFDict(topDictShape(top))})) +
		len(buildCFFIndex(keptStrings)) +
		len(buildCFFIndex(newGlobalSubrs))
	charsetOff := prefix
	charStringsOff := charsetOff + len(charsetData)
	privateOff := charStringsOff + len(buildCFFIndex(newCharStrings))

	out := rebuild(charsetOff, charStringsOff, privateOff)
	// The sizing pass must have predicted the layout exactly.
	if len(out) != privateOff+len(privateBlockBytes) {
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
		if cffSIDOperators[e.op] {
			continue
		}
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

// --- subroutine reachability ---
//
// Charstrings call subroutines by an index biased by the size of the
// subroutine index they come from. Finding which subroutines a subset
// still needs means interpreting enough of the Type 2 charstring format
// to follow those calls — and, crucially, to skip the variable-length
// hint masks, since misreading one would desynchronise everything after.

// subrBias is the offset applied to a subroutine number, which depends on
// how many subroutines the index holds.
func subrBias(n int) int {
	switch {
	case n < 1240:
		return 107
	case n < 33900:
		return 1131
	default:
		return 32768
	}
}

// charstringWalker marks the subroutines a set of charstrings reaches.
type charstringWalker struct {
	local, global [][]byte
	usedLocal     map[int]bool
	usedGlobal    map[int]bool
	localBias     int
	globalBias    int
	// giveUp is set when the charstrings do something this walker cannot
	// follow, in which case every subroutine must be kept.
	giveUp bool
}

func newCharstringWalker(local, global [][]byte) *charstringWalker {
	return &charstringWalker{
		local: local, global: global,
		usedLocal:  make(map[int]bool),
		usedGlobal: make(map[int]bool),
		localBias:  subrBias(len(local)),
		globalBias: subrBias(len(global)),
	}
}

// walk follows one charstring, marking the subroutines it calls.
func (w *charstringWalker) walk(cs []byte, numHints *int, depth int) {
	if w.giveUp || depth > 10 {
		if depth > 10 {
			w.giveUp = true
		}
		return
	}
	var stack []int
	push := func(v int) {
		if len(stack) < 48 {
			stack = append(stack, v)
		}
	}

	for i := 0; i < len(cs); {
		b := cs[i]
		switch {
		case b >= 32 || b == 28:
			// An operand.
			switch {
			case b == 28:
				if i+3 > len(cs) {
					w.giveUp = true
					return
				}
				push(int(int16(binary.BigEndian.Uint16(cs[i+1:]))))
				i += 3
			case b <= 246:
				push(int(b) - 139)
				i++
			case b <= 250:
				if i+2 > len(cs) {
					w.giveUp = true
					return
				}
				push((int(b)-247)*256 + int(cs[i+1]) + 108)
				i += 2
			case b <= 254:
				if i+2 > len(cs) {
					w.giveUp = true
					return
				}
				push(-(int(b)-251)*256 - int(cs[i+1]) - 108)
				i += 2
			default: // 255: a 16.16 fixed-point number
				if i+5 > len(cs) {
					w.giveUp = true
					return
				}
				push(int(int32(binary.BigEndian.Uint32(cs[i+1:]))) >> 16)
				i += 5
			}
			continue

		case b == 1 || b == 3 || b == 18 || b == 23:
			// Stem hints: each takes a pair of operands.
			*numHints += len(stack) / 2
			stack = stack[:0]
			i++

		case b == 19 || b == 20:
			// A hint mask is preceded by an implicit vstem when operands
			// are pending, and followed by one bit per hint.
			*numHints += len(stack) / 2
			stack = stack[:0]
			i++
			i += (*numHints + 7) / 8
			if i > len(cs) {
				w.giveUp = true
				return
			}

		case b == 10: // callsubr
			if len(stack) == 0 {
				w.giveUp = true
				return
			}
			idx := stack[len(stack)-1] + w.localBias
			stack = stack[:len(stack)-1]
			if idx < 0 || idx >= len(w.local) {
				w.giveUp = true
				return
			}
			if !w.usedLocal[idx] {
				w.usedLocal[idx] = true
				w.walk(w.local[idx], numHints, depth+1)
			}
			i++

		case b == 29: // callgsubr
			if len(stack) == 0 {
				w.giveUp = true
				return
			}
			idx := stack[len(stack)-1] + w.globalBias
			stack = stack[:len(stack)-1]
			if idx < 0 || idx >= len(w.global) {
				w.giveUp = true
				return
			}
			if !w.usedGlobal[idx] {
				w.usedGlobal[idx] = true
				w.walk(w.global[idx], numHints, depth+1)
			}
			i++

		case b == 11: // return
			return

		case b == 14: // endchar
			return

		case b == 12: // an escaped operator
			if i+1 >= len(cs) {
				w.giveUp = true
				return
			}
			stack = stack[:0]
			i += 2

		default:
			stack = stack[:0]
			i++
		}
	}
}

// reachableSubrs reports which local and global subroutines the given
// charstrings need. It returns nil sets when the charstrings cannot be
// followed, meaning every subroutine must be kept.
func reachableSubrs(charstrings, local, global [][]byte, keep map[uint16]bool) (map[int]bool, map[int]bool) {
	w := newCharstringWalker(local, global)
	for gid, cs := range charstrings {
		if gid != 0 && !keep[uint16(gid)] {
			continue // this glyph became an empty outline
		}
		hints := 0
		w.walk(cs, &hints, 0)
		if w.giveUp {
			return nil, nil
		}
	}
	return w.usedLocal, w.usedGlobal
}

// pruneSubrs replaces unused subroutines with a bare return, keeping the
// index the same length so the numbers charstrings call by stay valid.
func pruneSubrs(subrs [][]byte, used map[int]bool) [][]byte {
	if used == nil {
		return subrs
	}
	out := make([][]byte, len(subrs))
	for i := range subrs {
		if used[i] {
			out[i] = subrs[i]
		} else {
			out[i] = []byte{11} // return
		}
	}
	return out
}

// --- glyph names ---
//
// A name-keyed CFF names every glyph through its charset, which indexes
// the string index. A subset only needs names for the glyphs it keeps, so
// the rest are pointed at .notdef and the strings they used drop out.

// cffStandardStrings is the number of predefined strings; a SID below
// this refers to one of them rather than to the string index.
const cffStandardStrings = 391

// parseCharsetSIDs reads a charset into one string identifier per glyph.
func parseCharsetSIDs(data []byte, off, nGlyphs int) ([]uint16, error) {
	sids := make([]uint16, nGlyphs)
	if off < 0 || off >= len(data) {
		return nil, errCFF
	}
	switch format := data[off]; format {
	case 0:
		for gid := 1; gid < nGlyphs; gid++ {
			p := off + 1 + (gid-1)*2
			if p+2 > len(data) {
				return nil, errCFF
			}
			sids[gid] = binary.BigEndian.Uint16(data[p:])
		}
	case 1, 2:
		step := 3
		if format == 2 {
			step = 4
		}
		gid, p := 1, off+1
		for gid < nGlyphs {
			if p+step > len(data) {
				return nil, errCFF
			}
			first := binary.BigEndian.Uint16(data[p:])
			var left int
			if format == 1 {
				left = int(data[p+2])
			} else {
				left = int(binary.BigEndian.Uint16(data[p+2:]))
			}
			for k := 0; k <= left && gid < nGlyphs; k++ {
				sids[gid] = first + uint16(k)
				gid++
			}
			p += step
		}
	default:
		return nil, fmt.Errorf("gopdf: unsupported CFF charset format %d", format)
	}
	return sids, nil
}

// buildCharset writes a format 0 charset, one string identifier per glyph
// after .notdef.
func buildCharset(sids []uint16) []byte {
	out := make([]byte, 0, 1+(len(sids)-1)*2)
	out = append(out, 0)
	for _, sid := range sids[1:] {
		out = binary.BigEndian.AppendUint16(out, sid)
	}
	return out
}

// pruneGlyphNames points dropped glyphs at .notdef and rebuilds the
// string index with only the names that survive, renumbering as it goes.
func pruneGlyphNames(sids []uint16, keep map[uint16]bool, strings [][]byte) ([]uint16, [][]byte) {
	out := make([]uint16, len(sids))
	remap := make(map[uint16]uint16)
	var kept [][]byte

	for gid, sid := range sids {
		if gid != 0 && !keep[uint16(gid)] {
			continue // the glyph is empty; it needs no name
		}
		if sid < cffStandardStrings {
			out[gid] = sid // one of the predefined names
			continue
		}
		if mapped, seen := remap[sid]; seen {
			out[gid] = mapped
			continue
		}
		idx := int(sid) - cffStandardStrings
		if idx < 0 || idx >= len(strings) {
			continue // a dangling name is simply dropped
		}
		mapped := uint16(cffStandardStrings + len(kept))
		remap[sid] = mapped
		kept = append(kept, strings[idx])
		out[gid] = mapped
	}
	return out, kept
}

// cffSIDOperators are the top dictionary entries whose operand is a
// string identifier. A subset drops them: they are descriptive metadata,
// and keeping them would mean renumbering into the pruned string index.
var cffSIDOperators = map[int]bool{
	0: true, 1: true, 2: true, 3: true, 4: true, // version..Weight
	1200: true, // Copyright
	1221: true, // PostScript
	1222: true, // BaseFontName
	1238: true, // FontName
}
