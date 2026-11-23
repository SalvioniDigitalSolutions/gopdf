package gopdf

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// maxExtractedText bounds PageText output for pathological files.
const maxExtractedText = 8 << 20

// PageText extracts the text of a page (0-based index) in content order,
// including text inside nested form XObjects (so pages imported from other
// documents extract too). Line breaks are inferred from vertical movement
// of the text cursor.
//
// Text in fonts with a ToUnicode CMap (including everything this library
// generates) extracts exactly; other fonts fall back to their declared
// encoding, approximated as WinAnsi when the encoding is nonstandard.
func (r *Reader) PageText(index int) (string, error) {
	if index < 0 || index >= len(r.pages) {
		return "", fmt.Errorf("gopdf: page %d out of range", index)
	}
	pi := r.pages[index]
	content, err := r.pageContent(pi.dict)
	if err != nil {
		return "", err
	}
	ex := &textExtractor{r: r, lastY: math.Inf(1), visited: make(map[Ref]bool)}
	ex.run(content, pi.resources, identityMatrix, 0)
	return ex.sb.String(), nil
}

// textExtractor interprets content streams, accumulating decoded text.
type textExtractor struct {
	r       *Reader
	sb      strings.Builder
	lastY   float64
	started bool
	visited map[Ref]bool // form XObjects currently being expanded
	// patterns holds the tiling patterns already read: a pattern paints
	// its cell many times and says what it says once.
	patterns map[*rawStream]bool
}

const maxFormDepth = 12

// spaceFraction is how much of a space a gap has to span before it reads
// as a word break. Measured against pdftotext over 918 real documents,
// agreement peaks here and stays flat from about 0.65 to 0.85, so the
// value is a plateau rather than a knife edge.
const spaceFraction = 0.70

