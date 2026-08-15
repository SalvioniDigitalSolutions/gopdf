package gopdf

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
)

// ttfFont holds the parsed tables of a TrueType font needed for embedding.
type ttfFont struct {
	tables     map[string][]byte
	unitsPerEm int

	// Metrics in font units.
	ascent    int
	descent   int
	capHeight int
	bbox      [4]int

	italicAngle float64
	fixedPitch  bool

	numGlyphs int
	advances  []uint16 // advance width per glyph ID
	cmap      map[rune]uint16
	// symbolCmap marks a character map that is a symbol font's own
	// (3,0) table, whose codes are conventionally offset to F000.
	symbolCmap bool
	// glyphNames maps a glyph name from the post table to its index,
	// which is how a simple font's /Differences addresses a glyph.
	glyphNames map[string]uint16
	kern       map[uint32]int16 // leftGID<<16|rightGID -> adjustment
	loca       []uint32         // numGlyphs+1 offsets into glyf
	glyf       []byte
	psName     string

	// CFF-based OpenType fonts carry PostScript outlines in a single
	// table. They are embedded whole rather than subset, so program
	// holds the original file bytes.
	cff     bool
	program []byte
}

func be16(b []byte, off int) uint16 {
	return binary.BigEndian.Uint16(b[off:])
}

func be32(b []byte, off int) uint32 {
	return binary.BigEndian.Uint32(b[off:])
}

var errTTF = errors.New("gopdf: malformed TrueType font")

// parseTTF parses a TrueType font, the first font of a collection, or a
// CFF-based OpenType font.
func parseTTF(data []byte) (*ttfFont, error) {
	if len(data) < 12 {
		return nil, errTTF
	}
	base := 0
	if string(data[0:4]) == "ttcf" {
		if len(data) < 16 {
			return nil, errTTF
		}
		base = int(be32(data, 12)) // first font in the collection
		if base+12 > len(data) {
			return nil, errTTF
		}
	}
	cff := false
	switch be32(data, base) {
	case 0x00010000, 0x74727565: // 1.0, 'true'
	case 0x4F54544F: // 'OTTO': PostScript (CFF) outlines
		cff = true
	default:
		return nil, errTTF
	}

	numTables := int(be16(data, base+4))
	if base+12+numTables*16 > len(data) {
		return nil, errTTF
	}
	f := &ttfFont{tables: make(map[string][]byte, numTables)}
	for i := 0; i < numTables; i++ {
		rec := base + 12 + i*16
		tag := string(data[rec : rec+4])
		off := int(be32(data, rec+8))
		length := int(be32(data, rec+12))
		if off < 0 || length < 0 || off+length > len(data) {
			return nil, errTTF
		}
		f.tables[tag] = data[off : off+length]
	}

	if err := f.parseHead(); err != nil {
		return nil, err
	}
	if err := f.parseMaxp(); err != nil {
		return nil, err
	}
	if err := f.parseHheaHmtx(); err != nil {
		return nil, err
	}
	if err := f.parseCmap(); err != nil {
		return nil, err
	}
	if cff {
		// CFF outlines live in a single table rather than glyf/loca.
		// The whole font program is embedded as-is; there is no
		// charstring subsetter yet.
		if f.tables["CFF "] == nil {
			return nil, errors.New("gopdf: OpenType font has no CFF outlines")
		}
		f.cff = true
		f.program = data
	} else if err := f.parseGlyfLoca(); err != nil {
		return nil, err
	}
	f.parseOS2Post()
	f.parseName()
	f.parseKern()
	return f, nil
}

