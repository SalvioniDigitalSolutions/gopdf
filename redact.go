package gopdf

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"os"
	"regexp"
	"strings"
)

// Redaction removes content from a document permanently.
//
// The distinction that matters: covering something with a black rectangle
// hides it from a reader and leaves it in the file, where any parser will
// still hand it over. Redaction here deletes the content — the glyphs
// come out of the content stream, the pixels come out of the image, the
// annotation is dropped — and the result is written as a fresh file
// rather than appended as an incremental update, so the original bytes do
// not survive alongside the new ones.

// RedactionKind says what a mark will remove.
type RedactionKind string

const (
	// RedactText marks glyphs in a content stream.
	RedactText RedactionKind = "text"
	// RedactImage marks all or part of an image's pixels.
	RedactImage RedactionKind = "image"
	// RedactAnnotation marks an annotation, with whatever text it holds.
	RedactAnnotation RedactionKind = "annotation"
	// RedactPath marks vector artwork.
	RedactPath RedactionKind = "path"
	// RedactImageText marks words an OCR engine read inside an image.
	RedactImageText RedactionKind = "image-text"
	// RedactCopy marks a second copy of the page or an image — a
	// thumbnail, an alternate, a producer's private cache — dropped
	// because it would still show what was removed.
	RedactCopy RedactionKind = "copy"
)

// RedactionMark describes one piece of content that will be removed.
// Marks are what a caller should show for review before writing, since
// afterwards the content is gone.
type RedactionMark struct {
	// Kind is what sort of content this is.
	Kind RedactionKind
	// Page is the 0-based page index.
	Page int
	// X, Y, W and H bound the affected area, in points from the
	// top-left of the page.
	X, Y, W, H float64
	// Text is the text that will be removed, where the content has any.
	Text string
	// Partial reports that only part of the object is affected: some
	// characters of a run, or a region of an image.
	Partial bool
}

// rect is an axis-aligned rectangle in top-left page coordinates.
type rect struct{ x0, y0, x1, y1 float64 }

func (r rect) intersects(o rect) bool {
	return r.x0 < o.x1 && o.x0 < r.x1 && r.y0 < o.y1 && o.y0 < r.y1
}

func (r rect) contains(o rect) bool {
	return r.x0 <= o.x0 && r.y0 <= o.y0 && r.x1 >= o.x1 && r.y1 >= o.y1
}

func (r rect) valid() bool { return r.x1 > r.x0 && r.y1 > r.y0 }

// Redactor collects what to remove from a document and writes the
// redacted result. Create one with Redact, mark content with Area, Text,
// Pattern, Match or Image, then call Save or WriteTo.
//
// A Redactor is not safe for concurrent use.
type Redactor struct {
	r *Reader

	areas    map[int][]rect
	bars     map[int][]redactBar // boxes to paint, derived while planning
	literals []string
	patterns []*regexp.Regexp
	matchFn  func(*TextRun) bool
	images   []ImageRef

	// ocr, when set, reads the text in images so the literal and pattern
	// rules also reach words in a scan.
	ocr        OCREngine
	ocrMinConf float64
	// label is written into each bar, and labelColor is its colour.
	label        string
	tokens       []Pseudonym
	labelColor   Color
	labelFontRef Ref

	fill      Color
	verify    bool
	keepFiles bool
	// substrings relaxes matching so a literal is found inside a longer
	// word, which is what this package used to do.
	substrings   bool
	overlay      bool
	stripMeta    bool
	keepAnnots   bool
	planned      bool
	marks        []RedactionMark
	rw           *rewriter
	nextNum      int
	removedText  []string
	partialPaths int
	err          error
}

// Redact opens a document for redaction.
func Redact(r *Reader) *Redactor {
	return &Redactor{
		r:          r,
		areas:      make(map[int][]rect),
		bars:       make(map[int][]redactBar),
		fill:       Black,
		labelColor: White,
		overlay:    true,
		verify:     true,
		stripMeta:  true,
	}
}

// Area marks a rectangle on a page. Every piece of content that falls
// inside it is removed: text, images, vector artwork and annotations.
// Coordinates are in points from the top-left of the page.
func (rd *Redactor) Area(page int, x, y, w, h float64) {
	if w <= 0 || h <= 0 {
		return
	}
	rd.areas[page] = append(rd.areas[page], rect{x, y, x + w, y + h})
	rd.planned = false
}

// Text marks every occurrence of a literal string, on every page. The
// match is made against the text as extracted, so it sees the document's
// reading order rather than its visual layout.
// Text marks every occurrence of a literal string, on every page.
//
// An occurrence counts where the literal stands on its own: "Rossi" is
// found in "Sig. Rossi," and not inside "Rossini". For personal data
// removing too much is the lesser mistake, but removing the wrong thing
// is still a mistake, and a surname inside a longer surname is the wrong
// thing. MatchSubstrings turns that off.
//
// The literal is also sought in the spellings a document might have used
// instead — a non-breaking space for a space, a soft hyphen for a hyphen
// — so a caller need not know which the producer chose.
func (rd *Redactor) Text(s string) {
	if s == "" {
		return
	}
	for _, v := range expandVariants(Pseudonym{From: s}) {
		rd.literals = append(rd.literals, v.From)
	}
	rd.planned = false
}

