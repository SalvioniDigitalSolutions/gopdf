package gopdf

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// Embedded files.
//
// A PDF can carry other files inside it: the spreadsheet a table came
// from, the original of a scan, a signed original beside a flattened
// copy. They travel with the document, no page draws them, and nothing
// about looking at the pages reveals they are there.
//
// That makes them a feature and a hazard in the same breath. A library
// that redacts a document and leaves its attachments alone has removed
// the words from the page and left the source document sitting behind
// them, so they are worth being able to see, take out, and put in.
//
// They live in two places. The document's own list is a name tree under
// the catalog, and a file attachment annotation puts one on a page — the
// paperclip a reader clicks. Both are read here.

// Attachment is a file carried inside a document.
type Attachment struct {
	// Name is what the document calls the file.
	Name string
	// Description is the note attached to it, where there is one.
	Description string
	// MIMEType is the /Subtype the file specification declares.
	MIMEType string
	// Size is the declared length in bytes, which is not always the
	// actual length: Data is the authority.
	Size int
	// Created and Modified are the file's own dates, where given.
	Created, Modified time.Time
	// Page is the page a file attachment annotation sits on, or -1 for
	// one listed in the document's own collection.
	Page int

	r      *Reader
	stream *rawStream
	// ref names the object holding the file specification, which is what
	// removal has to find again.
	ref Ref
}

// Data returns the file's contents, decoded and decrypted.
func (a Attachment) Data() ([]byte, error) {
	if a.stream == nil {
		return nil, errors.New("gopdf: the attachment carries no data")
	}
	return a.r.decodeStream(a.stream.dict, a.stream.data)
}

// Attachments lists every file the document carries.
//
// The document's own collection comes first, in the order the name tree
// gives, followed by the ones attached to pages.
func (r *Reader) Attachments() []Attachment {
	var out []Attachment
	seen := make(map[int]bool)

	if names, ok := r.resolve(r.Catalog()["Names"]).(Dict); ok {
		for _, e := range r.nameTree(names["EmbeddedFiles"], 0) {
			if a, ok := r.attachment(e.value, -1); ok {
				if a.Name == "" {
					a.Name = e.key
				}
				markSeen(seen, e.value)
				out = append(out, a)
			}
		}
	}
	for page := 0; page < len(r.pages); page++ {
		annots, ok := r.resolve(r.pages[page].dict["Annots"]).(Array)
		if !ok {
			continue
		}
		for _, entry := range annots {
			d, ok := r.resolve(entry).(Dict)
			if !ok || r.resolve(d["Subtype"]) != Name("FileAttachment") {
				continue
			}
			if a, ok := r.attachment(d["FS"], page); ok {
				if ref, isRef := d["FS"].(Ref); isRef && seen[ref.Num] {
					continue // already listed in the collection
				}
				out = append(out, a)
			}
		}
	}
	return out
}

func markSeen(seen map[int]bool, v any) {
	if ref, ok := v.(Ref); ok {
		seen[ref.Num] = true
	}
}

// attachment reads one file specification.
func (r *Reader) attachment(v any, page int) (Attachment, bool) {
	spec, ok := r.resolve(v).(Dict)
	if !ok {
		return Attachment{}, false
	}
	ef, ok := r.resolve(spec["EF"]).(Dict)
	if !ok {
		return Attachment{}, false
	}
	// /F is the file itself; /UF and /DOS and the rest are the same file
	// named for other systems.
	var stm *rawStream
	for _, key := range []Name{"F", "UF", "DOS", "Mac", "Unix"} {
		if s, ok := r.resolve(ef[key]).(*rawStream); ok {
			stm = s
			break
		}
	}
	if stm == nil {
		return Attachment{}, false
	}
	a := Attachment{r: r, stream: stm, Page: page}
	if ref, ok := v.(Ref); ok {
		a.ref = ref
	}
	for _, key := range []Name{"UF", "F", "Desc"} {
		if s, ok := r.resolve(spec[key]).(String); ok && key != "Desc" && a.Name == "" {
			a.Name = decodeTextString(s)
		}
	}
	if s, ok := r.resolve(spec["Desc"]).(String); ok {
		a.Description = decodeTextString(s)
	}
	if s, ok := r.resolve(stm.dict["Subtype"]).(Name); ok {
		a.MIMEType = unescapeName(string(s))
	}
	if params, ok := r.resolve(stm.dict["Params"]).(Dict); ok {
		if n, ok := toInt(r.resolve(params["Size"])); ok {
			a.Size = n
		}
		if s, ok := r.resolve(params["CreationDate"]).(String); ok {
			a.Created = parsePDFDate(string(s))
		}
		if s, ok := r.resolve(params["ModDate"]).(String); ok {
			a.Modified = parsePDFDate(string(s))
		}
	}
	if a.Size == 0 {
		if n, ok := toInt(r.resolve(stm.dict["Length"])); ok {
			a.Size = n
		}
	}
	return a, true
}