// parseKern reads format 0 horizontal kerning pairs from an OpenType-style
// kern table. Fonts without one (or with Apple's incompatible version 1
// layout, or GPOS-only kerning) simply render unkerned.
func (f *ttfFont) parseKern() {
	kern := f.tables["kern"]
	if len(kern) < 4 || be16(kern, 0) != 0 {
		return
	}
	nTables := int(be16(kern, 2))
	off := 4
	for i := 0; i < nTables && off+6 <= len(kern); i++ {
		length := int(be16(kern, off+2))
		coverage := be16(kern, off+4)
		if length < 6 || off+length > len(kern) {
			return
		}
		// Format 0, horizontal, not minimum-values, not cross-stream.
		if coverage&0xFF07 == 0x0001 {
			sub := kern[off : off+length]
			if len(sub) >= 14 {
				nPairs := int(be16(sub, 6))
				if max := (len(sub) - 14) / 6; nPairs > max {
					nPairs = max
				}
				if f.kern == nil {
					f.kern = make(map[uint32]int16, nPairs)
				}
				for p := 0; p < nPairs; p++ {
					rec := 14 + p*6
					key := uint32(be16(sub, rec))<<16 | uint32(be16(sub, rec+2))
					if _, ok := f.kern[key]; !ok {
						f.kern[key] = int16(be16(sub, rec+4))
					}
				}
			}
		}
		off += length
	}
}

// kerning returns the kern adjustment between two glyphs in font units;
// negative values pull the pair closer together.
func (f *ttfFont) kerning(left, right uint16) int {
	if f.kern == nil {
		return 0
	}
	return int(f.kern[uint32(left)<<16|uint32(right)])
}

func (f *ttfFont) parseHead() error {
	head := f.tables["head"]
	if len(head) < 54 {
		return errTTF
	}
	f.unitsPerEm = int(be16(head, 18))
	if f.unitsPerEm == 0 {
		return errTTF
	}
	f.bbox = [4]int{
		int(int16(be16(head, 36))), int(int16(be16(head, 38))),
		int(int16(be16(head, 40))), int(int16(be16(head, 42))),
	}
	return nil
}

func (f *ttfFont) parseMaxp() error {
	maxp := f.tables["maxp"]
	if len(maxp) < 6 {
		return errTTF
	}
	f.numGlyphs = int(be16(maxp, 4))
	return nil
}

func (f *ttfFont) parseHheaHmtx() error {
	hhea := f.tables["hhea"]
	if len(hhea) < 36 {
		return errTTF
	}
	f.ascent = int(int16(be16(hhea, 4)))
	f.descent = int(int16(be16(hhea, 6)))
	numHMetrics := int(be16(hhea, 34))
	if numHMetrics == 0 || numHMetrics > f.numGlyphs {
		return errTTF
	}
	hmtx := f.tables["hmtx"]
	if len(hmtx) < numHMetrics*4 {
		return errTTF
	}
	f.advances = make([]uint16, f.numGlyphs)
	for i := 0; i < numHMetrics; i++ {
		f.advances[i] = be16(hmtx, i*4)
	}
	for i := numHMetrics; i < f.numGlyphs; i++ {
		f.advances[i] = f.advances[numHMetrics-1]
	}
	return nil
}

func (f *ttfFont) parseGlyfLoca() error {
	head := f.tables["head"]
	longLoca := int16(be16(head, 50)) != 0
	loca, glyf := f.tables["loca"], f.tables["glyf"]
	if loca == nil || glyf == nil {
		return errors.New("gopdf: font has no TrueType outlines")
	}
	n := f.numGlyphs + 1
	f.loca = make([]uint32, n)
	if longLoca {
		if len(loca) < n*4 {
			return errTTF
		}
		for i := 0; i < n; i++ {
			f.loca[i] = be32(loca, i*4)
		}
	} else {
		if len(loca) < n*2 {
			return errTTF
		}
		for i := 0; i < n; i++ {
			f.loca[i] = uint32(be16(loca, i*2)) * 2
		}
	}
	for i := 1; i < n; i++ {
		if f.loca[i] < f.loca[i-1] || f.loca[i] > uint32(len(glyf)) {
			return errTTF
		}
	}
	f.glyf = glyf
	return nil
}

