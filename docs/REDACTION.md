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
| **Images** | Pixels inside the area are overwritten and the image re-encoded, so the original samples are gone. One that cannot be decoded — JPEG 2000, or a JBIG2 using a symbol dictionary — is dropped whole rather than left in. |
| **Vector artwork** | A path lying entirely inside the area is deleted. One straddling the edge is reported by `PartialArtwork()`, not silently kept. A path that establishes a clip is never removed. |
| **Annotations** | Removed with whatever text they hold, unless `KeepAnnotations(true)`. |
| **Metadata** | Information dictionary and XMP discarded, unless `StripMetadata(false)`. Named destinations survive: they are structure, not metadata. |
| **Embedded files** | Removed, unless `KeepAttachments(true)`. No rule here reaches inside one, so a spreadsheet attached to a report still holds whatever the report said. |

### Options

```go
rd.SetFill(gopdf.RGB(0, 0, 0))  // colour of the bar; black by default
rd.SetOverlay(false)            // remove the content but paint no bar
rd.StripMetadata(false)         // keep the document metadata
rd.KeepAnnotations(true)        // leave annotations in place
rd.KeepAttachments(true)        // leave the embedded files in place
rd.SetVerify(false)             // skip the read-back check (not advised)
```

### Embedded files

A PDF can carry other files inside it — the spreadsheet a table came
from, the original of a scan. Nothing on the page shows they are there,
and no redaction rule reaches into one, which makes an attachment the
likeliest way for a redacted document to give up what it was redacted
for. They are removed by default.

Look before you decide:

```go
for _, a := range rd.Attachments() {
    log.Printf("%s (%d bytes) %s", a.Name, a.Size, a.Description)
}
rd.KeepAttachments(true) // if you have a reason to
```

Keeping one is checked, as far as it can be. A redaction told to keep an
attachment whose bytes still hold the words it removed is refused rather
than written:

```
gopdf: "Ada Lovelace" is still in the attached file "contacts.csv", which
redaction does not reach into; stop keeping the attachments or take that
one out yourself, and the output has been withheld
```

That check reads the attachment's bytes as they are, which finds the
words in a plain-text, comma-separated or XML file and does not find them
in a compressed container such as `.docx` or `.zip`. **Finding nothing is
not proof there is nothing.** For an attachment you cannot read yourself,
drop it.

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

### The token is set even when the document's font cannot

A subset font carries the glyphs its document draws and no others. Ask it
for `[[PII_NAME_1]]` and it very likely has no `[`, which would once have
refused the substitution.

Inserted text now falls back to one of the standard fourteen, added to the
page under a collision-free resource name. The face is matched from the
original — bold text gets Helvetica-Bold, italic gets Oblique — so the
token reads like its surroundings.

The permission is deliberately narrow: **only text the edit inserted may
change font.** Text the document already drew keeps its own whatever
happens, because restyling that would change a page you did not ask to
change. A token holding a character even Helvetica cannot set — anything
outside cp1252, an arrow say — is still refused rather than drawn as a
blank box.

### Spelling variants are matched for you

Swiss and German legal documents routinely set every gap as U+00A0, the
non-breaking space, and every compound hyphen as U+00AD, the soft hyphen.
Extraction reports the characters the file holds, so `"Ada Lovelace"`
typed with an ordinary space would match nothing at all.

Each mapping is therefore expanded into the spellings a document might
have used — non-breaking space, soft hyphen, non-breaking hyphen, and the
combinations — all replaced by the same token. They become ordinary
substitutions, so the check that nothing survives covers them without
knowing they exist.

```go
// Matches "Basel-Stadt", "Basel[nbsp]Stadt", "Basel[shy]Stadt", ...
{From: "Basel-Stadt", To: "[[REGION_1]]"}
```

### What counts as a match

One definition, shared by the matcher, the redactor and the read-back
that proves nothing survived — because a check stricter than the matcher
reports a correct pass as a failure, and a looser one lets a survivor
through.

| Rule | Effect |
|---|---|
| **Word boundaries** | `Rossi` matches in `Sig. Rossi,` and not inside `Rossini`. Digits count as word characters, so `123` is not found in `5123`. Punctuation and symbols do not block a match, so reference numbers, emails and IBANs are unaffected. |
| **Hyphen at a line break** | A word justified across two lines — `Bian-` then `chi` — is read as one word and replaced as one, taking the dangling hyphen with it. A hyphen in the middle of a line is real text: `CHE-290` stays whole. |
| **Fragmented lines** | A document that sets a line one piece at a time, carrying the gaps by positioning rather than by spaces, still reads as words. |
| **Spelling variants** | Non-breaking spaces and soft hyphens are matched from a mapping typed with ordinary ones. |

`MatchSubstrings(true)` on a `Redactor` turns the boundary rule off.

### Rules

- **Longest first.** Mappings are sorted so a rule for `Ada Lovelace` is
  not pre-empted by one for `Ada`.
- **A token may not contain its own original.** `{From: "Ada", To: "Ada L."}`
  is refused rather than leaving the name in place.
- **Fonts must be able to set the token.** Where the document's own
  subset cannot — no `[` in it — the inserted text falls back to a
  standard font, matched to the face, and the whole token is set in that
  one font. Only inserted text ever changes font. A token holding a
  character even Helvetica cannot set is still declined rather than drawn
  with blank boxes.

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

## 3. Text inside a scan (OCR)

A scanned letterhead is pixels. Nothing in the content stream says the
name, so every rule above walks straight past it. Plug in an engine and
`Text`, `Pattern` and `Substitute` also reach words inside images.