// MatchSubstrings finds a literal wherever it appears, including inside a
// longer word. It is off by default.
func (rd *Redactor) MatchSubstrings(on bool) {
	rd.substrings = on
	rd.planned = false
}

// mode returns how strictly this redaction matches.
func (rd *Redactor) mode() matchMode {
	if rd.substrings {
		return matchAnywhere
	}
	return matchWords
}

// Pattern marks every match of a regular expression. Use it for the
// shapes personal data comes in — account numbers, dates of birth, email
// addresses.
func (rd *Redactor) Pattern(re *regexp.Regexp) {
	if re == nil {
		return
	}
	rd.patterns = append(rd.patterns, re)
	rd.planned = false
}

// Match marks whole runs chosen by a callback, for decisions the other
// methods cannot express — a particular font, a position, a size.
func (rd *Redactor) Match(fn func(*TextRun) bool) {
	rd.matchFn = fn
	rd.planned = false
}

// Image marks an entire image for removal, wherever it is drawn.
func (rd *Redactor) Image(img ImageRef) {
	rd.images = append(rd.images, img)
	rd.planned = false
}

// SetFill sets the colour of the box painted over a redacted area. The
// default is black.
func (rd *Redactor) SetFill(c Color) { rd.fill = c }

// SetVerify controls whether the written document is read back and
// checked. It is on by default: a redaction that quietly leaves one
// occurrence behind is the worst way for this to fail, so the result is
// proved rather than assumed. Turn it off only where the cost of parsing
// the output again matters more than that.
func (rd *Redactor) SetVerify(on bool) { rd.verify = on }

// SetOverlay controls whether a box is painted over each redacted area.
// It is on by default, so a redaction is visible as one. Turning it off
// still removes the content; it just leaves no mark.
func (rd *Redactor) SetOverlay(on bool) { rd.overlay = on }

// StripMetadata controls whether the document information dictionary and
// XMP metadata stream are discarded. They are, by default: metadata
// routinely carries author names, file paths and earlier titles that the
// visible content no longer does.
func (rd *Redactor) StripMetadata(on bool) { rd.stripMeta = on }

// KeepAnnotations leaves annotations in place even where they fall inside
// a redacted area. Off by default, since an annotation's text is content
// like any other.
func (rd *Redactor) KeepAnnotations(on bool) { rd.keepAnnots = on }

// Attachments lists the files the document carries inside it.
//
// They are worth looking at before redacting. An attachment is not
// content on a page and no rule here reaches into one, so a spreadsheet
// attached to a report still holds whatever the report said — the whole
// point of redacting having been to remove it. RemoveAttachments takes
// them out.
func (rd *Redactor) Attachments() []Attachment { return rd.r.Attachments() }

// KeepAttachments leaves the files a document carries inside it.
//
// Off by default, so a redaction removes them. No rule here reaches into
// an attachment, and a spreadsheet attached to a report holds whatever
// the report said — which makes leaving one in place the likeliest way
// for a redacted document to give up what it was redacted for. Keeping
// them is a decision worth stating.
func (rd *Redactor) KeepAttachments(on bool) { rd.keepFiles = on }

// Marks lists what will be removed, without removing it. Call it to show
// a reviewer what is about to happen.
func (rd *Redactor) Marks() ([]RedactionMark, error) {
	if err := rd.plan(); err != nil {
		return nil, err
	}
	return rd.marks, nil
}

