package gopdf

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Text with its styling, position and size, one show-text operation at a
// time.
//
// PageText answers "what does this page say"; this answers "what is on
// this page, where, and set in what". The difference matters to anything
// that has to put something back: a detector reporting an offset needs
// the fragments to arrive in the same order every time, and a caller
// anchoring a frame over a name needs the baseline and the advance, not a
// paragraph.
//
// Nothing here decides what the text means. Fragments come out in the
// order the content stream draws them, which is usually reading order and
// is not promised to be.

// TextFragment is one show-text operation: the text it draws, where the
// baseline starts, and how it is set.
type TextFragment struct {
	// Text is the decoded text, through the font's ToUnicode CMap where
	// it has one and its encoding and /Differences where it does not.
	//
	// A code the font gives no mapping for becomes U+FFFD rather than
	// disappearing: an unreadable glyph is still a glyph, and dropping it
	// silently moves every offset after it.
	Text string
	// X and Y are the baseline's starting point in points, measured from
	// the top-left of the page — the same convention as ImageRef.
	X, Y float64
	// W is the advance width in points, with character, word and
	// horizontal spacing applied. It is zero when the font declares no
	// widths for what it drew.
	W float64
	// FontName is the /BaseFont, subset prefix and all, as in
	// "ABCDEF+OpenSans-Bold". It names the face; the resource name the
	// page happens to use for it does not.
	FontName string
	// FontSize is the effective size in points: the Tf operand after the
	// text and current transformation matrices have scaled it.
	FontSize float64
	// RenderMode is the Tr in force. Mode 3 draws nothing, which is how a
	// scanned page carries the OCR layer under its picture, and is what a
	// caller skips to avoid reading the same words twice.
	RenderMode int
	// FontKey tells the fonts of one page apart: two fragments of the
	// page have the same key exactly when they were drawn with the same
	// font dictionary. FontName does not: a page may name one face twice,
	// with two encodings, and only one of them may read rightly. The key
	// means nothing beyond the page it was read from.
	FontKey string
	// Advances places each character of Text along the baseline:
	// Advances[i] is how far from X, in points, character i (counted in
	// runes) starts, and the one entry more than Text has characters is
	// where the text ends. A space Text holds for a gap the operation
	// moved the pen over starts where the character before it ends. A
	// show-text array that draws a row of table headers, "First Fourth
	// First" with a column's width between the words, is one fragment;
	// these say which column each word stands in. nil when the
	// characters cannot be placed.
	Advances []float64
	// Widths says how far each character of Text moves the pen itself,
	// in points, a kern drawn after it not counted: a word ends where its
	// last character's start and width say, wherever the next character
	// is drawn. A space put in for a gap is as wide as the gap. nil
	// exactly when Advances is.
	Widths []float64
}

// Invisible reports whether the fragment is drawn in a mode that paints
// nothing — mode 3, or mode 7 which only adds to the clip.
func (f TextFragment) Invisible() bool {
	return f.RenderMode == 3 || f.RenderMode == 7
}

// PageTextFragments returns the page's text one show-text operation at a
// time, in the order the content stream draws them, descending into form
// XObjects with their matrices composed.
//
// A malformed content stream is reported as an error for that page, as is
// one too large to lex whole. It is never a panic, and never a silent
// prefix: a caller matching on offsets would take a missing tail for
// absent text.
//
// Measured against PageText over 24,254 pages of real documents, the two
// agree on all but ten. Where they differ the fragments hold more, save
// on eight pages of one corpus whose text is mis-decoded either way; that
// case is understood to exist and not yet understood in detail.
func (r *Reader) PageTextFragments(page int) (frags []TextFragment, err error) {
	if page < 0 || page >= len(r.pages) {
		return nil, fmt.Errorf("gopdf: page %d out of range (document has %d pages)",
			page, r.NumPages())
	}
	defer func() {
		if e := recover(); e != nil {
			frags, err = nil, fmt.Errorf("gopdf: page %d could not be read: %v", page+1, e)
		}
	}()

	pi := r.pages[page]
	content, cerr := r.pageContent(pi.dict)
	if cerr != nil {
		return nil, fmt.Errorf("gopdf: page %d: %w", page+1, cerr)
	}
	box := pi.mediaBox
	target := &editTarget{content: content, resources: pi.resources}
	sc := &runScanner{
		r:        r,
		mediaBox: &box,
		targets:  []*editTarget{target},
		seen:     make(map[Ref]bool),
		infos:    make(map[any]*fontInfo),
		// Nothing here rewrites anything, so no form is claimed — and
		// every form is still descended into.
		adoptForm: func(any) *rawStream { return nil },
		readOnly:  true,
	}
	sc.scan(target, pi.resources, identityMatrix, 0)
	if sc.truncated {
		// A prefix of a page is worse than no page: a caller matching on
		// offsets would take the missing tail for absent text.
		return nil, fmt.Errorf("gopdf: page %d has more content than can be "+
			"read at once (over %d tokens); its text would be incomplete",
			page+1, maxContentTokens)
	}

	out := make([]TextFragment, 0, len(sc.runs))
	for _, run := range sc.runs {
		text := spacedFragmentText(run)
		advances, widths := fragmentAdvances(run, text)
		out = append(out, TextFragment{
			Text:       text,
			Advances:   advances,
			Widths:     widths,
			X:          run.X,
			Y:          run.Y,
			W:          run.Width,
			FontName:   run.baseFont,
			FontSize:   run.FontSize,
			RenderMode: run.renderMode,
			FontKey:    fmt.Sprintf("%p", run.font),
		})
	}
	return out, nil
}

