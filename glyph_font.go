package gopdf

import (
	"strconv"
	"strings"
)

// Finding the outline for a character code.
//
// A PDF font dictionary says how text is encoded; the font program it
// carries says what the shapes are. Getting from one to the other is the
// fiddly part, and it goes a different way for each kind of font:
//
//   - A composite font addresses glyphs by CID. With a TrueType program
//     the CID becomes a glyph index through /CIDToGIDMap, which is nearly
//     always the identity. With a CID-keyed CFF program the charset says
//     which glyph carries which CID.
//   - A simple TrueType font addresses glyphs by byte, looked up in the
//     font's own character map — by Unicode where the font is text, and
//     by the code itself where the font is a symbol font, whose cmap is
//     conventionally keyed at F000.
//
// Simple fonts with a bare PostScript program are the gap. Reaching their
// glyphs means resolving glyph names against the built-in encodings,
// which this package deliberately does not carry. Their text is left
// undrawn rather than drawn wrong, and RenderPage reports how much was
// left out.

// glyphFont turns character codes into outlines.
type glyphFont struct {
	ttf *ttfFont
	cff *cffOutlines

	// unitScale maps font units to text space, where an em is 1.
	unitScale matrix

	cid      bool
	cidToGID []uint16 // from a /CIDToGIDMap stream
	symbolic bool

	// encoding is the simple font's code-to-rune table, used to look a
	// glyph up in the font's character map.
	encoding *[256]rune
	// names holds the glyph names a simple font's /Differences gives,
	// which address a glyph directly where a character map cannot.
	names map[uint32]string

	// substituted marks a font whose shapes come from a stand-in rather
	// than from the document, in which case codes are resolved through
	// characters instead of through glyph indices.
	substituted bool
	toUnicode   map[uint32]string
}

// loadGlyphFont reads the font program a font dictionary carries.
// It returns nil when the font has no program this package can draw.
func loadGlyphFont(r *Reader, dict Dict, substitute func(FontRequest) []byte) *glyphFont {
	target := dict
	composite := r.resolve(dict["Subtype"]) == Name("Type0")
	if composite {
		desc, ok := r.resolve(dict["DescendantFonts"]).(Array)
		if !ok || len(desc) == 0 {
			return nil
		}
		if d, ok := r.resolve(desc[0]).(Dict); ok {
			target = d
		}
	}
	fd, _ := r.resolve(target["FontDescriptor"]).(Dict)
	g := &glyphFont{cid: composite}
	if flags, ok := toInt(r.resolve(fd["Flags"])); ok {
		g.symbolic = flags&4 != 0 && flags&32 == 0
	}

	switch {
	case g.loadProgram(r, fd, "FontFile2"):
	case g.loadProgram(r, fd, "FontFile3"):
	}
	if composite {
		g.loadCIDToGID(r, target)
	} else {
		g.encoding = simpleEncoding(r, dict["Encoding"])
		g.names = encodingNames(r, dict["Encoding"])
	}
	if g.addressable() {
		return g
	}

	// The program is missing, or it is there and cannot be addressed: a
	// bare PostScript font is indexed by glyph name, and the names lead
	// through the built-in encodings this package does not carry. Either
	// way the way through is a substitute, which is addressed by
	// character — and the character is something the document's own
	// /Encoding gives, with no built-in table involved.
	*g = glyphFont{cid: composite, encoding: g.encoding}
	if substitute == nil || !g.loadSubstitute(r, dict, fd, substitute) {
		return nil
	}
	return g
}

// addressable reports whether a character code can be turned into a
// glyph of the program that was loaded.
//
// A composite font addresses glyphs by number, which always works. A
// simple font addresses them through the program's own character map,
// so a program without one — a bare CFF, which has glyph names instead —
// is of no use however complete it is.
func (g *glyphFont) addressable() bool {
	if g.ttf == nil && g.cff == nil {
		return false
	}
	if g.cid {
		return true
	}
	if g.cff != nil && g.ttf == nil {
		// A bare CFF is indexed by glyph name through the built-in
		// encodings, which this package does not carry.
		return false
	}
	// A TrueType program can be addressed by its character map, by the
	// glyph names the document gives, or — where it has no map at all —
	// by treating the code as the index, which is what a subsetter that
	// wrote no map expects.
	return g.ttf != nil
}

