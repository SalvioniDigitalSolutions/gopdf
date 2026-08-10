package gopdf

import (
	"fmt"
	"strings"
)

// AnnotType classifies an annotation.
type AnnotType string

const (
	AnnotText      AnnotType = "Text" // a sticky note
	AnnotLink      AnnotType = "Link"
	AnnotHighlight AnnotType = "Highlight"
	AnnotUnderline AnnotType = "Underline"
	AnnotStrikeOut AnnotType = "StrikeOut"
	AnnotSquare    AnnotType = "Square"
	AnnotCircle    AnnotType = "Circle"
	AnnotFreeText  AnnotType = "FreeText"
	AnnotWidget    AnnotType = "Widget" // a form field control
	AnnotPopup     AnnotType = "Popup"
	AnnotOther     AnnotType = "Other"
)

// Annotation describes an annotation found on a page.
type Annotation struct {
	// Type is the annotation's subtype.
	Type AnnotType
	// Rect is its rectangle in points, from the top-left of the page.
	Rect [4]float64
	// Contents is the note text, where the annotation has any.
	Contents string
	// Author is the /T entry, the name shown as the note's author.
	Author string
	// Color is the annotation's colour, if it declares one.
	Color *Color
	// URL is a link annotation's destination.
	URL string
	// Page is the 0-based index of the page it belongs to.
	Page int

	ref Ref // the source object, for removal
}

// Annotations lists the annotations on a page, in the order the file
// stores them.
func (r *Reader) Annotations(page int) ([]Annotation, error) {
	if page < 0 || page >= len(r.pages) {
		return nil, fmt.Errorf("gopdf: page %d out of range", page)
	}
	arr, _ := r.resolve(r.pages[page].dict["Annots"]).(Array)
	out := make([]Annotation, 0, len(arr))
	for _, entry := range arr {
		d, ok := r.resolve(entry).(Dict)
		if !ok {
			continue
		}
		a := Annotation{Page: page, Type: AnnotOther}
		if sub, ok := r.resolve(d["Subtype"]).(Name); ok {
			switch AnnotType(sub) {
			case AnnotText, AnnotLink, AnnotHighlight, AnnotUnderline,
				AnnotStrikeOut, AnnotSquare, AnnotCircle, AnnotFreeText,
				AnnotWidget, AnnotPopup:
				a.Type = AnnotType(sub)
			}
		}
		a.Rect = topLeftRect(r, rectOf(r, d["Rect"]), page)
		if s, ok := r.resolve(d["Contents"]).(String); ok {
			a.Contents = decodeTextString(s)
		}
		if s, ok := r.resolve(d["T"]).(String); ok {
			a.Author = decodeTextString(s)
		}
		if c := colorFromArray(r, d["C"]); c != nil {
			a.Color = c
		}
		if action, ok := r.resolve(d["A"]).(Dict); ok {
			if r.resolve(action["S"]) == Name("URI") {
				if s, ok := r.resolve(action["URI"]).(String); ok {
					a.URL = string(s)
				}
			}
		}
		if ref, ok := entry.(Ref); ok {
			a.ref = ref
		}
		out = append(out, a)
	}
	return out, nil
}

// colorFromArray reads a /C entry, which may be grey, RGB or CMYK.
func colorFromArray(r *Reader, v any) *Color {
	arr, ok := r.resolve(v).(Array)
	if !ok {
		return nil
	}
	comp := make([]float64, 0, len(arr))
	for _, e := range arr {
		f, ok := toFloat(r.resolve(e))
		if !ok {
			return nil
		}
		comp = append(comp, clamp01(f))
	}
	to8 := func(f float64) uint8 { return uint8(f*255 + 0.5) }
	switch len(comp) {
	case 1:
		return &Color{to8(comp[0]), to8(comp[0]), to8(comp[0])}
	case 3:
		return &Color{to8(comp[0]), to8(comp[1]), to8(comp[2])}
	case 4:
		// CMYK to RGB, the usual naive conversion.
		c, m, y, k := comp[0], comp[1], comp[2], comp[3]
		return &Color{
			to8((1 - c) * (1 - k)),
			to8((1 - m) * (1 - k)),
			to8((1 - y) * (1 - k)),
		}
	}
	return nil
}

// --- creating annotations ---

// NoteOptions configures an annotation being added to a page.
type NoteOptions struct {
	// Author is shown as the note's author in a viewer's comment list.
	Author string
	// Color tints the annotation; the zero value picks a sensible default
	// for the annotation's kind.
	Color Color
	// Opacity is the annotation's constant alpha, from 0 to 1. Zero means
	// the default for the kind.
	Opacity float64
	// Open shows a sticky note's popup immediately.
	Open bool
}