func (f *ttfFont) parseCmap() error {
	cmap := f.tables["cmap"]
	if len(cmap) < 4 {
		return errTTF
	}
	numSub := int(be16(cmap, 2))
	if len(cmap) < 4+numSub*8 {
		return errTTF
	}
	best, bestScore := -1, -1
	for i := 0; i < numSub; i++ {
		platform := be16(cmap, 4+i*8)
		encoding := be16(cmap, 4+i*8+2)
		score := -1
		switch {
		case platform == 3 && encoding == 10: // Windows, full Unicode
			score = 4
		case platform == 0: // Unicode
			score = 3
		case platform == 3 && encoding == 1: // Windows, BMP
			score = 3
		case platform == 3 && encoding == 0: // Windows, symbol
			score = 2
		case platform == 1 && encoding == 0: // Macintosh, one byte
			score = 1
		}
		if score > bestScore {
			best, bestScore = i, score
		}
	}
	if bestScore < 0 {
		return errors.New("gopdf: font has no Unicode cmap subtable")
	}
	f.symbolCmap = bestScore == 2
	off := int(be32(cmap, 4+best*8+4))
	if off+2 > len(cmap) {
		return errTTF
	}
	f.cmap = make(map[rune]uint16)
	// A malicious font can declare code-point ranges covering billions of
	// characters in a few bytes; the budget bounds total work. Real fonts
	// use a small fraction of it.
	budget := 1 << 22
	switch format := be16(cmap, off); format {
	case 0:
		return f.parseCmap0(cmap[off:])
	case 4:
		return f.parseCmap4(cmap[off:], &budget)
	case 6:
		return f.parseCmap6(cmap[off:])
	case 12:
		return f.parseCmap12(cmap[off:], &budget)
	default:
		return fmt.Errorf("gopdf: unsupported cmap subtable format %d", format)
	}
}

// parseCmap0 reads the oldest form: a flat table of 256 glyph indices,
// one per byte code. Subset fonts still emit it.
func (f *ttfFont) parseCmap0(t []byte) error {
	if len(t) < 262 {
		return errTTF
	}
	for code := 0; code < 256; code++ {
		if gid := uint16(t[6+code]); gid != 0 {
			f.cmap[rune(code)] = gid
		}
	}
	return nil
}

// parseCmap6 reads a single contiguous run of codes, which is what a
// subsetter emits when the glyphs it kept happen to be adjacent.
func (f *ttfFont) parseCmap6(t []byte) error {
	if len(t) < 10 {
		return errTTF
	}
	first, count := int(be16(t, 6)), int(be16(t, 8))
	if count < 0 || 10+count*2 > len(t) {
		return errTTF
	}
	for i := 0; i < count; i++ {
		if gid := be16(t, 10+i*2); gid != 0 {
			f.cmap[rune(first+i)] = gid
		}
	}
	return nil
}

func (f *ttfFont) parseCmap4(t []byte, budget *int) error {
	if len(t) < 14 {
		return errTTF
	}
	segCount := int(be16(t, 6)) / 2
	if len(t) < 16+segCount*8 {
		return errTTF
	}
	endOff := 14
	startOff := endOff + segCount*2 + 2
	deltaOff := startOff + segCount*2
	rangeOff := deltaOff + segCount*2
	for seg := 0; seg < segCount; seg++ {
		end := rune(be16(t, endOff+seg*2))
		start := rune(be16(t, startOff+seg*2))
		delta := be16(t, deltaOff+seg*2)
		ro := int(be16(t, rangeOff+seg*2))
		for c := start; c <= end && c != 0xFFFF; c++ {
			if *budget--; *budget < 0 {
				return nil
			}
			var gid uint16
			if ro == 0 {
				gid = uint16(c) + delta
			} else {
				idx := rangeOff + seg*2 + ro + int(c-start)*2
				if idx+2 > len(t) {
					continue
				}
				gid = be16(t, idx)
				if gid != 0 {
					gid += delta
				}
			}
			if gid != 0 && int(gid) < f.numGlyphs {
				f.cmap[c] = gid
			}
		}
	}
	return nil
}

