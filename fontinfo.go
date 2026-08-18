package gopdf

import (
	"fmt"
	"strings"
)

// fontInfo describes a font that already exists in a parsed document. It
// can both decode the font's character codes to text and encode text back
// into those codes, and it knows each code's advance width — everything
// needed to rewrite a run of text without disturbing the layout.
type fontInfo struct {
	decoder *fontDecoder
	name    Name // resource name in the page's /Font dictionary

	cid      bool
	encode   map[rune][]byte // text -> character code(s)
	widths   map[uint32]float64
	defWidth float64

	// A font embedded as a subset contains only the glyphs the document
	// actually draws, so an encoding table alone does not prove a glyph
	// exists. observed records the codes the content streams really use,
	// and glyphs holds the glyph IDs found in the embedded font program;
	// together they bound what can safely be written back.
	embedded bool
	observed map[uint32]bool
	glyphs   map[uint32]bool
	built    bool

	// A Type 3 font draws each glyph with a content stream of its own.
	// Its widths are in glyph space rather than the 1/1000 em every
	// other font uses, and only the codes with a procedure behind them
	// exist at all.
	type3 bool
	procs map[uint32]bool

	// baseFont is the font's /BaseFont with its subset prefix intact,
	// which is what names the face; the resource name is only what this
	// page happened to call it.
	baseFont string

	// builtin is set when this describes one of the standard fourteen
	// rather than a font read out of the document, which is how a
	// substituted token is measured when the document's own font cannot
	// set it.
	builtin *Font
	// bold and italic describe the original's face, so a fallback can be
	// chosen to match it.
	bold, italic bool
}

// newFontInfo builds encoding and metric tables for one font resource.
func newFontInfo(r *Reader, name Name, dict Dict, decoder *fontDecoder) *fontInfo {
	fi := &fontInfo{
		decoder:  decoder,
		name:     name,
		cid:      decoder.cid,
		encode:   make(map[rune][]byte),
		widths:   make(map[uint32]float64),
		observed: make(map[uint32]bool),
	}
	if fi.cid {
		fi.loadCIDWidths(r, dict)
	} else {
		fi.loadSimpleWidths(r, dict)
	}
	fi.loadEmbedded(r, dict)
	fi.loadFace(r, dict)
	fi.baseFont = baseFontName(r, dict)
	return fi
}

// baseFontName returns the /BaseFont of a font or, for a Type 0, of its
// descendant, keeping the subset prefix.
func baseFontName(r *Reader, dict Dict) string {
	if base, ok := r.resolve(dict["BaseFont"]).(Name); ok {
		return string(base)
	}
	if kids, ok := r.resolve(dict["DescendantFonts"]).(Array); ok && len(kids) > 0 {
		if d, ok := r.resolve(kids[0]).(Dict); ok {
			if base, ok := r.resolve(d["BaseFont"]).(Name); ok {
				return string(base)
			}
		}
	}
	// A Type 3 font has no BaseFont; its /Name is the nearest thing.
	if n, ok := r.resolve(dict["Name"]).(Name); ok {
		return string(n)
	}
	return ""
}

// loadFace works out whether the font is bold or italic, from the
// descriptor where it says so and from the name where it does not. It is
// what lets a substituted token be set in a face that matches.
func (fi *fontInfo) loadFace(r *Reader, dict Dict) {
	target := dict
	if fi.cid {
		if desc, ok := r.resolve(dict["DescendantFonts"]).(Array); ok && len(desc) > 0 {
			if d, ok := r.resolve(desc[0]).(Dict); ok {
				target = d
			}
		}
	}
	if fd, ok := r.resolve(target["FontDescriptor"]).(Dict); ok {
		if flags, ok := toInt(r.resolve(fd["Flags"])); ok {
			const italicFlag = 1 << 6 // bit 7, counting from one
			const forceBold = 1 << 18 // bit 19
			fi.italic = flags&italicFlag != 0
			fi.bold = flags&forceBold != 0
		}
		if sw, ok := toFloat(r.resolve(fd["StemV"])); ok && sw >= 120 {
			fi.bold = true
		}
	}
	// The name is the more reliable signal in practice: producers set the
	// flags inconsistently but almost always name the face.
	base, _ := r.resolve(target["BaseFont"]).(Name)
	name := strings.ToLower(string(base))
	if strings.Contains(name, "bold") || strings.Contains(name, "black") ||
		strings.Contains(name, "heavy") {
		fi.bold = true
	}
	if strings.Contains(name, "italic") || strings.Contains(name, "oblique") {
		fi.italic = true
	}
}