// pendingAnnot is an annotation waiting to be written, with the optional
// appearance stream that makes it render identically everywhere.
type pendingAnnot struct {
	dict Dict
	// ap is the appearance stream's content; nil leaves the viewer to
	// draw the annotation itself, which is right for sticky notes.
	ap   []byte
	bbox [4]float64
	// apResources is the appearance stream's own resource dictionary,
	// self-contained so the annotation needs no external objects.
	apResources Dict

	num, apNum int // assigned at write time
}

// annotBase builds the entries every annotation shares.
func annotBase(sub AnnotType, rect [4]float64, contents string, opts NoteOptions, c Color) Dict {
	d := Dict{
		"Type":    Name("Annot"),
		"Subtype": Name(string(sub)),
		"Rect":    Array{rect[0], rect[1], rect[2], rect[3]},
		"F":       int64(4), // print
		"C": Array{
			float64(c.R) / 255, float64(c.G) / 255, float64(c.B) / 255,
		},
	}
	if contents != "" {
		d["Contents"] = String(textStringBytes(contents))
	}
	if opts.Author != "" {
		d["T"] = String(textStringBytes(opts.Author))
	}
	if opts.Opacity > 0 && opts.Opacity < 1 {
		d["CA"] = opts.Opacity
	}
	return d
}

// quadPoints describes the region a text-markup annotation covers, in the
// corner order the specification requires.
func quadPoints(r [4]float64) Array {
	return Array{
		r[0], r[3], r[2], r[3],
		r[0], r[1], r[2], r[1],
	}
}

// markupAppearance draws a text-markup annotation into its own bounding
// box. The blend mode and alpha live in the appearance's own resource
// dictionary, so the annotation needs no external objects.
func markupAppearance(sub AnnotType, w, h float64, c Color, alpha float64) []byte {
	var b strings.Builder
	b.WriteString("/GS gs\n")
	fmt.Fprintf(&b, "%s rg\n", c.components())
	switch sub {
	case AnnotUnderline:
		fmt.Fprintf(&b, "0 0 %s %s re f\n", fl(w), fl(h*0.08+0.5))
	case AnnotStrikeOut:
		fmt.Fprintf(&b, "0 %s %s %s re f\n", fl(h*0.45), fl(w), fl(h*0.08+0.5))
	default: // Highlight
		fmt.Fprintf(&b, "0 0 %s %s re f\n", fl(w), fl(h))
	}
	_ = alpha
	return []byte(b.String())
}

// markupResources is the self-contained resource dictionary a markup
// appearance needs: one graphics state carrying its blend mode and alpha.
func markupResources(sub AnnotType, alpha float64) Dict {
	gs := Dict{"Type": Name("ExtGState"), "ca": alpha, "CA": alpha}
	if sub == AnnotHighlight {
		// Multiply keeps the text underneath legible.
		gs["BM"] = Name("Multiply")
	}
	return Dict{"ExtGState": Dict{"GS": gs}}
}

// addAnnotation is the shared entry point for the page-level helpers.
func addAnnotation(rect [4]float64, sub AnnotType, contents string,
	opts NoteOptions, defColor Color, defAlpha float64) *pendingAnnot {

	c := opts.Color
	if c == (Color{}) {
		c = defColor
	}
	alpha := opts.Opacity
	if alpha <= 0 || alpha > 1 {
		alpha = defAlpha
	}
	d := annotBase(sub, rect, contents, opts, c)
	pa := &pendingAnnot{dict: d}

	switch sub {
	case AnnotHighlight, AnnotUnderline, AnnotStrikeOut:
		d["QuadPoints"] = quadPoints(rect)
		w, h := rect[2]-rect[0], rect[3]-rect[1]
		pa.ap = markupAppearance(sub, w, h, c, alpha)
		pa.bbox = [4]float64{0, 0, w, h}
		pa.apResources = markupResources(sub, alpha)
	case AnnotText:
		d["Name"] = Name("Comment")
		if opts.Open {
			d["Open"] = true
		}
	case AnnotSquare, AnnotCircle:
		w, h := rect[2]-rect[0], rect[3]-rect[1]
		var b strings.Builder
		fmt.Fprintf(&b, "/GS gs\n%s RG 1 w\n", c.components())
		if sub == AnnotCircle {
			k := 0.5522847498
			cx, cy, rx, ry := w/2, h/2, w/2-0.5, h/2-0.5
			fmt.Fprintf(&b, "%s %s m\n", fl(cx+rx), fl(cy))
			fmt.Fprintf(&b, "%s %s %s %s %s %s c\n", fl(cx+rx), fl(cy+ry*k), fl(cx+rx*k), fl(cy+ry), fl(cx), fl(cy+ry))
			fmt.Fprintf(&b, "%s %s %s %s %s %s c\n", fl(cx-rx*k), fl(cy+ry), fl(cx-rx), fl(cy+ry*k), fl(cx-rx), fl(cy))
			fmt.Fprintf(&b, "%s %s %s %s %s %s c\n", fl(cx-rx), fl(cy-ry*k), fl(cx-rx*k), fl(cy-ry), fl(cx), fl(cy-ry))
			fmt.Fprintf(&b, "%s %s %s %s %s %s c S\n", fl(cx+rx*k), fl(cy-ry), fl(cx+rx), fl(cy-ry*k), fl(cx+rx), fl(cy))
		} else {
			fmt.Fprintf(&b, "0.5 0.5 %s %s re S\n", fl(w-1), fl(h-1))
		}
		pa.ap = []byte(b.String())
		pa.bbox = [4]float64{0, 0, w, h}
		pa.apResources = markupResources(sub, alpha)
	}
	return pa
}