// Save writes the redacted document to a file.
func (rd *Redactor) Save(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := rd.WriteTo(f); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// WriteTo writes the redacted document. The output is a complete file,
// not an incremental update, so the removed content is not left behind in
// an earlier revision.
func (rd *Redactor) WriteTo(w io.Writer) (int64, error) {
	if err := rd.plan(); err != nil {
		return 0, err
	}
	if !rd.verify {
		return rd.rw.writeTo(w)
	}
	// Written to memory first: a document that fails its own check must
	// not reach the caller looking redacted when it is not.
	var buf bytes.Buffer
	if _, err := rd.rw.writeTo(&buf); err != nil {
		return 0, err
	}
	if err := rd.checkRemoved(buf.Bytes()); err != nil {
		return 0, err
	}
	if err := rd.verifyOCR(buf.Bytes()); err != nil {
		return 0, err
	}
	if err := rd.checkAttachments(buf.Bytes()); err != nil {
		return 0, err
	}
	n, err := w.Write(buf.Bytes())
	return int64(n), err
}

// checkRemoved re-reads the output and confirms that nothing a global
// rule was supposed to remove can still be extracted.
//
// Only the literals and patterns are checked. Those apply to the whole
// document, so no occurrence should survive anywhere. An area or an image
// covers one place, and the same words may legitimately appear elsewhere.
func (rd *Redactor) checkRemoved(out []byte) error {
	if len(rd.literals) == 0 && len(rd.patterns) == 0 {
		return nil
	}
	r, err := NewReader(out)
	if err != nil {
		return fmt.Errorf("gopdf: the redacted document could not be read back: %w", err)
	}
	var text strings.Builder
	for i := 0; i < r.NumPages(); i++ {
		t, err := r.PageText(i)
		if err != nil {
			return fmt.Errorf("gopdf: the redacted document could not be read back: %w", err)
		}
		text.WriteString(t)
		text.WriteString("\n")
		// Annotations draw their words as well as holding them.
		text.WriteString(r.annotationText(i))
		text.WriteString("\n")
	}
	// A word the document split across a line break is one word, and the
	// matcher joins the halves before deciding anything about them. The
	// check has to read the page the same way or the two disagree in
	// both directions: reading the halves apart finds a word the matcher
	// was right to leave alone and refuses a correct redaction, and it
	// would miss a survivor that only reads as itself once joined.
	//
	// Joining loses nothing. Away from a hyphenated break the reading is
	// the raw text, so a survivor standing on its own is still found.
	got := dehyphenate(text.String())
	for _, lit := range rd.literals {
		if containsBounded(got, lit, rd.mode()) {
			return fmt.Errorf("gopdf: %q is still readable after redaction; "+
				"the document draws it in a way this could not reach, and the "+
				"output has been withheld", lit)
		}
	}
	for _, re := range rd.patterns {
		if m := re.FindString(got); m != "" {
			return fmt.Errorf("gopdf: %q still matches %v after redaction; "+
				"the document draws it in a way this could not reach, and the "+
				"output has been withheld", m, re)
		}
	}
	return nil
}

// checkAttachments looks for the redacted words inside the files the
// document carries.
//
// A page is text this package can read; an attachment is a file in
// whatever format its producer chose. Its bytes are searched as they
// are, which finds the words in a plain-text or comma-separated or XML
// attachment and does not find them in a compressed container. That
// asymmetry is the reason the error says what it says: what is found is
// proof of a leak, and finding nothing is not proof of the opposite.
func (rd *Redactor) checkAttachments(out []byte) error {
	if len(rd.literals) == 0 && len(rd.patterns) == 0 {
		return nil
	}
	r, err := NewReader(out)
	if err != nil {
		return nil // the document itself was already checked
	}
	for _, a := range r.Attachments() {
		data, err := a.Data()
		if err != nil {
			continue
		}
		text := string(data)
		for _, lit := range rd.literals {
			if !containsBounded(text, lit, rd.mode()) {
				continue
			}
			return fmt.Errorf("gopdf: %q is still in the attached file %q, which "+
				"redaction does not reach into; stop keeping the attachments "+
				"or take that one out yourself, and the output has been "+
				"withheld", lit, a.Name)
		}
		for _, re := range rd.patterns {
			if m := re.FindString(text); m != "" {
				return fmt.Errorf("gopdf: %q in the attached file %q still matches "+
					"%v; redaction does not reach into an attachment, so stop "+
					"keeping them or take that one out yourself, and the output "+
					"has been withheld", m, a.Name, re)
			}
		}
	}
	return nil
}

// --- planning ---

func (rd *Redactor) plan() error {
	if rd.planned {
		return rd.err
	}
	rd.marks = nil
	rd.removedText = nil
	rd.bars = make(map[int][]redactBar)
	rd.partialPaths = 0
	rd.rw = newRewriter(rd.r)
	rd.nextNum = rd.r.maxObjectNumber() + 1
	rd.err = rd.buildPlan()
	rd.planned = true
	return rd.err
}

// dropAttachments takes every embedded file out of the rewritten
// document.
//
// The catalog's collection is unlinked and the file specifications and
// their streams are dropped outright, so the bytes are not merely
// unreachable but absent: a rewrite keeps what is reachable, and an
// object left in place with nothing pointing at it would still be in the
// file for anyone who looked.
func (rd *Redactor) dropAttachments() {
	r := rd.r
	names, _ := r.resolve(r.Catalog()["Names"]).(Dict)
	if names != nil {
		if _, has := names["EmbeddedFiles"]; has {
			trimmed := names.Clone()
			delete(trimmed, "EmbeddedFiles")
			cat := rd.pendingRoot(r.Catalog())
			if len(trimmed) == 0 {
				delete(cat, "Names")
			} else {
				cat["Names"] = trimmed
			}
			if num := refNumOr(r.trailer["Root"]); num != 0 {
				rd.rw.replace[num] = cat
			}
		}
		dropTree(r, rd.rw, names["EmbeddedFiles"], 0)
	}
	// And the ones a page carries as a paperclip.
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
			if num := refNumOr(entry); num != 0 {
				rd.rw.drop[num] = true
			}
			dropFilespec(r, rd.rw, d["FS"])
		}
	}
}

// dropTree marks a name tree and everything it names for removal.
func dropTree(r *Reader, rw *rewriter, v any, depth int) {
	if depth > 32 {
		return
	}
	if num := refNumOr(v); num != 0 {
		rw.drop[num] = true
	}
	node, ok := r.resolve(v).(Dict)
	if !ok {
		return
	}
	if arr, ok := r.resolve(node["Names"]).(Array); ok {
		for i := 1; i < len(arr); i += 2 {
			dropFilespec(r, rw, arr[i])
		}
	}
	for _, kid := range arrayOf(r, node["Kids"]) {
		dropTree(r, rw, kid, depth+1)
	}
}