// encodingNames reads the glyph names an /Encoding names per code.
func encodingNames(r *Reader, encoding any) map[uint32]string {
	d, ok := r.resolve(encoding).(Dict)
	if !ok {
		return nil
	}
	diff, ok := r.resolve(d["Differences"]).(Array)
	if !ok {
		return nil
	}
	out := make(map[uint32]string)
	code := 0
	for _, e := range diff {
		switch v := r.resolve(e).(type) {
		case Name:
			if code >= 0 && code < 65536 {
				out[uint32(code)] = string(v)
			}
			code++
		default:
			if n, ok := toInt(v); ok {
				code = n
			}
		}
	}
	return out
}

// namedGlyph resolves a glyph name to an index.
//
// Two things can answer: the font's own post table, which spells its
// glyph names out, and the convention by which a subsetter names a glyph
// after its number — "g3", "glyph12", "index7", "cid44". Both are the
// document and the font talking about themselves, with no built-in
// table in between.
func (g *glyphFont) namedGlyph(name string) (uint16, bool) {
	if g.ttf != nil && g.ttf.glyphNames != nil {
		if gid, ok := g.ttf.glyphNames[name]; ok {
			return gid, true
		}
	}
	for _, prefix := range []string{"g", "glyph", "index", "cid", "G"} {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := name[len(prefix):]
		if rest == "" {
			continue
		}
		n, err := strconv.Atoi(rest)
		if err != nil || n < 0 || n > 65535 {
			continue
		}
		return uint16(n), true
	}
	return 0, false
}

// loadProgram reads one font-file entry and keeps whatever outlines it
// finds.
func (g *glyphFont) loadProgram(r *Reader, fd Dict, key Name) bool {
	stm, ok := r.resolve(fd[key]).(*rawStream)
	if !ok {
		return false
	}
	data, err := r.decodeStream(stm.dict, stm.data)
	if err != nil || len(data) < 4 {
		return false
	}
	// A bare CFF program starts with its own one-byte version, not an
	// sfnt tag, so the two are told apart by what the file begins with.
	tag := string(data[:4])
	sfnt := tag == "\x00\x01\x00\x00" || tag == "OTTO" || tag == "true" ||
		tag == "ttcf" || tag == "\x74\x72\x75\x65"
	if !sfnt {
		cff, err := parseCFFOutlines(data)
		if err != nil {
			return false
		}
		g.cff = cff
		g.unitScale = cff.fontMatrix
		return true
	}
	f, err := parseTTF(data)
	if err != nil {
		return false
	}
	if f.cff {
		// An OpenType font with PostScript outlines keeps them in a table
		// of its own, and its character map still applies.
		cff, err := parseCFFOutlines(f.tables["CFF "])
		if err != nil {
			return false
		}
		g.cff, g.ttf = cff, f
		g.unitScale = cff.fontMatrix
		return true
	}
	if f.unitsPerEm <= 0 {
		return false
	}
	g.ttf = f
	s := 1 / float64(f.unitsPerEm)
	g.unitScale = matrix{s, 0, 0, s, 0, 0}
	return true
}

// loadSubstitute asks the caller for a stand-in font program.
//
// The substitute supplies shapes and nothing else: widths still come from
// the document, so the text is spaced as the document intends. A
// composite font whose CIDs are glyph indices into a font that is no
// longer there cannot be substituted, because the numbers mean nothing
// in the replacement — unless the document says what the CIDs stand for,
// which is what ToUnicode does.
func (g *glyphFont) loadSubstitute(r *Reader, dict, fd Dict,
	substitute func(FontRequest) []byte) bool {

	data := substitute(fontRequestFor(r, dict, fd))
	if len(data) < 4 {
		return false
	}
	f, err := parseTTF(data)
	if err != nil {
		return false
	}
	if f.cff {
		cff, err := parseCFFOutlines(f.tables["CFF "])
		if err != nil {
			return false
		}
		g.cff = cff
		g.unitScale = cff.fontMatrix
	} else {
		if f.unitsPerEm <= 0 {
			return false
		}
		s := 1 / float64(f.unitsPerEm)
		g.unitScale = matrix{s, 0, 0, s, 0, 0}
	}
	g.ttf = f
	g.substituted = true
	g.symbolic = false // the substitute's map is a Unicode one

	// A stand-in is addressed by character, so the codes have to mean
	// characters. /ToUnicode says so outright, and a simple font's
	// /Encoding says so for the codes it names — a custom encoding of
	// glyph names leaves the rest to /ToUnicode, which is why both are
	// read and the encoding is only the fallback.
	if tu, ok := r.resolve(dict["ToUnicode"]).(*rawStream); ok {
		if b, err := r.decodeStream(tu.dict, tu.data); err == nil {
			g.toUnicode = parseToUnicodeCMap(b)
		}
	}
	if g.cid && g.toUnicode == nil {
		// A composite font's codes are glyph numbers in a font that is
		// not here; without /ToUnicode they mean nothing at all.
		return false
	}
	return true
}

