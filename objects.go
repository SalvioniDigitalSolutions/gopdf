package gopdf

import (
	"errors"
	"fmt"
	"sort"
)

// Direct access to the object graph.
//
// Everything else in this package is a considered opinion about what a
// PDF is for: pages, text, forms, signatures. This file is the admission
// that the opinions are not exhaustive. A PDF is a graph of dictionaries,
// and a library that only ever hands back its own summary of that graph
// leaves its caller stuck the first time they meet a file that does
// something the summary has no word for.
//
// So the graph is reachable. Read any object, walk from the trailer to
// wherever the file leads, and write objects back. Nothing here checks
// that what you write makes sense — that is the point of an escape
// hatch — but everything here goes through the same encryption,
// decoding, and cross-reference machinery the rest of the package uses,
// so what you read is what the file says and what you write is a valid
// object.

// Stream is a stream object: a dictionary and the bytes that follow it.
//
// The bytes are held as the file stores them, still encoded. Data
// decodes them; Raw hands them back untouched, which is what you want
// when copying a stream from one file to another without paying to
// decompress and recompress it.
type Stream struct {
	// Dict is the stream's dictionary, including /Filter and
	// /DecodeParms. It is the reader's own dictionary: copy it with
	// Clone before changing anything.
	Dict Dict

	r   *Reader
	raw []byte
}

// Data returns the stream's decoded contents.
func (s *Stream) Data() ([]byte, error) {
	if s == nil {
		return nil, errors.New("gopdf: no stream")
	}
	if s.r == nil {
		return append([]byte(nil), s.raw...), nil
	}
	return s.r.decodeStream(s.Dict, s.raw)
}

// Raw returns the stream's bytes exactly as the file stores them, still
// encoded by whatever /Filter the dictionary names.
func (s *Stream) Raw() []byte {
	if s == nil {
		return nil
	}
	return append([]byte(nil), s.raw...)
}

// NewStream builds a stream from a dictionary and its already-encoded
// bytes, for handing to Updater.AddObject. The dictionary should name
// whatever /Filter the bytes are in, and should not carry a /Length: the
// writer sets it from the data it actually writes.
func NewStream(d Dict, encoded []byte) *Stream {
	if d == nil {
		d = Dict{}
	}
	return &Stream{Dict: d, raw: encoded}
}

// Clone copies a dictionary one level deep.
//
// Everything the reader hands back is the reader's own. Change it in
// place and you change what the reader sees for the rest of its life,
// which is rarely what anyone means. Clone first, change the copy, and
// write the copy back with Updater.SetObject.
func (d Dict) Clone() Dict {
	if d == nil {
		return nil
	}
	out := make(Dict, len(d))
	for k, v := range d {
		out[k] = v
	}
	return out
}

// Clone copies an array one level deep. See Dict.Clone.
func (a Array) Clone() Array {
	if a == nil {
		return nil
	}
	return append(Array(nil), a...)
}

// Resolve follows an indirect reference to the object it names, and
// returns anything else unchanged. Use it on every value read out of a
// dictionary: a PDF may write any value indirectly, so a key that holds
// a number in one file holds a reference to a number in the next.
//
// A stream comes back as *Stream. Everything else comes back as one of
// Name, String, Array, Dict, int64, float64, bool, or nil.
func (r *Reader) Resolve(v any) any {
	return r.public(r.resolve(v))
}

// public converts an internal value to the form callers see. The reader
// travels with a stream, since decoding one needs the file it came from:
// /DecodeParms can reference other objects, and the bytes may be
// encrypted.
func (r *Reader) public(v any) any {
	if s, ok := v.(*rawStream); ok {
		return &Stream{Dict: s.dict, r: r, raw: s.data}
	}
	return v
}

// Object returns the object a reference names, or nil if the file has no
// such object. Resolve is usually what you want; Object is for walking
// the file by number.
func (r *Reader) Object(ref Ref) any {
	v, err := r.object(ref.Num)
	if err != nil {
		return nil
	}
	if s, ok := v.(*rawStream); ok {
		return &Stream{Dict: s.dict, r: r, raw: s.data}
	}
	return v
}

// Trailer returns the document's trailer dictionary, merged across the
// cross-reference chain so that /Root, /Info and /Encrypt are present
// wherever the file put them.
func (r *Reader) Trailer() Dict { return r.trailer }

// Catalog returns the document catalog, the root of the object graph.
func (r *Reader) Catalog() Dict {
	d, _ := r.resolve(r.trailer["Root"]).(Dict)
	return d
}

// PageDict returns a page's dictionary, or nil if there is no such page.
// Inherited attributes are not merged in: /Resources, /MediaBox and
// /Rotate may live on an ancestor node, which is what InheritedPageValue
// is for.
func (r *Reader) PageDict(index int) Dict {
	if index < 0 || index >= len(r.pages) {
		return nil
	}
	return r.pages[index].dict
}

// PageRef returns the reference naming a page's dictionary. The second
// result is false for the rare file that writes a page inline rather
// than as an indirect object.
func (r *Reader) PageRef(index int) (Ref, bool) {
	num, ok := r.pageObjectNumber(index)
	if !ok {
		return Ref{}, false
	}
	return Ref{Num: num}, true
}