// run interprets one content stream. base is the transform mapping the
// stream's coordinates onto the page, so text positions stay comparable
// across nested forms.
func (ex *textExtractor) run(content []byte, resources any, base matrix, depth int) {
	fonts := newFontDecoders(ex.r, resources)
	var cur *fontDecoder

	ctm := base
	tm, tlm := identityMatrix, identityMatrix
	leading := 0.0
	// The text state that decides what a string says and how far it
	// carries the pen. It is part of the graphics state, so q and Q save
	// and restore it along with the transform: a footnote set inside
	// "q /F2 9 Tf ... Q" must leave the font it found.
	fontSize, charSpacing, wordSpacing := 0.0, 0.0, 0.0
	horizScale := 1.0
	// penPos is where the last string left the pen, on the page.
	penPos := [2]float64{}

	type saved struct {
		ctm                              matrix
		font                             *fontDecoder
		size, charSp, wordSp, hScale, ld float64
	}
	var gsStack []saved

	translateLine := func(tx, ty float64) {
		tlm = matrix{tlm[0], tlm[1], tlm[2], tlm[3],
			tx*tlm[0] + ty*tlm[2] + tlm[4], tx*tlm[1] + ty*tlm[3] + tlm[5]}
		tm = tlm
	}
	// advancePen moves the text matrix along by dx text-space units,
	// which is what a shown string or a kern does.
	advancePen := func(dx float64) {
		tm = matrix{1, 0, 0, 1, dx, 0}.mul(tm)
	}

	show := func(s String) {
		if cur == nil || ex.sb.Len() > maxExtractedText {
			return
		}
		full := tm.mul(ctm)
		x, y := full.apply(0, 0)
		width := cur.advance(s, fontSize, charSpacing, wordSpacing, horizScale)
		scale := scaleOf(full)

		// Distances are measured along the baseline and across it rather
		// than in page x and y, so that text set at an angle — a rotated
		// page, a sideways caption — is read the same as any other.
		ux, uy := baselineDir(full)
		dx, dy := x-penPos[0], y-penPos[1]
		along := dx*ux + dy*uy
		across := -dx*uy + dy*ux

		text := cur.decode(s)
		if text != "" {
			switch {
			case ex.started && math.Abs(across) > 0.5:
				ex.sb.WriteByte('\n')
			case ex.started && needsSpace(ex.sb.String(), text, along,
				cur.spaceWidth(fontSize, horizScale)*scale):
				ex.sb.WriteByte(' ')
			}
			ex.sb.WriteString(text)
			ex.lastY = y
			ex.started = true
		}
		advancePen(width)
		penPos = [2]float64{x + width*scale*ux, y + width*scale*uy}
	}

	p := &parser{data: content}
	var operands []any
	for {
		tok, err := p.next()
		if err == io.EOF {
			return
		}
		if err != nil {
			// Tolerate junk mid-stream: resynchronize on next byte.
			p.pos++
			operands = operands[:0]
			continue
		}
		op, isOp := tok.(opKeyword)
		if !isOp {
			if len(operands) < 16 {
				operands = append(operands, tok)
			}
			continue
		}
		num := func(i int) float64 {
			if i < len(operands) {
				if f, ok := toFloat(operands[i]); ok {
					return f
				}
			}
			return 0
		}
		switch string(op) {
		case "q":
			if len(gsStack) < 64 {
				gsStack = append(gsStack, saved{ctm, cur, fontSize,
					charSpacing, wordSpacing, horizScale, leading})
			}
		case "Q":
			if n := len(gsStack); n > 0 {
				s := gsStack[n-1]
				gsStack = gsStack[:n-1]
				ctm, cur = s.ctm, s.font
				fontSize, charSpacing, wordSpacing = s.size, s.charSp, s.wordSp
				horizScale, leading = s.hScale, s.ld
			}
		case "cm":
			if len(operands) >= 6 {
				var m matrix
				for i := 0; i < 6; i++ {
					m[i] = num(i)
				}
				ctm = m.mul(ctm)
			}
		case "Do":
			if len(operands) >= 1 && depth < maxFormDepth {
				if n, ok := operands[0].(Name); ok {
					ex.runForm(n, resources, ctm, depth)
				}
			}
		case "ID":
			// The data of a picture written into the instructions is
			// passed over: read as instructions it swallows the text
			// that follows it (render_inline.go).
			from := p.pos
			if from < len(content) && isWS(content[from]) {
				from++
			}
			_, p.pos = inlineImageData(noPage, content, from, inlineValuesDict(operands))
		case "scn", "SCN":
			// A tiling pattern chosen to paint with says what its cell
			// says (runPattern).
			if len(operands) >= 1 && depth < maxFormDepth {
				if n, ok := operands[len(operands)-1].(Name); ok {
					ex.runPattern(n, resources, base, depth)
				}
			}
		case "BT":
			tm, tlm = identityMatrix, identityMatrix
		case "Tm":
			if len(operands) >= 6 {
				for i := 0; i < 6; i++ {
					tlm[i] = num(i)
				}
				tm = tlm
			}
		case "Td":
			if len(operands) >= 2 {
				translateLine(num(0), num(1))
			}
		case "TD":
			if len(operands) >= 2 {
				leading = -num(1)
				translateLine(num(0), num(1))
			}
		case "TL":
			leading = num(0)
		case "T*":
			translateLine(0, -leading)
		case "Tf":
			if len(operands) >= 2 {
				if n, ok := operands[0].(Name); ok {
					cur = fonts.get(n)
				}
				fontSize = num(1)
			}
		case "Tc":
			charSpacing = num(0)
		case "Tw":
			wordSpacing = num(0)
		case "Tz":
			if horizScale = num(0) / 100; horizScale == 0 {
				horizScale = 1
			}
		case "Tj":
			if len(operands) >= 1 {
				if s, ok := operands[0].(String); ok {
					show(s)
				}
			}
		case "'":
			translateLine(0, -leading)
			if len(operands) >= 1 {
				if s, ok := operands[0].(String); ok {
					show(s)
				}
			}
		case "\"":
			translateLine(0, -leading)
			if len(operands) >= 3 {
				if s, ok := operands[2].(String); ok {
					show(s)
				}
			}
		case "TJ":
			if len(operands) >= 1 {
				if arr, ok := operands[0].(Array); ok {
					for _, e := range arr {
						if s, ok := e.(String); ok {
							show(s)
							continue
						}
						// A kern moves the pen; whether the gap it opens
						// reads as a space is decided by measuring it,
						// not by its size in the array.
						if f, ok := toFloat(e); ok {
							advancePen(-f / 1000 * fontSize * horizScale)
						}
					}
				}
			}
		}
		operands = operands[:0]
	}
}

// runForm expands a form XObject invoked by the Do operator.
func (ex *textExtractor) runForm(name Name, resources any, ctm matrix, depth int) {
	res, ok := ex.r.resolve(resources).(Dict)
	if !ok {
		return
	}
	xobjects, ok := ex.r.resolve(res["XObject"]).(Dict)
	if !ok {
		return
	}
	entry := xobjects[name]
	if ref, isRef := entry.(Ref); isRef {
		if ex.visited[ref] {
			return // self-referential form
		}
		ex.visited[ref] = true
		defer delete(ex.visited, ref)
	}
	stm, ok := ex.r.resolve(entry).(*rawStream)
	if !ok || ex.r.resolve(stm.dict["Subtype"]) != Name("Form") {
		return
	}
	content, err := ex.r.decodeStream(stm.dict, stm.data)
	if err != nil {
		return
	}
	// A form's /Matrix maps its own space into the invoking space.
	inner := ctm
	if mArr, ok := ex.r.resolve(stm.dict["Matrix"]).(Array); ok && len(mArr) == 6 {
		var m matrix
		valid := true
		for i, e := range mArr {
			f, ok := toFloat(ex.r.resolve(e))
			if !ok {
				valid = false
				break
			}
			m[i] = f
		}
		if valid {
			inner = m.mul(ctm)
		}
	}
	formRes := stm.dict["Resources"]
	if formRes == nil {
		formRes = resources // forms may inherit the invoking resources
	}
	ex.run(content, formRes, inner, depth+1)
}