// loadCIDToGID reads a composite font's mapping from CID to glyph.
func (g *glyphFont) loadCIDToGID(r *Reader, target Dict) {
	stm, ok := r.resolve(target["CIDToGIDMap"]).(*rawStream)
	if !ok {
		return // /Identity, named or absent
	}
	data, err := r.decodeStream(stm.dict, stm.data)
	if err != nil {
		return
	}
	g.cidToGID = make([]uint16, len(data)/2)
	for i := range g.cidToGID {
		g.cidToGID[i] = be16(data, i*2)
	}
}

// gid returns the glyph a character code selects.
func (g *glyphFont) gid(code uint32) (uint16, bool) {
	if g.substituted {
		// A stand-in knows nothing of the original's glyph numbering, so
		// the code has to be turned into a character first.
		var ru rune
		if s := g.toUnicode[code]; s != "" {
			ru = []rune(s)[0]
		} else if g.encoding != nil && code < 256 && g.encoding[code] != 0 {
			ru = g.encoding[code]
		} else if !g.cid {
			ru = rune(code)
		}
		if ru == 0 || g.ttf == nil || g.ttf.cmap == nil {
			return 0, false
		}
		gid, ok := g.ttf.cmap[ru]
		return gid, ok && gid != 0
	}
	if g.cid {
		cid := uint16(code)
		if g.cidToGID != nil {
			if int(cid) >= len(g.cidToGID) {
				return 0, false
			}
			return g.cidToGID[cid], true
		}
		if g.cff != nil && g.cff.charsetCIDs != nil {
			return g.cff.gidForCID(cid)
		}
		return cid, true
	}

	// A name the document gave is the most direct answer there is: it
	// names the glyph rather than describing it.
	if name := g.names[code]; name != "" {
		if gid, ok := g.namedGlyph(name); ok {
			return gid, true
		}
	}
	// A simple font otherwise goes through the program's character map.
	if g.ttf != nil && len(g.ttf.cmap) > 0 {
		if g.symbolic || g.ttf.symbolCmap {
			// A symbol font's map is keyed either at F000 or at the code
			// itself, and fonts disagree about which.
			if gid, ok := g.ttf.cmap[rune(0xF000+code)]; ok && gid != 0 {
				return gid, true
			}
			if gid, ok := g.ttf.cmap[rune(code)]; ok && gid != 0 {
				return gid, true
			}
		}
		if g.encoding != nil && code < 256 {
			if ru := g.encoding[code]; ru != 0 {
				if gid, ok := g.ttf.cmap[ru]; ok && gid != 0 {
					return gid, true
				}
			}
		}
		if gid, ok := g.ttf.cmap[rune(code)]; ok && gid != 0 {
			return gid, true
		}
		if gid, ok := g.ttf.cmap[rune(0xF000+code)]; ok && gid != 0 {
			return gid, true
		}
		// A font with a map that does not mention the code has nothing
		// to say; using the code as a glyph index would draw a wrong
		// letter, which is worse than drawing none.
		return 0, false
	}
	if g.ttf != nil {
		// No character map at all. A subsetter that wrote none meant the
		// code to be the glyph index, and that is what viewers do.
		if int(code) < g.ttf.numGlyphs {
			return uint16(code), true
		}
	}
	// A bare CFF simple font needs glyph names to be addressed, which is
	// the case this package does not handle.
	return 0, false
}

// outline returns a glyph's shape in font units.
func (g *glyphFont) outline(gid uint16) *glyphOutline {
	if g.cff != nil {
		return g.cff.outline(gid)
	}
	if g.ttf != nil {
		return g.ttf.outline(gid)
	}
	return nil
}
