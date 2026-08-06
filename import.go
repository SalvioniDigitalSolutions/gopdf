package gopdf

import (
	"fmt"
)

// formXObject is an imported page wrapped as a Form XObject: its original
// content and resources stay intact in their own namespace, so overlay
// drawing can never collide with the source page's resource names.
type formXObject struct {
	bbox      [4]float64
	resources any // deep-copied parsed value
	group     any // optional /Group (transparency group)
	content   []byte
}

// matrix is a PDF transformation matrix [a b c d e f].
type matrix [6]float64

func (m matrix) apply(x, y float64) (float64, float64) {
	return m[0]*x + m[2]*y + m[4], m[1]*x + m[3]*y + m[5]
}

// mul returns m × n, the transform that applies m and then n.
func (m matrix) mul(n matrix) matrix {
	return matrix{
		m[0]*n[0] + m[1]*n[2],
		m[0]*n[1] + m[1]*n[3],
		m[2]*n[0] + m[3]*n[2],
		m[2]*n[1] + m[3]*n[3],
		m[4]*n[0] + m[5]*n[2] + n[4],
		m[4]*n[1] + m[5]*n[3] + n[5],
	}
}

var identityMatrix = matrix{1, 0, 0, 1, 0, 0}

// importer deep-copies object graphs from a Reader into a Document,
// rewriting indirect references and de-duplicating shared objects.
type importer struct {
	r    *Reader
	d    *Document
	memo map[Ref]rawRef
	// daFontSource is the source-side font object named by the form's
	// default appearance string, kept for metric lookups.
	daFontSource any
}

const maxCopyDepth = 128

