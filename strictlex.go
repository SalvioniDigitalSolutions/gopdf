package gopdf

import "fmt"

// StrictLexPages reports the first place in a document where two tokens
// of a content stream have run together.
//
// Writers here splice replacements into a stream that was written by
// somebody else, and a splice landing immediately after an operator whose
// trailing space it consumed leaves "Tc" and "1" as the single token
// "Tc1". Every reader in this package tolerates that, and so do the
// common ones, which is exactly why it goes unnoticed: the content is
// right and the file is not, and a strict parser rejects the page. Anyone
// who re-opens their own output to check it pays for the difference.
//
// It is exported for that check. Nothing in this package needs it, and
// output from this package should always pass it.
func StrictLexPages(data []byte) error {
	r, err := NewReader(data)
	if err != nil {
		return err
	}
	for i := 0; i < r.NumPages(); i++ {
		content, err := r.pageContent(r.pages[i].dict)
		if err != nil {
			return fmt.Errorf("gopdf: page %d: %w", i+1, err)
		}
		if err := strictLexContent(content); err != nil {
			return fmt.Errorf("gopdf: page %d: %w", i+1, err)
		}
	}
	return nil
}

// strictLexContent checks one content stream.
func strictLexContent(content []byte) error {
	inline := false
	for _, tok := range tokenizeContent(content) {
		op, ok := tok.val.(opKeyword)
		if !ok {
			continue
		}
		switch name := string(op); name {
		case "ID":
			inline = true // what follows is image data, not tokens
		case "EI":
			inline = false
		default:
			if inline || contentOperators[name] {
				continue
			}
			return fmt.Errorf("%q at byte %d is not an operator: two tokens "+
				"have run together", name, tok.start)
		}
	}
	return nil
}

// contentOperators is every operator a content stream may use, from the
// operator summary in the PDF specification, plus the three keywords that
// are values rather than operators.
var contentOperators = map[string]bool{
	// Graphics state
	"q": true, "Q": true, "cm": true, "w": true, "J": true, "j": true,
	"M": true, "d": true, "ri": true, "i": true, "gs": true,
	// Path construction and painting
	"m": true, "l": true, "c": true, "v": true, "y": true, "h": true,
	"re": true, "S": true, "s": true, "f": true, "F": true, "f*": true,
	"B": true, "B*": true, "b": true, "b*": true, "n": true,
	"W": true, "W*": true,
	// Text
	"BT": true, "ET": true, "Tc": true, "Tw": true, "Tz": true, "TL": true,
	"Tf": true, "Tr": true, "Ts": true, "Td": true, "TD": true, "Tm": true,
	"T*": true, "Tj": true, "TJ": true, "'": true, "\"": true,
	// Type 3 glyph metrics
	"d0": true, "d1": true,
	// Colour
	"CS": true, "cs": true, "SC": true, "SCN": true, "sc": true, "scn": true,
	"G": true, "g": true, "RG": true, "rg": true, "K": true, "k": true,
	// Shading, images, XObjects
	"sh": true, "BI": true, "ID": true, "EI": true, "Do": true,
	// Marked content and compatibility
	"MP": true, "DP": true, "BMC": true, "BDC": true, "EMC": true,
	"BX": true, "EX": true,
	// Values that lex as keywords
	"true": true, "false": true, "null": true,
}
