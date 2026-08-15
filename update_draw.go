package gopdf

import (
	"image"
	"io"
)

// Drawing new content onto a page during an incremental update.
//
// An UpdatablePage embeds a *Page, so the whole drawing API works on it.
// What is drawn becomes an additional content stream appended to the
// page's /Contents, and the resources it needs are merged into the page's
// resource dictionary under a prefix that cannot collide with the names
// the source file already uses. The page's original content stream is not
// touched unless its text was also edited.

// scratch returns the document that owns resources created for drawing.
// Its pages are never serialized; it exists only to register fonts,
// images, graphics states and gradients.
func (u *Updater) scratch() *Document {
	if u.res == nil {
		u.res = New()
	}
	return u.res
}

// SetCompress controls whether content and resource streams added by the
// update are compressed. It defaults to true; turn it off to inspect the
// appended objects.
func (u *Updater) SetCompress(on bool) {
	u.scratch().Compress = on
}

// newDrawingPage builds the Page that drawing calls target, matching the
// source page's geometry so coordinates mean the same thing as they do
// everywhere else in this package.
func (u *Updater) newDrawingPage(pi pageInfo, resources Dict) *Page {
	box := [4]float64{pi.mediaBox[0], pi.mediaBox[1], pi.mediaBox[2], pi.mediaBox[3]}
	return &Page{
		doc:       u.scratch(),
		w:         box[2] - box[0],
		h:         box[3] - box[1],
		state:     newGstate(),
		mediaBox:  &box,
		resPrefix: freeResourcePrefix(resources),
	}
}

// hasDrawing reports whether anything was drawn on this page.
func (p *UpdatablePage) hasDrawing() bool {
	return p.Page != nil && p.Page.buf.Len() > 0
}

// needsResources reports whether the update created any drawing resource
// — a font, image, graphics state or gradient — whether through drawing
// on a page or through restyling existing text.
func (u *Updater) needsResources() bool {
	d := u.res
	if d == nil {
		return false
	}
	return len(d.fonts)+len(d.images)+len(d.alphas)+len(d.shadings)+len(d.xobjects) > 0
}

// drawnContent returns the content stream for what was drawn, wrapped so
// it cannot inherit graphics state left behind by the page's own
// operators — content streams of a page are concatenated before
// interpretation, so state does carry across them.
func (p *UpdatablePage) drawnContent() []byte {
	body := p.Page.buf.Bytes()
	out := make([]byte, 0, len(body)+8)
	out = append(out, "q\n"...)
	out = append(out, body...)
	out = append(out, "\nQ\n"...)
	return out
}

// mergedResources builds the page's resource dictionary: the source's own
// entries, with this update's resources added under the page's prefix.
//
// Categories are resolved to direct dictionaries so the result is
// self-contained; the original resource objects stay in the file,
// unreferenced but untouched.
func (p *UpdatablePage) mergedResources(rs *resourceSet) Dict {
	return mergeResourceDict(p.u.r, p.sourceResources, rs, p.Page.resPrefix, true)
}

// mergeAdoptedForms gives every form XObject this update rewrote the
// resources the update added.
//
// An edit inside a form writes its operators into the form's own stream,
// and a font added for that edit was registered against the page. The
// two are different resource dictionaries: the form names /GpF1, the
// page is where /GpF1 was declared, and a reader resolving the name
// against the form finds nothing and refuses to draw the text. Poppler
// says "Unknown font tag" and stops; this package's own reader is more
// forgiving, which is why the file looked fine from the inside.
func (u *Updater) mergeAdoptedForms(rs *resourceSet, prefix string) {
	if rs == nil {
		return
	}
	for num, v := range u.changed {
		stm, ok := v.(*rawStream)
		if !ok || u.r.resolve(stm.dict["Subtype"]) != Name("Form") {
			continue
		}
		stm.dict["Resources"] = mergeResourceDict(u.r, stm.dict["Resources"],
			rs, prefix, false)
		_ = num
	}
}

