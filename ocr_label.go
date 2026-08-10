package gopdf

import (
	"fmt"
	"strings"
)

// Putting a token where the pixels were.
//
// Scrubbing a word out of a scan leaves a blank rectangle. For
// pseudonymization that is not enough: the point is that the reader sees
// [[PII_NAME_1]] and can follow the same person through the document. The
// pixels of the original are gone either way — what is added here is text
// drawn on the page over the hole, in a font of this library's choosing
// rather than the one in the picture, because there is no font in a
// picture.
//
// This direction is one-way. The token can be turned back into the name
// in a content stream, where the text is text; it cannot be turned back
// into the pixels it replaced, because those were overwritten. That is
// the trade a caller makes by pseudonymizing a scan.

// redactBar is a rectangle painted over removed content, with an optional
// token set into it.
type redactBar struct {
	box   rect
	label string
}

// labelFontName is the resource name given to the font used for tokens.
// It is deliberately unlikely to collide with a name already in the file.
const labelFontName = Name("GoPdfRedactionLabel")

// labelOps draws the bars for one page, and any token set into them.
//
// Coordinates arrive in this package's top-left system and are written in
// the content stream's bottom-up one.
func (rd *Redactor) labelOps(bars []redactBar, box [4]float64, hasFont bool) []byte {
	var b strings.Builder
	height := box[3] - box[1]
	b.WriteString("\nq ")
	fmt.Fprintf(&b, "%s rg\n", rd.fill.components())
	for _, bar := range bars {
		fmt.Fprintf(&b, "%s %s %s %s re f\n",
			fl(bar.box.x0+box[0]), fl(height-bar.box.y1+box[1]),
			fl(bar.box.x1-bar.box.x0), fl(bar.box.y1-bar.box.y0))
	}
	if hasFont {
		for _, bar := range bars {
			if bar.label == "" {
				continue
			}
			writeLabel(&b, bar, box, height, rd.labelColor)
		}
	}
	b.WriteString("Q\n")
	return []byte(b.String())
}

// writeLabel sets one token inside its bar, shrunk to fit.
func writeLabel(b *strings.Builder, bar redactBar, box [4]float64,
	height float64, colour Color) {
	w := bar.box.x1 - bar.box.x0
	h := bar.box.y1 - bar.box.y0
	if w <= 1 || h <= 1 {
		return
	}
	// Start from the height of the bar, then shrink until the token fits
	// across it. Helvetica's metrics are known, so this needs no font
	// program.
	size := h * 0.8
	if textW := Helvetica.TextWidth(bar.label, size); textW > w*0.95 && textW > 0 {
		size *= w * 0.95 / textW
	}
	if size < 1 {
		return
	}
	textW := Helvetica.TextWidth(bar.label, size)
	x := bar.box.x0 + box[0] + (w-textW)/2
	// Sit the baseline a little above the bottom of the bar.
	y := height - bar.box.y1 + box[1] + h*0.25
	fmt.Fprintf(b, "BT %s rg /%s %s Tf %s %s Td %s Tj ET\n",
		colour.components(), labelFontName, fl(size), fl(x), fl(y),
		pdfTextString(bar.label))
}

// labelFontDict is the font a token is set in: one of the standard
// fourteen, so nothing has to be embedded.
func labelFontDict() Dict {
	return Dict{
		"Type":     Name("Font"),
		"Subtype":  Name("Type1"),
		"BaseFont": Name("Helvetica"),
		"Encoding": Name("WinAnsiEncoding"),
	}
}

// withLabelFont adds the token font to a page's resources and returns the
// new resource dictionary.
func (rd *Redactor) withLabelFont(res any) Dict {
	dict, _ := rd.r.resolve(res).(Dict)
	out := cloneDict(dict)
	fonts, _ := rd.r.resolve(out["Font"]).(Dict)
	newFonts := cloneDict(fonts)
	if rd.labelFontRef.Num == 0 {
		rd.labelFontRef = rd.newObject(labelFontDict())
	}
	newFonts[labelFontName] = rd.labelFontRef
	out["Font"] = newFonts
	return out
}

// SetLabel sets the token written into every bar this redaction paints.
// It is what turns a blank rectangle into a marked one:
//
//	rd.SetLabel("[REDACTED]")
//
// A token is set in Helvetica, shrunk to fit the space the removed
// content occupied, so a long token in a small box comes out small. Pass
// an empty string to go back to a plain bar.
//
// For text in a content stream this is the cruder of two tools: a
// [Pseudonym] passed to [Pseudonymize] re-wraps the paragraph around the
// token and keeps the surrounding styling. Labels exist for the case that
// cannot do — words found by an OCR engine inside a picture, where there
// is no text to re-wrap and no font to match.
func (rd *Redactor) SetLabel(token string) {
	rd.label = token
	rd.planned = false
}

// SetLabelColor sets the colour a token is written in. The default is
// white, which shows against the black bar.
func (rd *Redactor) SetLabelColor(c Color) {
	rd.labelColor = c
	rd.planned = false
}

// tokenFor returns the token to set over a removed word. A substitution
// naming that word wins; otherwise the general label is used.
func (rd *Redactor) tokenFor(word string) string {
	for _, s := range rd.tokens {
		if strings.Contains(word, s.From) || wordOf(s.From, word) {
			return s.To
		}
	}
	return rd.label
}

// wordOf reports whether word is one of the words of a literal.
func wordOf(literal, word string) bool {
	trimmed := strings.Trim(word, ".,;:()[]{}\"'`?!")
	for _, part := range strings.Fields(literal) {
		if len(part) > 2 && trimmed == part {
			return true
		}
	}
	return false
}

// Substitute marks text and gives the token to write where it was. It is
// the pseudonymizing form of Text: the content is removed exactly as
// Text removes it, and the token is set into the bar left behind.
//
// For words an OCR engine finds inside a picture this is the only way to
// substitute, there being no text to rewrite and no font to match.
func (rd *Redactor) Substitute(from, to string) {
	if from == "" {
		return
	}
	rd.literals = append(rd.literals, from)
	rd.tokens = append(rd.tokens, Pseudonym{From: from, To: to})
	rd.planned = false
}