// runPattern reads the cell of a tiling pattern chosen as the colour
// to paint with: its text is on the page as often as the cell repeats,
// and is read once (see runScanner.scanPattern).
func (ex *textExtractor) runPattern(name Name, resources any, base matrix, depth int) {
	res, ok := ex.r.resolve(resources).(Dict)
	if !ok {
		return
	}
	patterns, ok := ex.r.resolve(res["Pattern"]).(Dict)
	if !ok {
		return
	}
	entry := patterns[name]
	stm, ok := ex.r.resolve(entry).(*rawStream)
	if !ok {
		return
	}
	if pt, _ := toInt(ex.r.resolve(stm.dict["PatternType"])); pt != 1 {
		return
	}
	if ex.patterns[stm] {
		return
	}
	if ref, isRef := entry.(Ref); isRef {
		if ex.visited[ref] {
			return
		}
		ex.visited[ref] = true
		defer delete(ex.visited, ref)
	}
	content, err := ex.r.decodeStream(stm.dict, stm.data)
	if err != nil {
		return
	}
	if ex.patterns == nil {
		ex.patterns = make(map[*rawStream]bool)
	}
	ex.patterns[stm] = true
	inner := base
	if mArr, ok := ex.r.resolve(stm.dict["Matrix"]).(Array); ok && len(mArr) == 6 {
		var m matrix
		valid := true
		for i, e := range mArr {
			f, ok := toFloat(ex.r.resolve(e))
			if !ok {
				valid = false
				break
			}
			m[i] = f
		}
		if valid {
			inner = m.mul(base)
		}
	}
	patRes := stm.dict["Resources"]
	if patRes == nil {
		patRes = resources
	}
	ex.run(content, patRes, inner, depth+1)
}

// --- font decoding ---

type fontDecoders struct {
	r     *Reader
	fonts Dict
	cache map[Name]*fontDecoder
}

type fontDecoder struct {
	cid       bool // 2-byte character codes (Type0)
	toUnicode map[uint32]string
	encoding  *[256]rune // simple fonts only
	// named holds, of a simple font, the characters its /Differences
	// name outright (namedEncoding): what a code whose /ToUnicode entry
	// is U+FFFD reads as instead.
	named *[256]rune
	// shown holds, of a simple font that names its encoding and sets
	// letters, the character each code draws (shownEncoding): what a
	// code reads as where the /ToUnicode map says otherwise in a way
	// no viewer shows it (shownOverMap).
	shown *[256]rune
	// info carries the font's advance widths, so extraction can tell a
	// gap that is a space from one that merely continues a word.
	info *fontInfo
}

func newFontDecoders(r *Reader, resources any) *fontDecoders {
	fd := &fontDecoders{r: r, cache: make(map[Name]*fontDecoder)}
	if res, ok := r.resolve(resources).(Dict); ok {
		fd.fonts, _ = r.resolve(res["Font"]).(Dict)
	}
	return fd
}

func (fd *fontDecoders) get(name Name) *fontDecoder {
	if d, ok := fd.cache[name]; ok {
		return d
	}
	d := &fontDecoder{}
	if f, ok := fd.r.resolve(fd.fonts[name]).(Dict); ok {
		d.cid = fd.r.resolve(f["Subtype"]) == Name("Type0")
		if tu, ok := fd.r.resolve(f["ToUnicode"]).(*rawStream); ok {
			if data, err := fd.r.decodeStream(tu.dict, tu.data); err == nil {
				d.toUnicode = parseToUnicodeCMap(data)
			}
		}
		if !d.cid {
			d.encoding = textEncoding(fd.r, f)
			d.named = namedEncoding(fd.r, f, d.encoding)
			d.shown = shownEncoding(fd.r, f, d.encoding)
		}
		d.info = newFontInfo(fd.r, name, f, d)
	}
	fd.cache[name] = d
	return d
}

// advance returns the width of a string in text space, in points, at the
// given size and state.
func (d *fontDecoder) advance(s String, size, charSpacing, wordSpacing, horizScale float64) float64 {
	if d.info == nil || size == 0 {
		return 0
	}
	em := d.info.stringWidth(s, charSpacing, wordSpacing, size)
	return em / 1000 * size * horizScale
}

// spaceWidth returns the width of a space in text space, in points, or a
// reasonable share of the size when the font does not say.
func (d *fontDecoder) spaceWidth(size, horizScale float64) float64 {
	if d.info != nil && size != 0 {
		if w, ok := d.info.spaceWidth1000(); ok {
			return w / 1000 * size * horizScale
		}
	}
	return size * 0.25 * horizScale
}

// decode converts a content-stream string to text using ToUnicode when
// available, falling back to the font's simple encoding.
func (d *fontDecoder) decode(s String) string {
	text, _ := d.decodeSpans(s)
	return text
}

