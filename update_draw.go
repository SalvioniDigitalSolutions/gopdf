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

// anyDrawing reports whether any page of the update has new content.
func (u *Updater) anyDrawing() bool {
	for _, p := range u.pages {
		if p.hasDrawing() {
			return true
		}
	}
	return false
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
	r := p.u.r
	merged := Dict{}
	if src, ok := r.resolve(p.sourceResources).(Dict); ok {
		for k, v := range src {
			merged[k] = v
		}
	}
	for _, cat := range resourceCategories {
		ours := rs.entryDict(p.Page.resPrefix, cat)
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
	if _, ok := merged["ProcSet"]; !ok {
		merged["ProcSet"] = Array{
			Name("PDF"), Name("Text"), Name("ImageB"), Name("ImageC"),
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