// unescapeName turns a name's #xx escapes back into their characters,
// which a MIME type needs since it contains a slash.
func unescapeName(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '#' && i+2 < len(s) {
			hi, err1 := hexVal(s[i+1])
			lo, err2 := hexVal(s[i+2])
			if err1 == nil && err2 == nil {
				out = append(out, hi<<4|lo)
				i += 2
				continue
			}
		}
		out = append(out, s[i])
	}
	return string(out)
}

// nameTreeEntry is one name and the value it leads to.
type nameTreeEntry struct {
	key   string
	value any
}

// nameTree flattens a name tree into its entries, in order.
//
// A name tree is a balanced tree of /Kids with the leaves holding /Names
// arrays of alternating keys and values. Reading only the root's /Names
// finds the attachments of a small document and misses every one of a
// large document, which is the kind of difference that shows up late.
func (r *Reader) nameTree(v any, depth int) []nameTreeEntry {
	if depth > 32 {
		return nil
	}
	node, ok := r.resolve(v).(Dict)
	if !ok {
		return nil
	}
	var out []nameTreeEntry
	if arr, ok := r.resolve(node["Names"]).(Array); ok {
		for i := 0; i+1 < len(arr); i += 2 {
			key, _ := r.resolve(arr[i]).(String)
			out = append(out, nameTreeEntry{key: decodeTextString(key), value: arr[i+1]})
		}
	}
	for _, kid := range arrayOf(r, node["Kids"]) {
		out = append(out, r.nameTree(kid, depth+1)...)
	}
	return out
}

// --- writing ---

// pendingAttachment is a file waiting to be written into a document
// being built.
type pendingAttachment struct {
	name        string
	description string
	data        []byte
}

// Attach adds a file to a document being built. The name is what a
// reader will see and offer to save it as.
func (d *Document) Attach(name string, data []byte) error {
	return d.AttachWithDescription(name, "", data)
}

// AttachWithDescription adds a file to a document being built, along
// with the note that goes with it.
func (d *Document) AttachWithDescription(name, description string, data []byte) error {
	if name == "" {
		return errors.New("gopdf: an attachment needs a name")
	}
	d.attachments = append(d.attachments, pendingAttachment{
		name: name, description: description, data: append([]byte(nil), data...),
	})
	return nil
}

// buildAttachments turns the pending files into objects and returns the
// name tree that lists them, or nil if there are none.
//
// It runs before object numbers are handed out, since everything it adds
// goes into the same pool the rest of the document's copied objects use.
func (d *Document) buildAttachments() any {
	if len(d.attachments) == 0 {
		return nil
	}
	files := append([]pendingAttachment(nil), d.attachments...)
	sort.SliceStable(files, func(i, j int) bool { return files[i].name < files[j].name })

	flat := make(Array, 0, len(files)*2)
	for _, f := range files {
		stream := rawRef(len(d.raw))
		d.raw = append(d.raw, &rawStream{
			dict: Dict{
				"Type":   Name("EmbeddedFile"),
				"Params": Dict{"Size": int64(len(f.data))},
			},
			data: f.data,
		})
		spec := Dict{
			"Type": Name("Filespec"),
			"F":    String(textStringBytes(f.name)),
			"UF":   String(textStringBytes(f.name)),
			"EF":   Dict{"F": stream},
		}
		if f.description != "" {
			spec["Desc"] = String(textStringBytes(f.description))
		}
		specRef := rawRef(len(d.raw))
		d.raw = append(d.raw, spec)
		flat = append(flat, String(textStringBytes(f.name)), specRef)
	}
	tree := rawRef(len(d.raw))
	d.raw = append(d.raw, Dict{"Names": flat})
	return tree
}

// Attach adds a file to an existing document, appended incrementally.
func (u *Updater) Attach(name string, data []byte) error {
	return u.AttachWithDescription(name, "", data)
}

// AttachWithDescription adds a file and its note to an existing document.
func (u *Updater) AttachWithDescription(name, description string, data []byte) error {
	if name == "" {
		return errors.New("gopdf: an attachment needs a name")
	}
	spec := u.buildFilespec(name, description, data)

	// The collection is a name tree, and its entries are sorted by name.
	entries := u.currentAttachmentNames()
	entries = append(entries, nameTreeEntry{key: name, value: spec})
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].key < entries[j].key })

	flat := make(Array, 0, len(entries)*2)
	for _, e := range entries {
		flat = append(flat, String(textStringBytes(e.key)), e.value)
	}
	tree := u.AddObject(Dict{"Names": flat})

	return u.SetCatalogEntry("Names", u.namesWith("EmbeddedFiles", tree))
}

// currentAttachmentNames lists the collection as it stands, including
// anything an earlier call in this update already added.
//
// The pending catalog has to be consulted first. A document with no
// attachments has no /Names at all, so reading only what the file says
// finds nothing and the second Attach replaces the first — which is the
// kind of bug that looks like the tree is fine and the writing is
// broken.
func (u *Updater) currentAttachmentNames() []nameTreeEntry {
	return u.r.nameTree(u.pendingValue(u.attachmentTreeRef()), 0)
}

