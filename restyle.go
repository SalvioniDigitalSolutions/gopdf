package gopdf

import (
	"fmt"
	"strings"
)

// Restyling existing text: changing a run's typeface, size or colour
// rather than the characters it draws.
//
// The style operators are emitted immediately before the run's own
// show-text operation and undone immediately after, so the change applies
// to that run alone and everything drawn later keeps the state the page
// set up for it.

// TextStyle describes a change to how a run of existing text is drawn.
// A zero field leaves that aspect of the run alone.
type TextStyle struct {
	// Font replaces the run's typeface. The replacement text is
	// re-encoded for it, and it is registered with the page.
	Font *Font
	// Size sets the type size in points.
	Size float64
	// Color sets the fill colour the text is painted with.
	Color *Color
}

// styleHost is whatever can register a font for a run being restyled: an
// edited page or a page being updated in place.
type styleHost interface {
	// registerFont adds a font to the page's resources and returns the
	// name the content stream should use for it.
	registerFont(f *Font) string
	// glyphUsage records which glyphs of an embedded font are used, so
	// the subset keeps them.
	glyphUsage(f *Font) map[uint16]rune
}

// Restyle changes how the run is drawn. The run's text is unaffected
// unless the new font cannot represent it, in which case Restyle reports
// the offending character and changes nothing.
func (run *TextRun) Restyle(s TextStyle) error {
	if s.Font == nil && s.Size == 0 && s.Color == nil {
		return nil
	}
	if s.Size < 0 {
		return fmt.Errorf("gopdf: type size must be positive")
	}
	if s.Font != nil {
		if run.owner == nil {
			return fmt.Errorf("gopdf: this run's page cannot take a new font")
		}
		// Prove the text is representable before changing anything.
		if _, err := encodeWithFont(s.Font, run.Text); err != nil {
			return err
		}
	}
	if run.style == nil {
		run.style = &TextStyle{}
	}
	if s.Font != nil {
		run.style.Font = s.Font
	}
	if s.Size != 0 {
		run.style.Size = s.Size
	}
	if s.Color != nil {
		c := *s.Color
		run.style.Color = &c
	}
	// Re-emit the run with the style applied.
	return run.reemit()
}

// reemit rewrites the run's operation with its current text and style.
func (run *TextRun) reemit() error {
	text := run.Text
	var codes []byte
	var err error
	if run.style != nil && run.style.Font != nil {
		codes, err = encodeWithFont(run.style.Font, text)
	} else {
		codes, err = run.font.encodeText(text)
	}
	if err != nil {
		return err
	}
	run.applySplice(codes, FitNone)
	run.replaced = true
	return nil
}

// encodeWithFont encodes text for one of this library's own fonts,
// reporting any character it cannot draw.
func encodeWithFont(f *Font, text string) ([]byte, error) {
	if f.ttf == nil {
		// winAnsiEncode produces one byte per rune, so the encoded bytes
		// must be walked by rune index, not by the byte index a range
		// over a string yields.
		out := winAnsiEncode(text)
		i := 0
		for _, r := range text {
			if i >= len(out) {
				break
			}
			if out[i] == '?' && r != '?' {
				return nil, fmt.Errorf("gopdf: font %s cannot represent %q; "+
					"the standard fonts cover only WinAnsi", f.Name(), r)
			}
			i++
		}
		return out, nil
	}
	out := make([]byte, 0, len(text)*2)
	for _, r := range text {
		gid, ok := f.ttf.cmap[r]
		if !ok || gid == 0 {
			return nil, fmt.Errorf("gopdf: font %s has no glyph for %q", f.Name(), r)
		}
		out = append(out, byte(gid>>8), byte(gid))
	}
	return out, nil
}

// rawToEffective converts an effective point size to the operand a Tf
// operator needs, undoing whatever scale the text and current transform
// matrices apply to this run.
func (run *TextRun) rawToEffective() float64 {
	if run.FontSize == 0 || run.fontSizeRaw == 0 {
		return 1
	}
	return run.fontSizeRaw / run.FontSize
}

// styleOps returns the operators that establish and then undo a run's
// restyling. Both are empty when the run is not restyled.
func (run *TextRun) styleOps() (setup, restore string) {
	if run.style == nil {
		return "", ""
	}
	var set, undo strings.Builder

	if run.style.Font != nil || run.style.Size != 0 {
		name := run.FontName
		size := run.fontSizeRaw
		if run.style.Size != 0 {
			// Size is given in the points the reader sees. Many files set
			// a nominal size with Tf and scale it through the text matrix,
			// so the requested size has to be divided back by that scale
			// to land at the right visual size.
			size = run.style.Size * run.rawToEffective()
		}
		if f := run.style.Font; f != nil {
			name = run.owner.registerFont(f)
			if f.ttf != nil {
				// The subset must keep whatever this run draws.
				usage := run.owner.glyphUsage(f)
				for _, r := range run.Text {
					if gid, ok := f.ttf.cmap[r]; ok {
						if _, seen := usage[gid]; !seen {
							usage[gid] = r
						}
					}
				}
			}
		}
		fmt.Fprintf(&set, "/%s %s Tf ", name, fl(size))
		fmt.Fprintf(&undo, " /%s %s Tf", run.FontName, fl(run.fontSizeRaw))
	}
	if c := run.style.Color; c != nil {
		fmt.Fprintf(&set, "%s rg ", c.components())
		prev := run.fillOp
		if prev == "" {
			prev = "0 g" // the default fill colour at the start of a page
		}
		fmt.Fprintf(&undo, " %s", prev)
	}
	return set.String(), undo.String()
}

// registerFont adds a font to an edited page's own resources.
func (e *EditablePage) registerFont(f *Font) string {
	return e.Page.resName("F", e.Page.doc.addFont(f)+1)
}

// glyphUsage exposes the document's subset bookkeeping for a font.
func (e *EditablePage) glyphUsage(f *Font) map[uint16]rune {
	return e.Page.doc.glyphUsage(e.Page.doc.addFont(f))
}

// registerFont adds a font to a page being updated in place.
func (p *UpdatablePage) registerFont(f *Font) string {
	return p.Page.resName("F", p.Page.doc.addFont(f)+1)
}

// glyphUsage exposes the subset bookkeeping for a font added by an update.
func (p *UpdatablePage) glyphUsage(f *Font) map[uint16]rune {
	return p.Page.doc.glyphUsage(p.Page.doc.addFont(f))
}
