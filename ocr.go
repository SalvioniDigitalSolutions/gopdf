package gopdf

import (
	"fmt"
	"image"
	"strings"
)

// Finding text that is not text.
//
// A scanned letterhead is pixels. Nothing in the content stream says
// "Ada Lovelace"; the name is a shape, and every rule in this package
// that works on characters walks straight past it. Redacting such a
// document by text alone produces a file that looks carefully handled
// and still shows the name to anyone who looks at it.
//
// So an engine can be plugged in to read the pixels. This package does
// not carry one: recognising text well is a large problem with mature
// solutions, and a poor one here would be worse than none, since a word
// it misses is a word left in a document someone believes is clean. What
// this package does is the part it is placed to do — turning the boxes an
// engine reports into regions of the right image, scrubbing those pixels,
// and reading the result back to confirm the words are gone.

// OCRWord is one word an engine recognised, positioned in the image's own
// pixel coordinates with the origin at the top-left.
type OCRWord struct {
	// Text is the word as recognised.
	Text string
	// X, Y, W and H bound the word in pixels.
	X, Y, W, H int
	// Confidence runs from 0 to 1. An engine that does not report one
	// should leave it at 1.
	Confidence float64
}

// OCREngine reads the text in an image.
//
// Implementations live outside this package: see ocr/tesseract for one
// that drives the tesseract command. An engine is called once per image
// per page, and may be called again on the redacted image to confirm the
// words are gone, so it should be safe to call more than once.
type OCREngine interface {
	Recognize(img image.Image) ([]OCRWord, error)
}

// SetOCR supplies an engine that reads text in images, so that Text and
// Pattern also match words inside a scan. A word that matches has its
// pixels scrubbed, exactly as an Area covering it would.
//
// Recognition is not exhaustive. An engine misses words, especially on
// poor scans, and a word it misses stays in the document. Review Marks
// before relying on the result, and prefer Area where the region to
// remove is known.
func (rd *Redactor) SetOCR(e OCREngine) {
	rd.ocr = e
	rd.planned = false
}

// SetOCRConfidence ignores recognised words the engine is less sure of
// than min, which runs from 0 to 1. The default is 0: for redaction a
// doubtful match is still worth removing, since removing too much is the
// lesser mistake.
func (rd *Redactor) SetOCRConfidence(min float64) {
	rd.ocrMinConf = min
	rd.planned = false
}

// ocrRegions runs the engine over one image and returns the fractions of
// it covered by words that should go, together with what they said.
func (rd *Redactor) ocrRegions(img ImageRef) ([]rect, []OCRWord, error) {
	if rd.ocr == nil {
		return nil, nil, nil
	}
	pixels, err := img.Decode()
	if err != nil {
		// An image whose pixels cannot be read cannot be searched. It is
		// left alone here; an Area still covers it.
		return nil, nil, nil
	}
	words, err := rd.ocr.Recognize(pixels)
	if err != nil {
		return nil, nil, fmt.Errorf("gopdf: reading the text in an image: %w", err)
	}
	b := pixels.Bounds()
	w, h := float64(b.Dx()), float64(b.Dy())
	if w <= 0 || h <= 0 {
		return nil, nil, nil
	}
	var regions []rect
	var matched []OCRWord
	for _, word := range words {
		if word.Confidence < rd.ocrMinConf || strings.TrimSpace(word.Text) == "" {
			continue
		}
		if !rd.matchesAnyRule(word.Text) {
			continue
		}
		// The engine's pixel box becomes a fraction of the image, which
		// is what the scrubber works in.
		regions = append(regions, rect{
			x0: clamp01(float64(word.X) / w),
			y0: clamp01(float64(word.Y) / h),
			x1: clamp01(float64(word.X+word.W) / w),
			y1: clamp01(float64(word.Y+word.H) / h),
		})
		matched = append(matched, word)
	}
	return regions, matched, nil
}

// matchesAnyRule reports whether a recognised word is covered by one of
// the literal or pattern rules.
//
// An engine reports one word at a time, so a literal naming several —
// "Ada Lovelace" — never matches a single box. Each word of the literal
// is therefore matched on its own. That removes a little more than was
// asked for, since a lone "Ada" elsewhere in the scan also goes, which is
// the right way round for redaction: what is left behind cannot be
// undone, what is taken can be seen in Marks.
func (rd *Redactor) matchesAnyRule(text string) bool {
	word := strings.Trim(text, ".,;:()[]{}\"'`?!")
	for _, lit := range rd.literals {
		if strings.Contains(text, lit) {
			return true
		}
		for _, part := range strings.Fields(lit) {
			if len(part) > 2 && word == part {
				return true
			}
		}
	}
	for _, re := range rd.patterns {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// pageRectFor converts a fraction of an image into the page rectangle it
// occupies, so a mark can say where on the page the word was.
func pageRectFor(img ImageRef, r rect) rect {
	return rect{
		x0: img.X + r.x0*img.W,
		y0: img.Y + r.y0*img.H,
		x1: img.X + r.x1*img.W,
		y1: img.Y + r.y1*img.H,
	}
}

// verifyOCR reads the written document's images again and reports any
// word that a rule should have removed and that can still be read.
//
// This is the only check that reaches text in a scan: nothing extracts
// it, so the ordinary read-back cannot see it either.
func (rd *Redactor) verifyOCR(out []byte) error {
	if rd.ocr == nil {
		return nil
	}
	r, err := NewReader(out)
	if err != nil {
		return fmt.Errorf("gopdf: the redacted document could not be read back: %w", err)
	}
	// Every image the document still reaches, not only the ones a page
	// draws: a thumbnail or an alternate is exactly the copy that looking
	// at the drawn images would miss.
	for _, img := range allImageRefs(r) {
		_, matched, err := rd.ocrRegions(img)
		if err != nil {
			return err
		}
		if len(matched) > 0 {
			return fmt.Errorf("gopdf: %q can still be read in an image after "+
				"redaction; the output has been withheld", matched[0].Text)
		}
	}
	return nil
}
