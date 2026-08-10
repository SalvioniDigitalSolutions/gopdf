# Redaction and pseudonymization

Two related jobs with different endings. Both guarantee the original text
is **not recoverable** from the file they write.

| | You want | Use |
|---|---|---|
| Take the text out, leave a gap | `Ada Lovelace` → ▮▮▮▮▮ | `Redact` |
| Take the text out, leave a marker | `Ada Lovelace` → `[REDACTED]` | `Pseudonymize` |
| Swap for a stable token | `Ada Lovelace` → `[[PII_NAME_1]]` | `Pseudonymize` |

The one thing neither is: a find-and-replace. Replacing text through an
ordinary edit **appends** to the file, and the original stays readable in
the revision underneath — truncate the file at its first `%%EOF` and it
comes straight back. Both of these write a complete new file with one
revision, so there is nothing to roll back to.

---

## 1. Redaction — remove, leave a gap

```go
r, _ := gopdf.Open("case.pdf")
rd := gopdf.Redact(r)

rd.Text("Ada Lovelace")                              // every occurrence
rd.Pattern(regexp.MustCompile(`\d{3}-\d{2}-\d{4}`))  // every match
rd.Area(2, 60, 200, 180, 40)                         // page 2, a rectangle
rd.Match(func(run *gopdf.TextRun) bool {             // anything else
    return run.FontSize > 20
})

marks, err := rd.Marks()          // review BEFORE writing; afterwards it is gone
for _, m := range marks {
    fmt.Printf("%s p%d %q\n", m.Kind, m.Page+1, m.Text)
}

err = rd.Save("redacted.pdf")
```

### What it removes

| Content | What happens |
|---|---|
| **Text** | Glyphs cut from the content stream. The characters kept are written back as the codes that originally drew them, and a gap of the same width is left behind, so nothing else on the line moves. |
| **Images** | Pixels inside the area are overwritten and the image re-encoded, so the original samples are gone. One that cannot be decoded (fax, JBIG2, JPEG 2000) is dropped whole rather than left in. |
| **Vector artwork** | A path lying entirely inside the area is deleted. One straddling the edge is reported by `PartialArtwork()`, not silently kept. A path that establishes a clip is never removed. |
| **Annotations** | Removed with whatever text they hold, unless `KeepAnnotations(true)`. |
| **Metadata** | Information dictionary and XMP discarded, unless `StripMetadata(false)`. |

### Options

```go
rd.SetFill(gopdf.RGB(0, 0, 0))  // colour of the bar; black by default
rd.SetOverlay(false)            // remove the content but paint no bar
rd.StripMetadata(false)         // keep the document metadata
rd.KeepAnnotations(true)        // leave annotations in place
rd.SetVerify(false)             // skip the read-back check (not advised)
```

### It checks its own work

The output is read back and the text that `Text` and `Pattern` removed is
searched for again. If any is still readable — because the document draws
it in a way redaction could not reach — `WriteTo` **returns an error and
writes nothing**, rather than handing back a file that looks redacted and
is not.

```go
if err := rd.Save("redacted.pdf"); err != nil {
    // e.g. gopdf: "Ada Lovelace" is still readable after redaction;
    // the document draws it in a way this could not reach, and the
    // output has been withheld
}
```

`Area` and `Image` marks are not checked this way: they cover one place,
and the same words may legitimately appear elsewhere.

### Command line

```bash
go run ./examples/redact -in case.pdf -list -text "Ada Lovelace"
go run ./examples/redact -in case.pdf -out clean.pdf \
    -text "Ada Lovelace" -pattern '\d{3}-\d{2}-\d{4}'
```

---

## 2. Pseudonymization — remove, leave a token

Use this when the text must be replaced rather than blanked: a stable
token so the same person can be followed through a document, or a plain
`[REDACTED]` marker.

```go
r, _ := gopdf.Open("case.pdf")
out, _ := os.Create("anonymous.pdf")

res, err := gopdf.Pseudonymize(r, out, []gopdf.Pseudonym{
    {From: "Ada Lovelace",   To: "[[PII_NAME_1]]"},
    {From: "Charles Babbage", To: "[[PII_NAME_2]]"},
    {From: "12 Dorset Street", To: "[REDACTED]"},
})
fmt.Println(res.Total(), "paragraphs changed across", res.Pages, "pages")
```

Or between two paths:

```go
res, err := gopdf.PseudonymizeFile("case.pdf", "anonymous.pdf", subs)
```

### The token need not be the same length

The paragraph is re-wrapped around it, measuring each piece in its own
font, and takes however many lines it needs. Each part of the paragraph
keeps the styling it had, so replacing a name inside a bold phrase leaves
the phrase bold and the sentence around it alone.

### Where it looks

Page text is the visible half. The same name also sits in places nothing
draws but everything reads, and all of them are rewritten:

- the information dictionary — title, author, subject, keywords
- the XMP metadata packet
- annotation notes and their authors
- bookmark titles
- form field values, defaults and tooltips
- file attachment names and descriptions

### Rules

- **Longest first.** Mappings are sorted so a rule for `Ada Lovelace` is
  not pre-empted by one for `Ada`.
- **A token may not contain its own original.** `{From: "Ada", To: "Ada L."}`
  is refused rather than leaving the name in place.
- **Fonts must be able to set the token.** A document whose subset font
  has no `[` will be declined, not drawn with blank boxes.

### It proves the result

Before you get the file, it is read back and searched — the page text,
every string in every object still reachable, and any metadata packet. If
an original is still findable, `Pseudonymize` **returns an error and
writes nothing**.

```go
// gopdf: "Ada Lovelace" survives in a string (object 42);
// the output has been withheld
```

`PseudonymizeFile` writes to disk only after that check passes, so a
failure cannot leave a half-anonymous file behind.

---

## What "not recoverable" covers

✅ Not in the page text, as any reader or `pdftotext` sees it
✅ Not in any string in the object graph — metadata, annotations,
bookmarks, field values
✅ Not in an XMP metadata packet
✅ Not in an earlier revision: the output has exactly one, so there is
nothing to roll back to
✅ Superseded objects from the source document's own earlier edits are
dropped too

### What it does not cover

- **Structural names.** A string can also be a font's `/BaseFont` or an
  embedded file's name. Those are structure, not content, and are left
  alone — check `Marks()` if that matters for your document.
- **Text inside an image.** A scan of a letterhead is pixels. Use `Area`
  or `Image` to remove the region; the library does not run OCR.
- **Encryption.** A rewrite writes an encrypted source out unencrypted,
  since re-encrypting is a decision to take deliberately.
- **Artwork that straddles a redaction area** is covered by the bar but
  not deleted; `PartialArtwork()` reports how many, so you can enlarge the
  area.

---

## Choosing between them

Reach for **`Redact`** when the fact that something was removed is the
point — a disclosure with blacked-out passages — or when you are working
by area, by image, or by regular expression against a whole page.

Reach for **`Pseudonymize`** when the document has to stay readable and
consistent: research data, a case file where the same person recurs, or
anywhere a `[REDACTED]` marker reads better than a gap.
