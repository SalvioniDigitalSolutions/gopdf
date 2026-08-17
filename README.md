# gopdf

**Create, read, edit, fill, sign and redact PDFs — in pure Go, with nothing but the standard library.**

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
  glyphs you use, outlines, subroutines and glyph names alike, CID-keyed
  fonts included — a 55 MB CJK face embeds as a few hundred kilobytes — with pair
  kerning and ToUnicode maps for full Unicode text that stays searchable
- Vector graphics: lines, rectangles, rounded rectangles, circles,
  ellipses, polygons, Bézier paths, dash patterns, caps and joins,
  fill/stroke opacity, clipping, and scoped transforms
- **Axial and radial gradients** with any number of colour stops, painted
  into a rectangle, a circle or any path you clip to
- Images: JPEG embedded byte-for-byte, PNG/GIF/`image.Image` with alpha
  preserved as soft masks, grayscale, and Adobe CMYK handling
- Word wrapping, alignment, links, and a nestable bookmark tree
- **Encryption**: AES-128 or AES-256 with per-field permissions
- **Page labels**: the numbering a reader sees — roman front matter,
  prefixed appendices — read, written and resolved a page at a time
- **Layers**: declare optional content, draw on it, and switch it on or
  off in a document that already has some
- **XMP metadata**: read a packet in whichever shape its producer wrote,
  and write one generated from the information dictionary so the two
  cannot disagree
- **PDF/A**: write to the archival profile — the metadata, the output
  intent and the identification it needs, and a refusal rather than a
  file that claims a conformance it does not meet — and check an existing
  document against it

**Reading and manipulating**

- A native parser: classic xref tables, PDF 1.5+ cross-reference streams,
  object streams, hybrid files; Flate (with PNG/TIFF predictors), LZW,
  ASCII85, ASCIIHex and RunLength filters
- Merge, split, rotate, stamp and watermark
- **Styled text fragments**: `PageTextFragments` gives every show-text
  operation with its baseline, advance width, `/BaseFont`, effective size
  and render mode, in content-stream order, descending into forms —
  enough to anchor a frame over a word or to feed a detector that reports
  offsets
- **Text extraction** through ToUnicode CMaps and simple-font encodings,
  descending into nested form XObjects, with word breaks decided by
  measuring the gap rather than guessing — troff and TeX output reads as
  words, not as `BA SH` and `Softw are`
- **Type 3 fonts**: glyph-space widths scaled through the font matrix, and
  text that extracts instead of coming out empty
- **In-place text editing** that preserves the layout exactly
- **Paragraph reflow** that re-wraps text across a paragraph's own lines
- **Flow engine**: replace text of any length, keeping each part's
  styling, growing or shrinking the paragraph by whole lines and pushing
  what follows out of the way
- **Interactive forms**: read fields, fill them (flattened or still
  editable), and author new ones from scratch
- **Images**: list what a page draws, with placement, draw matrix and
  colour space, decode the pixels, and replace one in place
- **Restyling**: change an existing run's typeface, size or colour, not
  just the characters it draws
- **Annotations**: read, add and remove highlights, underlines,
  strike-outs, sticky notes, boxes and links — on new pages or in place
- **Embedded files**: list what a document carries inside it, extract
  it, attach more — to a document being built or an existing one — and
  take them out again, including the paperclip annotations on a page
- **Page operations**: delete, reorder and move pages, in place
- **Incremental update**: edit text, draw, annotate and reorder, appended
  so the original file survives byte for byte — including everything the
  library does not model
- **Digital signatures**: sign a document with an X.509 certificate, read
  the signatures already on one, and tell whether a file was changed after
  it was signed
- **Redaction** that actually removes: glyphs come out of the content
  stream, pixels out of the image, annotations out of the page, and the
  result is a fresh file rather than an appended revision — then read back
  and checked, so a document that still shows the text is withheld