// loadEmbedded records whether the font program travels with the file and,
// for embedded TrueType outlines, which glyph IDs it actually contains.
func (fi *fontInfo) loadEmbedded(r *Reader, dict Dict) {
	target := dict
	if fi.cid {
		if desc, ok := r.resolve(dict["DescendantFonts"]).(Array); ok && len(desc) > 0 {
			if d, ok := r.resolve(desc[0]).(Dict); ok {
				target = d
			}
		}
	}
	fd, ok := r.resolve(target["FontDescriptor"]).(Dict)
	if !ok {
		return
	}
	var program *rawStream
	for _, key := range []Name{"FontFile2", "FontFile", "FontFile3"} {
		if s, ok := r.resolve(fd[key]).(*rawStream); ok {
			fi.embedded = true
			if key == "FontFile2" {
				program = s
			}
			break
		}
	}
	if program == nil {
		return
	}
	data, err := r.decodeStream(program.dict, program.data)
	if err != nil {
		return
	}
	ttf, err := parseTTF(data)
	if err != nil {
		return
	}
	// With Identity CID-to-GID mapping (what subsetters emit), the
	// character code is the glyph ID, so a non-empty outline proves the
	// glyph is present.
	fi.glyphs = make(map[uint32]bool)
	for gid := 0; gid < ttf.numGlyphs; gid++ {
		if len(ttf.glyphData(uint16(gid))) > 0 {
			fi.glyphs[uint32(gid)] = true
		}
	}
}

// observe records character codes seen in the content stream; those
// glyphs are demonstrably present in the font.
func (fi *fontInfo) observe(s []byte) {
	for _, c := range fi.codes(s) {
		fi.observed[c] = true
	}
}

// canUse reports whether a character code is safe to write back.
func (fi *fontInfo) canUse(code uint32) bool {
	if fi.type3 {
		// A Type 3 glyph exists only if the font carries a procedure
		// that draws it.
		return fi.procs == nil || fi.procs[code]
	}
	if !fi.embedded {
		return true // the viewer supplies the full font
	}
	if fi.observed[code] {
		return true // the page already draws this glyph
	}
	// Glyphs with outlines in an Identity-mapped embedded program are
	// present even if this page does not use them.
	return fi.cid && fi.glyphs != nil && fi.glyphs[code]
}

// buildEncoder inverts the font's decoding tables, keeping only characters
// the font can genuinely render, so replacement text the font does not
// cover is reported rather than silently drawn as blank boxes.
func (fi *fontInfo) buildEncoder() {
	if fi.built {
		return
	}
	fi.built = true
	// A font's ToUnicode map is authoritative where it exists.
	for code, text := range fi.decoder.toUnicode {
		runes := []rune(text)
		if len(runes) != 1 {
			continue // ligatures and multi-rune mappings are not invertible
		}
		if _, taken := fi.encode[runes[0]]; taken || !fi.canUse(code) {
			continue
		}
		if fi.cid {
			fi.encode[runes[0]] = []byte{byte(code >> 8), byte(code)}
		} else if code < 256 {
			fi.encode[runes[0]] = []byte{byte(code)}
		}
	}
	if fi.decoder.encoding != nil {
		for code, r := range fi.decoder.encoding {
			if r == 0 || !fi.canUse(uint32(code)) {
				continue
			}
			// A subset font is free to put any glyph at any code, and
			// its ToUnicode says which. Where the two disagree the map
			// wins: writing code 0x54 because an encoding calls it "T"
			// drew a P in a font whose own map says 0x54 is a P, and the
			// token came out as nonsense that still looked like text.
			if fi.contradicts(uint32(code), r) {
				continue
			}
			if _, taken := fi.encode[r]; !taken {
				fi.encode[r] = []byte{byte(code)}
			}
		}
	}
}

