package gopdf

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Reaching the copies of a name that are not page text.
//
// Replacing what a page draws is the visible half of the job. A name also
// sits in the information dictionary, in the XMP packet beside it, in an
// annotation's note, a bookmark's title, a form field's value, a file
// attachment's description. None of that is drawn, all of it is read back
// by anything that opens the file, and leaving it there makes an
// otherwise anonymous document identify its subject on the first look at
// its properties.
//
// So every string the document still reaches is rewritten, not just the
// ones on the page, and the result is checked the same way.

// scrubStrings rewrites every string in the reachable object graph,
// substituting each mapping. It registers the changed objects with the
// rewriter, which is what will emit them.
func scrubStrings(rw *rewriter, subs []Pseudonym) error {
	live, err := rw.reachableFromTrailer()
	if err != nil {
		return err
	}
	for num := range live {
		obj, err := rw.object(num)
		if err != nil || obj == nil {
			continue
		}
		// A signature's blob is binary held in a string; rewriting bytes
		// inside it would corrupt it to no purpose, the rewrite having
		// already broken the signature.
		if d, ok := obj.(Dict); ok && r_isSignature(d) {
			continue
		}
		if out, changed := substituteStrings(obj, subs, 0); changed {
			rw.replace[num] = out
		}
	}
	return nil
}

// r_isSignature reports whether a dictionary is a signature value.
func r_isSignature(d Dict) bool {
	_, hasByteRange := d["ByteRange"]
	return d["Type"] == Name("Sig") || (hasByteRange && d["Contents"] != nil)
}

// substituteStrings deep-copies a value, replacing text inside every
// string it holds and inside any XMP metadata packet.
func substituteStrings(v any, subs []Pseudonym, depth int) (any, bool) {
	if depth > maxCopyDepth {
		return v, false
	}
	switch t := v.(type) {
	case String:
		text := decodeTextString(t)
		got := applySubs(text, subs)
		if got == text {
			return t, false
		}
		return String(textStringBytes(got)), true

	case Dict:
		var out Dict
		for _, k := range sortedKeys(t) {
			cp, changed := substituteStrings(t[k], subs, depth+1)
			if !changed {
				continue
			}
			if out == nil {
				out = cloneDict(t)
			}
			out[k] = cp
		}
		if out == nil {
			return t, false
		}
		// An annotation's appearance stream is a rendering of the very
		// strings just rewritten. Keeping it would leave the old text
		// drawn on the page under the new text in the dictionary, which
		// is the worst of both. Dropping it asks the viewer to draw the
		// annotation again from what it now says.
		if isAnnotation(t) {
			delete(out, "AP")
			out["NeedAppearances"] = true
		}
		return out, true

	case Array:
		var out Array
		for i, e := range t {
			cp, changed := substituteStrings(e, subs, depth+1)
			if !changed {
				continue
			}
			if out == nil {
				out = append(Array{}, t...)
			}
			out[i] = cp
		}
		if out == nil {
			return t, false
		}
		return out, true

	case *rawStream:
		dict, changed := substituteStrings(t.dict, subs, depth+1)
		d, _ := dict.(Dict)
		if d == nil {
			d = t.dict
		}
		// An XMP packet is XML text carried in a stream, so the names in
		// it are plain bytes rather than PDF strings.
		if data, ok := scrubXMP(t, subs); ok {
			return &rawStream{dict: d, data: data}, true
		}
		if changed {
			return &rawStream{dict: d, data: t.data}, true
		}
		return t, false
	}
	return v, false
}

// scrubXMP substitutes inside an uncompressed XMP metadata packet,
// reporting whether it did.
func scrubXMP(stm *rawStream, subs []Pseudonym) ([]byte, bool) {
	if stm.dict["Subtype"] != Name("XML") && stm.dict["Type"] != Name("Metadata") {
		return nil, false
	}
	// XMP is required to be readable without filters, so the bytes are
	// the text; anything filtered is left to the check to catch.
	if _, filtered := stm.dict["Filter"]; filtered {
		return nil, false
	}
	got := applySubs(string(stm.data), subs)
	if got == string(stm.data) {
		return nil, false
	}
	return []byte(got), true
}

// applySubs runs every mapping over a piece of text.
func applySubs(s string, subs []Pseudonym) string {
	for _, sub := range subs {
		s = strings.ReplaceAll(s, sub.From, sub.To)
	}
	return s
}