- **Pseudonymization**: swap identifying text for tokens of any length,
  reflowing the paragraph and reaching the copies in metadata, annotations,
  bookmarks and form fields — then proving none of the original is left
- **OCR-driven redaction**: plug in an engine (a tesseract adapter ships
  in the repo) and text rules also reach words inside a scan — pixels
  overwritten, a token drawn in their place, then read again to prove it
- **Page rendering**: `RenderPage` draws a page to an image — paths,
  fills, strokes with caps, joins and dashes, clips, colour spaces, axial
  and radial shadings, mesh shadings, tiling and shading patterns, soft
  masks, all fifteen blend modes, raster images, annotation appearances,
  and **text**, set from
  the outlines the document's own fonts carry. Layers the document
  switches off are not painted. Each layer is a separate switch, so the
  artwork behind live text is one call and a full-page picture is
  another
- **Glyph outlines**: TrueType `glyf` contours including composites, and
  CFF Type 2 charstrings run properly rather than approximated —
  the same shapes a viewer draws
- **Font substitution**: a document may name Arial without embedding it.
  `SubstituteFont` lets you supply the shapes and `SystemFonts` builds
  that from the machine's own fonts; advances still come from the
  document, so the text lands exactly where the document says
- **The object graph**: `Resolve`, `Object`, `Catalog`, `PageDict` and
  `Walk` read any object in the file, and `AddObject`/`SetObject` write
  them back — the escape hatch for anything the typed API does not model,
  going through the same decryption, filters and cross-reference
  machinery as everything else
- **Tagged PDF**: read the structure tree, with the document's own
  element names mapped through its role map, the alternate text a screen
  reader depends on, and the heading outline a document has whether or
  not it also has bookmarks
- **Repairs damaged files**: a wrong `startxref`, bytes before the header
  or a broken table are recovered by scanning for the objects
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

### Replace text of any length, keeping the styling

`ReplaceText` swaps text inside one line. When the replacement is a
different length, a flow re-wraps the whole paragraph instead.

```go
r, _ := gopdf.Open("contract.pdf")
u := gopdf.Update(r)
page, _ := u.Page(0)

// Rewrites every paragraph containing the phrase, and moves the ones
// below to make room.
page.ReplaceTextFlow("twelve months", "thirty-six calendar months from the effective date")

// Or work a paragraph at a time.
for _, f := range page.Flows() {
	f.Replace("EUR 1,200", "EUR 27,450.99")   // stays bold if it was bold
	fmt.Println(f.LineCount(), f.LineDelta()) // how it grew
}
u.Save("revised.pdf")
```

Two things it gets right. **Styling survives**: a paragraph is modelled as
styled spans rather than lines, so a replacement inherits the styling of
the text it replaces and everything around it keeps its own — swap a
figure inside a bold phrase and it stays bold, while the sentence around
it does not. **Length is free**: the paragraph is re-wrapped to its own
column using each span's own font metrics, taking however many lines it
needs, and everything below it on the page moves down or up to match.

A word split across two operations, or drawn one glyph at a time as
justified documents often are, is matched all the same. Cap the growth
with `SetMaxExtraLines` where a paragraph must not run past its box.

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

### Draw a page

```go
r, _ := gopdf.Open("report.pdf")

// The artwork only: paths, shadings, patterns and soft masks, with the
// text left out. Useful behind a live text layer.
art, _, _ := r.RenderPageDetail(0, gopdf.RenderOpts{
    DPI: 150, IncludeVector: true,
})

// Or the whole page. Fonts the document embeds are drawn from their own
// outlines; SystemFonts stands in for the ones it only names.
img, report, err := r.RenderPageDetail(0, gopdf.RenderOpts{
    DPI:                 150,
    IncludeVector:       true,
    IncludeText:         true,
    IncludeRasterImages: true,
    SubstituteFont:      gopdf.SystemFonts(),
})
if report.Missing > 0 {
    log.Printf("%d glyphs had no font to draw them with", report.Missing)
}
```