// contradicts reports whether the font's own ToUnicode map says a code
// means something other than the rune an encoding claims for it.
func (fi *fontInfo) contradicts(code uint32, r rune) bool {
	if fi.decoder == nil || fi.decoder.toUnicode == nil {
		return false
	}
	text, ok := fi.decoder.toUnicode[code]
	if !ok {
		return false // the map is silent, so it disagrees with nothing
	}
	runes := []rune(text)
	return len(runes) != 1 || runes[0] != r
}

func (fi *fontInfo) loadSimpleWidths(r *Reader, dict Dict) {
	fi.type3 = r.resolve(dict["Subtype"]) == Name("Type3")
	// A Type 3 font measures its glyphs in whatever space its font
	// matrix defines; scaling by that matrix puts the widths into the
	// 1/1000 em the rest of this package works in.
	scale := 1.0
	if fi.type3 {
		scale = type3WidthScale(r, dict)
		fi.loadType3Procs(r, dict)
	}

	fi.defWidth = 0
	if fd, ok := r.resolve(dict["FontDescriptor"]).(Dict); ok {
		if mw, ok := toFloat(r.resolve(fd["MissingWidth"])); ok {
			fi.defWidth = mw * scale
		}
	}
	first, _ := toInt(r.resolve(dict["FirstChar"]))
	widths, ok := r.resolve(dict["Widths"]).(Array)
	if !ok {
		// No /Widths: fall back to the standard-font metrics when the
		// base font is one of the standard 14.
		fi.loadStandardWidths(r, dict)
		return
	}
	for i, e := range widths {
		if w, ok := toFloat(r.resolve(e)); ok {
			fi.widths[uint32(first+i)] = w * scale
		}
	}
}

// type3WidthScale converts a Type 3 font's glyph-space widths into the
// 1/1000 em used elsewhere, by way of the horizontal component of its
// font matrix. The usual matrix is [0.001 0 0 0.001 0 0], which leaves
// the widths unchanged, but the entry is free to be anything.
func type3WidthScale(r *Reader, dict Dict) float64 {
	m, ok := r.resolve(dict["FontMatrix"]).(Array)
	if !ok || len(m) != 6 {
		return 1
	}
	a, ok := toFloat(r.resolve(m[0]))
	if !ok || a == 0 {
		return 1
	}
	return a * 1000
}

// loadType3Procs records which character codes the font actually draws,
// by pairing its encoding differences with the procedures it carries.
// A code with no procedure behind it renders as nothing, so writing one
// back would silently drop text.
func (fi *fontInfo) loadType3Procs(r *Reader, dict Dict) {
	procs, ok := r.resolve(dict["CharProcs"]).(Dict)
	if !ok {
		return
	}
	enc, ok := r.resolve(dict["Encoding"]).(Dict)
	if !ok {
		return
	}
	diffs, ok := r.resolve(enc["Differences"]).(Array)
	if !ok {
		return
	}
	fi.procs = make(map[uint32]bool)
	code := 0
	for _, e := range diffs {
		switch t := r.resolve(e).(type) {
		case int64:
			code = int(t)
		case float64:
			code = int(t)
		case Name:
			if code >= 0 && code < 256 {
				if _, has := procs[t]; has {
					fi.procs[uint32(code)] = true
				}
				code++
			}
		}
	}
}

// loadStandardWidths supplies metrics for non-embedded standard fonts,
// which legally omit /Widths.
func (fi *fontInfo) loadStandardWidths(r *Reader, dict Dict) {
	base, _ := r.resolve(dict["BaseFont"]).(Name)
	name := string(base)
	if i := strings.IndexByte(name, '+'); i >= 0 && i+1 < len(name) {
		name = name[i+1:]
	}
	var std *Font
	for _, f := range []*Font{
		Helvetica, HelveticaBold, HelveticaOblique, HelveticaBoldOblique,
		TimesRoman, TimesBold, TimesItalic, TimesBoldItalic,
		Courier, CourierBold, CourierOblique, CourierBoldOblique,
		Symbol, ZapfDingbats,
	} {
		if f.name == name {
			std = f
			break
		}
	}
	if std == nil {
		fi.defWidth = 500
		return
	}
	fi.defWidth = float64(std.defaultWidth)
	for code := 0; code < 256; code++ {
		fi.widths[uint32(code)] = float64(std.glyphWidth(byte(code)))
	}
}

