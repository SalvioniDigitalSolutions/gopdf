package gopdf

import (
	"fmt"
	"math"
	"strings"
)

// TextBlock is a paragraph: a group of consecutive single-run lines that
// share a font, a left edge and a constant leading. Editing a block
// re-wraps its text across the lines the paragraph already occupies, so a
// sentence can grow or shrink without the paragraph losing its shape.
type TextBlock struct {
	// Text is the paragraph's text, its lines joined with single spaces.
	Text string
	// X and Y position the first line's baseline, from the top-left of
	// the page.
	X, Y float64
	// Width is the column width in points: the widest line in the block.
	Width float64
	// LineHeight is the vertical distance between baselines, in points.
	LineHeight float64
	// FontSize is the effective size in points.
	FontSize float64
	// FontName is the font resource name in the source file.
	FontName string

	lines []*TextRun
	// Settings and the change hook come from whichever editing path
	// produced the block.
	fit      FitMode
	maxExtra int
	onChange func()
	// wrapEm is the column width in 1/1000 em, the unit line widths are
	// measured in, so wrapping needs no scale conversion.
	wrapEm float64
	// leadingTS is the baseline-to-baseline step in text space, used to
	// place lines beyond the paragraph's original extent.
	leadingTS float64
}

// Lines returns the runs making up the block, one per line.
func (b *TextBlock) Lines() []*TextRun { return b.lines }

// SetMaxExtraLines allows a reflowed paragraph to grow by up to n lines
// beyond the ones it already occupies. The default is zero: a replacement
// that does not fit is refused rather than allowed to overrun whatever
// follows it on the page, which reflow cannot move.
func (e *EditablePage) SetMaxExtraLines(n int) {
	if n < 0 {
		n = 0
	}
	e.maxExtraLines = n
}

// Blocks groups the page's runs into paragraphs. A block needs at least
// one line; lines built from several runs (a bold word inside a sentence,
// say) are not reflowable and become single-line blocks of their own.
func (e *EditablePage) Blocks() []*TextBlock {
	blocks := groupBlocks(e.runs, nil)
	for _, b := range blocks {
		b.fit, b.maxExtra = e.fit, e.maxExtraLines
	}
	return blocks
}

// groupBlocks partitions runs into paragraphs. onChange, when set, is
// called whenever a block is rewritten.
func groupBlocks(runs []*TextRun, onChange func()) []*TextBlock {
	var blocks []*TextBlock
	var cur *TextBlock

	flush := func() {
		if cur != nil {
			cur.finish()
			blocks = append(blocks, cur)
			cur = nil
		}
	}

	for i := 0; i < len(runs); {
		// Collect every run sharing this baseline.
		j := i + 1
		for j < len(runs) && math.Abs(runs[j].Y-runs[i].Y) < 0.5 &&
			runs[j].target == runs[i].target {
			j++
		}
		line := runs[i]
		multi := j-i > 1
		i = j

		if multi {
			// Not reflowable: emit whatever was accumulating, then the
			// composite line on its own.
			flush()
			continue
		}
		if cur != nil && cur.accepts(line) {
			cur.lines = append(cur.lines, line)
			continue
		}
		flush()
		cur = &TextBlock{lines: []*TextRun{line}, onChange: onChange}
	}
	flush()
	return blocks
}

// accepts reports whether a line continues the block: same font and size,
// same left edge, and a leading consistent with the lines so far.
func (b *TextBlock) accepts(line *TextRun) bool {
	first := b.lines[0]
	last := b.lines[len(b.lines)-1]
	if line.target != first.target ||
		line.FontName != first.FontName ||
		line.font != first.font ||
		math.Abs(line.FontSize-first.FontSize) > 0.01 ||
		math.Abs(line.X-first.X) > 0.5 {
		return false
	}
	gap := line.Y - last.Y // positive downwards
	if gap <= 0.1 || gap > first.FontSize*3 {
		return false
	}
	if len(b.lines) == 1 {
		return true // this gap establishes the leading
	}
	established := b.lines[1].Y - b.lines[0].Y
	return math.Abs(gap-established) < 0.5
}

// finish computes the block's public geometry once its lines are known.
func (b *TextBlock) finish() {
	first := b.lines[0]
	b.X, b.Y = first.X, first.Y
	b.FontSize, b.FontName = first.FontSize, first.FontName

	texts := make([]string, len(b.lines))
	for i, line := range b.lines {
		texts[i] = strings.TrimSpace(line.Text)
		if line.advance > b.wrapEm {
			b.wrapEm = line.advance
		}
		if line.Width > b.Width {
			b.Width = line.Width
		}
	}
	b.Text = strings.Join(texts, " ")
	if len(b.lines) > 1 {
		b.LineHeight = b.lines[1].Y - b.lines[0].Y
		b.leadingTS = b.lines[1].tm[5] - b.lines[0].tm[5]
	}
}