Text that clips is followed whether or not text is drawn, because a
headline used as a clip decides where a gradient shows through — ignore
it and the gradient covers the page.

### Reach the object graph

Everything else in the package is an opinion about what a PDF is for.
When a file does something those opinions have no word for, the graph
itself is reachable:

```go
r, _ := gopdf.Open("odd.pdf")

// Read: follow a reference, walk from the trailer, decode a stream.
lang := r.Resolve(r.Catalog()["Lang"])
if stm, ok := r.Resolve(r.PageDict(0)["Contents"]).(*gopdf.Stream); ok {
    data, _ := stm.Data() // decoded, and decrypted if the file is
    _ = data
}
r.Walk(func(ref gopdf.Ref, obj any) bool {
    _ = obj
    return true // false stops the walk
})

// Write: add objects and replace existing ones, appended incrementally.
u := gopdf.Update(r)
ref := u.AddObject(gopdf.NewStream(gopdf.Dict{}, []byte("q 1 0 0 RG 4 w 0 0 m 99 99 l S Q")))
u.SetCatalogEntry("Lang", gopdf.String("en-GB"))
_ = u.Save("odd-out.pdf")
_, _ = ref, lang
```

The reader hands back its own dictionaries: `Clone` one before changing
it, and write the copy back with `SetObject`.

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

### Redact, and mean it

Covering something with a black rectangle hides it from a reader and
leaves it in the file. This removes it.

```go
r, _ := gopdf.Open("case-file.pdf")
rd := gopdf.Redact(r)

rd.Text("Ada Lovelace")                                  // every occurrence
rd.Pattern(regexp.MustCompile(`\d{3}-\d{2}-\d{4}`))      // every match
rd.Area(2, 60, 200, 180, 40)                             // a rectangle on page 2
rd.Match(func(run *gopdf.TextRun) bool {                 // anything else
	return run.FontName == "F3"
})

marks, _ := rd.Marks()          // review before committing to it
for _, m := range marks {
	fmt.Printf("%s p%d %q\n", m.Kind, m.Page, m.Text)
}
rd.Save("redacted.pdf")
```

What gets removed, and how:

| Content | What happens |
| --- | --- |
| Text | The glyphs are cut out of the content stream. A gap the same width is left behind, so nothing on the line moves. |
| Images | The pixels in the area are overwritten and the image re-encoded. One that cannot be decoded is dropped whole rather than left. |
| Vector artwork | A path lying entirely inside the area is deleted. One that straddles the edge is reported by `PartialArtwork`, not silently kept. |
| Annotations | Removed, along with whatever text they hold. |
| Metadata | The information dictionary and XMP stream go by default. |
| Second copies | `/Thumb`, `/Alternates` and `/PieceInfo` are dropped: each can hold the page as it was before redaction. |
| Scans | With an OCR engine set, words inside images are found, their pixels overwritten, and every image read again to prove it. |
| Annotations | Their appearance streams are read as well as their strings, and a stale appearance is dropped when the strings change. |

Matching is word-bounded (`Rossi` not inside `Rossini`), joins words
hyphenated across a line break, reads lines a document set one fragment at
a time, and matches non-breaking-space and soft-hyphen spellings — with
the same definition used by the read-back, so the check and the matcher
never disagree.

The output is read back before you get it. If a document draws text in a
way redaction could not reach, `WriteTo` reports it and writes nothing,
rather than handing back a file that looks redacted and is not. Turn the
check off with `SetVerify(false)` if the second parse costs more than the
assurance is worth.

Two properties it is built around. First, a word a content stream split
in two — `Administra` then `tion`, or one glyph at a time, as justified
documents often are — is still matched, because matching runs over a whole
line rather than one operation at a time. Second, the output is a
**complete rewrite**, not an incremental update: an update appends, and
everything it replaced stays readable in the bytes underneath it.

### Pseudonymize, when a marker beats a gap