func (f *ttfFont) parseCmap12(t []byte, budget *int) error {
	if len(t) < 16 {
		return errTTF
	}
	nGroups := int(be32(t, 12))
	if len(t) < 16+nGroups*12 {
		return errTTF
	}
	for g := 0; g < nGroups; g++ {
		off := 16 + g*12
		start := rune(be32(t, off))
		end := rune(be32(t, off+4))
		gid := be32(t, off+8)
		if end-start > 0x10FFFF { // corrupt group; skip
			continue
		}
		for c := start; c <= end; c++ {
			if *budget--; *budget < 0 {
				return nil
			}
			id := uint16(gid + uint32(c-start))
			if id != 0 && int(id) < f.numGlyphs {
				f.cmap[c] = id
			}
		}
	}
	return nil
}

func (f *ttfFont) parseOS2Post() {
	f.capHeight = f.ascent
	if os2 := f.tables["OS/2"]; len(os2) >= 90 && be16(os2, 0) >= 2 {
		f.capHeight = int(int16(be16(os2, 88)))
	}
	if post := f.tables["post"]; len(post) >= 16 {
		f.italicAngle = float64(int32(be32(post, 4))) / 65536
		f.fixedPitch = be32(post, 12) != 0
		f.parsePostNames(post)
	}
}

// parsePostNames reads the glyph names a version 2 post table carries.
//
// This is how a simple font's /Differences reaches a glyph: the array
// names a glyph per code, and the names mean whatever the font says they
// mean. Only the names the font spells out are read — the standard
// Macintosh ordering the format also allows is a built-in table, and a
// glyph addressed only that way is left alone.
func (f *ttfFont) parsePostNames(post []byte) {
	if len(post) < 34 || be32(post, 0) != 0x00020000 {
		return
	}
	n := int(be16(post, 32))
	if n <= 0 || n > f.numGlyphs+1 || 34+n*2 > len(post) {
		return
	}
	indices := make([]int, n)
	maxCustom := -1
	for i := 0; i < n; i++ {
		v := int(be16(post, 34+i*2))
		indices[i] = v
		if v >= 258 && v-258 > maxCustom {
			maxCustom = v - 258
		}
	}
	if maxCustom < 0 {
		return // every glyph uses the standard ordering
	}
	names := make([]string, 0, maxCustom+1)
	for p := 34 + n*2; p < len(post) && len(names) <= maxCustom; {
		length := int(post[p])
		p++
		if p+length > len(post) {
			break
		}
		names = append(names, string(post[p:p+length]))
		p += length
	}
	f.glyphNames = make(map[string]uint16, len(names))
	for gid, idx := range indices {
		if idx < 258 {
			continue
		}
		if k := idx - 258; k < len(names) {
			if _, seen := f.glyphNames[names[k]]; !seen {
				f.glyphNames[names[k]] = uint16(gid)
			}
		}
	}
}

func (f *ttfFont) parseName() {
	name := f.tables["name"]
	if len(name) < 6 {
		return
	}
	count := int(be16(name, 2))
	strOff := int(be16(name, 4))
	for i := 0; i < count && 6+i*12+12 <= len(name); i++ {
		rec := 6 + i*12
		platform := be16(name, rec)
		nameID := be16(name, rec+6)
		length := int(be16(name, rec+8))
		off := strOff + int(be16(name, rec+10))
		if nameID != 6 || off+length > len(name) {
			continue
		}
		raw := name[off : off+length]
		if platform == 3 || platform == 0 { // UTF-16BE
			var s []rune
			for j := 0; j+1 < len(raw); j += 2 {
				s = append(s, rune(be16(raw, j)))
			}
			f.psName = sanitizeName(string(s))
		} else {
			f.psName = sanitizeName(string(raw))
		}
		if f.psName != "" {
			return
		}
	}
}