// dropFilespec marks a file specification and its stream for removal.
func dropFilespec(r *Reader, rw *rewriter, v any) {
	if num := refNumOr(v); num != 0 {
		rw.drop[num] = true
	}
	spec, ok := r.resolve(v).(Dict)
	if !ok {
		return
	}
	ef, ok := r.resolve(spec["EF"]).(Dict)
	if !ok {
		return
	}
	for _, key := range []Name{"F", "UF", "DOS", "Mac", "Unix"} {
		if num := refNumOr(ef[key]); num != 0 {
			rw.drop[num] = true
		}
	}
}

func (rd *Redactor) newObject(v any) Ref {
	num := rd.nextNum
	rd.nextNum++
	rd.rw.replace[num] = v
	return Ref{Num: num}
}

func (rd *Redactor) buildPlan() error {
	if rd.stripMeta {
		rd.rw.stripInfo = true
	}
	if !rd.keepFiles {
		rd.dropAttachments()
	}
	byImage := make(map[Ref][]rect)
	for _, img := range rd.images {
		byImage[img.ref] = nil // nil means the whole image
	}

	for page := 0; page < rd.r.NumPages(); page++ {
		if err := rd.planPage(page, byImage); err != nil {
			return fmt.Errorf("gopdf: redacting page %d: %w", page, err)
		}
	}
	if err := rd.planImages(byImage); err != nil {
		return err
	}
	if rd.stripMeta {
		rd.stripDocumentMetadata()
	}
	return nil
}

// planPage works out every change to one page.
func (rd *Redactor) planPage(page int, byImage map[Ref][]rect) error {
	pi := rd.r.pages[page]
	areas := rd.areas[page]

	content, err := rd.r.pageContent(pi.dict)
	if err != nil {
		return err
	}
	pageTarget := &editTarget{content: content, resources: pi.resources}

	// Form XObjects are claimed so their contents can be redacted too;
	// each becomes a fresh object in the rewritten file.
	claimed := make(map[Ref]*rawStream)
	claimedOf := make(map[*rawStream]Ref)
	box := pi.mediaBox
	sc := &runScanner{
		r:        rd.r,
		mediaBox: &box,
		targets:  []*editTarget{pageTarget},
		seen:     make(map[Ref]bool),
		infos:    make(map[any]*fontInfo),
		adoptForm: func(entry any) *rawStream {
			ref, ok := entry.(Ref)
			if !ok {
				return nil
			}
			if s, done := claimed[ref]; done {
				return s
			}
			stm, ok := rd.r.resolve(ref).(*rawStream)
			if !ok {
				return nil
			}
			data, err := rd.r.decodeStream(stm.dict, stm.data)
			if err != nil {
				return nil
			}
			copyStm := &rawStream{dict: cloneDict(stm.dict), data: data}
			claimed[ref] = copyStm
			claimedOf[copyStm] = ref
			return copyStm
		},
	}
	sc.scan(pageTarget, pi.resources, identityMatrix, 0)

	// Text. Literal and pattern matches are found across runs first, so
	// a word a content stream split in two is still caught.
	matched := rd.matchesInChains(sc.runs)
	for _, run := range sc.runs {
		if err := rd.redactRun(page, run, areas, matched[run]); err != nil {
			return err
		}
	}

	// Words an engine reads inside an image, where a rule matches them.
	if rd.ocr != nil {
		imgs, err := rd.r.PageImages(page)
		if err == nil {
			for _, img := range imgs {
				regions, matched, err := rd.ocrRegions(img)
				if err != nil {
					return err
				}
				for i, reg := range regions {
					if _, whole := byImage[img.ref]; whole && byImage[img.ref] == nil {
						break // already going entirely
					}
					byImage[img.ref] = append(byImage[img.ref], reg)
					box := pageRectFor(img, reg)
					rd.marks = append(rd.marks, RedactionMark{
						Kind: RedactImageText, Page: page,
						X: box.x0, Y: box.y0,
						W: box.x1 - box.x0, H: box.y1 - box.y0,
						Text: matched[i].Text, Partial: true,
					})
					rd.bars[page] = append(rd.bars[page],
						redactBar{box: box, label: rd.tokenFor(matched[i].Text)})
				}
			}
		}
	}

	// Images drawn on this page that fall inside an area.
	if len(areas) > 0 {
		imgs, err := rd.r.PageImages(page)
		if err == nil {
			for _, img := range imgs {
				if img.W == 0 || img.H == 0 {
					continue
				}
				drawn := rect{img.X, img.Y, img.X + img.W, img.Y + img.H}
				for _, a := range areas {
					if !a.intersects(drawn) {
						continue
					}
					if _, whole := byImage[img.ref]; whole && byImage[img.ref] == nil {
						break // already marked entirely
					}
					byImage[img.ref] = append(byImage[img.ref], imageRegion(a, drawn))
					rd.mark(RedactionMark{
						Kind: RedactImage, Page: page,
						X: maxF(a.x0, drawn.x0), Y: maxF(a.y0, drawn.y0),
						W:       minF(a.x1, drawn.x1) - maxF(a.x0, drawn.x0),
						H:       minF(a.y1, drawn.y1) - maxF(a.y0, drawn.y0),
						Partial: !a.contains(drawn),
					})
					break
				}
			}
		}
	}

	// Vector artwork and inline images inside the areas.
	rd.planPaths(page, pageTarget, areas, pi.mediaBox)

	// Write the redacted content back.
	newContent, err := applySplices(pageTarget.content, pageTarget.splices)
	if err != nil {
		return err
	}
	bars := rd.bars[page]
	for _, a := range areas {
		bars = append(bars, redactBar{box: a, label: rd.label})
	}
	needFont := false
	for _, bar := range bars {
		if bar.label != "" {
			needFont = true
			break
		}
	}
	if rd.overlay && len(bars) > 0 {
		newContent = append(newContent, rd.labelOps(bars, pi.mediaBox, needFont)...)
	}
	pageDict := cloneDict(pi.dict)
	pageDict["Contents"] = rd.newObject(compressedStream(newContent))
	if rd.overlay && needFont {
		pageDict["Resources"] = rd.withLabelFont(pi.resources)
	}

	// Each form XObject the scan descended into carries its own splices
	// and becomes a fresh object in the rewritten file.
	for _, target := range sc.targets {
		if target.stream == nil {
			continue // the page's own content, already written above
		}
		ref, ok := claimedOf[target.stream]
		if !ok {
			continue
		}
		data, err := applySplices(target.content, target.splices)
		if err != nil {
			return err
		}
		rd.rw.replace[ref.Num] = compressedStreamWith(target.stream.dict, data)
	}

	if !rd.keepAnnots && len(areas) > 0 {
		rd.dropAnnotations(page, pageDict, areas)
	}
	rd.hardenPage(page, pageDict)
	num, ok := rd.r.pageObjectNumber(page)
	if !ok {
		return fmt.Errorf("the page is not an indirect object")
	}
	rd.rw.replace[num] = pageDict
	return nil
}

