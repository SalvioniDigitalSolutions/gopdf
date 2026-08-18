package gopdf

import "fmt"

// Two things a paragraph can do that a caller needs told about.
//
// A token can be wider than the space it has. "[[PII_REG_NUMBER_001]]"
// dropped into a one-line table cell has nowhere to wrap to, so it runs
// into the cell beside it. Wrapping cannot help — there is one line — so
// the only choices are to set it smaller or to refuse.
//
// And a paragraph that grows pushes the ones below it down, which at the
// bottom of a page pushes them off it. Silently is the wrong way for that
// to happen.

// SetShrinkToFit lets an inserted token be set smaller when it will not
// fit the width the paragraph has, down to a floor of minSize points.
//
// It applies only to inserted text: the document's own words keep the
// size they were set in. Off by default, because a token in a size
// nobody chose is a surprise; on, it is the difference between a table
// that still reads and one whose cells collide.
func (f *Flow) SetShrinkToFit(on bool, minSize float64) {
	f.shrink = on
	if minSize <= 0 {
		minSize = 4
	}
	f.shrinkFloor = minSize
}

// shrinkInserted reduces the size of inserted spans until the longest
// word fits the column, and reports whether it had to.
func (f *Flow) shrinkInserted(spans []FlowSpan) ([]FlowSpan, bool) {
	if !f.shrink || f.widthTS <= 0 {
		return spans, false
	}
	// The widest single word decides: a word cannot be broken, so if it
	// does not fit, nothing else can be done by wrapping.
	worst := 0.0
	for _, s := range spans {
		if !s.inserted {
			continue
		}
		for _, chunk := range splitKeepingBreaks(s.Text) {
			if chunk == " " || chunk == "\n" {
				continue
			}
			if w, ok := s.style.advance(chunk); ok && w > worst {
				worst = w
			}
		}
	}
	if worst <= f.widthTS || worst == 0 {
		return spans, false
	}
	scale := f.widthTS / worst
	out := make([]FlowSpan, len(spans))
	copy(out, spans)
	changed := false
	for i := range out {
		if !out[i].inserted {
			continue
		}
		size := out[i].style.fontSizeRaw * scale
		if size < f.shrinkFloor {
			size = f.shrinkFloor
		}
		if size < out[i].style.fontSizeRaw {
			// Scaled by what the size actually became, not by what it
			// would have become: at the floor those differ, and the two
			// fields have to describe the same text.
			out[i].FontSize *= size / out[i].style.fontSizeRaw
			out[i].style.fontSizeRaw = size
			out[i].fitted = true
			changed = true
		}
	}
	return out, changed
}

// OverflowsPage reports whether the paragraph, as it now stands, extends
// below the bottom of the page it sits on.
//
// A paragraph that grew pushes the ones under it down, and at the foot of
// a page that pushes them off it. Nothing here clips or refuses on its
// own — a caller may well be happy for a footer to move — but the
// question is worth being able to ask before writing.
func (f *Flow) OverflowsPage(pageHeight float64) bool {
	if pageHeight <= 0 || f.LineHeight <= 0 {
		return false
	}
	lines := f.curLines
	if lines == 0 {
		lines = len(f.lines)
	}
	bottom := f.Y + f.shiftPoints() + float64(lines-1)*f.LineHeight
	return bottom > pageHeight
}

// shiftPoints converts the paragraph's accumulated displacement from text
// space into points down the page.
func (f *Flow) shiftPoints() float64 {
	if f.leadingTS == 0 || f.LineHeight == 0 {
		return 0
	}
	// leadingTS is negative for a step down the page, and LineHeight is
	// the same step in points.
	return -f.shiftTS / -f.leadingTS * f.LineHeight
}

// checkPageOverflow reports the first paragraph pushed off the page.
func checkPageOverflow(flows []*Flow, pageHeight float64) error {
	for _, f := range flows {
		if f.OverflowsPage(pageHeight) {
			return fmt.Errorf("gopdf: the text no longer fits the page: a paragraph "+
				"now ends %.0f points below the bottom; shorten the replacement, "+
				"or cap the growth with SetMaxExtraLines",
				f.Y+f.shiftPoints()+float64(f.curLines-1)*f.LineHeight-pageHeight)
		}
	}
	return nil
}

// --- fitting a token to the width it replaces ---

// A different answer to the same problem shrinkInserted solves. That one
// asks whether the token fits the column and shrinks it until it does,
// which keeps the paragraph readable but lets the line breaks fall
// wherever the new width puts them. This one asks the narrower question:
// make the token exactly as wide as the words it replaced, so every line
// break in the paragraph lands where it already was and nothing below it
// moves. It is what a reader wants from a redaction — the page they had,
// with one phrase covered — and it is worth a smaller token to get.

// fitWidthFloor is the fraction of a run's size below which a fitted
// token is not shrunk. Past this the token is legible only in theory,
// and a re-wrapped paragraph is the better failure.
const fitWidthFloor = 0.45

// minFitScale is the smallest floor a caller may ask for. Below this a
// token is not small, it is absent: a mark a page or two wide at a
// tenth of a point, which no reader will find and no eye will see.
const minFitScale = 0.05

// floorOr picks the floor an edit should use: the one asked for, or the
// paragraph's own where the edit named none.
func floorOr(asked, own float64) float64 {
	if asked > 0 {
		return asked
	}
	return own
}

// SetFitWidthFloor sets how far a fitted replacement in this paragraph
// may be shrunk, as a fraction of the run's size. Zero restores the
// default of 0.45.
func (f *Flow) SetFitWidthFloor(min float64) { f.fitFloor = min }

// SetFitWidth makes a replacement occupy the width of the text it
// replaces, by setting it smaller rather than by re-wrapping.
//
// It applies to the replacements this package inserts, never to the
// document's own words, and it only ever shrinks: a replacement narrower
// than what it replaces keeps its size and the line simply gains slack.
// Where even the floor leaves it wider, it is set at the floor and the
// paragraph re-wraps as it otherwise would — being able to read the
// token matters more than where the line ends.
func (f *Flow) SetFitWidth(on bool) { f.fitWidth = on }

// fitSize returns the size at which text, set in style, is exactly want
// wide, and whether that is smaller than the size style already has.
//
// The advance of a string is affine in the font size — each glyph's
// width scales with it, while character and word spacing are absolute
// and do not — so measuring at two sizes recovers both parts and the
// answer can be solved for rather than guessed at and iterated. With no
// extra spacing set, which is the usual case, this comes to exactly
// size × want/width.
func fitSize(style flowStyle, text string, want float64, floor float64) (float64, bool) {
	size := style.fontSizeRaw
	if size <= 0 || want <= 0 || text == "" {
		return 0, false
	}
	full, ok := style.advance(text)
	if !ok || full <= want {
		return 0, false // it already fits, and fitting never enlarges
	}
	half := style
	half.fontSizeRaw = size / 2
	halfWidth, ok := half.advance(text)
	if !ok {
		return 0, false
	}
	// width(s) = scaled*s + fixed, solved from the two measurements.
	scaled := (full - halfWidth) / (size / 2)
	if scaled <= 0 {
		return 0, false // no glyph width to trade away
	}
	fixed := full - scaled*size
	fitted := (want - fixed) / scaled
	if floor <= 0 {
		floor = fitWidthFloor
	}
	if bottom := size * floor; fitted < bottom {
		// Clamped: the token stays readable and the paragraph re-wraps
		// around a width this could not bring down far enough.
		fitted = bottom
	}
	if fitted >= size {
		return 0, false
	}
	return fitted, true
}
