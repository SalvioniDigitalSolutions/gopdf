package gopdf

import "bytes"

// Strings that live inside content streams as operands.
//
// The page rewrite handles what is drawn; scrubStrings handles the
// strings of the object graph. A marked-content sequence carries a third
// kind: /Span << /ActualText (…) >> BDC puts the text a screen reader
// should speak — or what the glyphs abbreviate — into an inline
// dictionary of the content stream, and /Alt and /E travel the same way.
// A tagged document written by Word or Acrobat holds every word of the
// page there a second time. Neither pass above reaches it, so this one
// walks the content streams and rewrites the strings of every inline
// dictionary it finds.

// scrubContentStrings substitutes inside the inline dictionaries of every
// reachable content stream: page contents, form XObjects (appearance
// streams and stamps included) and tiling patterns.
func scrubContentStrings(rw *rewriter, subs []Pseudonym) error {
	live, err := rw.reachableFromTrailer()
	if err != nil {
		return err
	}
	// Page content streams, found through the pages AS THEY WILL BE
	// WRITTEN: the redactor merges a page's content into a new object
	// and re-points the page at it, so the original page dictionary
	// would name a stream that no longer ships.
	contents := map[int]bool{}
	for num := range live {
		obj, err := rw.object(num)
		if err != nil {
			continue
		}
		d, ok := obj.(Dict)
		if !ok || d["Type"] != Name("Page") {
			continue
		}
		for _, ref := range contentRefs(rw, d["Contents"]) {
			contents[ref] = true
		}
	}
	for num := range live {
		obj, err := rw.object(num)
		if err != nil || obj == nil {
			continue
		}
		stm, ok := obj.(*rawStream)
		if !ok {
			continue
		}
		pt, _ := toInt(stm.dict["PatternType"])
		if !contents[num] && stm.dict["Subtype"] != Name("Form") && pt != 1 {
			continue
		}
		data, err := rw.r.decodeStream(stm.dict, stm.data)
		if err != nil {
			continue
		}
		toks, truncated := tokenizeContentLimited(data)
		if truncated {
			continue
		}
		var out bytes.Buffer
		cursor := 0
		changed := false
		for _, t := range toks {
			d, ok := t.val.(Dict)
			if !ok {
				continue
			}
			nd, ch := substituteStrings(d, subs, 0)
			if !ch {
				continue
			}
			out.Write(data[cursor:t.start])
			writeValue(&out, nd, &writeCtx{})
			cursor = t.end
			changed = true
		}
		if !changed {
			continue
		}
		out.Write(data[cursor:])
		rw.replace[num] = compressedStreamWith(stm.dict, out.Bytes())
	}
	return nil
}

// contentRefs lists the object numbers a page's Contents entry names —
// one reference, an array of them, or a reference to such an array —
// following substitutions rather than the source file.
func contentRefs(rw *rewriter, v any) []int {
	var out []int
	switch t := v.(type) {
	case Ref:
		if obj, err := rw.object(t.Num); err == nil {
			if arr, ok := obj.(Array); ok {
				for _, e := range arr {
					if r, ok := e.(Ref); ok {
						out = append(out, r.Num)
					}
				}
				return out
			}
		}
		out = append(out, t.Num)
	case Array:
		for _, e := range t {
			if r, ok := e.(Ref); ok {
				out = append(out, r.Num)
			}
		}
	}
	return out
}

// scrubXFA substitutes inside an XFA form's XML streams. A dynamic form
// keeps every field value in its datasets packet — a stream of XML the
// string pass never reads — under /AcroForm /XFA, either one stream or
// an array of (name, stream) pairs.
func scrubXFA(rw *rewriter, subs []Pseudonym) error {
	rootRef, ok := rw.r.trailer["Root"].(Ref)
	if !ok {
		return nil
	}
	root, _ := rw.r.resolve(rootRef).(Dict)
	acro, _ := rw.r.resolve(root["AcroForm"]).(Dict)
	if acro == nil {
		return nil
	}
	var refs []Ref
	switch x := acro["XFA"].(type) {
	case Ref:
		refs = append(refs, x)
	case Array:
		for _, e := range x {
			if r, ok := e.(Ref); ok {
				refs = append(refs, r)
			}
		}
	default:
		if arr, ok := rw.r.resolve(acro["XFA"]).(Array); ok {
			for _, e := range arr {
				if r, ok := e.(Ref); ok {
					refs = append(refs, r)
				}
			}
		}
	}
	for _, ref := range refs {
		obj, err := rw.object(ref.Num)
		if err != nil {
			continue
		}
		stm, ok := obj.(*rawStream)
		if !ok {
			continue
		}
		data, err := rw.r.decodeStream(stm.dict, stm.data)
		if err != nil {
			continue
		}
		got := applySubs(string(data), subs)
		if got == string(data) {
			continue
		}
		rw.replace[ref.Num] = compressedStreamWith(stm.dict, []byte(got))
	}
	return nil
}

// stripLeakRoutesInGraph drops, from every reachable page and image, the
// entries that carry a copy of the unredacted content (see
// redact_harden.go): a page's /Thumb and /PieceInfo, an image's
// /Alternates. The redactor does this per page as it works; the
// pseudonymizer rewrites the graph instead, so it does it here.
func stripLeakRoutesInGraph(rw *rewriter) error {
	live, err := rw.reachableFromTrailer()
	if err != nil {
		return err
	}
	for num := range live {
		obj, err := rw.object(num)
		if err != nil || obj == nil {
			continue
		}
		switch t := obj.(type) {
		case Dict:
			if t["Type"] != Name("Page") {
				continue
			}
			d := cloneDict(t)
			if len(stripLeakRoutes(d, leakRoutesOnPage)) > 0 {
				rw.replace[num] = d
			}
		case *rawStream:
			if t.dict["Subtype"] != Name("Image") {
				continue
			}
			d := cloneDict(t.dict)
			if len(stripLeakRoutes(d, leakRoutesOnImage)) > 0 {
				rw.replace[num] = &rawStream{dict: d, data: t.data}
			}
		}
	}
	return nil
}
