package gopdf

import (
	"fmt"
	"os"
)

// Font is a typeface usable with Page.SetFont.
//
// The package provides the standard 14 PDF fonts, which every viewer
// renders without the font being embedded in the file; they are limited to
// the WinAnsi (CP-1252) character set. For full Unicode text, load a
// TrueType font with LoadFont or ParseFont: it is embedded in the document,
// subset to the glyphs actually used.
type Font struct {
	name         string
	widths       *[95]int16     // glyph widths for ASCII 32..126, in 1/1000 em
	specials     map[byte]int16 // widths for WinAnsi punctuation outside ASCII
	defaultWidth int16          // width assumed for characters outside the tables
	winAnsi      bool           // written with /Encoding /WinAnsiEncoding
	ttf          *ttfFont       // non-nil for embedded TrueType fonts
}

// Name returns the PostScript name of the font.
func (f *Font) Name() string { return f.name }

// LoadFont loads a font for embedding: TrueType (.ttf), the first font of
// a collection (.ttc), or CFF-based OpenType (.otf). The font may be used
// with any number of documents.
//
// TrueType fonts are subset to the glyphs actually used. OpenType fonts
// with PostScript outlines are embedded whole, so they produce larger
// files.
func LoadFont(path string) (*Font, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f, err := ParseFont(data)
	if err != nil {
		return nil, fmt.Errorf("%w (%s)", err, path)
	}
	return f, nil
}

// ParseFont parses TrueType or OpenType font data for embedding.
func ParseFont(data []byte) (*Font, error) {
	t, err := parseTTF(data)
	if err != nil {
		return nil, err
	}
	name := t.psName
	if name == "" {
		name = "Embedded"
	}
	return &Font{name: name, ttf: t}, nil
}

// The standard 14 PDF fonts. Symbol and ZapfDingbats use their built-in
// symbolic encodings; the others use WinAnsi (CP-1252).
var (
	Courier              = &Font{name: "Courier", defaultWidth: 600, winAnsi: true}
	CourierBold          = &Font{name: "Courier-Bold", defaultWidth: 600, winAnsi: true}
	CourierOblique       = &Font{name: "Courier-Oblique", defaultWidth: 600, winAnsi: true}
	CourierBoldOblique   = &Font{name: "Courier-BoldOblique", defaultWidth: 600, winAnsi: true}
	Helvetica            = &Font{name: "Helvetica", widths: &helveticaWidths, specials: helveticaSpecials, defaultWidth: 556, winAnsi: true}
	HelveticaBold        = &Font{name: "Helvetica-Bold", widths: &helveticaBoldWidths, specials: helveticaBoldSpecials, defaultWidth: 556, winAnsi: true}
	HelveticaOblique     = &Font{name: "Helvetica-Oblique", widths: &helveticaWidths, specials: helveticaSpecials, defaultWidth: 556, winAnsi: true}
	HelveticaBoldOblique = &Font{name: "Helvetica-BoldOblique", widths: &helveticaBoldWidths, specials: helveticaBoldSpecials, defaultWidth: 556, winAnsi: true}
	TimesRoman           = &Font{name: "Times-Roman", widths: &timesRomanWidths, specials: timesSpecials, defaultWidth: 500, winAnsi: true}
	TimesBold            = &Font{name: "Times-Bold", widths: &timesBoldWidths, specials: timesBoldSpecials, defaultWidth: 500, winAnsi: true}
	TimesItalic          = &Font{name: "Times-Italic", widths: &timesItalicWidths, specials: timesItalicSpecials, defaultWidth: 500, winAnsi: true}
	TimesBoldItalic      = &Font{name: "Times-BoldItalic", widths: &timesBoldItalicWidths, specials: timesBoldSpecials, defaultWidth: 500, winAnsi: true}
	Symbol               = &Font{name: "Symbol", defaultWidth: 600}
	ZapfDingbats         = &Font{name: "ZapfDingbats", defaultWidth: 700}
)

// Widths of the common WinAnsi punctuation glyphs that have no same-width
// ASCII equivalent, from the Adobe AFM files: euro, ellipsis, curly quotes,
// bullet, em dash and trademark.
var (
	helveticaSpecials = map[byte]int16{
		0x80: 556, 0x85: 1000, 0x91: 222, 0x92: 222, 0x93: 333, 0x94: 333,
		0x95: 350, 0x97: 1000, 0x99: 1000,
	}
	helveticaBoldSpecials = map[byte]int16{
		0x80: 556, 0x85: 1000, 0x91: 278, 0x92: 278, 0x93: 500, 0x94: 500,
		0x95: 350, 0x97: 1000, 0x99: 1000,
	}
	timesSpecials = map[byte]int16{
		0x80: 500, 0x85: 1000, 0x91: 333, 0x92: 333, 0x93: 444, 0x94: 444,
		0x95: 350, 0x97: 1000, 0x99: 980,
	}
	timesBoldSpecials = map[byte]int16{
		0x80: 500, 0x85: 1000, 0x91: 333, 0x92: 333, 0x93: 500, 0x94: 500,
		0x95: 350, 0x97: 1000, 0x99: 1000,
	}
	timesItalicSpecials = map[byte]int16{
		0x80: 500, 0x85: 1000, 0x91: 333, 0x92: 333, 0x93: 556, 0x94: 556,
		0x95: 350, 0x97: 889, 0x99: 980,
	}
)