// decodeSpans decodes a string and also reports how many bytes of text
// each character code produced. Mapping a range of the text back to the
// codes that drew it is what lets a caller remove some characters of a
// run without re-encoding the ones it keeps.
func (d *fontDecoder) decodeSpans(s String) (string, []int) {
	var spans []int
	var sb strings.Builder
	step := 1
	if d.cid {
		step = 2
	}
	for i := 0; i+step <= len(s); i += step {
		var code uint32
		for k := 0; k < step; k++ {
			code = code<<8 | uint32(s[i+k])
		}
		before := sb.Len()
		tu := ""
		if d.toUnicode != nil {
			tu = d.toUnicode[code]
		}
		if tu == "\uFFFD" && d.named != nil && code < 256 && d.named[code] != 0 {
			tu = string(d.named[code])
		}
		if tu != "" && d.shown != nil && code < 256 {
			tu = shownOverMap(tu, d.shown[code])
		}
		switch {
		case tu != "":
			sb.WriteString(tu)
		case d.encoding != nil:
			if r := d.encoding[byte(code)]; r != 0 {
				sb.WriteRune(r)
			}
		case !d.cid && code >= 32 && code < 127:
			sb.WriteByte(byte(code)) // last-resort ASCII assumption
		}
		spans = append(spans, sb.Len()-before)
	}
	return sb.String(), spans
}

// A font's /ToUnicode map can say a code is another character than the
// one the font draws there. A newsletter's bold face drew "www.fda.gov"
// and "IHS HIV", and its map read the dots as spaces, the space as a tab,
// the "1" as a control character and the capitals H, I, N, O and S as
// small letters: "www fda gov", "ihs\thiV". Poppler reads the page as it
// is drawn. Read by the map, a web address was no address and "IHS" no
// "IHS": a marked "IHS" was neither removed from the line nor seen there
// by the check after it, and the copy went out with it.
//
// Where a font that names its encoding sets letters, what it draws at a
// code is what the encoding names there: a viewer picks the glyph by
// that name. Its map is still read for everything else a map is for —
// ligatures, glyphs no name tells, characters the encoding has no name
// for — and is overruled only where it cannot be right: a blank or a
// control character for a glyph the encoding names as a visible
// character (or a tab for a space), and a letter in the other case.

// shownEncoding returns the characters a simple font draws at its codes
// as its encoding names them, for a font that names an encoding and is
// no symbol or pictograph font; nil for any other.
func shownEncoding(r *Reader, font Dict, text *[256]rune) *[256]rune {
	if text == nil || r.resolve(font["Encoding"]) == nil || symbolicFont(r, font) {
		return nil
	}
	switch r.resolve(font["Subtype"]) {
	case Name("Type1"), Name("MMType1"), Name("TrueType"):
	default:
		return nil
	}
	return text
}

// shownOverMap returns what a code reads as: the character its map
// gives (mapped), or the one the font draws (shown) where the map cannot
// be right about it.
func shownOverMap(mapped string, shown rune) string {
	if shown == 0 {
		return mapped
	}
	m, size := utf8.DecodeRuneInString(mapped)
	if size != len(mapped) || m == shown {
		return mapped
	}
	blank := func(r rune) bool { return r <= ' ' || (r >= 0x7f && r <= 0x9f) || unicode.IsSpace(r) }
	switch {
	case blank(m) && (!blank(shown) || shown == ' '):
		return string(shown)
	case unicode.IsLetter(m) && unicode.ToLower(m) == unicode.ToLower(shown):
		return string(shown)
	}
	return mapped
}

// parseToUnicodeCMap extracts bfchar and bfrange mappings from a ToUnicode
// CMap stream.
func parseToUnicodeCMap(data []byte) map[uint32]string {
	m := make(map[uint32]string)
	budget := 1 << 17
	p := &parser{data: data}
	var stack []any
	for {
		tok, err := p.next()
		if err == io.EOF {
			return m
		}
		if err != nil {
			p.pos++
			continue
		}
		op, isOp := tok.(opKeyword)
		if !isOp {
			if len(stack) < 8 {
				stack = append(stack, tok)
			}
			continue
		}
		switch string(op) {
		case "beginbfchar":
			for {
				src, err1 := p.next()
				if _, done := src.(opKeyword); done || err1 != nil {
					break
				}
				dst, err2 := p.next()
				if err2 != nil {
					break
				}
				if s, ok := src.(String); ok {
					if t, ok := dst.(String); ok {
						m[beCode(s)] = utf16BEString(t)
					}
				}
				if budget--; budget < 0 {
					return m
				}
			}
		case "beginbfrange":
			for {
				lo, err1 := p.next()
				if _, done := lo.(opKeyword); done || err1 != nil {
					break
				}
				hi, err2 := p.next()
				dst, err3 := p.next()
				if err2 != nil || err3 != nil {
					break
				}
				loS, ok1 := lo.(String)
				hiS, ok2 := hi.(String)
				if !ok1 || !ok2 {
					continue
				}
				start, end := beCode(loS), beCode(hiS)
				if end < start || end-start > 1<<16 {
					continue
				}
				switch t := dst.(type) {
				case String:
					base := utf16BEUnits(t)
					for c := start; c <= end; c++ {
						m[c] = string(utf16.Decode(base))
						if len(base) > 0 {
							base[len(base)-1]++
						}
						if budget--; budget < 0 {
							return m
						}
					}
				case Array:
					for i, e := range t {
						if s, ok := e.(String); ok {
							m[start+uint32(i)] = utf16BEString(s)
						}
						if budget--; budget < 0 {
							return m
						}
					}
				}
			}
		}
		stack = stack[:0]
	}
}

