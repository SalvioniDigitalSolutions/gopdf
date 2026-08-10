# gopdf

**Create, read, edit and fill PDFs — in pure Go, with nothing but the standard library.**

[![Go Reference](https://pkg.go.dev/badge/github.com/SalvioniDigitalSolutions/gopdf.svg)](https://pkg.go.dev/github.com/SalvioniDigitalSolutions/gopdf)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

No cgo. No third-party dependencies. No native PDF library underneath — the
document writer, the file parser, the font subsetter, the filters and
the encryption are all implemented here.

📖 **[Documentation](https://salvionidigitalsolutions.github.io/gopdf/)**

```go
package main

import (
	"log"

	"github.com/SalvioniDigitalSolutions/gopdf"
)

func main() {
	doc := gopdf.New()
	page := doc.AddPage()
	page.SetFont(gopdf.Helvetica, 14)
	page.Text(72, 72, "Hello, PDF!")
	if err := doc.Save("hello.pdf"); err != nil {
		log.Fatal(err)
	}
}
```

```
go get github.com/SalvioniDigitalSolutions/gopdf
```

## What it does

**Writing**

- Multi-page documents, standard sizes (A3–A5, Letter, Legal) or custom, in
  either orientation, with Unicode metadata
- The standard 14 fonts with accurate metrics, plus **TrueType and
  OpenType embedding** — `.ttf`, `.ttc` and `.otf` are all subset to the
  glyphs you use — with pair kerning and ToUnicode maps for full Unicode
  text that stays searchable
- Vector graphics: lines, rectangles, rounded rectangles, circles,
  ellipses, polygons, Bézier paths, dash patterns, caps and joins,
  fill/stroke opacity, clipping, and scoped transforms
- **Axial and radial gradients** with any number of colour stops, painted
  into a rectangle, a circle or any path you clip to
- Images: JPEG embedded byte-for-byte, PNG/GIF/`image.Image` with alpha
  preserved as soft masks, grayscale, and Adobe CMYK handling
- Word wrapping, alignment, links, and a nestable bookmark tree
- **Encryption**: AES-128 or AES-256 with per-field permissions

**Reading and manipulating**

- A native parser: classic xref tables, PDF 1.5+ cross-reference streams,
  object streams, hybrid files; Flate (with PNG/TIFF predictors), LZW,
  ASCII85, ASCIIHex and RunLength filters
- Merge, split, rotate, stamp and watermark
- **Text extraction** through ToUnicode CMaps and simple-font encodings,
  descending into nested form XObjects
- **In-place text editing** that preserves the layout exactly
- **Paragraph reflow** that re-wraps text across a paragraph's own lines
- **Interactive forms**: read fields, fill them (flattened or still
  editable), and author new ones from scratch
- **Images**: list what a page draws, with placement and colour space,
  decode the pixels, and replace one in place
- **Restyling**: change an existing run's typeface, size or colour, not
  just the characters it draws
- **Annotations**: read, add and remove highlights, underlines,
  strike-outs, sticky notes, boxes and links — on new pages or in place
- **Page operations**: delete, reorder and move pages, in place
- **Incremental update**: edit text, draw, annotate and reorder, appended
  so the original file survives byte for byte — including everything the
  library does not model
- Reads encrypted files (RC4, AES-128, AES-256) with either password

## Highlights

### Edit text without destroying the layout

```go
src, _ := gopdf.Open("invoice.pdf")
doc := gopdf.New()

page, _ := doc.EditPage(src, 0)          // keeps the original operators
page.ReplaceText("DRAFT", "FINAL")       // drawn in the page's own font
doc.Save("final.pdf")
```

The replacement is encoded with the font the original text used, so it
renders identically, and the width difference is compensated so nothing
else on the page moves. Editing a real-world PDF this way changes only the
pixels of the edited lines — the rest of the page is byte-identical.

If the page's font is a subset without a glyph your replacement needs, the
edit is **refused with a clear message** rather than rendering blank boxes.

### Fill a form, or build one

```go
// Fill and flatten — the result cannot be changed by the recipient
doc.FillForm(src, map[string]string{"applicant": "Ada Lovelace"})

// Fill and keep it editable, with freshly generated appearances
doc.FillFormInteractive(src, map[string]string{"applicant": "Ada Lovelace"})

// Or author a form from scratch
page.AddTextField("name", 160, 100, 240, 20, gopdf.FieldOptions{MaxLen: 60})
page.AddCheckbox("newsletter", 160, 160, 16, gopdf.FieldOptions{Selected: true})
page.AddRadioButton("plan", "pro", 240, 190, 14, gopdf.FieldOptions{Selected: true})
page.AddChoiceField("country", 160, 130, 160, 20,
	[]string{"Italy", "France", "Spain"}, gopdf.FieldOptions{Value: "Italy"})
```

### Update a file without rewriting it

```go
r, _ := gopdf.Open("contract.pdf")
u := gopdf.Update(r)

page, _ := u.Page(0)
page.ReplaceText("2024", "2026")          // edit what is there
page.SetFont(gopdf.HelveticaBold, 48)     // draw on top, same pass
page.SetFillColor(gopdf.RGB(200, 30, 30))
page.Text(120, 400, "REVISED")
page.AddHighlight(60, 300, 200, 14, "check", gopdf.NoteOptions{Author: "AL"})
u.SetFormValues(map[string]string{"signatory": "A. Lovelace"})
u.MovePage(3, 0)                          // and reorder

u.Save("contract.pdf")   // safe to overwrite the source
```

An incremental update writes the original bytes out unchanged and appends
only what differs, chained to the old cross-reference table. Structure
trees, embedded files, optional content, scripts — anything gopdf does not
model — survives untouched, because it is never rewritten. Rebuilding a
document with `EditPage` or `ImportPage` keeps only what the library
understands; `Update` keeps everything.

An updated page carries the full drawing API, so stamps, watermarks and
signatures can be added without rewriting a single original object: the
drawn content becomes an extra content stream and its resources are merged
under a collision-proof prefix.

### Images and restyling

```go
for _, im := range r.PageImages(0) {
	fmt.Printf("%dx%d %s at (%.0f,%.0f)\n", im.Width, im.Height, im.ColorSpace, im.X, im.Y)
	pixels, err := im.Decode()          // an image.Image
}

u := gopdf.Update(r)
u.ReplaceImage(im, newLogo)             // scaled into the same box

page, _ := u.Page(0)
for _, run := range page.Runs() {
	if run.Text == "Heading" {
		blue := gopdf.RGB(20, 70, 190)
		run.Restyle(gopdf.TextStyle{Font: gopdf.HelveticaBold, Size: 15, Color: &blue})
	}
}
```

### Gradients

```go
page.FillGradientRect(30, 60, 200, 80, gopdf.GradientVertical,
	gopdf.Stop(0, gopdf.RGB(40, 90, 200)),
	gopdf.Stop(1, gopdf.RGB(230, 240, 255)))

page.FillGradientCircle(300, 100, 45,
	gopdf.Stop(0, gopdf.White), gopdf.Stop(1, gopdf.RGB(180, 30, 90)))

// Or into any shape you clip to
page.Push()
page.Circle(cx, cy, r, gopdf.ClipPath)
page.PaintLinearGradient(x0, y0, x1, y1, stops...)
page.Pop()
```

### Merge, watermark, encrypt

```go
gopdf.Merge("combined.pdf", "a.pdf", "b.pdf")
gopdf.ExtractPages("first-two.pdf", "input.pdf", 0, 1)

doc := gopdf.New()
for i := 0; i < src.NumPages(); i++ {
	page, _ := doc.ImportPage(src, i)      // an imported page is a normal Page
	page.Push()
	page.SetAlpha(0.3, 0.3)
	page.RotateAt(45, page.Width()/2, page.Height()/2)
	page.SetFont(gopdf.HelveticaBold, 72)
	page.TextAligned(0, page.Height()/2, page.Width(), gopdf.AlignCenter, "DRAFT")
	page.Pop()
}
doc.Encrypt("", "owner-password", gopdf.AllowPrint, gopdf.AES256)
doc.Save("watermarked.pdf")
```

Full guides, the complete API tour and the design notes are in the
**[documentation](https://salvionidigitalsolutions.github.io/gopdf/)**.

## Examples

| Command | What it shows |
| --- | --- |
| [`examples/demo`](examples/demo) | Every drawing feature on three pages |
| [`examples/stamp`](examples/stamp) | Watermarking an existing PDF |
| [`examples/edit`](examples/edit) | Listing and rewriting text in place |

```bash
go run ./examples/edit -in report.pdf -list
```

```bash
go run ./examples/edit -in report.pdf -out final.pdf -replace "DRAFT=FINAL"
```

## Correctness

Coordinates are in points (1/72 inch) with the origin at the **top-left**
of the page; `Mm`, `Cm` and `Inch` convert other units.

- **138 tests** covering the writer, the parser, the font subsetter, the
  filters, encryption, editing, reflow and forms
- **Fuzz targets** for the PDF reader and the TrueType parser, with a
  checked-in regression corpus of 600+ inputs. Fuzzing has found and fixed
  real bugs, including a denial-of-service in `cmap` parsing
- **Validated against Poppler** in both directions: files gopdf writes are
  read by an independent implementation, and files other tools wrote are
  read, edited and rewritten by gopdf with their text preserved
- Unencrypted output is byte-for-byte **deterministic**
- Stream decoding is bounded against decompression bombs

## Limitations

Stated plainly, because they matter when choosing a library:

- Editing can only use glyphs a document's fonts actually contain. Subset
  fonts routinely lack characters; those edits are refused, not mangled.
- An incremental update only grows a file; superseded objects stay in it.
- Object streams are opt-in and skipped for encrypted documents, whose
  strings are protected per object rather than by the enclosing stream.
- Reflow re-wraps a paragraph within the lines it already occupies. It
  cannot push later content down the page.
- `FillForm` flattens; `FillFormInteractive` keeps fields editable.
- OpenType subsetting reduces the outlines but keeps glyph names and
  subroutines, so `.otf` embeds are larger than the equivalent TrueType.
  CID-keyed CFF fonts are embedded whole.
- Permission flags on encrypted documents are advisory, as the PDF
  specification defines them — they are not a security boundary.

## Roadmap

- Subsetting CFF subroutines and glyph names, for smaller `.otf` embeds
- Cascading reflow that pushes later content down the page
- Public-key (certificate) security handlers
- PDF/A conformance

## License

[MIT](LICENSE)
