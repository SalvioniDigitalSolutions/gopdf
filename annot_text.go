package gopdf

import "math"

// The text an annotation draws.
//
// An annotation carries its words twice: as a string in /Contents, which
// anything reading the file can see, and as drawing operators in an
// appearance stream, which is what a viewer actually shows. Scrubbing the
// string leaves the appearance untouched, and the appearance is neither
// page content nor a form XObject the flow engine descends into — so a
// name written into a FreeText note survives both the substitution and,
// unless it is looked for here, the check that nothing survived.
//
// That combination is the one that must never happen: missed by the
// matcher and missed by the check is a document reported clean while
// still showing the name. So the appearances are read, and anything found
// in one is reported rather than passed over.

// annotationText returns the text drawn by every annotation appearance on
// a page, joined.
func (r *Reader) annotationText(page int) string {
	if page < 0 || page >= len(r.pages) {
		return ""
	}
	annots, ok := r.resolve(r.pages[page].dict["Annots"]).(Array)
	if !ok {
		return ""
	}
	var out string
	for _, entry := range annots {
		ad, ok := r.resolve(entry).(Dict)
		if !ok {
			continue
		}
		for _, stm := range appearanceStreams(r, ad) {
			data, err := r.decodeStream(stm.dict, stm.data)
			if err != nil {
				continue
			}
			ex := &textExtractor{r: r, lastY: math.Inf(1), visited: make(map[Ref]bool)}
			ex.run(data, stm.dict["Resources"], identityMatrix, 0)
			if s := ex.sb.String(); s != "" {
				out += s + "\n"
			}
		}
	}
	return out
}

// appearanceStreams collects the streams an annotation's /AP holds. The
// normal appearance may be a stream or, for a widget with states, a
// dictionary of them; both are searched.
func appearanceStreams(r *Reader, annot Dict) []*rawStream {
	ap, ok := r.resolve(annot["AP"]).(Dict)
	if !ok {
		return nil
	}
	var out []*rawStream
	for _, which := range []Name{"N", "R", "D"} {
		switch t := r.resolve(ap[which]).(type) {
		case *rawStream:
			out = append(out, t)
		case Dict:
			for _, k := range sortedKeys(t) {
				if stm, ok := r.resolve(t[k]).(*rawStream); ok {
					out = append(out, stm)
				}
			}
		}
	}
	return out
}