// SetText replaces the paragraph's text, re-wrapping it to the block's
// column width. Lines the new text no longer needs are cleared; if it
// needs more lines than the block has (plus any allowance from
// SetMaxExtraLines), SetText reports how many are missing and changes
// nothing.
func (b *TextBlock) SetText(s string) error {
	first := b.lines[0]
	if len(b.lines) == 1 && b.maxExtra == 0 {
		// Nothing to wrap into: fall back to a plain in-line rewrite.
		if err := first.SetText(s, b.fit); err != nil {
			return err
		}
		b.Text = s
		b.changed()
		return nil
	}
	fi := first.font

	lines, err := b.wrap(s)
	if err != nil {
		return err
	}
	if extra := len(lines) - len(b.lines); extra > b.maxExtra {
		return fmt.Errorf("gopdf: reflowed text needs %d more line(s) than the "+
			"paragraph occupies; allow them with SetMaxExtraLines or shorten the text",
			extra)
	}

	// Encode every line before mutating anything.
	encoded := make([][]byte, len(lines))
	for i, line := range lines {
		codes, err := fi.encodeText(line)
		if err != nil {
			return err
		}
		encoded[i] = codes
	}

	for i, run := range b.lines {
		switch {
		case i < len(encoded) && i < len(b.lines)-1:
			run.spliceRaw(fmt.Sprintf("<%X> Tj", encoded[i]))
			run.Text = lines[i]
		case i == len(b.lines)-1:
			// The final original line also carries any extra lines, then
			// restores the text line matrix so later positioning that is
			// relative to it still lands where it did.
			var sb strings.Builder
			if i < len(encoded) {
				fmt.Fprintf(&sb, "<%X> Tj", encoded[i])
				run.Text = lines[i]
			} else {
				run.Text = ""
			}
			for k := len(b.lines); k < len(encoded); k++ {
				step := matrix{1, 0, 0, 1, 0, b.leadingTS * float64(k-i)}.mul(run.tm)
				fmt.Fprintf(&sb, " %s %s %s %s %s %s Tm <%X> Tj",
					fl(step[0]), fl(step[1]), fl(step[2]), fl(step[3]),
					fl(step[4]), fl(step[5]), encoded[k])
			}
			if len(encoded) > len(b.lines) {
				fmt.Fprintf(&sb, " %s %s %s %s %s %s Tm",
					fl(run.tm[0]), fl(run.tm[1]), fl(run.tm[2]),
					fl(run.tm[3]), fl(run.tm[4]), fl(run.tm[5]))
			}
			run.spliceRaw(sb.String())
		default:
			// A line the new text no longer needs: drop its operator and
			// leave the positioning around it intact.
			run.spliceRaw("")
			run.Text = ""
		}
		run.replaced = true
	}
	b.Text = strings.Join(lines, " ")
	b.changed()
	return nil
}

// changed notifies the owning editor that this block was rewritten.
func (b *TextBlock) changed() {
	if b.onChange != nil {
		b.onChange()
	}
}

// wrap greedily breaks s into lines no wider than the block's column,
// measuring with the paragraph's own font metrics.
func (b *TextBlock) wrap(s string) ([]string, error) {
	first := b.lines[0]
	fi := first.font
	width := func(text string) (float64, error) {
		codes, err := fi.encodeText(text)
		if err != nil {
			return 0, err
		}
		return fi.stringWidth(codes, first.charSpacing, first.wordSpacing,
			first.fontSizeRaw), nil
	}

	var out []string
	for _, paragraph := range strings.Split(s, "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := words[0]
		for _, word := range words[1:] {
			candidate := line + " " + word
			w, err := width(candidate)
			if err != nil {
				return nil, err
			}
			if w <= b.wrapEm {
				line = candidate
				continue
			}
			out = append(out, line)
			line = word
		}
		out = append(out, line)
	}
	return out, nil
}

// spliceRaw records a verbatim replacement of the run's whole operation.
func (run *TextRun) spliceRaw(s string) {
	run.target.splices = append(run.target.splices, splice{
		start: run.start, end: run.end, repl: []byte(s),
	})
}

// ReplaceTextReflow replaces occurrences of old with new across the page's
// paragraphs, re-wrapping each affected paragraph so the change flows
// across its lines instead of stretching one of them. It returns the
// number of paragraphs rewritten.
//
// Use it when the replacement changes a sentence's length appreciably;
// ReplaceText is the better fit for short, in-place substitutions such as
// a date or an amount.
func (e *EditablePage) ReplaceTextReflow(old, new string) (int, error) {
	if old == "" {
		return 0, fmt.Errorf("gopdf: ReplaceTextReflow called with empty search text")
	}
	n := 0
	for _, block := range e.Blocks() {
		if !strings.Contains(block.Text, old) {
			continue
		}
		if err := block.SetText(strings.ReplaceAll(block.Text, old, new)); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