func (fi *fontInfo) loadCIDWidths(r *Reader, dict Dict) {
	fi.defWidth = 1000
	descendants, ok := r.resolve(dict["DescendantFonts"]).(Array)
	if !ok || len(descendants) == 0 {
		return
	}
	desc, ok := r.resolve(descendants[0]).(Dict)
	if !ok {
		return
	}
	if dw, ok := toFloat(r.resolve(desc["DW"])); ok {
		fi.defWidth = dw
	}
	w, ok := r.resolve(desc["W"]).(Array)
	if !ok {
		return
	}
	// /W is a sequence of either "c [w1 w2 ...]" or "cFirst cLast w".
	for i := 0; i < len(w); {
		start, ok := toInt(r.resolve(w[i]))
		if !ok || i+1 >= len(w) {
			return
		}
		switch next := r.resolve(w[i+1]).(type) {
		case Array:
			for j, e := range next {
				if width, ok := toFloat(r.resolve(e)); ok {
					fi.widths[uint32(start+j)] = width
				}
			}
			i += 2
		default:
			end, ok1 := toInt(next)
			if !ok1 || i+2 >= len(w) {
				return
			}
			width, ok2 := toFloat(r.resolve(w[i+2]))
			if ok2 && end >= start && end-start <= 1<<16 {
				for c := start; c <= end; c++ {
					fi.widths[uint32(c)] = width
				}
			}
			i += 3
		}
	}
}

// codeWidth returns the advance width of one character code, in 1/1000 em.
func (fi *fontInfo) codeWidth(code uint32) float64 {
	if w, ok := fi.widths[code]; ok {
		return w
	}
	return fi.defWidth
}

// codes splits an encoded string into its character codes.
func (fi *fontInfo) codes(s []byte) []uint32 {
	step := 1
	if fi.cid {
		step = 2
	}
	out := make([]uint32, 0, len(s)/step)
	for i := 0; i+step <= len(s); i += step {
		var c uint32
		for k := 0; k < step; k++ {
			c = c<<8 | uint32(s[i+k])
		}
		out = append(out, c)
	}
	return out
}

// stringWidth returns the unscaled advance of an encoded string, in
// 1/1000 em, including per-character and word spacing.
func (fi *fontInfo) stringWidth(s []byte, charSpacing, wordSpacing, fontSize float64) float64 {
	if fontSize == 0 {
		return 0
	}
	var total float64
	for _, c := range fi.codes(s) {
		total += fi.codeWidth(c)
		// Spacing is in text-space units; convert to 1/1000 em.
		total += charSpacing * 1000 / fontSize
		if !fi.cid && c == 32 {
			total += wordSpacing * 1000 / fontSize
		}
	}
	return total
}

// spaceWidth1000 returns how wide a space is in this font, in
// thousandths of an em, and whether the font actually says.
//
// Asking for code 32 is only right for a simple font. In a CID font the
// codes are CIDs, so 32 is whichever glyph happens to sit there, and
// codeWidth answers for a code the font does not list with /DW — a full
// em, three times too wide for a space. Either way that is a guess in a
// measurement's clothes, and a space three times too wide stops the
// extractor seeing word breaks at all.
//
// Going through the encoder asks the question properly: what code does
// this font use for a space, and how wide is that? A font that has no
// space says so, which is its own useful answer.
func (fi *fontInfo) spaceWidth1000() (float64, bool) {
	fi.buildEncoder()
	code, ok := fi.encode[' ']
	if !ok {
		return 0, false
	}
	// A size of 1000 with no spacing leaves the glyph width alone.
	if w := fi.stringWidth(code, 0, 0, 1000); w > 0 {
		return w, true
	}
	return 0, false
}

// encodeText converts text into the font's character codes, reporting the
// first character the font cannot represent.
func (fi *fontInfo) encodeText(s string) ([]byte, error) {
	fi.buildEncoder()
	out := make([]byte, 0, len(s)*2)
	for _, r := range s {
		code, ok := fi.encode[r]
		if !ok {
			hint := "the font in the source file has no glyph for it"
			if fi.embedded {
				hint = "the source file embeds only a subset of this font, " +
					"which does not include that character"
			}
			return nil, fmt.Errorf("gopdf: font /%s cannot represent %q: %s",
				fi.name, r, hint)
		}
		out = append(out, code...)
	}
	return out, nil
}
