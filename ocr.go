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

// SetOCRWordFilter decides which words of a literal may match on their
// own inside an image.
//
// An engine reports one word at a time, so a literal naming several is
// looked for twice. Where its words stand together in reading order the
// whole run goes, whatever they are. Elsewhere each word is looked for by
// itself, which is what catches a surname mentioned alone further down
// the page — and is also what bars every "die" in a scan because one
// literal was "Stiftung für die Kinder". That protects nothing and leaves
// a page nobody can read.
//
// Reading order is worked out from the boxes the engine reports, and a
// badly tilted scan or an unusual layout can defeat it. A literal not
// recognised as written out is then left to the second search, where a
// word the filter holds back stays. A filter trades that risk for a
// readable page: let it hold back only words that identify nobody.
//
// alone is asked about each word of a literal, as the rule spelled it,
// and reports whether that word may be removed where it stands by itself.
// Which words are too common to identify anyone depends on the language
// of the document, so the list is the caller's to keep; this package
// carries none.
//
// The filter only narrows the second search. A literal seen whole — in
// one box or across neighbouring ones — is removed regardless, and so is
// anything a Pattern matches. The read-back after writing applies the
// same rule, so a word the filter let stand is not reported as left
// behind.
//
// The default, restored by passing nil, lets a word match alone when it
// is longer than two bytes.
func (rd *Redactor) SetOCRWordFilter(alone func(word string) bool) {
	rd.ocrAlone = alone
	rd.planned = false
}

// matchesAlone reports whether one word of a longer literal may match by
// itself.
func (rd *Redactor) matchesAlone(part string) bool {
	if rd.ocrAlone != nil {
		return rd.ocrAlone(part)
	}
	return len(part) > 2
}

// ocrHit is one recognised word that a rule covers.
type ocrHit struct {
	word OCRWord
	// region is the fraction of the image the word occupies, which is
	// what the scrubber works in.
	region rect
	// literal is the rule that covered the word where it was read in
	// place — written out by neighbouring words, or whole in this one
	// box — and empty where only the one-word rule matched it.
	literal string
}

// ocrRegions runs the engine over one image and returns the words in it
// that should go, each with the part of the image it covers. cleared
// lists the parts of the image that are being removed anyway, as
// fractions of it: the words there will not be read again, which matters
// to the words beside them.
func (rd *Redactor) ocrRegions(img ImageRef, cleared []rect) ([]ocrHit, error) {
	if rd.ocr == nil {
		return nil, nil
	}
	pixels, err := img.Decode()
	if err != nil {
		// An image whose pixels cannot be read cannot be searched. It is
		// left alone here; an Area still covers it.
		return nil, nil
	}
	words, err := rd.ocr.Recognize(pixels)
	if err != nil {
		return nil, fmt.Errorf("gopdf: reading the text in an image: %w", err)
	}
	b := pixels.Bounds()
	w, h := float64(b.Dx()), float64(b.Dy())
	if w <= 0 || h <= 0 {
		return nil, nil
	}
	// Words the engine doubts, and boxes with nothing in them, are set
	// aside before anything is matched.
	kept := make([]OCRWord, 0, len(words))
	for _, word := range words {
		if word.Confidence < rd.ocrMinConf || strings.TrimSpace(word.Text) == "" {
			continue
		}
		kept = append(kept, word)
	}
	var leaving []bool
	if len(cleared) > 0 {
		leaving = make([]bool, len(kept))
		for i, word := range kept {
			// A word is judged by its middle: an engine does not read a
			// word whose middle has been painted over.
			x := (float64(word.X) + float64(word.W)/2) / w
			y := (float64(word.Y) + float64(word.H)/2) / h
			for _, c := range cleared {
				if x >= c.x0 && x <= c.x1 && y >= c.y0 && y <= c.y1 {
					leaving[i] = true
					break
				}
			}
		}
	}
	goes, literal := rd.ocrMatches(kept, leaving)
	var hits []ocrHit
	for i, word := range kept {
		if !goes[i] {
			continue
		}
		// The engine's pixel box becomes a fraction of the image.
		hits = append(hits, ocrHit{
			word:    word,
			literal: literal[i],
			region: rect{
				x0: clamp01(float64(word.X) / w),
				y0: clamp01(float64(word.Y) / h),
				x1: clamp01(float64(word.X+word.W) / w),
				y1: clamp01(float64(word.Y+word.H) / h),
			},
		})
	}
	return hits, nil
}

// ocrMaxPasses bounds how often the words left standing are read again.
// Two passes settle an ordinary page. A page built to need one pass per
// word is stopped here, and whatever the plan then missed the read-back
// finds, and refuses.
const ocrMaxPasses = 16

// ocrMatches decides which of the words read in one image should go, and
// for those covered by a literal read in place, by which. leaving marks
// the words that something other than a rule is already removing — an
// Area drawn over them — and may be nil.
//
// A literal spelled out by neighbouring words takes all of them; what is
// left is matched a word at a time. Then the words that stay are read
// once more, and again until nothing changes, because taking a word out
// brings its neighbours together: with "Kinder" gone from "für Kinder
// die", what remains reads "für die", and if that is a literal it is now
// on the page. The read-back sees the image as it will be, not as it
// was, so the plan has to as well — otherwise it removes one thing and
// the check refuses the document over another.
func (rd *Redactor) ocrMatches(words []OCRWord, leaving []bool) (goes []bool, literal []string) {
	goes = make([]bool, len(words))
	literal = make([]string, len(words))
	left := make([]int, len(words))
	for i := range left {
		left[i] = i
	}
	for pass := 0; len(left) > 0 && pass < ocrMaxPasses; pass++ {
		standing := make([]OCRWord, len(left))
		for k, i := range left {
			standing[k] = words[i]
		}
		phrases := rd.ocrPhrases(standing)
		found := false
		rest := left[:0]
		for k, i := range left {
			switch {
			case phrases[k] != "":
				goes[i], literal[i] = true, phrases[k]
			// A word judged by itself is judged the same however often
			// it is asked, so once is enough.
			case pass == 0 && rd.matchesAnyRule(words[i].Text):
				goes[i] = true
			// A word under an Area is read with the rest the first
			// time, so a literal it is part of is still found, and is
			// gone from then on whether a rule wanted it or not.
			case pass == 0 && leaving != nil && leaving[i]:
			default:
				rest = append(rest, i)
				continue
			}
			found = true
		}
		left = rest
		if !found {
			break
		}
	}
	return goes, literal
}

// matchesAnyRule reports whether a recognised word, taken by itself, is
// covered by one of the literal or pattern rules.
//
// An engine reports one word at a time, so a literal naming several —
// "Ada Lovelace" — never matches a single box. Each word of the literal
// is therefore matched on its own. That removes a little more than was
// asked for, since a lone "Ada" elsewhere in the scan also goes, which is
// the right way round for redaction: what is left behind cannot be
// undone, what is taken can be seen in Marks. Which words may go this
// way is for SetOCRWordFilter to say.
func (rd *Redactor) matchesAnyRule(text string) bool {
	word := strings.Trim(text, ".,;:()[]{}\"'`?!")
	for _, lit := range rd.literals {
		if containsBounded(text, lit, rd.mode()) {
			return true
		}
		for _, part := range strings.Fields(lit) {
			if word == part && rd.matchesAlone(part) {
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
		hits, err := rd.ocrRegions(img, nil)
		if err != nil {
			return err
		}
		if len(hits) > 0 {
			return fmt.Errorf("gopdf: %q can still be read in an image after "+
				"redaction; the output has been withheld", hits[0].word.Text)
		}
	}
	return nil
}
