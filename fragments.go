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
		out = append(out, TextFragment{
			Text:       fragmentText(run),
			X:          run.X,
			Y:          run.Y,
			W:          run.Width,
			FontName:   run.baseFont,
			FontSize:   run.FontSize,
			RenderMode: run.renderMode,
		})
	}
	return out, nil
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