// sanitizeName strips characters not allowed in a PDF name / PostScript
// font name.
func sanitizeName(s string) string {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		if r > ' ' && r < 0x7F && r != '/' && r != '[' && r != ']' && r != '(' && r != ')' && r != '<' && r != '>' && r != '{' && r != '}' && r != '%' && r != '#' {
			out = append(out, byte(r))
		}
	}
	return string(out)
}

// toEm converts font units to 1/1000 em (PDF glyph space).
func (f *ttfFont) toEm(v int) int {
	return int(math.Round(float64(v) * 1000 / float64(f.unitsPerEm)))
}

func (f *ttfFont) glyphData(gid uint16) []byte {
	if f.cff || int(gid) >= f.numGlyphs || len(f.loca) <= int(gid)+1 {
		return nil
	}
	return f.glyf[f.loca[gid]:f.loca[gid+1]]
}

// addComponents adds the component glyphs of composite glyphs to the set,
// recursively, so subset fonts keep every glyph they reference.
func (f *ttfFont) addComponents(glyphs map[uint16]bool) {
	queue := make([]uint16, 0, len(glyphs))
	for gid := range glyphs {
		queue = append(queue, gid)
	}
	for len(queue) > 0 {
		gid := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		data := f.glyphData(gid)
		if len(data) < 10 || int16(be16(data, 0)) >= 0 {
			continue // empty or simple glyph
		}
		for off := 10; off+4 <= len(data); {
			flags := be16(data, off)
			component := be16(data, off+2)
			if !glyphs[component] {
				glyphs[component] = true
				queue = append(queue, component)
			}
			off += 4
			if flags&0x0001 != 0 { // ARG_1_AND_2_ARE_WORDS
				off += 4
			} else {
				off += 2
			}
			switch {
			case flags&0x0008 != 0: // WE_HAVE_A_SCALE
				off += 2
			case flags&0x0040 != 0: // X_AND_Y_SCALE
				off += 4
			case flags&0x0080 != 0: // TWO_BY_TWO
				off += 8
			}
			if flags&0x0020 == 0 { // no MORE_COMPONENTS
				break
			}
		}
	}
}

// subset builds a valid TrueType font containing outline data only for the
// given glyphs. Glyph IDs are preserved (unused glyphs become empty), so
// the subset works directly with Identity-H encoded text.
func (f *ttfFont) subset(used map[uint16]bool) ([]byte, error) {
	if f.cff {
		return f.subsetCFFProgram(used), nil
	}
	glyphs := make(map[uint16]bool, len(used)+1)
	glyphs[0] = true
	for gid := range used {
		if int(gid) < f.numGlyphs {
			glyphs[gid] = true
		}
	}
	f.addComponents(glyphs)

	// Rebuild glyf/loca with long (32-bit) offsets, keeping glyph data
	// 4-byte aligned.
	newLoca := make([]uint32, f.numGlyphs+1)
	var newGlyf []byte
	for gid := 0; gid < f.numGlyphs; gid++ {
		newLoca[gid] = uint32(len(newGlyf))
		if glyphs[uint16(gid)] {
			data := f.glyphData(uint16(gid))
			newGlyf = append(newGlyf, data...)
			for len(newGlyf)%4 != 0 {
				newGlyf = append(newGlyf, 0)
			}
		}
	}
	newLoca[f.numGlyphs] = uint32(len(newGlyf))
	locaBytes := make([]byte, len(newLoca)*4)
	for i, v := range newLoca {
		binary.BigEndian.PutUint32(locaBytes[i*4:], v)
	}

	head := append([]byte(nil), f.tables["head"]...)
	binary.BigEndian.PutUint32(head[8:], 0)  // checkSumAdjustment, patched below
	binary.BigEndian.PutUint16(head[50:], 1) // indexToLocFormat: long

	include := [][2]interface{}{}
	add := func(tag string, data []byte) {
		if data != nil {
			include = append(include, [2]interface{}{tag, data})
		}
	}
	add("cmap", f.tables["cmap"])
	add("cvt ", f.tables["cvt "])
	add("fpgm", f.tables["fpgm"])
	add("glyf", newGlyf)
	add("head", head)
	add("hhea", f.tables["hhea"])
	add("hmtx", f.tables["hmtx"])
	add("loca", locaBytes)
	add("maxp", f.tables["maxp"])
	add("prep", f.tables["prep"])
	sort.Slice(include, func(i, j int) bool {
		return include[i][0].(string) < include[j][0].(string)
	})

	return buildSfnt(0x00010000, include), nil
}

