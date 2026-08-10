package gopdf

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
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
}

const maxFormDepth = 12

// run interprets one content stream. base is the transform mapping the
// stream's coordinates onto the page, so text positions stay comparable
// across nested forms.
func (ex *textExtractor) run(content []byte, resources any, base matrix, depth int) {
	fonts := newFontDecoders(ex.r, resources)
	var cur *fontDecoder

	ctm := base
	var ctmStack []matrix
	tm, tlm := identityMatrix, identityMatrix
	leading := 0.0
	pendingSpace := false

	translateLine := func(tx, ty float64) {
		tlm = matrix{tlm[0], tlm[1], tlm[2], tlm[3],
			tx*tlm[0] + ty*tlm[2] + tlm[4], tx*tlm[1] + ty*tlm[3] + tlm[5]}
		tm = tlm
	}
	show := func(s String) {
		if cur == nil || ex.sb.Len() > maxExtractedText {
			return
		}
		text := cur.decode(s)
		if text == "" {
			return
		}
		_, y := tm.mul(ctm).apply(0, 0)
		if ex.started && math.Abs(y-ex.lastY) > 0.5 {
			ex.sb.WriteByte('\n')
		} else if pendingSpace && ex.started {
			ex.sb.WriteByte(' ')
		}
		ex.sb.WriteString(text)
		ex.lastY = y
		ex.started = true
		pendingSpace = false
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
			if len(ctmStack) < 64 {
				ctmStack = append(ctmStack, ctm)
			}
		case "Q":
			if n := len(ctmStack); n > 0 {
				ctm = ctmStack[n-1]
				ctmStack = ctmStack[:n-1]
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
				if num(0) > 0 && num(1) == 0 {
					pendingSpace = true
				}
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
						} else if f, ok := toFloat(e); ok && f < -100 {
							pendingSpace = true
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
			d.encoding = simpleEncoding(fd.r, f["Encoding"])
		}
	}
	fd.cache[name] = d
	return d
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
		switch {
		case d.toUnicode != nil && d.toUnicode[code] != "":
			sb.WriteString(d.toUnicode[code])
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

// simpleEncoding builds a byte-to-rune table for a simple (1-byte) font
// from its base encoding, with any /Differences applied on top.
func simpleEncoding(r *Reader, encoding any) *[256]rune {
	enc := r.resolve(encoding)
	encDict, isDict := enc.(Dict)

	baseName, _ := enc.(Name)
	if isDict {
		baseName, _ = r.resolve(encDict["BaseEncoding"]).(Name)
	}
	table := winAnsiRunes()
	if baseName == "MacRomanEncoding" {
		table = macRomanRunes()
	}
	if isDict {
		if diffs, ok := r.resolve(encDict["Differences"]).(Array); ok {
			code := 0
			for _, e := range diffs {
				switch t := r.resolve(e).(type) {
				case int64:
					code = int(t)
				case float64:
					code = int(t)
				case Name:
					if code >= 0 && code < 256 {
						table[code] = glyphNameToRune(string(t))
						code++
					}
				}
			}
		}
	}
	return table
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
}
