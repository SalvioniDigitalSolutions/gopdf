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

// spacedText reports text holding a space other than one ordinary space
// between two characters that are not spaces.
func spacedText(s string) bool {
	return strings.Contains(s, "  ") || strings.ContainsAny(s, "\t\u00a0")
}

// isRunSpace reports a character that a run of spaces is made of. Line
// breaks are not: a value is matched along a line.
func isRunSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\u00a0'
}

// collapseSpaces writes each run of spaces in s as one ordinary space,
// and returns for each byte of the result, and for its end, where it
// stands in s: a match in the result maps back to the bytes of s it
// covers, the whole run of spaces included.
func collapseSpaces(s string) (string, []int) {
	var b strings.Builder
	at := make([]int, 0, len(s)+1)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if isRunSpace(r) {
			j := i + size
			for j < len(s) {
				r2, size2 := utf8.DecodeRuneInString(s[j:])
				if !isRunSpace(r2) {
					break
				}
				j += size2
			}
			b.WriteByte(' ')
			at = append(at, i)
			i = j
			continue
		}
		b.WriteString(s[i : i+size])
		for k := 0; k < size; k++ {
			at = append(at, i+k)
		}
		i += size
	}
	at = append(at, len(s))
	return b.String(), at
}

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
			// Overlapping occurrences are not sought: advancing past
			// this one keeps "aa" in "aaa" to a single hit, which is
			// what a reader would count.
			at = hi
		} else {
			// An occurrence that does not stand on its own says nothing
			// about one that begins inside it. "22 22" is not a word at
			// the start of "122 22 22" and is one three characters on,
			// so the search resumes just past where this one began, not
			// past where it ended.
			_, size := utf8.DecodeRuneInString(text[lo:])
			at = lo + size
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
