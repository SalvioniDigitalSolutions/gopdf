package gopdf

import "strings"

// The characters a document writes and a reader reports are not always
// the same one.
//
// Swiss and German legal documents routinely set every gap as U+00A0, the
// non-breaking space, and every hyphen in a compound as U+00AD, the soft
// hyphen. Extraction normalises neither: what comes out holds the
// characters the file holds. So a caller asking to replace
// "Ada Lovelace", typed with an ordinary space, matches nothing at all in
// a document that wrote "Ada Lovelace" — and is told, correctly and
// unhelpfully, that there was nothing to replace.
//
// Rather than have every caller know this, each mapping is expanded into
// the variants a document might have used. They are ordinary
// substitutions once expanded, so the check that nothing survives covers
// them without knowing they exist.

// variantChars maps a character a caller is likely to type to the ones a
// document is likely to have written instead.
var variantChars = []struct{ typed, written string }{
	{" ", " "}, // non-breaking space
	{"-", "­"}, // soft hyphen
	{"-", "‑"}, // non-breaking hyphen
}

// expandVariants returns the mapping together with every spelling of it
// the document might carry, all replaced by the same token.
//
// Order is significant: the caller's own spelling comes first, and the
// list is free of duplicates, so a string with nothing to vary expands to
// itself alone.
func expandVariants(sub Pseudonym) []Pseudonym {
	forms := []string{sub.From}
	seen := map[string]bool{sub.From: true}

	// Each variant character is applied to every form found so far, so a
	// string with both a space and a hyphen yields the combinations too.
	for _, v := range variantChars {
		for _, form := range append([]string(nil), forms...) {
			if !strings.Contains(form, v.typed) {
				continue
			}
			alt := strings.ReplaceAll(form, v.typed, v.written)
			if seen[alt] {
				continue
			}
			seen[alt] = true
			forms = append(forms, alt)
		}
	}
	out := make([]Pseudonym, 0, len(forms))
	for _, form := range forms {
		out = append(out, Pseudonym{From: form, To: sub.To})
	}
	return out
}

// expandAllVariants expands every mapping, dropping any spelling another
// mapping already claims so the longest-first order still decides.
func expandAllVariants(subs []Pseudonym) []Pseudonym {
	var out []Pseudonym
	seen := map[string]bool{}
	for _, sub := range subs {
		for _, v := range expandVariants(sub) {
			if seen[v.From] {
				continue
			}
			seen[v.From] = true
			out = append(out, v)
		}
	}
	return out
}