func (im *importer) copy(v any, depth int) (any, error) {
	if depth > maxCopyDepth {
		return nil, fmt.Errorf("gopdf: object graph too deep to import")
	}
	switch t := v.(type) {
	case Ref:
		if rr, ok := im.memo[t]; ok {
			return rr, nil
		}
		obj, err := im.r.object(t.Num)
		if err != nil {
			return nil, err
		}
		// Reserve the slot before recursing so cycles resolve.
		rr := rawRef(len(im.d.raw))
		im.d.raw = append(im.d.raw, nil)
		im.memo[t] = rr
		cp, err := im.copy(obj, depth+1)
		if err != nil {
			return nil, err
		}
		im.d.raw[rr] = cp
		return rr, nil
	case *rawStream:
		dict := make(Dict, len(t.dict))
		for k, e := range t.dict {
			if k == "Length" { // recomputed at write time
				continue
			}
			cp, err := im.copy(e, depth+1)
			if err != nil {
				return nil, err
			}
			dict[k] = cp
		}
		return &rawStream{dict: dict, data: t.data}, nil
	case Dict:
		out := make(Dict, len(t))
		for k, e := range t {
			cp, err := im.copy(e, depth+1)
			if err != nil {
				return nil, err
			}
			out[k] = cp
		}
		return out, nil
	case Array:
		out := make(Array, len(t))
		for i, e := range t {
			cp, err := im.copy(e, depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = cp
		}
		return out, nil
	case String:
		return String(append([]byte(nil), t...)), nil
	default: // nil, bool, int64, float64, Name
		return v, nil
	}
}

// ImportPage copies a page from a parsed file into the document and
// returns it as a regular Page. The imported content is placed as the
// page background — the page's rotation is normalized away — and the full
// drawing API works on top of it for stamping and watermarking. External
// (URI) link annotations are preserved; internal links and other
// annotations are dropped.
func (d *Document) ImportPage(r *Reader, index int) (*Page, error) {
	if index < 0 || index >= len(r.pages) {
		return nil, fmt.Errorf("gopdf: page %d out of range (document has %d pages)", index, len(r.pages))
	}
	pi := r.pages[index]

	content, err := r.pageContent(pi.dict)
	if err != nil {
		return nil, fmt.Errorf("gopdf: importing page %d: %w", index, err)
	}
	im := &importer{r: r, d: d, memo: d.importMemo(r)}

	xo := &formXObject{bbox: pi.mediaBox, content: content}
	if pi.resources != nil {
		if xo.resources, err = im.copy(pi.resources, 0); err != nil {
			return nil, fmt.Errorf("gopdf: importing page %d: %w", index, err)
		}
	}
	if g, ok := pi.dict["Group"]; ok {
		if xo.group, err = im.copy(g, 0); err != nil {
			return nil, fmt.Errorf("gopdf: importing page %d: %w", index, err)
		}
	}
	d.xobjects = append(d.xobjects, xo)
	xoIdx := len(d.xobjects)

	// Normalize the source rotation into the placement matrix so the
	// imported page appears upright on an unrotated target page.
	ox, oy := pi.mediaBox[0], pi.mediaBox[1]
	w := pi.mediaBox[2] - pi.mediaBox[0]
	h := pi.mediaBox[3] - pi.mediaBox[1]
	var m matrix
	size := PageSize{W: w, H: h}
	switch pi.rotate {
	case 90:
		m = matrix{0, -1, 1, 0, -oy, w + ox}
		size = PageSize{W: h, H: w}
	case 180:
		m = matrix{-1, 0, 0, -1, w + ox, h + oy}
	case 270:
		m = matrix{0, 1, -1, 0, h + oy, -ox}
		size = PageSize{W: h, H: w}
	default:
		m = matrix{1, 0, 0, 1, -ox, -oy}
	}

	p := d.AddPageSize(size)
	p.op("q %s %s %s %s %s %s cm /%s Do Q",
		fl(m[0]), fl(m[1]), fl(m[2]), fl(m[3]), fl(m[4]), fl(m[5]), p.resName("X", xoIdx))

	if err := im.copyURIAnnots(p, pi.dict["Annots"], m); err != nil {
		return nil, fmt.Errorf("gopdf: importing page %d: %w", index, err)
	}
	return p, nil
}

// importMemo returns the per-reader memo table so objects shared between
// pages (fonts, images) are copied once per document.
func (d *Document) importMemo(r *Reader) map[Ref]rawRef {
	if d.importMemos == nil {
		d.importMemos = make(map[*Reader]map[Ref]rawRef)
	}
	memo, ok := d.importMemos[r]
	if !ok {
		memo = make(map[Ref]rawRef)
		d.importMemos[r] = memo
	}
	return memo
}

// copyURIAnnots copies link annotations with URI actions, transforming
// their rectangles by the page placement matrix.
func (im *importer) copyURIAnnots(p *Page, annots any, m matrix) error {
	arr, ok := im.r.resolve(annots).(Array)
	if !ok {
		return nil
	}
	for _, a := range arr {
		ad, ok := im.r.resolve(a).(Dict)
		if !ok || im.r.resolve(ad["Subtype"]) != Name("Link") {
			continue
		}
		action, ok := im.r.resolve(ad["A"]).(Dict)
		if !ok || im.r.resolve(action["S"]) != Name("URI") {
			continue
		}
		rect, ok := im.r.resolve(ad["Rect"]).(Array)
		if !ok || len(rect) != 4 {
			continue
		}
		var coords [4]float64
		valid := true
		for i, e := range rect {
			f, ok := toFloat(im.r.resolve(e))
			if !ok {
				valid = false
				break
			}
			coords[i] = f
		}
		if !valid {
			continue
		}
		copied := make(Dict, len(ad))
		for k, v := range ad {
			switch k {
			case "P", "Dest", "StructParent", "Popup", "Rect":
				continue
			}
			cp, err := im.copy(v, 0)
			if err != nil {
				return err
			}
			copied[k] = cp
		}
		x1, y1 := m.apply(coords[0], coords[1])
		x2, y2 := m.apply(coords[2], coords[3])
		copied["Rect"] = Array{minF(x1, x2), minF(y1, y2), maxF(x1, x2), maxF(y1, y2)}
		p.rawAnnots = append(p.rawAnnots, copied)
	}
	return nil
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// AppendPDF imports every page of a parsed file into the document.
func (d *Document) AppendPDF(r *Reader) error {
	for i := 0; i < r.NumPages(); i++ {
		if _, err := d.ImportPage(r, i); err != nil {
			return err
		}
	}
	return nil
}

// Merge combines the pages of the source PDF files, in order, into a
// single new file at dst.
func Merge(dst string, sources ...string) error {
	doc := New()
	for _, src := range sources {
		r, err := Open(src)
		if err != nil {
			return err
		}
		if err := doc.AppendPDF(r); err != nil {
			return fmt.Errorf("%w (%s)", err, src)
		}
	}
	return doc.Save(dst)
}

// ExtractPages writes the given pages (0-based indexes, in the given
// order) of the source PDF to a new file at dst.
func ExtractPages(dst, src string, pages ...int) error {
	r, err := Open(src)
	if err != nil {
		return err
	}
	doc := New()
	for _, i := range pages {
		if _, err := doc.ImportPage(r, i); err != nil {
			return err
		}
	}
	return doc.Save(dst)
}
