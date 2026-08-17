package gopdf

import "errors"

// Subsetting a CID-keyed CFF font.
//
// A CID-keyed font is organised differently from an ordinary one, which
// is why the plain subsetter refuses it. Its glyphs are addressed by
// character identifier rather than by name, so the charset holds numbers
// instead of names and cannot be pruned — the numbers are what the
// document uses to ask for a glyph. And its private dictionaries come in
// a set: an FDArray of them, with an FDSelect saying which glyph belongs
// to which, because a font covering several scripts hints them
// differently.
//
// The consequence of not subsetting one is a whole CJK font in every
// document that uses a dozen of its glyphs, which is fifteen megabytes
// where thirty kilobytes would do. So each private dictionary's local
// subroutines are rebuilt from just the glyphs that use it, and the
// glyphs nothing draws become a bare endchar as they do elsewhere.

// subsetCIDCFF reduces a CID-keyed CFF to the glyphs in keep.
func subsetCIDCFF(cff []byte, keep map[uint16]bool, nGlyphs int) ([]byte, error) {
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
	stringIdx, err := parseCFFIndex(cff, topIdx.end)
	if err != nil {
		return nil, err
	}
	gsubrIdx, err := parseCFFIndex(cff, stringIdx.end)
	if err != nil {
		return nil, err
	}
	if len(topIdx.items) == 0 {
		return nil, errCFF
	}
	top, err := parseCFFDict(topIdx.items[0])
	if err != nil {
		return nil, err
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

	// The charset is the CID for each glyph and is kept whole: a
	// document asks for a glyph by CID, and a CID that no longer appears
	// is a glyph that can no longer be reached.
	var charsetData []byte
	charsetPredefined := 0
	if e := dictEntry(top, cffOpCharset); e != nil && len(e.values) == 1 {
		if v := e.values[0]; v > 2 {
			sids, err := parseCharsetSIDs(cff, v, nGlyphs)
			if err != nil {
				return nil, err
			}
			charsetData = buildCharset(sids)
		} else {
			charsetPredefined = v
		}
	}

	// FDSelect says which private dictionary a glyph uses. Without it
	// every glyph uses the first, which is what a single-dictionary
	// font means.
	fdSelect := make([]uint8, nGlyphs)
	var fdSelectData []byte
	if e := dictEntry(top, cffOpFDSelect); e != nil && len(e.values) == 1 {
		got := parseFDSelect(cff, e.values[0], nGlyphs)
		if got == nil {
			return nil, errCFF
		}
		fdSelect = got
		fdSelectData = buildFDSelect(fdSelect)
	}

	fdEntry := dictEntry(top, cffOpFDArray)
	if fdEntry == nil || len(fdEntry.values) != 1 {
		return nil, errors.New("gopdf: CID-keyed CFF font has no font dictionaries")
	}
	fdIdx, err := parseCFFIndex(cff, fdEntry.values[0])
	if err != nil {
		return nil, err
	}
	if len(fdIdx.items) == 0 || len(fdIdx.items) > 256 {
		return nil, errCFF
	}

	// Which glyphs each private dictionary is responsible for, so its
	// local subroutines are pruned against those and no others.
	keptPerFD := make([]map[uint16]bool, len(fdIdx.items))
	for i := range keptPerFD {
		keptPerFD[i] = map[uint16]bool{}
	}
	for gid := 0; gid < nGlyphs; gid++ {
		if !keep[uint16(gid)] && gid != 0 {
			continue
		}
		fd := 0
		if gid < len(fdSelect) {
			fd = int(fdSelect[gid])
		}
		if fd < len(keptPerFD) {
			keptPerFD[fd][uint16(gid)] = true
		}
	}

	// Each font dictionary keeps its own private block, with the local
	// subroutines rebuilt.
	type fontDict struct {
		dict     []cffDictEntry
		privSize int
		privRel  int // where the local subroutines sit inside the block
		block    []byte
	}
	fds := make([]fontDict, len(fdIdx.items))
	usedGlobal := map[int]bool{}
	for i, item := range fdIdx.items {
		fd, err := parseCFFDict(item)
		if err != nil {
			return nil, err
		}
		entry := fontDict{dict: fd}
		if e := dictEntry(fd, cffOpPrivate); e != nil && len(e.values) == 2 {
			size, off := e.values[0], e.values[1]
			if off < 0 || size < 0 || off+size > len(cff) {
				return nil, errCFF
			}
			privDict := cff[off : off+size]
			priv, err := parseCFFDict(privDict)
			if err != nil {
				return nil, err
			}
			var localSubrs [][]byte
			if sub := dictEntry(priv, cffOpSubrs); sub != nil && len(sub.values) == 1 {
				entry.privRel = sub.values[0]
				idx, err := parseCFFIndex(cff, off+entry.privRel)
				if err != nil {
					return nil, err
				}
				localSubrs = idx.items
			}
			usedLocal, usedG := reachableSubrs(charStrings.items, localSubrs,
				gsubrIdx.items, keptPerFD[i])
			for k := range usedG {
				usedGlobal[k] = true
			}
			block := append([]byte(nil), privDict...)
			if localSubrs != nil {
				pruned := buildCFFIndex(pruneSubrs(localSubrs, usedLocal))
				for len(block) < entry.privRel {
					block = append(block, 0)
				}
				block = block[:entry.privRel]
				block = append(block, pruned...)
			}
			entry.privSize = size
			entry.block = block
		}
		fds[i] = entry
	}

	// Replace the outlines nothing draws with a bare endchar.
	newCharStrings := make([][]byte, len(charStrings.items))
	for gid := range charStrings.items {
		if keep[uint16(gid)] || gid == 0 {
			newCharStrings[gid] = charStrings.items[gid]
		} else {
			newCharStrings[gid] = []byte{14} // endchar
		}
	}
	newGlobalSubrs := pruneSubrs(gsubrIdx.items, usedGlobal)

	// ROS names its registry and ordering through the string index, so
	// those strings have to stay; the rest of the index is only
	// descriptive and goes.
	keptStrings := stringIdx.items

	// Assemble. Every offset is written in the fixed five-byte form, so
	// the dictionaries do not change size when the values do and one
	// sizing pass is exact.
	buildFontDicts := func(privOffsets []int) []byte {
		items := make([][]byte, len(fds))
		for i, fd := range fds {
			entries := make([]cffDictEntry, 0, len(fd.dict))
			for _, e := range fd.dict {
				if e.op == cffOpPrivate {
					entries = append(entries, cffDictEntry{op: e.op,
						operands: append(cffLongInt(fd.privSize),
							cffLongInt(privOffsets[i])...)})
					continue
				}
				entries = append(entries, e)
			}
			items[i] = buildCFFDict(entries)
		}
		return buildCFFIndex(items)
	}

	rebuild := func(charsetOff, fdSelectOff, charStringsOff, fdArrayOff int,
		privOffsets []int) []byte {

		entries := make([]cffDictEntry, 0, len(top))
		for _, e := range top {
			switch e.op {
			case cffOpEncoding:
				continue // a CID font is addressed by index, not by code
			case cffOpCharset:
				if charsetData == nil {
					entries = append(entries, cffDictEntry{op: e.op,
						operands: cffLongInt(charsetPredefined)})
				} else {
					entries = append(entries, cffDictEntry{op: e.op,
						operands: cffLongInt(charsetOff)})
				}
			case cffOpFDSelect:
				if fdSelectData == nil {
					continue
				}
				entries = append(entries, cffDictEntry{op: e.op,
					operands: cffLongInt(fdSelectOff)})
			case cffOpCharStrings:
				entries = append(entries, cffDictEntry{op: e.op,
					operands: cffLongInt(charStringsOff)})
			case cffOpFDArray:
				entries = append(entries, cffDictEntry{op: e.op,
					operands: cffLongInt(fdArrayOff)})
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
		out = append(out, fdSelectData...)
		out = append(out, buildCFFIndex(newCharStrings)...)
		out = append(out, buildFontDicts(privOffsets)...)
		for _, fd := range fds {
			out = append(out, fd.block...)
		}
		return out
	}

	// Sizing pass. The top dictionary is measured with placeholders of
	// the same width, and the font dictionaries likewise.
	placeholders := make([]int, len(fds))
	prefix := hdrSize +
		len(buildCFFIndex(nameIdx.items)) +
		len(buildCFFIndex([][]byte{buildCFFDict(cidTopShape(top, fdSelectData != nil))})) +
		len(buildCFFIndex(keptStrings)) +
		len(buildCFFIndex(newGlobalSubrs))
	charsetOff := prefix
	fdSelectOff := charsetOff + len(charsetData)
	charStringsOff := fdSelectOff + len(fdSelectData)
	fdArrayOff := charStringsOff + len(buildCFFIndex(newCharStrings))
	privStart := fdArrayOff + len(buildFontDicts(placeholders))

	privOffsets := make([]int, len(fds))
	at := privStart
	for i, fd := range fds {
		privOffsets[i] = at
		at += len(fd.block)
	}

	out := rebuild(charsetOff, fdSelectOff, charStringsOff, fdArrayOff, privOffsets)
	if len(out) != at {
		return nil, errors.New("gopdf: CID-keyed CFF subset layout did not converge")
	}
	return out, nil
}

// cidTopShape builds a top dictionary the same size as the final one,
// for measuring what comes before the tables it points at.
func cidTopShape(top []cffDictEntry, hasFDSelect bool) []cffDictEntry {
	out := make([]cffDictEntry, 0, len(top))
	for _, e := range top {
		switch e.op {
		case cffOpEncoding:
			continue
		case cffOpFDSelect:
			if !hasFDSelect {
				continue
			}
			out = append(out, cffDictEntry{op: e.op, operands: cffLongInt(0)})
		case cffOpCharset, cffOpCharStrings, cffOpFDArray:
			out = append(out, cffDictEntry{op: e.op, operands: cffLongInt(0)})
		default:
			out = append(out, e)
		}
	}
	return out
}

// buildFDSelect writes the glyph-to-dictionary map in the ranged form,
// which is smaller than one byte per glyph for every real font: a font
// groups its scripts, so the map is a handful of runs.
func buildFDSelect(fd []uint8) []byte {
	if len(fd) == 0 {
		return nil
	}
	type run struct {
		first int
		fd    uint8
	}
	runs := []run{{0, fd[0]}}
	for i := 1; i < len(fd); i++ {
		if fd[i] != runs[len(runs)-1].fd {
			runs = append(runs, run{i, fd[i]})
		}
	}
	out := make([]byte, 0, 5+len(runs)*3)
	out = append(out, 3) // format 3: ranges
	out = append(out, byte(len(runs)>>8), byte(len(runs)))
	for _, r := range runs {
		out = append(out, byte(r.first>>8), byte(r.first), r.fd)
	}
	out = append(out, byte(len(fd)>>8), byte(len(fd)))
	return out
}
