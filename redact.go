package gopdf

import (
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
	bars     map[int][]rect // boxes to paint, derived while planning
	literals []string
	patterns []*regexp.Regexp
	matchFn  func(*TextRun) bool
	images   []ImageRef

	fill         Color
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
		r:         r,
		areas:     make(map[int][]rect),
		bars:      make(map[int][]rect),
		fill:      Black,
		overlay:   true,
		stripMeta: true,
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
func (rd *Redactor) Text(s string) {
	if s == "" {
		return
	}
	rd.literals = append(rd.literals, s)
	rd.planned = false
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
	return rd.rw.writeTo(w)
}

// --- planning ---

func (rd *Redactor) plan() error {
	if rd.planned {
		return rd.err
	}
	rd.marks = nil
	rd.removedText = nil
	rd.bars = make(map[int][]rect)
	rd.partialPaths = 0
	rd.rw = newRewriter(rd.r)
	rd.nextNum = rd.r.maxObjectNumber() + 1
	rd.err = rd.buildPlan()
	rd.planned = true
	return rd.err
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
	if boxes := append(append([]rect(nil), areas...), rd.bars[page]...); rd.overlay && len(boxes) > 0 {
		newContent = append(newContent, rd.overlayOps(boxes, pi.mediaBox)...)
	}
	pageDict := cloneDict(pi.dict)
	pageDict["Contents"] = rd.newObject(compressedStream(newContent))

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
		rd.bars[page] = append(rd.bars[page], box)
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

// overlayOps paints a box over each redacted area.
func (rd *Redactor) overlayOps(areas []rect, box [4]float64) []byte {
	var b strings.Builder
	height := box[3] - box[1]
	b.WriteString("\nq ")
	fmt.Fprintf(&b, "%s rg\n", rd.fill.components())
	for _, a := range areas {
		// Content-stream coordinates have their origin at the bottom.
		fmt.Fprintf(&b, "%s %s %s %s re f\n",
			fl(a.x0+box[0]), fl(height-a.y1+box[1]),
			fl(a.x1-a.x0), fl(a.y1-a.y0))
	}
	b.WriteString("Q\n")
	return []byte(b.String())
}

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
// copy of the title, author and history the information dictionary does.
func (rd *Redactor) stripDocumentMetadata() {
	rootRef, ok := rd.r.trailer["Root"].(Ref)
	if !ok {
		return
	}
	root, ok := rd.r.resolve(rootRef).(Dict)
	if !ok {
		return
	}
	newRoot := cloneDict(root)
	changed := false
	for _, key := range []Name{"Metadata", "Names", "OpenAction", "AA"} {
		if _, has := newRoot[key]; has {
			delete(newRoot, key)
			changed = true
		}
	}
	if changed {
		rd.rw.replace[rootRef.Num] = newRoot
	}
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

// literalRanges finds every occurrence of a literal in a run's text.
func literalRanges(text, lit string) [][2]int {
	var out [][2]int
	for at := 0; ; {
		i := strings.Index(text[at:], lit)
		if i < 0 {
			return out
		}
		out = append(out, [2]int{at + i, at + i + len(lit)})
		at += i + len(lit)
	}
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
	// A mask would let the original show through the scrubbed area.
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
