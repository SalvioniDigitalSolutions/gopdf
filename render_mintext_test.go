package gopdf

import (
	"image"
	"strings"
	"testing"
)

// A watermark is set many times larger than the body it sits over. That
// difference is enough to separate them: a render with a threshold
// between the two sizes draws the watermark from the document's own
// matrices — so it lands exactly where the document puts it — and never
// draws the body at all. Not drawn and covered: never drawn, so the
// image can be handed on without a stripped copy of the file having to
// exist.

// watermarkedPage sets body text at 11pt and a watermark at 96pt, the
// watermark scaled through the text matrix as a producer would.
func watermarkedPage(t *testing.T) []byte {
	t.Helper()
	var b strings.Builder
	for i, line := range []string{
		"Confidential terms of the agreement between the parties,",
		"which are the words nobody outside the room should read,",
		"set at the size a body of running text is set at.",
	} {
		b.WriteString("BT /F1 11 Tf 1 0 0 1 60 " + fl(700-float64(i)*16) +
			" Tm (" + line + ") Tj ET\n")
	}
	// The watermark: a nominal size of 1 scaled by 96 through the text
	// matrix, which is how a producer usually writes one and why the
	// threshold has to look at the effective size rather than the
	// operand.
	b.WriteString("q 0.8 g BT /F1 1 Tf 96 0 0 96 70 400 Tm (DRAFT) Tj ET Q\n")
	return rawPageDoc(t, b.String())
}

func inkPixels(t *testing.T, data []byte, min float64) (int, image.Image) {
	t.Helper()
	img, err := NewReaderOrFail(t, data).RenderPage(0, RenderOpts{
		DPI: 100, IncludeText: true, MinTextSize: min,
		SubstituteFont: SystemFonts(),
	})
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	n := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if r, _, _, a := img.At(x, y).RGBA(); a > 0x4000 && r < 0xE000 {
				n++
			}
		}
	}
	return n, img
}

// TestMinTextSizeDrawsOnlyTheWatermark is the whole point: above the
// body's size and below the watermark's, one is drawn and the other is
// not there at all.
func TestMinTextSizeDrawsOnlyTheWatermark(t *testing.T) {
	src := watermarkedPage(t)
	all, _ := inkPixels(t, src, 0)
	mark, markImg := inkPixels(t, src, 72)
	if all == 0 || mark == 0 {
		t.Fatalf("the fixture drew nothing: all=%d mark=%d", all, mark)
	}
	if mark >= all {
		t.Errorf("the threshold removed nothing: %d pixels of %d", mark, all)
	}
	// The body lines sit at y≈700..668 in PDF space, near the top of the
	// page; the watermark is far below them. With only the watermark
	// drawn, the body's band has to be empty.
	if n := inkInBand(markImg, 180, 215); n != 0 {
		t.Errorf("%d pixels of body text survived the threshold", n)
	}
	// And a threshold above everything draws nothing at all.
	none, _ := inkPixels(t, src, 200)
	if none != 0 {
		t.Errorf("a threshold above every size still drew %d pixels", none)
	}
}

// TestMinTextSizeKeepsThePlace: the watermark is drawn from the
// document's own matrices, so it lands exactly where a full render puts
// it. That is what makes a backdrop pass line up by construction.
func TestMinTextSizeKeepsThePlace(t *testing.T) {
	src := watermarkedPage(t)
	_, full := inkPixels(t, src, 0)
	_, mark := inkPixels(t, src, 72)
	// Every pixel the threshold render inks must be inked by the full
	// one too, and in the same place.
	b := mark.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, _, _, a := mark.At(x, y).RGBA()
			if a <= 0x4000 || r >= 0xE000 {
				continue
			}
			if fr, _, _, fa := full.At(x, y).RGBA(); fa <= 0x4000 || fr >= 0xE000 {
				t.Fatalf("the watermark inked (%d,%d), which the full render "+
					"leaves blank: it is not in the document's own place", x, y)
			}
		}
	}
}

// TestMinTextSizeCountsNothingMissing: a glyph below the threshold was
// not attempted, so it is neither drawn nor missing. Reporting it
// missing would tell a caller the page could not be rendered when the
// caller asked for exactly this.
func TestMinTextSizeCountsNothingMissing(t *testing.T) {
	src := watermarkedPage(t)
	_, rep, err := NewReaderOrFail(t, src).RenderPageDetail(0, RenderOpts{
		DPI: 100, IncludeText: true, MinTextSize: 72,
		SubstituteFont: SystemFonts(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Missing != 0 {
		t.Errorf("report says %d glyphs missing; below the threshold is not "+
			"a failure to draw", rep.Missing)
	}
	if rep.Glyphs != len("DRAFT") {
		t.Errorf("report says %d glyphs drawn, want the %d of the watermark",
			rep.Glyphs, len("DRAFT"))
	}
}

// TestMinTextSizeStillAdvances: the pen moves over what it does not
// draw, so a threshold that lets part of a line through leaves that part
// where it was.
func TestMinTextSizeStillAdvances(t *testing.T) {
	// One operation, two sizes: small text, then large on the same line.
	src := rawPageDoc(t, "BT /F1 8 Tf 1 0 0 1 60 700 Tm (tiny) Tj ET\n"+
		"BT /F1 40 Tf 1 0 0 1 60 600 Tm (BIG) Tj ET\n")
	full, _ := inkPixels(t, src, 0)
	part, img := inkPixels(t, src, 20)
	if part == 0 || part >= full {
		t.Fatalf("the threshold drew %d of %d pixels", part, full)
	}
	// The large text is untouched. Its baseline is at 600 in PDF space,
	// which on an A4 page at 100 dpi is around row 336 from the top.
	if n := inkInBand(img, 290, 350); n == 0 {
		t.Error("the large text was not drawn")
	}
	// And the small text is gone: its baseline is at 700, near row 197.
	if n := inkInBand(img, 180, 205); n != 0 {
		t.Errorf("%d pixels of the small text survived the threshold", n)
	}
}

// inkInBand counts inked pixels between two rows.
func inkInBand(img image.Image, y0, y1 int) int {
	b := img.Bounds()
	n := 0
	for y := y0; y < y1 && y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			if r, _, _, a := img.At(b.Min.X+x, b.Min.Y+y).RGBA(); a > 0x4000 && r < 0xE000 {
				n++
			}
		}
	}
	return n
}