// reachableFromTrailer returns every object the document still refers to,
// counting the information dictionary, which hangs off the trailer rather
// than the catalog.
func (rw *rewriter) reachableFromTrailer() (map[int]bool, error) {
	var roots []any
	if ref, ok := rw.r.trailer["Root"].(Ref); ok {
		roots = append(roots, ref)
	}
	if ref, ok := rw.r.trailer["Info"].(Ref); ok {
		roots = append(roots, ref)
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("gopdf: the document has no catalog")
	}
	return rw.reachable(roots)
}

// findResidue looks through everything a document still holds for text
// that a substitution was meant to remove, and says where it found it.
// It is the check behind the promise that the original is not
// recoverable, so it looks past the page text at the places a name hides.
func findResidue(r *Reader, subs []Pseudonym) (string, error) {
	// The pages, as anyone reading the document would see them — which
	// includes reading a word hyphenated across a line break as one
	// word. The check and the matcher have to agree on that, or a
	// correctly replaced split word looks like a survivor, and a real
	// survivor passes unseen.
	for i := 0; i < r.NumPages(); i++ {
		text, err := r.PageText(i)
		if err != nil {
			return "", fmt.Errorf("gopdf: page %d could not be read back: %w", i, err)
		}
		// An annotation draws its words in an appearance stream as well
		// as holding them in a string. Scrubbing the string leaves the
		// drawing, which nothing else here reads.
		text += "\n" + r.annotationText(i)
		for _, reading := range []string{text, dehyphenate(text)} {
			for _, s := range subs {
				if containsBounded(reading, s.From, matchWords) {
					return fmt.Sprintf("%q is still readable on page %d", s.From, i+1), nil
				}
			}
		}
	}

	// Every string and every unfiltered stream the document still holds.
	rw := newRewriter(r)
	live, err := rw.reachableFromTrailer()
	if err != nil {
		return "", err
	}
	for num := range live {
		obj, err := r.object(num)
		if err != nil || obj == nil {
			continue
		}
		if where := residueIn(obj, subs, 0); where != "" {
			return fmt.Sprintf("%s (object %d)", where, num), nil
		}
	}
	return "", nil
}

// residueIn searches one object for text a substitution should have
// removed.
func residueIn(v any, subs []Pseudonym, depth int) string {
	if depth > maxCopyDepth {
		return ""
	}
	switch t := v.(type) {
	case String:
		text := decodeTextString(t)
		for _, s := range subs {
			if containsBounded(text, s.From, matchWords) {
				return fmt.Sprintf("%q survives in a string", s.From)
			}
		}
	case Dict:
		for _, k := range sortedKeys(t) {
			if where := residueIn(t[k], subs, depth+1); where != "" {
				return where
			}
		}
	case Array:
		for _, e := range t {
			if where := residueIn(e, subs, depth+1); where != "" {
				return where
			}
		}
	case *rawStream:
		if where := residueIn(t.dict, subs, depth+1); where != "" {
			return where
		}
		// Metadata packets carry their text in the open.
		if t.dict["Type"] == Name("Metadata") || t.dict["Subtype"] == Name("XML") {
			for _, s := range subs {
				if bytes.Contains(t.data, []byte(s.From)) {
					return fmt.Sprintf("%q survives in a metadata packet", s.From)
				}
			}
		}
	}
	return ""
}

// dehyphenate joins words a line break split, so the check reads a page
// the way the matcher does.
func dehyphenate(text string) string {
	var b strings.Builder
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		if i+1 < len(lines) && endsWithWordHyphen(trimmed) {
			b.WriteString(trimmed[:len(trimmed)-hyphenLen(trimmed)])
			continue
		}
		b.WriteString(line)
		if i+1 < len(lines) {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// endsWithWordHyphen reports whether a line ends in a hyphen that follows
// a letter or digit, which is a word split rather than a dash.
func endsWithWordHyphen(line string) bool {
	r, size := utf8.DecodeLastRuneInString(line)
	if !isHyphen(r) {
		return false
	}
	before, _ := utf8.DecodeLastRuneInString(line[:len(line)-size])
	return isWordRune(before)
}

func hyphenLen(line string) int {
	_, size := utf8.DecodeLastRuneInString(line)
	return size
}

// isAnnotation reports whether a dictionary is an annotation, which is
// what makes its /AP a cache of its strings rather than content of its
// own.
func isAnnotation(d Dict) bool {
	if d["Type"] == Name("Annot") {
		return true
	}
	_, hasSubtype := d["Subtype"]
	_, hasRect := d["Rect"]
	return hasSubtype && hasRect
}