```go
res, _ := gopdf.PseudonymizeFile("case.pdf", "anonymous.pdf", []gopdf.Pseudonym{
	{From: "Ada Lovelace", To: "[[PII_NAME_1]]"},
	{From: "12 Dorset Street", To: "[REDACTED]"},
})
```

The token need not be the same length — the paragraph re-wraps around it
and keeps its styling. Where the document's own subset font cannot set the
token (no `[` in it), the inserted text falls back to a standard font
matched to the face, and only ever the inserted text. Mappings are also
expanded into the spellings a document might have used, so a name typed
with an ordinary space still matches one written with a non-breaking one. It reaches the copies of a name that nothing draws
but everything reads: the metadata, the XMP packet, annotation notes,
bookmark titles, form field values. Then it reads the result back and
withholds it if any original is still findable.

Full guide: **[docs/REDACTION.md](docs/REDACTION.md)**.

### Sign a document, and check the ones already on it

```go
r, _ := gopdf.Open("contract.pdf")
u := gopdf.Update(r)
u.Sign(gopdf.SignOptions{
	Certificate: cert,           // *x509.Certificate
	Key:         key,            // any crypto.Signer, including an HSM
	Name:        "Ada Lovelace",
	Reason:      "Approval",
})
u.Save("signed.pdf")
```

The signature is a detached PKCS#7 blob covering every byte of the file
except itself, written as an incremental update — so signing a document
twice leaves the first signature intact and still valid. Reading works on
anything, not just files gopdf wrote:

```go
for _, s := range r.Signatures() {
	fmt.Println(s.Signer, s.When, s.CoversWholeFile)
}
```

`CoversWholeFile` is the one that matters: a signature whose byte range
stops short of the end of the file was signed before something else was
appended.