// redactRun removes whatever part of a run is marked.
func (rd *Redactor) redactRun(page int, run *TextRun, areas []rect, matched [][2]int) error {
	whole := false
	if rd.matchFn != nil && rd.matchFn(run) {
		whole = true
	}
	runBox := runRect(run)
	var covered []rect
	for _, a := range areas {
		if !a.intersects(runBox) {
			continue
		}
		if a.contains(runBox) || !axisAligned(run) {
			whole = true
			break
		}
		covered = append(covered, a)
	}

	// Character ranges matched by text and patterns, plus anything an
	// area only partly covers.
	var ranges [][2]int
	if !whole {
		ranges = append(ranges, matched...)
		for _, a := range covered {
			if lo, hi, ok := runRangeInArea(run, a); ok {
				ranges = append(ranges, [2]int{lo, hi})
			}
		}
	}
	if !whole && len(ranges) == 0 {
		return nil
	}
	if whole {
		ranges = [][2]int{{0, len(run.Text)}}
	}
	ranges = mergeRanges(ranges, len(run.Text))
	if len(ranges) == 0 {
		return nil
	}

	repl, removed, err := redactedOps(run, ranges)
	if err != nil {
		// A font that cannot re-encode the kept text is not a reason to
		// leave the marked text in place: drop the whole operation.
		repl, removed = []byte(""), run.Text
		ranges = [][2]int{{0, len(run.Text)}}
	}
	run.target.splices = append(run.target.splices, splice{run.start, run.end, repl})
	run.replaced = true
	rd.removedText = append(rd.removedText, removed)

	// One mark per removed span, bounded to the characters that actually
	// went, so a reviewer sees where the gap is and the bar covers it.
	for _, rg := range ranges {
		x0, x1 := runSpanX(run, rg[0], rg[1])
		box := rect{x0, runBox.y0, x1, runBox.y1}
		if !box.valid() {
			box = runBox
		}
		rd.marks = append(rd.marks, RedactionMark{
			Kind: RedactText, Page: page,
			X: box.x0, Y: box.y0, W: box.x1 - box.x0, H: box.y1 - box.y0,
			Text:    run.Text[rg[0]:rg[1]],
			Partial: removed != run.Text,
		})
		rd.bars[page] = append(rd.bars[page], redactBar{box: box, label: rd.label})
	}
	return nil
}

// runSpanX returns the horizontal extent of a byte range of a run's text,
// measured with the run's own font metrics.
func runSpanX(run *TextRun, lo, hi int) (float64, float64) {
	x0 := run.X + prefixWidth(run, run.Text[:lo])
	return x0, x0 + prefixWidth(run, run.Text[lo:hi])
}

// prefixWidth returns the advance of a piece of a run's text, in points.
func prefixWidth(run *TextRun, s string) float64 {
	if s == "" || run.font == nil || run.fontSizeRaw == 0 {
		return 0
	}
	codes, err := run.font.encodeText(s)
	if err != nil {
		// Fall back to an even share of the run's measured width.
		if n := len([]rune(run.Text)); n > 0 {
			return run.Width * float64(len([]rune(s))) / float64(n)
		}
		return 0
	}
	em := run.font.stringWidth(codes, run.charSpacing, run.wordSpacing, run.fontSizeRaw)
	return em / 1000 * run.FontSize * run.horizScale
}