func beCode(s String) uint32 {
	var v uint32
	for _, b := range s {
		v = v<<8 | uint32(b)
	}
	return v
}

func utf16BEUnits(s String) []uint16 {
	units := make([]uint16, 0, len(s)/2)
	for i := 0; i+1 < len(s); i += 2 {
		units = append(units, uint16(s[i])<<8|uint16(s[i+1]))
	}
	return units
}

func utf16BEString(s String) string {
	return string(utf16.Decode(utf16BEUnits(s)))
}

// fontEncoding builds the byte-to-rune table of a simple (1-byte) font:
// its base encoding, with any /Differences applied on top. A font that
// names no base encoding has its built-in one (builtInEncoding).
func fontEncoding(r *Reader, font Dict) *[256]rune {
	return simpleEncodingOf(r, font["Encoding"], func() *[256]rune { return builtInEncoding(r, font) }, numberedNamesFont(r, font))
}

// numberedNamesFont reports a font whose glyph names in /Differences
// may be numbered ones (numberedNameRunes): a Type 1 font, and a Type 3
// font — whose glyph procedures a chart program names by the codes of
// the characters they draw, "/70" for F and "/105" for i, with the
// codes the page uses renumbered from 1. A TrueType font's numbered
// names are glyph indices.
func numberedNamesFont(r *Reader, font Dict) bool {
	switch r.resolve(font["Subtype"]) {
	case Name("Type1"), Name("MMType1"), Name("Type3"):
		return true
	}
	return false
}

// textEncoding is fontEncoding as text is read. A pictograph font —
// Wingdings, ZapfDingbats and their like — draws pictures at its codes,
// not the letters an encoding table names there: Wingdings' 0xD8 is an
// arrowhead that WinAnsi calls "Ø", and ZapfDingbats' "l" a black
// circle. Read as letters, a bullet and the word after it made one word
// ("ØDOE approved method"), and the word was found neither by the
// redactor nor by the check after it. A pictograph font's codes read as
// the private-use characters Windows gives such fonts (U+F000 plus the
// code), which are no letters; a glyph its /Differences names stays
// what the name says.
func textEncoding(r *Reader, font Dict) *[256]rune {
	if !pictographFont(r, font) {
		return fontEncoding(r, font)
	}
	return simpleEncoding(r, font["Encoding"], nil)
}

// simpleEncoding builds the table from an /Encoding entry. builtIn gives
// the table of a font that names no base encoding; nil means the font is
// a pictograph font, whose codes stand for themselves whatever base it
// names.
func simpleEncoding(r *Reader, encoding any, builtIn func() *[256]rune) *[256]rune {
	return simpleEncodingOf(r, encoding, builtIn, false)
}

// simpleEncodingOf is simpleEncoding; numbered says that glyph names
// nothing knows may be numbered names (numberedNameRunes).
func simpleEncodingOf(r *Reader, encoding any, builtIn func() *[256]rune, numbered bool) *[256]rune {
	table, _ := simpleEncodingNamed(r, encoding, builtIn, numbered)
	return table
}

// A converter that could not tell what its glyphs were wrote a
// /ToUnicode map that says so of every code — U+FFFD, "unknown" — while
// the font's /Differences named each glyph by its Windows code: "G69"
// for E, "G110" for n. Read by the map alone, a table of diseases and
// the pathogens that cause them read as a run of U+FFFD, and every
// value in it was neither found nor checked. Where the map says only
// that it does not know, a character the encoding names outright is
// read instead: never a base encoding's guess, which may be anything.

// namedEncoding returns the characters of a simple font's codes that its
// /Differences name outright — by a glyph name a glyph list knows, or by
// a numbered name (numberedNameRunes) — and that the font's text reading
// (text) agrees with: a pictograph font's codes read as its own
// pictures, never as the letters a name would make of them. Nil when
// there are none.
func namedEncoding(r *Reader, font Dict, text *[256]rune) *[256]rune {
	if text == nil {
		return nil
	}
	_, named := simpleEncodingNamed(r, font["Encoding"], func() *[256]rune { return builtInEncoding(r, font) }, numberedNamesFont(r, font))
	if named == nil {
		return nil
	}
	some := false
	for code, ru := range named {
		if ru != 0 && text[code] != ru {
			named[code] = 0
		}
		some = some || named[code] != 0
	}
	if !some {
		return nil
	}
	return named
}