// subsetCFFProgram rebuilds an OpenType font around a reduced CFF table.
// If anything about the font is beyond the subsetter, the original
// program is returned unchanged, which is always correct.
func (f *ttfFont) subsetCFFProgram(used map[uint16]bool) []byte {
	cff := f.tables["CFF "]
	if cff == nil {
		return f.program
	}
	reduced, err := subsetCFF(cff, used, f.numGlyphs)
	if err != nil {
		return f.program
	}
	var include [][2]interface{}
	add := func(tag string, data []byte) {
		if data != nil {
			include = append(include, [2]interface{}{tag, data})
		}
	}
	add("CFF ", reduced)
	for _, tag := range []string{"cmap", "head", "hhea", "hmtx", "maxp", "name", "OS/2", "post"} {
		add(tag, f.tables[tag])
	}
	sort.Slice(include, func(i, j int) bool {
		return include[i][0].(string) < include[j][0].(string)
	})
	out := buildSfnt(0x4F54544F, include) // 'OTTO'

	// A subset that will not parse back is not worth shipping.
	if again, err := parseTTF(out); err != nil || again.numGlyphs != f.numGlyphs {
		return f.program
	}
	return out
}

// buildSfnt assembles a font file from its tables, which must already be
// sorted by tag.
func buildSfnt(version uint32, include [][2]interface{}) []byte {
	numTables := len(include)
	entrySelector := 0
	for 1<<(entrySelector+1) <= numTables {
		entrySelector++
	}
	searchRange := (1 << entrySelector) * 16

	out := make([]byte, 12+numTables*16)
	binary.BigEndian.PutUint32(out[0:], version)
	binary.BigEndian.PutUint16(out[4:], uint16(numTables))
	binary.BigEndian.PutUint16(out[6:], uint16(searchRange))
	binary.BigEndian.PutUint16(out[8:], uint16(entrySelector))
	binary.BigEndian.PutUint16(out[10:], uint16(numTables*16-searchRange))

	headOffset := -1
	for i, entry := range include {
		tag, data := entry[0].(string), entry[1].([]byte)
		offset := len(out)
		if tag == "head" {
			headOffset = offset
		}
		rec := 12 + i*16
		copy(out[rec:], tag)
		binary.BigEndian.PutUint32(out[rec+4:], tableChecksum(data))
		binary.BigEndian.PutUint32(out[rec+8:], uint32(offset))
		binary.BigEndian.PutUint32(out[rec+12:], uint32(len(data)))
		out = append(out, data...)
		for len(out)%4 != 0 {
			out = append(out, 0)
		}
	}

	// Whole-font checksum, stored in head.checkSumAdjustment.
	if headOffset >= 0 {
		binary.BigEndian.PutUint32(out[headOffset+8:], 0)
		adjustment := 0xB1B0AFBA - tableChecksum(out)
		binary.BigEndian.PutUint32(out[headOffset+8:], adjustment)
	}
	return out
}

func tableChecksum(data []byte) uint32 {
	var sum uint32
	for i := 0; i < len(data); i += 4 {
		var v uint32
		for j := 0; j < 4; j++ {
			v <<= 8
			if i+j < len(data) {
				v |= uint32(data[i+j])
			}
		}
		sum += v
	}
	return sum
}