func (rd *Redactor) mark(m RedactionMark) { rd.marks = append(rd.marks, m) }

// dropAnnotations removes annotations that fall inside a redacted area.
func (rd *Redactor) dropAnnotations(page int, pageDict Dict, areas []rect) {
	arr, ok := rd.r.resolve(pageDict["Annots"]).(Array)
	if !ok {
		return
	}
	box := rd.r.pages[page].mediaBox
	height := box[3] - box[1]
	kept := make(Array, 0, len(arr))
	for _, entry := range arr {
		ad, ok := rd.r.resolve(entry).(Dict)
		if !ok {
			kept = append(kept, entry)
			continue
		}
		r, ok := annotRect(rd.r, ad, height, box)
		if !ok {
			kept = append(kept, entry)
			continue
		}
		hit := false
		for _, a := range areas {
			if a.intersects(r) {
				hit = true
				break
			}
		}
		if !hit {
			kept = append(kept, entry)
			continue
		}
		text := ""
		if s, ok := rd.r.resolve(ad["Contents"]).(String); ok {
			text = decodeTextString(s)
		}
		rd.mark(RedactionMark{
			Kind: RedactAnnotation, Page: page,
			X: r.x0, Y: r.y0, W: r.x1 - r.x0, H: r.y1 - r.y0,
			Text: text,
		})
		if ref, ok := entry.(Ref); ok {
			rd.rw.drop[ref.Num] = true
		}
	}
	pageDict["Annots"] = kept
}

// annotRect reads an annotation's rectangle in top-left coordinates.
func annotRect(r *Reader, ad Dict, height float64, box [4]float64) (rect, bool) {
	arr, ok := r.resolve(ad["Rect"]).(Array)
	if !ok || len(arr) != 4 {
		return rect{}, false
	}
	var v [4]float64
	for i, e := range arr {
		f, ok := toFloat(r.resolve(e))
		if !ok {
			return rect{}, false
		}
		v[i] = f
	}
	x0, x1 := minF(v[0], v[2])-box[0], maxF(v[0], v[2])-box[0]
	yTop := height - (maxF(v[1], v[3]) - box[1])
	yBot := height - (minF(v[1], v[3]) - box[1])
	return rect{x0, yTop, x1, yBot}, true
}

// planImages rewrites the image objects that redaction touches.
func (rd *Redactor) planImages(byImage map[Ref][]rect) error {
	for ref, regions := range byImage {
		stm, ok := rd.r.resolve(ref).(*rawStream)
		if !ok {
			continue
		}
		img := ImageRef{ref: ref, stream: stm, r: rd.r}
		if w, ok := toInt(rd.r.resolve(stm.dict["Width"])); ok {
			img.Width = w
		}
		if h, ok := toInt(rd.r.resolve(stm.dict["Height"])); ok {
			img.Height = h
		}
		m, err := img.Decode()
		if err != nil || regions == nil || img.Width <= 0 || img.Height <= 0 {
			// An image whose pixels cannot be read is removed whole: a
			// partial scrub is not possible, and leaving it is not an
			// option.
			rd.rw.replace[ref.Num] = blankImage(stm, rd.r)
			continue
		}
		scrubbed := scrubRegions(m, regions, rd.fill)
		rd.rw.replace[ref.Num] = encodeRGBStream(scrubbed, stm, rd.r)
	}
	return nil
}

// stripDocumentMetadata discards the XMP metadata stream, which holds a
// copy of the title, author and history the information dictionary does,
// along with the actions a document can carry.
//
// It used to delete the catalog's whole /Names dictionary, which did
// remove the embedded files hiding in there and took the named
// destinations with them — so every internal link in the document
// stopped working. The entries are now named one at a time: the scripts
// go, because a script is behaviour rather than content and is a way out
// of the document; the destinations stay, because a heading a link
// points at is not metadata. Embedded files are their own decision, and
// KeepAttachments is where it is made.
func (rd *Redactor) stripDocumentMetadata() {
	rootRef, ok := rd.r.trailer["Root"].(Ref)
	if !ok {
		return
	}
	root, ok := rd.r.resolve(rootRef).(Dict)
	if !ok {
		return
	}
	newRoot := rd.pendingRoot(root)
	changed := false
	for _, key := range []Name{"Metadata", "OpenAction", "AA"} {
		if _, has := newRoot[key]; has {
			delete(newRoot, key)
			changed = true
		}
	}
	if names, ok := rd.r.resolve(newRoot["Names"]).(Dict); ok {
		if _, has := names["JavaScript"]; has {
			trimmed := names.Clone()
			delete(trimmed, "JavaScript")
			if len(trimmed) == 0 {
				delete(newRoot, "Names")
			} else {
				newRoot["Names"] = trimmed
			}
			changed = true
		}
	}
	if changed {
		rd.rw.replace[rootRef.Num] = newRoot
	}
}