Full guides, the complete API tour and the design notes are in the
**[documentation](https://salvionidigitalsolutions.github.io/gopdf/)**.

## Examples

| Command | What it shows |
| --- | --- |
| [`examples/demo`](examples/demo) | Every drawing feature on three pages |
| [`examples/stamp`](examples/stamp) | Watermarking an existing PDF |
| [`examples/edit`](examples/edit) | Listing and rewriting text in place |
| [`examples/redact`](examples/redact) | Removing text permanently, with a dry run first |

```bash
go run ./examples/edit -in report.pdf -list
```

```bash
go run ./examples/edit -in report.pdf -out final.pdf -replace "DRAFT=FINAL"
```

```bash
go run ./examples/redact -in case.pdf -list -text "Ada Lovelace"
```

## Correctness

Coordinates are in points (1/72 inch) with the origin at the **top-left**
of the page; `Mm`, `Cm` and `Inch` convert other units.

- **460 tests** at **84% statement coverage**, covering the writer, the
  parser, the font subsetter, the filters, encryption, editing, reflow,
  flow, forms, signatures, redaction, rendering and attachments
- **Text extraction measured against `pdftotext`** over 918 real
  documents: agreement rose from 0.773 to 0.849 when word breaks started
  being measured rather than guessed, improving 581 files and regressing
  35
- **Swept against 4,635 real PDFs** — macOS and application resources, Go
  module fixtures, and a 130,000-file legal corpus spanning Word,
  StarOffice, LibreOffice, iText, Aspose, Quartz, groff and TeX, PDF 1.1
  through 2.0. Every one opens, extracts and round-trips. A further
  **3,613-file redaction sweep** removed a word from each and confirmed it
  was gone from both the text and the raw bytes, and a **2,000-file flow
  sweep** replaced a word with a much longer one and checked the
  paragraph reflowed intact. Between them the sweeps found four real
  bugs, all fixed and now regression-tested.

  A later **4,000-document redaction sweep** removed a word from each and
  had `pdftotext` confirm it was gone: 3,986 succeeded, none silently,
  and none produced a damaged document. The 137 that were refused rather
  than written turned out to be the matcher and the verifier reading the
  same page differently — a gap inside one show-text operation, and a
  word hyphenated across a line — and fixing both left 14
- **Fuzz targets** for the PDF reader and the TrueType parser, with a
  checked-in regression corpus of 700+ inputs. Fuzzing has found and fixed
  real bugs, including a denial-of-service in `cmap` parsing
- **Rendering measured against `pdftoppm`**: across 1,500 documents
  99.9% of the glyphs a page asks for are drawn, and on the check that
  matters — ink where the reference has none — the median page scores
  zero and 99% are under two per cent
- **Validated against Poppler** in both directions: files gopdf writes are
  read by an independent implementation, and files other tools wrote are
  read, edited and rewritten by gopdf with their text preserved.
  Attachments extracted by gopdf are byte-identical to `pdfdetach`, and
  every font a stream selects is checked to be declared where that
  stream resolves names — a thing gopdf's own forgiving reader could not
  have caught.
  Signatures are checked with `pdfsig`, which reports them valid and the
  document wholly covered, and the CMS blob parses under OpenSSL
- Unencrypted output is byte-for-byte **deterministic**
- Stream decoding is bounded against decompression bombs

## Limitations

Stated plainly, because they matter when choosing a library:

- Editing can only use glyphs a document's fonts actually contain. Subset
  fonts routinely lack characters; those edits are refused, not mangled.
- An incremental update only grows a file; superseded objects stay in it.
- Object streams are opt-in and skipped for encrypted documents, whose
  strings are protected per object rather than by the enclosing stream.
- Reflow (`TextBlock`) re-wraps within the lines a paragraph already
  occupies. Use a `Flow` when the length changes: it adds or removes
  lines and moves the text below. A flow moves text, not images or
  rules, and needs the font to have a space glyph — a document that
  positions every word separately and never draws a space cannot be
  re-wrapped.
- `FillForm` flattens; `FillFormInteractive` keeps fields editable.
- Permission flags on encrypted documents are advisory, as the PDF
  specification defines them — they are not a security boundary.
- Signatures are `adbe.pkcs7.detached` with SHA-256. Signing produces the
  blob and the byte range; obtaining a timestamp from a TSA, and deciding
  whether a certificate is one you trust, are left to the caller.
- The archival profile is checked for the things that go wrong in
  practice — a font that is not embedded, an encrypted file, a script,
  a missing intent. It is not a certificate: a full validator also checks
  colour management and the internals of embedded font programs.
- A JBIG2 image is decoded when it is coded as a generic region, which is
  what a scanner in its ordinary mode produces. One using a symbol
  dictionary is reported rather than guessed at, and JPEG 2000 is not
  decoded at all; both are dropped whole by redaction rather than
  part-scrubbed.
- Rendering draws a glyph from the outlines the document carries. A font
  the document names but does not embed has no outlines to draw, and a
  bare PostScript font is addressed by glyph name through the built-in
  encodings this package does not carry. Both are handled by supplying a
  substitute; without one their text is left undrawn and
  `RenderPageDetail` reports how much.
- Redaction removes *content*. A string can also live somewhere
  structural — a font's `/BaseFont` name, for instance — and that is not
  content to remove. Attachments are removed by default, and a redaction
  that is told to keep one is refused if the words it removed are still
  readable inside it; a compressed attachment cannot be searched that
  way, so finding nothing there is not proof of the opposite. Vector artwork that straddles the edge
  of an area is covered but not deleted; `PartialArtwork` reports it so
  you can enlarge the area. A rewrite writes an encrypted source out
  unencrypted, since re-encrypting is a decision to take deliberately.

## Roadmap

Nothing outstanding from the original plan. Candidates, in no order:
CID-keyed CFF subsetting, PAdES timestamps, public-key (certificate)
security handlers, JPEG 2000 decoding, JBIG2 symbol dictionaries,
linearization, and reflow that cascades across pages.

## License

[MIT](LICENSE)