// spacedFragmentText is fragmentText with a space where the operation
// moved the pen far enough for a word break (TextRun spaceAt), as the
// page's text and the redactor read it. A journal that draws no space
// character, only moves between its words, read "Astocksolutionof
// D.vulgaris" one string at a time — and every reader built on these
// fragments with it.
func spacedFragmentText(run *TextRun) string {
	text := fragmentText(run)
	if len(run.spaceAt) == 0 || text != run.Text {
		// A run with codes mapped to nothing keeps its own offsets: the
		// replacement characters already moved them.
		return text
	}
	spaced, _ := readRun(run)
	return spaced
}

// fragmentAdvances places the characters of text, the fragment's reading
// of run (spacedFragmentText), along the baseline in points from the
// run's start: each character's start, then where the last one ends; and
// each character's own width. A code that wrote several characters ("ﬁ"
// read "fi") shares its width among them evenly. nil when the run's codes
// were not placed, or text is no reading of them this function knows.
func fragmentAdvances(run *TextRun, text string) (advances, widths []float64) {
	if len(run.codeAt) == 0 || len(run.codeAt) != len(run.codeText) || len(run.codeW) != len(run.codeAt) {
		return nil, nil
	}
	n := utf8.RuneCountInString(text)
	adv := make([]float64, 0, n+1)
	wid := make([]float64, 0, n)
	// The characters each code wrote, in order, with its place.
	type char struct{ start, end float64 }
	var chars []char // one per rune of run.Text, or of fragmentText's reading
	missing := false
	for _, k := range run.codeText {
		if k == 0 {
			missing = true
			break
		}
	}
	off := 0
	for i, k := range run.codeText {
		at, w := run.codeAt[i], run.codeW[i]
		if k == 0 {
			if missing {
				chars = append(chars, char{at, at + w})
			}
			continue
		}
		if off+k > len(run.Text) {
			return nil, nil
		}
		m := utf8.RuneCountInString(run.Text[off : off+k])
		for j := 0; j < m; j++ {
			chars = append(chars, char{at + w*float64(j)/float64(m), at + w*float64(j+1)/float64(m)})
		}
		off += k
	}
	if off != len(run.Text) {
		return nil, nil
	}
	if missing || len(run.spaceAt) == 0 || text == run.Text {
		// fragmentText: one character for each, a replacement character
		// where a code wrote none.
		if len(chars) != n {
			return nil, nil
		}
		for _, c := range chars {
			adv = append(adv, c.start*run.emPts)
			wid = append(wid, (c.end-c.start)*run.emPts)
		}
	} else {
		// readRun: the run's characters with a space put in at each gap.
		spaced, inserted := readRun(run)
		if spaced != text {
			return nil, nil
		}
		isInserted := make(map[int]bool, len(inserted))
		for _, p := range inserted {
			isInserted[p] = true
		}
		ci := 0
		for p := range text {
			if isInserted[p] {
				// From the end of the character before to the start of
				// the one after.
				from, to := 0.0, 0.0
				switch {
				case ci > 0 && ci < len(chars):
					from, to = chars[ci-1].end, chars[ci].start
				case ci > 0:
					from, to = chars[ci-1].end, chars[ci-1].end
				case ci < len(chars):
					from, to = chars[ci].start, chars[ci].start
				}
				adv = append(adv, from*run.emPts)
				wid = append(wid, (to-from)*run.emPts)
				continue
			}
			if ci >= len(chars) {
				return nil, nil
			}
			adv = append(adv, chars[ci].start*run.emPts)
			wid = append(wid, (chars[ci].end-chars[ci].start)*run.emPts)
			ci++
		}
		if ci != len(chars) {
			return nil, nil
		}
	}
	end := 0.0
	if len(chars) > 0 {
		end = chars[len(chars)-1].end
	}
	return append(adv, end*run.emPts), wid
}

// fragmentText rebuilds a run's text with a replacement character where a
// code mapped to nothing.
//
// The scanner drops those codes, which is right for reading a page and
// wrong for locating anything in it: every offset after the hole is off
// by the length of what went missing.
func fragmentText(run *TextRun) string {
	if len(run.codeText) == 0 {
		return run.Text
	}
	missing := false
	for _, n := range run.codeText {
		if n == 0 {
			missing = true
			break
		}
	}
	if !missing {
		return run.Text
	}
	var b strings.Builder
	b.Grow(len(run.Text) + 3*len(run.codeText))
	at := 0
	for _, n := range run.codeText {
		if n == 0 {
			b.WriteRune(utf8.RuneError)
			continue
		}
		if at+n > len(run.Text) {
			break
		}
		b.WriteString(run.Text[at : at+n])
		at += n
	}
	return b.String()
}
