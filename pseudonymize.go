package gopdf

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// Pseudonymization: swapping identifying text for tokens.
//
// This is not redaction, and not an edit either. Redaction takes content
// out and leaves a gap; an edit puts new text in but appends it, leaving
// the original readable in the revision underneath. Pseudonymization
// needs both halves — the token has to appear and the original has to be
// gone — so the substitution is reflowed into the page and the result is
// written as a fresh file rather than appended.
//
// Doing that in one call matters: the two steps are easy to write
// separately and easy to stop after the first, which produces a document
// that looks anonymous and hands the original back to anyone who
// truncates it at the first end-of-file marker.

// Pseudonym is one substitution: every occurrence of From becomes To.
type Pseudonym struct {
	From, To string
}

// PseudonymizeResult reports what a substitution pass did.
type PseudonymizeResult struct {
	// Replaced counts the paragraphs rewritten, per original string.
	Replaced map[string]int
	// Pages is how many pages were touched.
	Pages int
}

// Total returns the number of paragraphs rewritten across every mapping.
func (r PseudonymizeResult) Total() int {
	n := 0
	for _, v := range r.Replaced {
		n += v
	}
	return n
}

// Pseudonymize replaces identifying text with tokens throughout a
// document and writes the result.
//
// Each paragraph it changes is re-wrapped, so a token need not be the
// same length as the name it replaces, and each part of the paragraph
// keeps the styling it had. The output is a complete file rather than an
// incremental update, so the original text cannot be recovered from an
// earlier revision, and it is read back and checked before being handed
// over: if any original is still readable, Pseudonymize reports that and
// writes nothing.
//
// It reports an error, and writes nothing, if a document's fonts cannot
// represent a token — writing one that renders as blank boxes would be
// worse than declining.
func Pseudonymize(r *Reader, w io.Writer, subs []Pseudonym) (PseudonymizeResult, error) {
	var out PseudonymizeResult
	if r == nil {
		return out, fmt.Errorf("gopdf: no document to pseudonymize")
	}
	clean := make([]Pseudonym, 0, len(subs))
	for _, s := range subs {
		if s.From == "" {
			return out, fmt.Errorf("gopdf: a substitution has no text to replace")
		}
		if strings.Contains(s.To, s.From) {
			return out, fmt.Errorf("gopdf: replacing %q with %q would leave the "+
				"original inside the token", s.From, s.To)
		}
		clean = append(clean, s)
	}
	if len(clean) == 0 {
		return out, fmt.Errorf("gopdf: no substitutions given")
	}
	// Longer originals first, so replacing "Ada Lovelace" is not
	// pre-empted by a rule for "Ada".
	sort.SliceStable(clean, func(i, j int) bool {
		return len(clean[i].From) > len(clean[j].From)
	})
	out.Replaced = make(map[string]int, len(clean))

	u := Update(r)
	touched := make(map[int]bool)
	for i := 0; i < r.NumPages(); i++ {
		page, err := u.Page(i)
		if err != nil {
			return PseudonymizeResult{}, fmt.Errorf("gopdf: page %d: %w", i, err)
		}
		for _, s := range clean {
			n, err := page.ReplaceTextFlow(s.From, s.To)
			if err != nil {
				return PseudonymizeResult{}, fmt.Errorf(
					"gopdf: replacing %q on page %d: %w", s.From, i, err)
			}
			if n > 0 {
				out.Replaced[s.From] += n
				touched[i] = true
			}
		}
	}
	out.Pages = len(touched)

	// The update carries the original text in the bytes it appends to, so
	// it is never what the caller receives; the rewrite drops it.
	var updated bytes.Buffer
	if _, err := u.WriteTo(&updated); err != nil {
		return PseudonymizeResult{}, err
	}
	reread, err := NewReader(updated.Bytes())
	if err != nil {
		return PseudonymizeResult{}, fmt.Errorf(
			"gopdf: the substituted document could not be read back: %w", err)
	}
	// The page text is only the visible half. A name also sits in the
	// metadata, in annotations, bookmarks and field values, none of which
	// is drawn and all of which is read back by anything that opens the
	// file.
	rw := newRewriter(reread)
	if err := scrubStrings(rw, clean); err != nil {
		return PseudonymizeResult{}, err
	}
	var final bytes.Buffer
	if _, err := rw.writeTo(&final); err != nil {
		return PseudonymizeResult{}, err
	}
	if err := checkPseudonymized(final.Bytes(), clean); err != nil {
		return PseudonymizeResult{}, err
	}
	if _, err := w.Write(final.Bytes()); err != nil {
		return PseudonymizeResult{}, err
	}
	return out, nil
}

// PseudonymizeFile is Pseudonymize between two paths.
func PseudonymizeFile(src, dst string, subs []Pseudonym) (PseudonymizeResult, error) {
	r, err := Open(src)
	if err != nil {
		return PseudonymizeResult{}, err
	}
	var buf bytes.Buffer
	res, err := Pseudonymize(r, &buf, subs)
	if err != nil {
		return res, err
	}
	// Written only once the substitution has proved itself, so a failure
	// cannot leave a half-anonymous file on disk.
	return res, os.WriteFile(dst, buf.Bytes(), 0o644)
}

// checkPseudonymized confirms that nothing a mapping was meant to remove
// can still be found — on the page, in a string anywhere in the object
// graph, or in a metadata packet.
func checkPseudonymized(out []byte, subs []Pseudonym) error {
	r, err := NewReader(out)
	if err != nil {
		return fmt.Errorf("gopdf: the pseudonymized document could not be read back: %w", err)
	}
	where, err := findResidue(r, subs)
	if err != nil {
		return err
	}
	if where != "" {
		return fmt.Errorf("gopdf: %s; the output has been withheld", where)
	}
	return nil
}