// TextWidth returns the rendered width of s in points at the given font
// size. For embedded TrueType fonts the widths come from the font's metric
// tables, including kerning, and are exact for every glyph. For the
// standard fonts they are exact for the WinAnsi letters and most symbols
// (and for all characters in the Courier family); the few remaining
// characters use an approximate per-font default.
func (f *Font) TextWidth(s string, size float64) float64 {
	total := 0
	if f.ttf != nil {
		prev, first := uint16(0), true
		for _, r := range s {
			gid := f.ttf.cmap[r]
			total += f.ttf.toEm(int(f.ttf.advances[gid]))
			if !first {
				total += f.ttf.toEm(f.ttf.kerning(prev, gid))
			}
			prev, first = gid, false
		}
	} else {
		for _, b := range winAnsiEncode(s) {
			total += int(f.glyphWidth(b))
		}
	}
	return float64(total) * size / 1000
}

func (f *Font) glyphWidth(b byte) int16 {
	if f.widths == nil {
		return f.defaultWidth // Courier and the symbolic fonts
	}
	if b >= 32 && b <= 126 {
		return f.widths[b-32]
	}
	if w, ok := f.specials[b]; ok {
		return w
	}
	// Most non-ASCII WinAnsi glyphs share the width of an ASCII glyph in
	// the standard fonts (é and e, × and +, non-breaking and plain space).
	if base, ok := winAnsiWidthAlias[b]; ok {
		return f.widths[base-32]
	}
	return f.defaultWidth
}

// winAnsiWidthAlias maps WinAnsi code points to an ASCII character with the
// same advance width in all the metric-backed standard fonts, per the Adobe
// AFM files.
var winAnsiWidthAlias = map[byte]byte{
	0x8A: 'S', 0x8E: 'Z', 0x9A: 's', 0x9E: 'z', 0x9F: 'Y', // Š Ž š ž Ÿ
	0xA0: ' ',                                  // non-breaking space
	0xA1: '(',                                  // ¡ (333 in Helvetica and Times, like the parenthesis)
	0xA2: '0', 0xA3: '0', 0xA4: '0', 0xA5: '0', // ¢ £ ¤ ¥ have digit width
	0xA6: '|', 0xA7: '0', 0xA8: '-',
	0xAB: '0', 0xAC: '+', 0xAD: '-', 0xAF: '-',
	0xB1: '+', 0xB4: '-', 0xB5: 'u', 0xB7: '.', 0xB8: '-', 0xBB: '0',
	0xC0: 'A', 0xC1: 'A', 0xC2: 'A', 0xC3: 'A', 0xC4: 'A', 0xC5: 'A',
	0xC7: 'C',
	0xC8: 'E', 0xC9: 'E', 0xCA: 'E', 0xCB: 'E',
	0xCC: 'I', 0xCD: 'I', 0xCE: 'I', 0xCF: 'I',
	0xD0: 'D', 0xD1: 'N',
	0xD2: 'O', 0xD3: 'O', 0xD4: 'O', 0xD5: 'O', 0xD6: 'O',
	0xD7: '+', 0xD8: 'O',
	0xD9: 'U', 0xDA: 'U', 0xDB: 'U', 0xDC: 'U',
	0xDD: 'Y', 0xDE: 'P',
	0xE0: 'a', 0xE1: 'a', 0xE2: 'a', 0xE3: 'a', 0xE4: 'a', 0xE5: 'a',
	0xE7: 'c',
	0xE8: 'e', 0xE9: 'e', 0xEA: 'e', 0xEB: 'e',
	0xEC: 'i', 0xED: 'i', 0xEE: 'i', 0xEF: 'i',
	0xF0: 'd', 0xF1: 'n',
	0xF2: 'o', 0xF3: 'o', 0xF4: 'o', 0xF5: 'o', 0xF6: 'o',
	0xF7: '+', 0xF8: 'o',
	0xF9: 'u', 0xFA: 'u', 0xFB: 'u', 0xFC: 'u',
	0xFD: 'y', 0xFE: 'p', 0xFF: 'y',
	0x96: '0',            // en dash has digit width in Helvetica and Times
	0x8B: '(', 0x9B: '(', // ‹ › (333, like the parenthesis)
}

// winAnsiSpecials maps the Unicode code points that WinAnsi (CP-1252)
// places in the 0x80–0x9F range.
var winAnsiSpecials = map[rune]byte{
	'€': 0x80, // €
	'‚': 0x82,
	'ƒ': 0x83,
	'„': 0x84,
	'…': 0x85, // …
	'†': 0x86,
	'‡': 0x87,
	'ˆ': 0x88,
	'‰': 0x89,
	'Š': 0x8A,
	'‹': 0x8B,
	'Œ': 0x8C,
	'Ž': 0x8E,
	'‘': 0x91, // '
	'’': 0x92, // '
	'“': 0x93, // "
	'”': 0x94, // "
	'•': 0x95, // •
	'–': 0x96, // –
	'—': 0x97, // —
	'˜': 0x98,
	'™': 0x99, // ™
	'š': 0x9A,
	'›': 0x9B,
	'œ': 0x9C,
	'ž': 0x9E,
	'Ÿ': 0x9F,
}

// winAnsiEncode converts a UTF-8 string to WinAnsi (CP-1252) bytes.
// Characters without a WinAnsi code point become '?'.
func winAnsiEncode(s string) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		switch {
		case r < 0x80:
			out = append(out, byte(r))
		case r >= 0xA0 && r <= 0xFF:
			out = append(out, byte(r))
		default:
			if b, ok := winAnsiSpecials[r]; ok {
				out = append(out, b)
			} else {
				out = append(out, '?')
			}
		}
	}
	return out
}