The library ships no engine — recognising text well is a large problem
with mature solutions, and a poor one here would be worse than none. The
adapter for [tesseract](https://github.com/tesseract-ocr/tesseract) is in
this repo and shells out to the binary, so nothing is added to your Go
dependencies.

```bash
brew install tesseract        # or: apt install tesseract-ocr
```

```go
import "github.com/SalvioniDigitalSolutions/gopdf/ocr/tesseract"

engine, err := tesseract.New(tesseract.Options{Languages: []string{"eng"}})
if err != nil {
    log.Fatal(err)  // tesseract is not installed
}

rd := gopdf.Redact(r)
rd.SetOCR(engine)
rd.Text("Ada Lovelace")                      // black bar over the pixels
rd.Substitute("4815162342", "[[ACCOUNT_1]]") // or a token in its place
rd.Save("redacted.pdf")
```

A matched word has its **pixels overwritten** and the image re-encoded,
then a bar is drawn over it with the token set into it.

```go
rd.SetLabel("[REDACTED]")             // one token for every bar
rd.SetLabelColor(gopdf.White)         // default
rd.SetOCRConfidence(0.5)              // ignore doubtful reads (default 0)
```

### It is checked by reading the pixels again

Nothing extracts text from an image, so the ordinary read-back cannot see
it. Instead the engine is run again over **every image the finished
document still reaches** — not only the ones a page draws, which is what
catches a thumbnail or an alternate. If a word can still be read, the
output is withheld.

### Two honest limits

- **Recognition is not exhaustive.** An engine misses words, especially on
  a poor scan, and a word it misses stays in the document. Review
  `Marks()`; prefer `Area` where you know the region.
- **A literal of several words is matched a word at a time**, since an
  engine reports one box per word. Redacting `Ada Lovelace` therefore also
  removes a lone `Ada`. That removes slightly more than asked, which is
  the right way round here.

---

## 4. Undoing it

Substitutions in **text** are reversible if you keep the key:

```go
key := []gopdf.Pseudonym{{From: "Ada Lovelace", To: "[[PII_NAME_1]]"}}

gopdf.Pseudonymize(r, anon, key)                  // anonymize
gopdf.Pseudonymize(r2, back, gopdf.Reverse(key))  // and back
```

`Reverse` swaps each `From` and `To`. Keep the key somewhere the
pseudonymized document is not, or there was no point.

**Pixels are not reversible.** A word an OCR engine found in a picture was
removed by overwriting it; the token drawn over the hole is all that is
left. `Key.Reversible()` reports `false` once any pixels went.

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
✅ **Second copies of the page are dropped**: `/Thumb` (a rendering of the
page as it was), `/Alternates` (another version of the same image), and
`/PieceInfo` (whatever the producing application cached — for a word
processor, the text it laid out). Each is reported as a `RedactCopy` mark.
✅ With an engine set, every image in the finished document is read again
and the output withheld if a marked word survives
✅ **Annotation appearances** are read as well as annotation strings. An
annotation holds its words twice — as a string and as drawing operators —
and a stale appearance is dropped when the strings behind it change, so
the old wording cannot stay drawn under the new

### What it does not cover

- **Structural names.** A string can also be a font's `/BaseFont` or an
  embedded file's name. Those are structure, not content, and are left
  alone — check `Marks()` if that matters for your document.
- **Text inside an image** is only reached when you plug in an engine —
  see above — and then only as well as that engine reads.
- **Encryption.** A rewrite writes an encrypted source out unencrypted,
  since re-encrypting is a decision to take deliberately.
- **Artwork that straddles a redaction area** is covered by the bar but
  not deleted; `PartialArtwork()` reports how many, so you can enlarge the
  area.

---

## Fitting

Two things a paragraph can do that are worth deciding about rather than
discovering.

**A token wider than its space.** `[[PII_REG_NUMBER_001]]` dropped into a
one-line table cell has nowhere to wrap to. Off by default it simply
overruns; `SetShrinkToFit(true, 6)` sets it smaller instead, down to a
floor in points below which it is left alone rather than made unreadable.

```go
for _, f := range page.Flows() {
    f.SetShrinkToFit(true, 6)
}
```

**Growth past the bottom of the page.** A paragraph that grows pushes the
ones below it down, and at the foot of a page that pushes them off it.
Nothing clips silently, but nothing refuses on your behalf either — ask
before writing:

```go
if f.OverflowsPage(pageHeight) {
    // shorten the replacement, or cap it with SetMaxExtraLines
}
```

## The output parses strictly

Every writer here splices replacements into a content stream somebody else
wrote. A splice landing immediately after an operator whose trailing space
it consumed leaves `Tc` and `1` as the single token `Tc1` — content that
is correct and a file that is not. Readers tolerate it, which is why it
goes unnoticed until something strict refuses the page, and then the cost
falls on whoever tries to re-open their own output to check it.

Splices are separated on both sides, so that cannot happen. If you want to
assert it yourself:

```go
if err := gopdf.StrictLexPages(out); err != nil {
    // a keyword in the content stream is not an operator
}
```

It is exported for exactly that: verifying a pipeline's output with a
second parser, rather than trusting the one that wrote it.

## Choosing between them

Reach for **`Redact`** when the fact that something was removed is the
point — a disclosure with blacked-out passages — or when you are working
by area, by image, or by regular expression against a whole page.

Reach for **`Pseudonymize`** when the document has to stay readable and
consistent: research data, a case file where the same person recurs, or
anywhere a `[REDACTED]` marker reads better than a gap.