// mergeResourceDict combines a source resource dictionary with the
// resources an update added.
func mergeResourceDict(r *Reader, source any, rs *resourceSet,
	prefix string, addProcSet bool) Dict {

	merged := Dict{}
	if src, ok := r.resolve(source).(Dict); ok {
		for k, v := range src {
			merged[k] = v
		}
	}
	for _, cat := range resourceCategories {
		ours := rs.entryDict(prefix, cat)
		if len(ours) == 0 {
			continue
		}
		combined := Dict{}
		if existing, ok := r.resolve(merged[Name(cat)]).(Dict); ok {
			for k, v := range existing {
				combined[k] = v
			}
		}
		for k, v := range ours {
			combined[k] = v
		}
		merged[Name(cat)] = combined
	}
	if addProcSet {
		if _, ok := merged["ProcSet"]; !ok {
			merged["ProcSet"] = Array{
				Name("PDF"), Name("Text"), Name("ImageB"), Name("ImageC"),
			}
		}
	}
	return merged
}

// contentsWith returns the page's /Contents value with an extra stream
// appended, preserving whatever the page already had.
//
// base may reference an object this update just created, which the reader
// knows nothing about, so it is examined directly rather than resolved
// first — resolving would report it as missing and silently drop it.
func (p *UpdatablePage) contentsWith(base any, extra Ref) any {
	switch t := base.(type) {
	case nil:
		return extra
	case Array:
		return append(append(Array{}, t...), extra)
	case Ref:
		// An existing /Contents may itself be an array of streams; a
		// reference to a stream, or to an object this update created,
		// simply becomes the first element.
		if arr, ok := p.u.r.resolve(t).(Array); ok {
			return append(append(Array{}, arr...), extra)
		}
		return Array{t, extra}
	default:
		return Array{base, extra}
	}
}

// AddImage registers an image for drawing onto updated pages, mirroring
// Document.AddImage.
func (u *Updater) AddImage(m image.Image) (*Image, error) {
	return u.scratch().AddImage(m)
}

// AddImageFile registers an image file (JPEG, PNG or GIF) for drawing
// onto updated pages.
func (u *Updater) AddImageFile(path string) (*Image, error) {
	return u.scratch().AddImageFile(path)
}

// AddImageReader registers an image read from r for drawing onto updated
// pages.
func (u *Updater) AddImageReader(r io.Reader) (*Image, error) {
	return u.scratch().AddImageReader(r)
}

// --- annotations on an updated page ---

// hasAnnots reports whether annotations were added to this page.
func (p *UpdatablePage) hasAnnots() bool {
	return p.Page != nil && (len(p.Page.annots) > 0 || len(p.Page.links) > 0)
}

// annotObjects writes the page's new annotations as appended objects and
// returns references to them.
func (u *Updater) annotObjects(p *UpdatablePage) Array {
	var refs Array
	for _, l := range p.Page.links {
		refs = append(refs, Ref{Num: u.add(linkAnnotDict(p.Page, l))})
	}
	for _, a := range p.Page.annots {
		dict := cloneDict(a.dict)
		if a.ap != nil {
			stream := &rawStream{
				dict: Dict{
					"Type": Name("XObject"), "Subtype": Name("Form"),
					"BBox": Array{a.bbox[0], a.bbox[1], a.bbox[2], a.bbox[3]},
				},
				data: a.ap,
			}
			if a.apResources != nil {
				stream.dict["Resources"] = a.apResources
			}
			dict["AP"] = Dict{"N": Ref{Num: u.add(stream)}}
		}
		refs = append(refs, Ref{Num: u.add(dict)})
	}
	return refs
}

// existingAnnots returns the page's current /Annots entries, minus any the
// update has marked for removal.
func (p *UpdatablePage) existingAnnots() Array {
	arr, _ := p.u.r.resolve(p.u.pageDict(p.pageNum, p.index)["Annots"]).(Array)
	if len(p.removed) == 0 {
		return append(Array{}, arr...)
	}
	out := make(Array, 0, len(arr))
	for _, e := range arr {
		if ref, ok := e.(Ref); ok && p.removed[ref.Num] {
			continue
		}
		out = append(out, e)
	}
	return out
}

// RemoveAnnotations drops the annotations on a page for which drop
// returns true, and reports how many were removed. The annotation objects
// stay in the file; the page simply stops referencing them.
func (u *Updater) RemoveAnnotations(pageIndex int, drop func(Annotation) bool) (int, error) {
	page, err := u.Page(pageIndex)
	if err != nil {
		return 0, err
	}
	annots, err := u.r.Annotations(pageIndex)
	if err != nil {
		return 0, err
	}
	if page.removed == nil {
		page.removed = make(map[int]bool)
	}
	n := 0
	for _, a := range annots {
		if a.ref.Num == 0 || !drop(a) {
			continue
		}
		page.removed[a.ref.Num] = true
		n++
	}
	return n, nil
}
