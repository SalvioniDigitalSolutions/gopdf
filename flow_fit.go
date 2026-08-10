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
			out[i].style.fontSizeRaw = size
			out[i].FontSize = out[i].FontSize * scale
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