// Default colours for each annotation kind.
var (
	defaultHighlight = Color{255, 235, 60}
	defaultNote      = Color{255, 210, 70}
	defaultMarkup    = Color{220, 50, 50}
)

// AddHighlight marks a rectangle as highlighted. Position it over the text
// you want to mark — Reader.PageText and the editing API report where text
// sits.
func (p *Page) AddHighlight(x, y, w, h float64, contents string, opts NoteOptions) {
	p.annots = append(p.annots, addAnnotation(p.pdfRect(x, y, w, h),
		AnnotHighlight, contents, opts, defaultHighlight, 0.4))
}

// AddUnderline draws an underline annotation across a rectangle.
func (p *Page) AddUnderline(x, y, w, h float64, contents string, opts NoteOptions) {
	p.annots = append(p.annots, addAnnotation(p.pdfRect(x, y, w, h),
		AnnotUnderline, contents, opts, defaultMarkup, 1))
}

// AddStrikeOut draws a strike-through annotation across a rectangle.
func (p *Page) AddStrikeOut(x, y, w, h float64, contents string, opts NoteOptions) {
	p.annots = append(p.annots, addAnnotation(p.pdfRect(x, y, w, h),
		AnnotStrikeOut, contents, opts, defaultMarkup, 1))
}

// AddNote attaches a sticky note at a point. Viewers draw their own icon
// and show the text when it is opened.
func (p *Page) AddNote(x, y float64, contents string, opts NoteOptions) {
	const icon = 20
	p.annots = append(p.annots, addAnnotation(p.pdfRect(x, y, icon, icon),
		AnnotText, contents, opts, defaultNote, 1))
}

// AddSquareAnnotation outlines a rectangle as an annotation, which stays
// selectable and removable rather than becoming part of the page.
func (p *Page) AddSquareAnnotation(x, y, w, h float64, contents string, opts NoteOptions) {
	p.annots = append(p.annots, addAnnotation(p.pdfRect(x, y, w, h),
		AnnotSquare, contents, opts, defaultMarkup, 1))
}

// AddCircleAnnotation outlines an ellipse as an annotation.
func (p *Page) AddCircleAnnotation(x, y, w, h float64, contents string, opts NoteOptions) {
	p.annots = append(p.annots, addAnnotation(p.pdfRect(x, y, w, h),
		AnnotCircle, contents, opts, defaultMarkup, 1))
}

// writeAnnotation emits an annotation and, when it has one, the
// appearance stream that renders it.
func writeAnnotation(ow *offsetWriter, ctx *writeCtx, a *pendingAnnot,
	beginObj func(int), endObj func(), compress bool) error {

	beginObj(a.num)
	ow.str("<<")
	for _, k := range sortedKeys(a.dict) {
		ow.str(" ")
		writeName(ow, k)
		ow.str(" ")
		writeValue(ow, a.dict[k], ctx)
	}
	if a.ap != nil {
		ow.printf(" /AP << /N %d 0 R >>", a.apNum)
	}
	ow.str(" >>\n")
	endObj()

	if a.ap == nil {
		return nil
	}
	beginObj(a.apNum)
	var extra strings.Builder
	fmt.Fprintf(&extra, "/Type /XObject /Subtype /Form /BBox [%s %s %s %s] ",
		fl(a.bbox[0]), fl(a.bbox[1]), fl(a.bbox[2]), fl(a.bbox[3]))
	if a.apResources != nil {
		extra.WriteString("/Resources ")
		writeValue(&extra, a.apResources, ctx)
		extra.WriteString(" ")
	}
	if err := ow.writeStream(extra.String(), a.ap, compress); err != nil {
		return err
	}
	endObj()
	return nil
}

// linkAnnotDict builds the dictionary for a link the drawing API created.
func linkAnnotDict(p *Page, l link) Dict {
	rect := p.pdfRect(l.x, l.y, l.w, l.h)
	d := Dict{
		"Type":    Name("Annot"),
		"Subtype": Name("Link"),
		"Rect":    Array{rect[0], rect[1], rect[2], rect[3]},
		"Border":  Array{int64(0), int64(0), int64(0)},
	}
	if l.url != "" {
		d["A"] = Dict{"S": Name("URI"), "URI": String(l.url)}
	}
	return d
}