// pendingRoot returns the catalog this plan is building, so two steps
// that both change it do not undo one another.
func (rd *Redactor) pendingRoot(root Dict) Dict {
	if num := refNumOr(rd.r.trailer["Root"]); num != 0 {
		if pending, ok := rd.rw.replace[num].(Dict); ok {
			return pending
		}
	}
	return cloneDict(root)
}

// --- geometry and text helpers ---

// runRect returns a run's bounding box. The vertical extent is taken from
// the font size, since a run carries no per-glyph outline.
func runRect(run *TextRun) rect {
	const ascender, descender = 0.75, 0.25
	return rect{
		x0: run.X, x1: run.X + run.Width,
		y0: run.Y - run.FontSize*ascender,
		y1: run.Y + run.FontSize*descender,
	}
}

// axisAligned reports whether a run reads left to right, which is what
// splitting it at a character boundary assumes.
func axisAligned(run *TextRun) bool {
	return run.tm[1] == 0 && run.tm[2] == 0 && run.tm[0] > 0
}

// runRangeInArea returns the character range of a run that falls inside
// an area, measured by advancing through the run's own metrics.
func runRangeInArea(run *TextRun, a rect) (int, int, bool) {
	runes := []rune(run.Text)
	if len(runes) == 0 || run.Width <= 0 {
		return 0, 0, false
	}
	lo, hi := -1, -1
	x := run.X
	byteAt := 0
	for i, ch := range runes {
		w := run.Width / float64(len(runes))
		if adv, ok := runeAdvance(run, ch); ok {
			w = adv
		}
		// A character counts as inside when its middle is.
		if mid := x + w/2; mid >= a.x0 && mid <= a.x1 {
			if lo < 0 {
				lo = byteAt
			}
			hi = byteAt + len(string(ch))
		}
		x += w
		byteAt += len(string(ch))
		_ = i
	}
	if lo < 0 {
		return 0, 0, false
	}
	return lo, hi, true
}

// runeAdvance returns one character's advance in points, in the run's
// font and size.
func runeAdvance(run *TextRun, ch rune) (float64, bool) {
	if run.font == nil || run.fontSizeRaw == 0 {
		return 0, false
	}
	codes, err := run.font.encodeText(string(ch))
	if err != nil {
		return 0, false
	}
	em := run.font.stringWidth(codes, run.charSpacing, run.wordSpacing, run.fontSizeRaw)
	scale := run.FontSize
	if run.fontSizeRaw != 0 {
		scale = run.FontSize
	}
	return em / 1000 * scale * run.horizScale, true
}

