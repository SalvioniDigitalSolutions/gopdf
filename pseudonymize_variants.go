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
		// The whole mapping is carried over and only the spelling
		// changed. Naming the fields instead lost FitWidth once and
		// MinScale after it: a variant is the same substitution, so it
		// has to be the same substitution in every respect there is.
		v := sub
		v.From = form
		out = append(out, v)
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

// documentSpellings finds, page by page, how the document spaces each
// literal of several words where it spaces it otherwise: two spaces, a
// non-breaking space, a tab between the words ("File No.  3-45678"). The
// read-back after a replacement reads every run of spaces as one, so it
// found such a value, which no replacement had sought in that spelling,
// and the copy was withheld. Each spelling found is one more variant of
// its literal; the matching itself stays exact.
func documentSpellings(r *Reader, lits []string) map[string][]string {
	var spaced []string
	for _, lit := range lits {
		if strings.ContainsAny(lit, " \t ") {
			spaced = append(spaced, lit)
		}
	}
	if len(spaced) == 0 || r == nil {
		return nil
	}
	out := map[string][]string{}
	seen := map[string]bool{}
	for i := 0; i < r.NumPages(); i++ {
		text, err := r.PageText(i)
		if err != nil || !spacedText(text) {
			continue
		}
		ct, at := collapseSpaces(text)
		for _, lit := range spaced {
			cl, _ := collapseSpaces(lit)
			for _, rg := range literalRanges(ct, cl, matchWords) {
				spelling := text[at[rg[0]]:at[rg[1]]]
				if spelling == lit || seen[lit+"\x00"+spelling] {
					continue
				}
				seen[lit+"\x00"+spelling] = true
				out[lit] = append(out[lit], spelling)
			}
		}
	}
	return out
}

// withDocumentSpellings adds to subs, after its own spellings, the ones
// documentSpellings finds, each replaced by the same token.
func withDocumentSpellings(r *Reader, subs []Pseudonym) []Pseudonym {
	lits := make([]string, len(subs))
	for i, s := range subs {
		lits[i] = s.From
	}
	found := documentSpellings(r, lits)
	if len(found) == 0 {
		return subs
	}
	have := map[string]bool{}
	for _, s := range subs {
		have[s.From] = true
	}
	out := append([]Pseudonym(nil), subs...)
	for _, s := range subs {
		for _, spelling := range found[s.From] {
			if have[spelling] {
				continue
			}
			have[spelling] = true
			v := s
			v.From = spelling
			out = append(out, v)
		}
	}
	return out
}