// attachmentTreeRef finds the embedded-file tree, preferring the version
// this update has already written over the one in the file.
func (u *Updater) attachmentTreeRef() any {
	cat := u.r.Catalog()
	if pending, ok := u.changed[refNumOr(u.r.trailer["Root"])].(Dict); ok {
		cat = pending
	}
	names, ok := u.r.resolve(u.pendingValue(cat["Names"])).(Dict)
	if !ok {
		return nil
	}
	return names["EmbeddedFiles"]
}

// pendingValue follows a reference to the value this update gave it, if
// it gave it one.
func (u *Updater) pendingValue(v any) any {
	ref, ok := v.(Ref)
	if !ok {
		return v
	}
	if pending, changed := u.changed[ref.Num]; changed {
		return pending
	}
	return v
}

// buildFilespec writes the stream and the specification that names it.
func (u *Updater) buildFilespec(name, description string, data []byte) Ref {
	params := Dict{"Size": int64(len(data))}
	stream := u.AddObject(NewStream(Dict{
		"Type": Name("EmbeddedFile"), "Params": params,
	}, data))
	spec := Dict{
		"Type": Name("Filespec"),
		"F":    String(textStringBytes(name)),
		"UF":   String(textStringBytes(name)),
		"EF":   Dict{"F": stream},
	}
	if description != "" {
		spec["Desc"] = String(textStringBytes(description))
	}
	return u.AddObject(spec)
}

// RemoveAttachments takes files out of a document.
//
// It returns how many it removed. The file specifications are emptied
// rather than merely unlinked from the collection, because an object a
// document no longer points at is still an object in the file: an
// incremental update appends, and what it stops referring to is still
// there to be found.
func (u *Updater) RemoveAttachments(drop func(Attachment) bool) (int, error) {
	list := u.r.Attachments()
	if len(list) == 0 {
		return 0, nil
	}
	removed := 0
	kept := make([]nameTreeEntry, 0, len(list))
	dropped := make(map[int]bool)

	for _, e := range u.r.nameTree(u.pendingValue(u.attachmentTreeRef()), 0) {
		a, ok := u.r.attachment(e.value, -1)
		if !ok {
			kept = append(kept, e)
			continue
		}
		if a.Name == "" {
			a.Name = e.key
		}
		if drop != nil && !drop(a) {
			kept = append(kept, e)
			continue
		}
		removed++
		markDropped(dropped, e.value)
	}

	// The page annotations that carry their own file.
	for page := 0; page < len(u.r.pages); page++ {
		annots, ok := u.r.resolve(u.r.pages[page].dict["Annots"]).(Array)
		if !ok {
			continue
		}
		var keep Array
		changed := false
		for _, entry := range annots {
			d, ok := u.r.resolve(entry).(Dict)
			if !ok || u.r.resolve(d["Subtype"]) != Name("FileAttachment") {
				keep = append(keep, entry)
				continue
			}
			a, ok := u.r.attachment(d["FS"], page)
			if !ok || (drop != nil && !drop(a)) {
				keep = append(keep, entry)
				continue
			}
			removed++
			changed = true
			markDropped(dropped, d["FS"])
		}
		if changed {
			if err := u.SetPageEntry(page, "Annots", keep); err != nil {
				return removed, err
			}
		}
	}
	if removed == 0 {
		return 0, nil
	}

	// Emptying the specifications is what actually removes the bytes.
	for num := range dropped {
		if err := u.SetObject(Ref{Num: num}, Dict{"Type": Name("Filespec")}); err != nil {
			return removed, fmt.Errorf("gopdf: removing attachment %d: %w", num, err)
		}
	}
	flat := make(Array, 0, len(kept)*2)
	for _, e := range kept {
		flat = append(flat, String(textStringBytes(e.key)), e.value)
	}
	var tree any
	if len(flat) > 0 {
		tree = u.AddObject(Dict{"Names": flat})
	}
	names := u.namesWith("EmbeddedFiles", tree)
	if len(names) == 0 {
		return removed, u.SetCatalogEntry("Names", nil)
	}
	return removed, u.SetCatalogEntry("Names", names)
}

func markDropped(dropped map[int]bool, v any) {
	if ref, ok := v.(Ref); ok {
		dropped[ref.Num] = true
	}
}

// namesWith returns the catalog's /Names dictionary with one entry set,
// or removed when the value is nil, built on whatever this update has
// already written there.
func (u *Updater) namesWith(key Name, v any) Dict {
	cat := u.r.Catalog()
	if pending, ok := u.changed[refNumOr(u.r.trailer["Root"])].(Dict); ok {
		cat = pending
	}
	names := Dict{}
	if existing, ok := u.r.resolve(u.pendingValue(cat["Names"])).(Dict); ok {
		names = existing.Clone()
	}
	if v == nil {
		delete(names, key)
	} else {
		names[key] = v
	}
	return names
}

// refNumOr is the object number a value references, or zero.
func refNumOr(v any) int {
	if ref, ok := v.(Ref); ok {
		return ref.Num
	}
	return 0
}