// simpleEncodingNamed is simpleEncodingOf, with the characters the
// /Differences name outright beside the table (zero where they name
// none); nil when the encoding has no /Differences.
func simpleEncodingNamed(r *Reader, encoding any, builtIn func() *[256]rune, numbered bool) (*[256]rune, *[256]rune) {
	enc := r.resolve(encoding)
	encDict, isDict := enc.(Dict)

	baseName, _ := enc.(Name)
	if isDict {
		baseName, _ = r.resolve(encDict["BaseEncoding"]).(Name)
	}
	var table *[256]rune
	switch {
	case builtIn == nil:
		table = pictographRunes()
	case baseName == "MacRomanEncoding":
		table = macRomanRunes()
	case baseName == "StandardEncoding":
		table = standardRunes()
	case baseName == "":
		table = builtIn()
	default:
		// WinAnsiEncoding, and MacExpertEncoding, which is not carried.
		table = winAnsiRunes()
	}
	var named *[256]rune
	if isDict {
		if diffs, ok := r.resolve(encDict["Differences"]).(Array); ok {
			named = new([256]rune)
			code := 0
			unknown := map[int]string{}
			for _, e := range diffs {
				switch t := r.resolve(e).(type) {
				case int64:
					code = int(t)
				case float64:
					code = int(t)
				case Name:
					if code >= 0 && code < 256 {
						// A name that means nothing to us leaves the base
						// encoding in place rather than blanking the code.
						// Type 3 fonts routinely name their glyphs /g17 or
						// /a0, and taking those as "no character" loses the
						// text of the whole document.
						if r := glyphNameToRune(string(t)); r != 0 {
							table[code] = r
							named[code] = r
							delete(unknown, code)
						} else {
							unknown[code] = string(t)
							named[code] = 0
						}
						code++
					}
				}
			}
			if numbered {
				for code, ru := range numberedNameRunes(unknown) {
					table[code] = ru
					named[code] = ru
				}
			}
		}
	}
	return table, named
}

// builtInEncoding is the encoding of a simple font whose dictionary
// names none: the font program's own (ISO 32000-1, 9.6.6.1). For a
// nonsymbolic Type 1 font — the standard fonts, and the fonts set in
// their image — that is StandardEncoding, as viewers read it: 0xD0 is an
// em dash, 0xB1 an en dash, 0x27 and 0x60 the curly quotes. Read as
// WinAnsi, the dash was "Ð", a letter, and the words either side of it
// one word: "Background—Alien Property" read as "BackgroundÐAlien
// Property", and a value after the dash was neither removed nor found by
// the check after it. A symbolic font, and a TrueType font, keep the
// WinAnsi reading this package has always given them: the tables they
// would need are not carried here.
//
// Where the font's embedded program spells out an encoding of its own,
// the codes it names read as it names them (programEncodingNames); the
// rest keep the reading above.
func builtInEncoding(r *Reader, font Dict) *[256]rune {
	t := guessedBuiltInEncoding(r, font)
	for code, name := range programEncodingNames(r, font) {
		if ru := glyphNameToRune(name); ru != 0 {
			t[code] = ru
		}
	}
	return t
}

func guessedBuiltInEncoding(r *Reader, font Dict) *[256]rune {
	switch r.resolve(font["Subtype"]) {
	case Name("Type1"), Name("MMType1"):
	default:
		return winAnsiRunes()
	}
	if symbolicFont(r, font) {
		return winAnsiRunes()
	}
	return standardRunes()
}

// symbolicFont reports a font that sets symbols rather than a Latin
// alphabet: so flagged by its descriptor (Symbolic and not Nonsymbolic),
// or one of the symbol fonts by name.
func symbolicFont(r *Reader, font Dict) bool {
	if pictographFont(r, font) || fontFamily(r, font) == "symbol" {
		return true
	}
	fd, _ := r.resolve(font["FontDescriptor"]).(Dict)
	flags, ok := toInt(r.resolve(fd["Flags"]))
	return ok && flags&4 != 0 && flags&32 == 0
}

// pictographFont reports a font of pictures — bullets, arrows, check
// marks — by its name.
func pictographFont(r *Reader, font Dict) bool {
	family := fontFamily(r, font)
	for _, p := range []string{"wingdings", "webdings", "zapfdingbats", "itczapfdingbats", "dingbats", "monotypesorts", "marlett"} {
		if strings.HasPrefix(family, p) {
			return true
		}
	}
	return false
}

// fontFamily returns a font's base name in lower case, without its subset
// prefix ("CFAPJI+"), its style ("Arial,Bold") and its spaces and hyphens.
func fontFamily(r *Reader, font Dict) string {
	name := baseFontName(r, font)
	if i := strings.IndexByte(name, '+'); i == 6 {
		name = name[i+1:]
	}
	if i := strings.IndexByte(name, ','); i >= 0 {
		name = name[:i]
	}
	name = strings.NewReplacer(" ", "", "-", "", "_", "").Replace(name)
	return strings.ToLower(name)
}

// pictographRunes reads each code as the private-use character U+F000
// plus the code.
func pictographRunes() *[256]rune {
	var t [256]rune
	for c := 32; c < 256; c++ {
		t[c] = rune(0xF000 + c)
	}
	return &t
}

