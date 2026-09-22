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
	contents := map[int]bool{}
	for _, pg := range rw.r.pages {
		for _, ref := range refsOf(rw.r.resolve(pg.dict["Contents"]), pg.dict["Contents"]) {
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

// refsOf lists the object numbers a page's Contents entry names: one
// reference, or an array of them.
func refsOf(resolved any, raw any) []int {
	var out []int
	add := func(v any) {
		if r, ok := v.(Ref); ok {
			out = append(out, r.Num)
		}
	}
	add(raw)
	if arr, ok := resolved.(Array); ok {
		for _, e := range arr {
			add(e)
		}
	}
	return out
}
