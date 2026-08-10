package gopdf

import "strings"

// Closing the routes by which a redacted page stays readable.
//
// Scrubbing the pixels a word sits on is necessary and not sufficient. A
// PDF may carry a second copy of the same picture in more than one place,
// none of which is the image the page draws:
//
//   - /Thumb on a page is a small rendering of that page, made before the
//     redaction and showing everything it removed.
//   - /Alternates on an image offers other versions of it, typically a
//     higher-resolution original kept for print.
//   - /PieceInfo holds whatever the producing application wanted to keep,
//     which for a word processor can be the text it laid out.
//
// None of these is drawn, all of them travel with the file, and any of
// them will hand back what the redaction was for. They are dropped.

// leakRoutesOnPage are page entries that can hold a copy of the page as
// it was before redaction.
var leakRoutesOnPage = []Name{"Thumb", "PieceInfo"}

// leakRoutesOnImage are image entries that can hold another version of
// the same picture.
var leakRoutesOnImage = []Name{"Alternates"}

// stripLeakRoutes removes the entries that can carry an unredacted copy.
// It reports which it removed, so a caller can be told.
func stripLeakRoutes(d Dict, keys []Name) []string {
	var gone []string
	for _, k := range keys {
		if _, has := d[k]; has {
			delete(d, k)
			gone = append(gone, "/"+string(k))
		}
	}
	return gone
}

// allImageRefs returns every image the document still reaches, whether or
// not a page draws it.
//
// Verification walks these rather than the images on each page: a
// thumbnail or an alternate is exactly the copy that would be missed by
// looking only at what is drawn.
func allImageRefs(r *Reader) []ImageRef {
	rw := newRewriter(r)
	live, err := rw.reachableFromTrailer()
	if err != nil {
		return nil
	}
	var out []ImageRef
	for num := range live {
		stm, ok := r.resolve(Ref{Num: num}).(*rawStream)
		if !ok {
			continue
		}
		if r.resolve(stm.dict["Subtype"]) != Name("Image") {
			continue
		}
		img := ImageRef{ref: Ref{Num: num}, stream: stm, r: r}
		if w, ok := toInt(r.resolve(stm.dict["Width"])); ok {
			img.Width = w
		}
		if h, ok := toInt(r.resolve(stm.dict["Height"])); ok {
			img.Height = h
		}
		if img.Width > 0 && img.Height > 0 {
			out = append(out, img)
		}
	}
	return out
}

// hardenPage drops the leak routes from a page dictionary and records
// what went, so Marks can report it.
func (rd *Redactor) hardenPage(page int, pageDict Dict) {
	for _, what := range stripLeakRoutes(pageDict, leakRoutesOnPage) {
		rd.marks = append(rd.marks, RedactionMark{
			Kind: RedactCopy, Page: page,
			Text: what + " held a copy of the page as it was",
		})
	}
}

// describeLeaks summarises what was dropped, for a caller that wants to
// say so rather than read the marks.
func describeLeaks(marks []RedactionMark) string {
	var seen []string
	for _, m := range marks {
		if m.Kind == RedactCopy {
			seen = append(seen, m.Text)
		}
	}
	return strings.Join(seen, "; ")
}
