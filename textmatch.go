package gopdf

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// What counts as a hit.
//
// Three places look for the same text — the flow engine when it
// substitutes, the redactor when it removes, and the read-back when it
// checks nothing survived — and they have to agree. If the matcher
// respects word boundaries and the check does not, a correct pass is
// reported as a failure, because "Rossini" looks to the check like a
// surviving "Rossi". So the definition lives here, once.

// matchMode says how strictly a literal has to sit in the text.
type matchMode int

const (
	// matchWords finds a literal only where it is not flanked by a letter
	// or a digit. "Rossi" is found in "Sig. Rossi," and not in "Rossini".
	matchWords matchMode = iota
	// matchAnywhere finds a literal wherever it appears, which is what
	// this package used to do everywhere.
	matchAnywhere
)

// literalRanges finds every occurrence of a literal in text.
//
// Under matchWords an occurrence counts only where neither neighbour is a
// letter or a digit. Punctuation and symbols do not block a match, so a
// reference number sitting between a space and a full stop is still
// found, and neither do the edges of the text.
func literalRanges(text, lit string, mode matchMode) [][2]int {
	if lit == "" {
		return nil
	}
	var out [][2]int
	for at := 0; ; {
		i := strings.Index(text[at:], lit)
		if i < 0 {
			return out
		}
		lo := at + i
		hi := lo + len(lit)
		if mode == matchAnywhere || wordBounded(text, lo, hi) {
			out = append(out, [2]int{lo, hi})
		}
		// Overlapping occurrences are not sought: advancing past this one
		// keeps "aa" in "aaa" to a single hit, which is what a reader
		// would count.
		at = hi
		if hi == lo {
			at++
		}
		if at > len(text) {
			return out
		}
	}
}

// wordBounded reports whether the range [lo,hi) of text stands on its own
// — no letter or digit immediately either side.
func wordBounded(text string, lo, hi int) bool {
	if lo > 0 {
		r, _ := utf8.DecodeLastRuneInString(text[:lo])
		if isWordRune(r) {
			return false
		}
	}
	if hi < len(text) {
		r, _ := utf8.DecodeRuneInString(text[hi:])
		if isWordRune(r) {
			return false
		}
	}
	return true
}

// isWordRune reports whether a character continues a word. Digits count,
// so a mapping for "123" is not found inside "5123".
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// containsBounded reports whether text holds the literal as a word, which
// is what the read-back asks.
func containsBounded(text, lit string, mode matchMode) bool {
	return len(literalRanges(text, lit, mode)) > 0
}