// mergeRanges sorts and merges overlapping character ranges.
func mergeRanges(in [][2]int, n int) [][2]int {
	var clean [][2]int
	for _, r := range in {
		lo, hi := r[0], r[1]
		if lo < 0 {
			lo = 0
		}
		if hi > n {
			hi = n
		}
		if lo < hi {
			clean = append(clean, [2]int{lo, hi})
		}
	}
	for i := 1; i < len(clean); i++ {
		for j := i; j > 0 && clean[j][0] < clean[j-1][0]; j-- {
			clean[j], clean[j-1] = clean[j-1], clean[j]
		}
	}
	var out [][2]int
	for _, r := range clean {
		if len(out) > 0 && r[0] <= out[len(out)-1][1] {
			if r[1] > out[len(out)-1][1] {
				out[len(out)-1][1] = r[1]
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

// redactedOps builds the content-stream operation that draws a run with
// the marked characters missing. The gap they occupied is preserved with
// a kern, so nothing else on the line moves.
func redactedOps(run *TextRun, ranges [][2]int) ([]byte, string, error) {
	if len(ranges) == 1 && ranges[0][0] == 0 && ranges[0][1] == len(run.Text) {
		return nil, run.Text, nil
	}
	// The kept characters are written back as the very codes that drew
	// them. Re-encoding through the font would fail on any document
	// whose encoding cannot be inverted, and the fallback for that is to
	// drop the whole run — far more than was asked for.
	if len(run.codes) == 0 || len(run.codeText) == 0 {
		return nil, run.Text, errNoCodes
	}
	var b strings.Builder
	var removed strings.Builder
	b.WriteString("[")
	at := 0
	emit := func(lo, hi int) {
		if lo >= hi {
			return
		}
		if codes := run.codeSlice(lo, hi); len(codes) > 0 {
			fmt.Fprintf(&b, "<%X>", codes)
		}
	}
	for _, r := range ranges {
		emit(at, r[0])
		removed.WriteString(run.Text[r[0]:r[1]])
		gone := run.codeSlice(r[0], r[1])
		em := run.font.stringWidth(gone, run.charSpacing, run.wordSpacing, run.fontSizeRaw)
		// A negative number in a TJ array advances the pen, leaving the
		// removed text's space empty so nothing after it moves.
		fmt.Fprintf(&b, " %s ", fl(-em))
		at = r[1]
	}
	emit(at, len(run.Text))
	b.WriteString("] TJ")
	return []byte(b.String()), removed.String(), nil
}

// errNoCodes reports a run whose original character codes were not
// recorded, which leaves removing the whole operation as the only option.
var errNoCodes = errors.New("gopdf: the run's character codes are unknown")

// codeSlice returns the character codes that drew a byte range of a run's
// text. The mapping runs through codeText, which records how much text
// each code produced, so a ligature or a multi-character code maps
// correctly rather than by assuming one code per character.
func (run *TextRun) codeSlice(lo, hi int) []byte {
	step := run.codeStep
	if step < 1 {
		step = 1
	}
	var out []byte
	at := 0
	for i, n := range run.codeText {
		start, end := i*step, (i+1)*step
		if end > len(run.codes) {
			break
		}
		// A code is kept when the text it produced lies in the range.
		// A code that produced nothing follows the character before it.
		if at >= lo && (at+n <= hi || (n == 0 && at < hi)) {
			out = append(out, run.codes[start:end]...)
		}
		at += n
	}
	return out
}

// --- image scrubbing ---

// imageRegion converts a page-space area into the image's own pixel-space
// fractions, as a rectangle with coordinates from 0 to 1.
func imageRegion(area, drawn rect) rect {
	w, h := drawn.x1-drawn.x0, drawn.y1-drawn.y0
	if w <= 0 || h <= 0 {
		return rect{0, 0, 1, 1}
	}
	return rect{
		x0: clamp01((maxF(area.x0, drawn.x0) - drawn.x0) / w),
		y0: clamp01((maxF(area.y0, drawn.y0) - drawn.y0) / h),
		x1: clamp01((minF(area.x1, drawn.x1) - drawn.x0) / w),
		y1: clamp01((minF(area.y1, drawn.y1) - drawn.y0) / h),
	}
}

// scrubRegions paints over the marked fractions of an image and returns
// the result.
func scrubRegions(m image.Image, regions []rect, fill Color) *image.RGBA {
	b := m.Bounds()
	out := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			out.Set(x, y, m.At(x, y))
		}
	}
	c := color.RGBA{R: fill.R, G: fill.G, B: fill.B, A: 255}
	w, h := float64(b.Dx()), float64(b.Dy())
	for _, r := range regions {
		x0 := b.Min.X + int(r.x0*w)
		x1 := b.Min.X + int(r.x1*w+0.999)
		y0 := b.Min.Y + int(r.y0*h)
		y1 := b.Min.Y + int(r.y1*h+0.999)
		for y := maxI(y0, b.Min.Y); y < minI(y1, b.Max.Y); y++ {
			for x := maxI(x0, b.Min.X); x < minI(x1, b.Max.X); x++ {
				out.SetRGBA(x, y, c)
			}
		}
	}
	return out
}

// encodeRGBStream re-encodes a scrubbed image as a plain Flate RGB image,
// dropping whatever filter and colour space it had. Re-encoding is the
// point: the original samples must not survive.
func encodeRGBStream(m *image.RGBA, old *rawStream, r *Reader) *rawStream {
	b := m.Bounds()
	raw := make([]byte, 0, b.Dx()*b.Dy()*3)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := m.RGBAAt(x, y)
			raw = append(raw, c.R, c.G, c.B)
		}
	}
	dict := Dict{
		"Type":             Name("XObject"),
		"Subtype":          Name("Image"),
		"Width":            int64(b.Dx()),
		"Height":           int64(b.Dy()),
		"ColorSpace":       Name("DeviceRGB"),
		"BitsPerComponent": int64(8),
		"Filter":           Name("FlateDecode"),
	}
	// Only the plainest entries carry over. A mask would let the
	// original show through the scrubbed area, and /Alternates offers a
	// second, unscrubbed version of the very same picture.
	for _, keep := range []Name{"Intent"} {
		if v, ok := old.dict[keep]; ok {
			dict[keep] = v
		}
	}
	return &rawStream{dict: dict, data: mustFlate(raw)}
}

// blankImage replaces an image whose pixels could not be decoded with a
// single opaque pixel, which removes the samples entirely.
func blankImage(old *rawStream, r *Reader) *rawStream {
	return &rawStream{
		dict: Dict{
			"Type":             Name("XObject"),
			"Subtype":          Name("Image"),
			"Width":            int64(1),
			"Height":           int64(1),
			"ColorSpace":       Name("DeviceGray"),
			"BitsPerComponent": int64(8),
			"Filter":           Name("FlateDecode"),
		},
		data: mustFlate([]byte{0}),
	}
}

// --- stream helpers ---

func compressedStream(data []byte) *rawStream {
	return &rawStream{
		dict: Dict{"Filter": Name("FlateDecode")},
		data: mustFlate(data),
	}
}

func compressedStreamWith(old Dict, data []byte) *rawStream {
	dict := cloneDict(old)
	delete(dict, "DecodeParms")
	delete(dict, "DP")
	dict["Filter"] = Name("FlateDecode")
	return &rawStream{dict: dict, data: mustFlate(data)}
}

func minI(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxI(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// mustFlate compresses, falling back to the raw bytes if compression
// fails, which keeps a stream writable either way.
func mustFlate(b []byte) []byte {
	out, err := flateCompress(b)
	if err != nil {
		return b
	}
	return out
}