// standardEncodingUpper names the glyphs of Adobe's StandardEncoding from
// 0xA1 on; below, it is ASCII but for its quotes.
var standardEncodingUpper = map[byte]string{
	0xA1: "exclamdown", 0xA2: "cent", 0xA3: "sterling", 0xA4: "fraction",
	0xA5: "yen", 0xA6: "florin", 0xA7: "section", 0xA8: "currency",
	0xA9: "quotesingle", 0xAA: "quotedblleft", 0xAB: "guillemotleft",
	0xAC: "guilsinglleft", 0xAD: "guilsinglright", 0xAE: "fi", 0xAF: "fl",
	0xB1: "endash", 0xB2: "dagger", 0xB3: "daggerdbl", 0xB4: "periodcentered",
	0xB6: "paragraph", 0xB7: "bullet", 0xB8: "quotesinglbase",
	0xB9: "quotedblbase", 0xBA: "quotedblright", 0xBB: "guillemotright",
	0xBC: "ellipsis", 0xBD: "perthousand", 0xBF: "questiondown",
	0xC1: "grave", 0xC2: "acute", 0xC3: "circumflex", 0xC4: "tilde",
	0xC5: "macron", 0xC6: "breve", 0xC7: "dotaccent", 0xC8: "dieresis",
	0xCA: "ring", 0xCB: "cedilla", 0xCD: "hungarumlaut", 0xCE: "ogonek",
	0xCF: "caron", 0xD0: "emdash", 0xE1: "AE", 0xE3: "ordfeminine",
	0xE8: "Lslash", 0xE9: "Oslash", 0xEA: "OE", 0xEB: "ordmasculine",
	0xF1: "ae", 0xF5: "dotlessi", 0xF8: "lslash", 0xF9: "oslash",
	0xFA: "oe", 0xFB: "germandbls",
}

// standardRunes returns the StandardEncoding byte-to-rune table. A
// code StandardEncoding leaves empty keeps its WinAnsi reading: a
// producer that wrote one meant WinAnsi — 0xA0 for a non-breaking
// space between two words — and read as nothing, the code joined the
// words either side of it into one, which a value of two words is not
// found in.
func standardRunes() *[256]rune {
	t := winAnsiRunes()
	t['\''] = '’'
	t['`'] = '‘'
	for code, name := range standardEncodingUpper {
		t[code] = glyphNameToRune(name)
	}
	return t
}

// winAnsiRunes returns the WinAnsi (CP-1252) byte-to-rune table.
func winAnsiRunes() *[256]rune {
	var t [256]rune
	for i := 32; i < 127; i++ {
		t[i] = rune(i)
	}
	for i := 0xA0; i <= 0xFF; i++ {
		t[i] = rune(i)
	}
	for r, b := range winAnsiSpecials {
		t[b] = r
	}
	return &t
}

// macRomanUpper holds the MacRomanEncoding characters for codes
// 0x80-0xFF; the lower half matches ASCII.
const macRomanUpper = "ÄÅÇÉÑÖÜáàâäãåçéè" +
	"êëíìîïñóòôöõúùûü" +
	"†°¢£§•¶ß®©™´¨≠ÆØ" +
	"∞±≤≥¥µ∂∑∏π∫ªºΩæø" +
	"¿¡¬√ƒ≈∆«»… ÀÃÕŒœ" +
	"–—“”‘’÷◊ÿŸ⁄€‹›ﬁﬂ" +
	"‡·‚„‰ÂÊÁËÈÍÎÏÌÓÔ" +
	"ÒÚÛÙıˆ˜¯˘˙˚¸˝˛ˇ"

// macRomanRunes returns the MacRomanEncoding byte-to-rune table.
func macRomanRunes() *[256]rune {
	var t [256]rune
	for i := 32; i < 127; i++ {
		t[i] = rune(i)
	}
	for i, r := range []rune(macRomanUpper) {
		if 0x80+i < 256 {
			t[0x80+i] = r
		}
	}
	return &t
}

// glyphNameToRune maps an Adobe glyph name to its rune, covering the names
// used by WinAnsi/Standard encoding differences plus uniXXXX forms.
func glyphNameToRune(name string) rune {
	if r, ok := glyphNames[name]; ok {
		return r
	}
	if strings.HasPrefix(name, "uni") && len(name) >= 7 {
		if v, err := strconv.ParseUint(name[3:7], 16, 32); err == nil {
			return rune(v)
		}
	}
	if strings.HasPrefix(name, "u") && len(name) >= 5 && len(name) <= 7 {
		if v, err := strconv.ParseUint(name[1:], 16, 32); err == nil {
			return rune(v)
		}
	}
	if len(name) == 1 {
		return rune(name[0])
	}
	return 0
}

