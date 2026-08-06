// Package gopdf is a pure-Go PDF generation library with no dependencies
// outside the standard library.
//
// A minimal document:
//
//	doc := gopdf.New()
//	page := doc.AddPage()
//	page.SetFont(gopdf.Helvetica, 14)
//	page.Text(72, 72, "Hello, PDF!")
//	if err := doc.Save("hello.pdf"); err != nil {
//		log.Fatal(err)
//	}
//
// All coordinates use points (1/72 inch) with the origin at the top-left
// corner of the page. The Mm, Cm and Inch constants convert other units to
// points, e.g. 25*gopdf.Mm.
package gopdf

import (
	"os"
	"time"
)

// Unit conversion factors to points, e.g. 10*gopdf.Mm is ten millimeters.
const (
	Pt   = 1.0
	Inch = 72.0
	Cm   = 72.0 / 2.54
	Mm   = 72.0 / 25.4
)

// PageSize is a page size in points.
type PageSize struct {
	W, H float64
}

// Standard page sizes in portrait orientation.
var (
	A3     = PageSize{841.89, 1190.55}
	A4     = PageSize{595.28, 841.89}
	A5     = PageSize{419.53, 595.28}
	Letter = PageSize{612, 792}
	Legal  = PageSize{612, 1008}
)

// Landscape returns the size with the longer edge horizontal.
func (s PageSize) Landscape() PageSize {
	if s.W < s.H {
		return PageSize{s.H, s.W}
	}
	return s
}

// Portrait returns the size with the longer edge vertical.
func (s PageSize) Portrait() PageSize {
	if s.W > s.H {
		return PageSize{s.H, s.W}
	}
	return s
}

// Info holds document metadata written to the PDF information dictionary.
type Info struct {
	Title    string
	Author   string
	Subject  string
	Keywords string
	Creator  string
	Producer string
}

// Document is an in-progress PDF document. Create one with New, add pages
// and content, then call Save or WriteTo.
//
// A Document is not safe for concurrent use.
type Document struct {
	// Compress enables Flate compression of content streams, image data
	// and embedded fonts. It defaults to true; disable it to produce
	// human-readable output for debugging.
	Compress bool

	// CreationDate is stamped into the document metadata. It defaults to
	// the time New was called.
	CreationDate time.Time

	info       Info
	pageSize   PageSize
	pages      []*Page
	fonts      []*Font
	fontIndex  map[*Font]int
	fontUsage  map[int]map[uint16]rune // font index -> glyph ID -> rune
	images     []*imageData
	alphas     []alphaState
	alphaIndex map[alphaState]int
	shadings   []*shading
	outlines   []*Outline

	// Imported-content state: form XObjects wrapping imported pages, the
	// raw parsed objects they reference, and per-Reader copy memos.
	xobjects    []*formXObject
	raw         []any
	importMemos map[*Reader]map[Ref]rawRef

	// encryptSetup is non-nil when Encrypt has been called.
	encryptSetup *encryptSetup

	// editables are pages imported with EditPage; their pending text
	// edits are materialized when the document is written.
	editables []*EditablePage

	// acroForm is an imported form definition, set by
	// FillFormInteractive; acroFields are fields authored through the
	// Page.Add*Field methods.
	acroForm   any
	acroFields []*acroField

	// fontNums holds the font object numbers during serialization, so
	// authored appearance streams can reference them.
	fontNums []int
}

// alphaState is a fill/stroke opacity pair backed by an ExtGState object.
type alphaState struct {
	fill, stroke float64
}

// New creates an empty document with A4 pages and compression enabled.
func New() *Document {
	return &Document{
		Compress:     true,
		CreationDate: time.Now(),
		pageSize:     A4,
		fontIndex:    make(map[*Font]int),
		fontUsage:    make(map[int]map[uint16]rune),
		alphaIndex:   make(map[alphaState]int),
		info:         Info{Producer: "gopdf"},
	}
}

// SetInfo sets the document metadata.
func (d *Document) SetInfo(info Info) {
	if info.Producer == "" {
		info.Producer = "gopdf"
	}
	d.info = info
}

// SetPageSize sets the default size used by AddPage.
func (d *Document) SetPageSize(s PageSize) {
	d.pageSize = s
}

// AddPage appends a new page of the document's default size.
func (d *Document) AddPage() *Page {
	return d.AddPageSize(d.pageSize)
}

// AddPageSize appends a new page of the given size.
func (d *Document) AddPageSize(s PageSize) *Page {
	p := &Page{doc: d, w: s.W, h: s.H, state: newGstate()}
	d.pages = append(d.pages, p)
	return p
}

// addFont registers a font with the document and returns its resource index.
func (d *Document) addFont(f *Font) int {
	if i, ok := d.fontIndex[f]; ok {
		return i
	}
	i := len(d.fonts)
	d.fonts = append(d.fonts, f)
	d.fontIndex[f] = i
	return i
}

// glyphUsage returns the used-glyph set of an embedded font, for subsetting
// and ToUnicode generation.
func (d *Document) glyphUsage(fontIdx int) map[uint16]rune {
	u, ok := d.fontUsage[fontIdx]
	if !ok {
		u = make(map[uint16]rune)
		d.fontUsage[fontIdx] = u
	}
	return u
}

// addAlpha registers a fill/stroke opacity pair and returns its ExtGState
// resource index.
func (d *Document) addAlpha(fill, stroke float64) int {
	s := alphaState{fill, stroke}
	if i, ok := d.alphaIndex[s]; ok {
		return i
	}
	i := len(d.alphas)
	d.alphas = append(d.alphas, s)
	d.alphaIndex[s] = i
	return i
}

// Outline is a bookmark in the document's outline tree, shown in the
// viewer's sidebar.
type Outline struct {
	title    string
	page     *Page
	y        float64
	children []*Outline
}

// AddOutline adds a bookmark that jumps to y points from the top of page.
// Pass parent nil for a top-level entry, or a previously returned Outline
// to nest.
func (d *Document) AddOutline(parent *Outline, title string, page *Page, y float64) *Outline {
	o := &Outline{title: title, page: page, y: y}
	if parent == nil {
		d.outlines = append(d.outlines, o)
	} else {
		parent.children = append(parent.children, o)
	}
	return o
}

// Save writes the document to a file.
func (d *Document) Save(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := d.WriteTo(f); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
