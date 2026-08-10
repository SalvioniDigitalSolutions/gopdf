package gopdf

import (
	"strings"
	"unicode/utf8"
)

// A word broken across two lines is still one word.
//
// Justified text hyphenates: "Bianchi" set at the end of a line becomes
// "Bian-" and "chi", and a paragraph that reads that way to a person
// reads as two words to anything matching on characters. A mapping for
// "Bianchi" then finds nothing, and — worse — the read-back that proves
// nothing survived finds nothing either, so a split name passes the check
// unseen.
//
// The seam is only visible here. A flow knows which of its spans is the
// space it put between two lines, so it can tell a hyphen that ends a
// line from one in the middle of "CHE-290", which is real text and must
// stay whole.

// joinedReading is a paragraph's text with words rejoined across line
// breaks, and the map back to where each byte came from.
type joinedReading struct {
	text string
	// origin[i] is the byte offset in the flattened original text that
	// byte i of text came from.
	origin []int
	// flat is the original text, spans concatenated.
	flat string
}

// readParagraph flattens spans and, where a line break interrupts a
// hyphenated word, offers the joined reading as well.
//
// The hyphen and the break are dropped from the reading but not from the
// map, so a match maps back onto a range of the original that includes
// them: replacing the word takes the dangling hyphen with it.
func readParagraph(spans []FlowSpan) joinedReading {
	var flat strings.Builder
	for _, s := range spans {
		flat.WriteString(s.Text)
	}
	out := joinedReading{flat: flat.String()}

	var text strings.Builder
	at := 0
	for i, s := range spans {
		if s.lineBreak && endsHyphenated(spans, i) {
			// Drop the break, and the hyphen already written before it.
			trimHyphen(&text, &out.origin)
			at += len(s.Text)
			continue
		}
		for b := 0; b < len(s.Text); b++ {
			text.WriteByte(s.Text[b])
			out.origin = append(out.origin, at+b)
		}
		at += len(s.Text)
	}
	out.text = text.String()
	return out
}

// endsHyphenated reports whether the span before a line break ends in a
// hyphen that continues a word on the next line.
func endsHyphenated(spans []FlowSpan, breakAt int) bool {
	prev := previousText(spans, breakAt)
	next := nextText(spans, breakAt)
	if prev == "" || next == "" {
		return false
	}
	r, size := utf8.DecodeLastRuneInString(prev)
	if !isHyphen(r) {
		return false
	}
	// A letter has to sit either side, or this is a dash between things
	// rather than a word split in two.
	before, _ := utf8.DecodeLastRuneInString(prev[:len(prev)-size])
	after, _ := utf8.DecodeRuneInString(next)
	return isWordRune(before) && isWordRune(after)
}

// isHyphen covers the characters a producer uses to break a word.
func isHyphen(r rune) bool {
	return r == '-' || r == '­' || r == '‐' || r == '‑'
}

func previousText(spans []FlowSpan, i int) string {
	for j := i - 1; j >= 0; j-- {
		if spans[j].Text != "" {
			return spans[j].Text
		}
	}
	return ""
}

func nextText(spans []FlowSpan, i int) string {
	for j := i + 1; j < len(spans); j++ {
		if spans[j].Text != "" {
			return spans[j].Text
		}
	}
	return ""
}

// trimHyphen removes the hyphen just written, and its entry in the map.
func trimHyphen(text *strings.Builder, origin *[]int) {
	s := text.String()
	r, size := utf8.DecodeLastRuneInString(s)
	if !isHyphen(r) {
		return
	}
	text.Reset()
	text.WriteString(s[:len(s)-size])
	*origin = (*origin)[:len(*origin)-size]
}

// rangeInOriginal maps a range of the joined reading back onto the
// flattened original. The result spans everything between the first and
// last matched byte, which is what carries a dropped hyphen and line
// break along with the word.
func (j joinedReading) rangeInOriginal(lo, hi int) (int, int, bool) {
	if lo < 0 || hi > len(j.origin) || lo >= hi {
		return 0, 0, false
	}
	start := j.origin[lo]
	last := j.origin[hi-1]
	// The last byte's own length: the map is per byte, so one past it.
	end := last + 1
	return start, end, true
}