var glyphNames = map[string]rune{
	"space": ' ', "exclam": '!', "quotedbl": '"', "numbersign": '#',
	"dollar": '$', "percent": '%', "ampersand": '&', "quotesingle": '\'',
	"parenleft": '(', "parenright": ')', "asterisk": '*', "plus": '+',
	"comma": ',', "hyphen": '-', "period": '.', "slash": '/',
	"zero": '0', "one": '1', "two": '2', "three": '3', "four": '4',
	"five": '5', "six": '6', "seven": '7', "eight": '8', "nine": '9',
	"colon": ':', "semicolon": ';', "less": '<', "equal": '=',
	"greater": '>', "question": '?', "at": '@', "bracketleft": '[',
	"backslash": '\\', "bracketright": ']', "asciicircum": '^',
	"underscore": '_', "grave": '`', "braceleft": '{', "bar": '|',
	"braceright": '}', "asciitilde": '~',

	"exclamdown": '¡', "cent": '¢', "sterling": '£', "currency": '¤',
	"yen": '¥', "brokenbar": '¦', "section": '§', "dieresis": '¨',
	"copyright": '©', "ordfeminine": 'ª', "guillemotleft": '«',
	"logicalnot": '¬', "registered": '®', "macron": '¯', "degree": '°',
	"plusminus": '±', "acute": '´', "mu": 'µ', "paragraph": '¶',
	"periodcentered": '·', "cedilla": '¸', "ordmasculine": 'º',
	"guillemotright": '»', "onequarter": '¼', "onehalf": '½',
	"threequarters": '¾', "questiondown": '¿', "multiply": '×',
	"divide": '÷',

	"Agrave": 'À', "Aacute": 'Á', "Acircumflex": 'Â', "Atilde": 'Ã',
	"Adieresis": 'Ä', "Aring": 'Å', "AE": 'Æ', "Ccedilla": 'Ç',
	"Egrave": 'È', "Eacute": 'É', "Ecircumflex": 'Ê', "Edieresis": 'Ë',
	"Igrave": 'Ì', "Iacute": 'Í', "Icircumflex": 'Î', "Idieresis": 'Ï',
	"Eth": 'Ð', "Ntilde": 'Ñ', "Ograve": 'Ò', "Oacute": 'Ó',
	"Ocircumflex": 'Ô', "Otilde": 'Õ', "Odieresis": 'Ö', "Oslash": 'Ø',
	"Ugrave": 'Ù', "Uacute": 'Ú', "Ucircumflex": 'Û', "Udieresis": 'Ü',
	"Yacute": 'Ý', "Thorn": 'Þ', "germandbls": 'ß',
	"agrave": 'à', "aacute": 'á', "acircumflex": 'â', "atilde": 'ã',
	"adieresis": 'ä', "aring": 'å', "ae": 'æ', "ccedilla": 'ç',
	"egrave": 'è', "eacute": 'é', "ecircumflex": 'ê', "edieresis": 'ë',
	"igrave": 'ì', "iacute": 'í', "icircumflex": 'î', "idieresis": 'ï',
	"eth": 'ð', "ntilde": 'ñ', "ograve": 'ò', "oacute": 'ó',
	"ocircumflex": 'ô', "otilde": 'õ', "odieresis": 'ö', "oslash": 'ø',
	"ugrave": 'ù', "uacute": 'ú', "ucircumflex": 'û', "udieresis": 'ü',
	"yacute": 'ý', "thorn": 'þ', "ydieresis": 'ÿ',

	"Euro": '€', "quotesinglbase": '‚', "florin": 'ƒ',
	"quotedblbase": '„', "ellipsis": '…', "dagger": '†', "daggerdbl": '‡',
	"circumflex": 'ˆ', "perthousand": '‰', "Scaron": 'Š',
	"guilsinglleft": '‹', "OE": 'Œ', "Zcaron": 'Ž', "quoteleft": '‘',
	"quoteright": '’', "quotedblleft": '“', "quotedblright": '”',
	"bullet": '•', "endash": '–', "emdash": '—', "tilde": '˜',
	"trademark": '™', "scaron": 'š', "guilsinglright": '›', "oe": 'œ',
	"zcaron": 'ž', "Ydieresis": 'Ÿ', "fi": 'ﬁ', "fl": 'ﬂ',
	"dotlessi": 'ı', "lslash": 'ł', "Lslash": 'Ł',

	"fraction": '⁄', "breve": '˘', "dotaccent": '˙', "ring": '˚',
	"hungarumlaut": '˝', "ogonek": '˛', "caron": 'ˇ',
}

// scaleOf returns how far a unit of text space carries on the page.
func scaleOf(m matrix) float64 {
	s := math.Hypot(m[0], m[1])
	if s == 0 {
		return 1
	}
	return s
}

// needsSpace decides whether a gap between two pieces of text should be
// read as a word break.
//
// Positioning each fragment of a word separately is ordinary: troff and
// TeX move the pen a fraction of a point between letters to kern them,
// and treating every forward move as a space breaks words apart. Only a
// gap approaching the width of a real space is one.
func needsSpace(prev, text string, gap, spaceWidth float64) bool {
	if r, _ := utf8.DecodeRuneInString(text); unicode.IsSpace(r) || r == 0x00A0 {
		return false // the text supplies its own separator
	}
	// And the same rule from the other side. Justified text is set by
	// drawing the space and then moving the pen the rest of the way, so
	// the gap that follows a space is the justification, not a second
	// word break. Reading it as one puts two spaces between every pair
	// of words, which no literal search for a name will survive.
	if r, _ := utf8.DecodeLastRuneInString(prev); unicode.IsSpace(r) || r == 0x00A0 {
		return false
	}
	if spaceWidth <= 0 {
		spaceWidth = 1
	}
	return gap > spaceWidth*spaceFraction
}

// baselineDir returns the unit vector the text advances along.
func baselineDir(m matrix) (float64, float64) {
	n := math.Hypot(m[0], m[1])
	if n == 0 {
		return 1, 0
	}
	return m[0] / n, m[1] / n
}