// InheritedPageValue looks a key up on a page and then on its ancestors
// in the page tree, which is where /Resources, /MediaBox, /CropBox and
// /Rotate are allowed to live.
func (r *Reader) InheritedPageValue(index int, key Name) any {
	page := r.PageDict(index)
	if page == nil {
		return nil
	}
	seen := 0
	for node := page; node != nil; seen++ {
		if v, ok := node[key]; ok {
			return r.public(r.resolve(v))
		}
		if seen > 64 { // a page tree that loops is not one to follow
			return nil
		}
		node, _ = r.resolve(node["Parent"]).(Dict)
	}
	return nil
}

// Objects lists every object the file defines, in numeric order. The
// list includes objects nothing points at, which is how you find what a
// document is carrying that its page tree never mentions.
func (r *Reader) Objects() []Ref {
	out := make([]Ref, 0, len(r.xref))
	for num, e := range r.xref {
		if !e.inObjStm && e.offset < 0 {
			continue // a freed entry names no object
		}
		out = append(out, Ref{Num: num})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Num < out[j].Num })
	return out
}

// Walk visits every object reachable from the trailer, depth first,
// calling fn with the reference that named it and the object itself.
// Each object is visited once however many times it is referenced, so a
// shared font or image arrives one time only. Returning false from fn
// stops the walk.
//
// Values found inline rather than behind a reference are visited with a
// zero Ref, since they have no number of their own.
func (r *Reader) Walk(fn func(ref Ref, obj any) bool) {
	seen := make(map[int]bool)
	stop := false
	var visit func(ref Ref, v any)
	visit = func(ref Ref, v any) {
		if stop {
			return
		}
		if inner, ok := v.(Ref); ok {
			if seen[inner.Num] {
				return
			}
			seen[inner.Num] = true
			visit(inner, r.Object(inner))
			return
		}
		if v == nil {
			return
		}
		if !fn(ref, v) {
			stop = true
			return
		}
		switch t := v.(type) {
		case Dict:
			for _, k := range sortedKeys(t) {
				visit(Ref{}, t[k])
			}
		case Array:
			for _, e := range t {
				visit(Ref{}, e)
			}
		case *Stream:
			for _, k := range sortedKeys(t.Dict) {
				visit(Ref{}, t.Dict[k])
			}
		}
	}
	visit(Ref{}, r.trailer)
}

// --- writing ---

// Reader returns the document the update is built on.
func (u *Updater) Reader() *Reader { return u.r }

// AddObject writes a brand-new indirect object into the update and
// returns the reference naming it. The value may be any of Name, String,
// Array, Dict, Ref, a number, a bool, or a *Stream.
func (u *Updater) AddObject(v any) Ref {
	return Ref{Num: u.add(internal(v))}
}

// SetObject replaces an existing object. The reference must name an
// object the file already defines, or one AddObject returned; anything
// else is an error rather than a silent hole in the document.
func (u *Updater) SetObject(ref Ref, v any) error {
	if ref.Num <= 0 {
		return fmt.Errorf("gopdf: object %d is not a valid number", ref.Num)
	}
	if ref.Num >= u.nextID {
		if _, pending := u.changed[ref.Num]; !pending {
			return fmt.Errorf("gopdf: object %d is not in the document", ref.Num)
		}
	}
	u.set(ref.Num, internal(v))
	return nil
}

// internal converts a caller's value to the form the writer expects.
func internal(v any) any {
	if s, ok := v.(*Stream); ok {
		return &rawStream{dict: s.Dict, data: s.raw}
	}
	return v
}

// SetCatalogEntry sets one key on the document catalog, adding the
// catalog to the update. It saves the read-clone-write dance for the
// common case of switching something on at the top of a document.
func (u *Updater) SetCatalogEntry(key Name, v any) error {
	num, ok := refNum(u.r.trailer["Root"])
	if !ok {
		return errors.New("gopdf: the document has no catalog to change")
	}
	cat := u.r.Catalog()
	if cat == nil {
		return errors.New("gopdf: the document has no catalog to change")
	}
	if pending, ok := u.changed[num].(Dict); ok {
		cat = pending
	} else {
		cat = cat.Clone()
	}
	if v == nil {
		delete(cat, key)
	} else {
		cat[key] = internal(v)
	}
	u.set(num, cat)
	return nil
}

// SetPageEntry sets one key on a page's dictionary.
func (u *Updater) SetPageEntry(index int, key Name, v any) error {
	num, ok := u.r.pageObjectNumber(index)
	if !ok {
		return fmt.Errorf("gopdf: page %d is not an indirect object", index)
	}
	page := u.pageDict(num, index)
	if page == nil {
		return fmt.Errorf("gopdf: page %d out of range", index)
	}
	d := page.Clone()
	if v == nil {
		delete(d, key)
	} else {
		d[key] = internal(v)
	}
	u.set(num, d)
	return nil
}

// refNum returns the object number a value references.
func refNum(v any) (int, bool) {
	ref, ok := v.(Ref)
	if !ok {
		return 0, false
	}
	return ref.Num, true
}
