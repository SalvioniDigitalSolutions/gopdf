package gopdf

import (
	"bytes"
	"fmt"
	"image"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// functionWords stands for the list a caller keeps. The package has none
// of its own: which words say nothing depends on the language.
var functionWords = map[string]bool{
	"die": true, "für": true, "von": true, "AG": true, "GmbH": true,
}

// notFunctionWord is a filter as a caller would write one.
func notFunctionWord(w string) bool { return len(w) > 2 && !functionWords[w] }

// The literal the tests below look for: a name made mostly of words that
// identify nobody.
const foundation = "Stiftung für die Kinder AG"

// line lays words out along one line of an image, left to right.
func line(y int, texts ...string) []OCRWord {
	var out []OCRWord
	x := 10
	for _, text := range texts {
		w := 8 * len([]rune(text))
		out = append(out, OCRWord{Text: text, X: x, Y: y, W: w, H: 12, Confidence: 1})
		x += w + 6
	}
	return out
}

// letter is a scan in which the literal is written out once, its function
// words turn up again in a sentence, and one of its telling words turns
// up again alone.
func letter() []OCRWord {
	var words []OCRWord
	words = append(words, line(10, "Stiftung", "für", "die", "Kinder", "AG")...)
	words = append(words, line(60, "Wir", "danken", "für", "die", "Hilfe")...)
	words = append(words, line(110, "Kinder", "spielen")...)
	return words
}

// markedOn redacts a scan carrying words and returns what was marked, as
// "word@y" with y the top of the word's line in the image.
func markedOn(t *testing.T, words []OCRWord, setup func(rd *Redactor)) []string {
	t.Helper()
	const w, h = 400, 200
	r, err := NewReader(scanDoc(t, w, h))
	if err != nil {
		t.Fatal(err)
	}
	rd := Redact(r)
	rd.SetOCR(&stubOCR{gone: true, words: words})
	setup(rd)
	marks, err := rd.Marks()
	if err != nil {
		t.Fatal(err)
	}
	page, _ := r.PageSize(0)
	var out []string
	for _, m := range marks {
		if m.Kind != RedactImageText {
			t.Errorf("unexpected mark kind %q", m.Kind)
		}
		out = append(out, fmt.Sprintf("%s@%.0f", m.Text, m.Y/page.H*h))
	}
	sort.Strings(out)
	return out
}

func sorted(s ...string) []string {
	sort.Strings(s)
	return s
}

// TestOCRWordFilterDefault pins what happens with no filter set: a word
// of a literal matches alone when it is longer than two bytes.
func TestOCRWordFilterDefault(t *testing.T) {
	var words []OCRWord
	words = append(words, line(10, "die")...)
	words = append(words, line(60, "Kinder")...)
	words = append(words, line(110, "AG")...)
	words = append(words, line(160, "Hilfe")...)

	got := markedOn(t, words, func(rd *Redactor) { rd.Text(foundation) })
	if want := sorted("die@10", "Kinder@60"); !reflect.DeepEqual(got, want) {
		t.Errorf("default rule marked %v, want %v", got, want)
	}

	// Passing nil goes back to it.
	got = markedOn(t, words, func(rd *Redactor) {
		rd.Text(foundation)
		rd.SetOCRWordFilter(notFunctionWord)
		rd.SetOCRWordFilter(nil)
	})
	if want := sorted("die@10", "Kinder@60"); !reflect.DeepEqual(got, want) {
		t.Errorf("after resetting the filter: marked %v, want %v", got, want)
	}
}

// TestOCRWordFilter is the page that came out unreadable: one literal
// with an article in it, and every article in the scan barred.
func TestOCRWordFilter(t *testing.T) {
	// Without a filter the sentence loses its "für" and its "die".
	got := markedOn(t, letter(), func(rd *Redactor) { rd.Text(foundation) })
	want := sorted(
		"Stiftung@10", "für@10", "die@10", "Kinder@10", "AG@10",
		"für@60", "die@60",
		"Kinder@110",
	)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("without a filter: marked %v, want %v", got, want)
	}

	// With one, the literal still goes whole where it is written out, the
	// telling word still goes where it stands alone, and the sentence is
	// left as it was.
	got = markedOn(t, letter(), func(rd *Redactor) {
		rd.Text(foundation)
		rd.SetOCRWordFilter(notFunctionWord)
	})
	want = sorted(
		"Stiftung@10", "für@10", "die@10", "Kinder@10", "AG@10",
		"Kinder@110",
	)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("with a filter: marked %v, want %v", got, want)
	}
}

// TestOCRWordFilterAsksAboutTheRulesWord checks what the filter is shown:
// the word as the literal spelled it, and only once a recognised word has
// turned out to be that word.
func TestOCRWordFilterAsksAboutTheRulesWord(t *testing.T) {
	asked := map[string]bool{}
	markedOn(t, line(10, "Kinder,", "Hilfe"), func(rd *Redactor) {
		rd.Text(foundation)
		rd.SetOCRWordFilter(func(w string) bool {
			asked[w] = true
			return true
		})
	})
	if !asked["Kinder"] {
		t.Errorf("the filter was not asked about the matched word: %v", asked)
	}
	for w := range asked {
		if w != "Kinder" {
			t.Errorf("the filter was asked about %q, which no recognised word was", w)
		}
	}
}

// TestOCRWordFilterLeavesWholeRulesAlone: the filter narrows the search
// for parts of a literal, and nothing else.
func TestOCRWordFilterLeavesWholeRulesAlone(t *testing.T) {
	never := func(string) bool { return false }

	// A literal that is itself a function word was asked for by name.
	got := markedOn(t, letter(), func(rd *Redactor) {
		rd.Text("die")
		rd.SetOCRWordFilter(never)
	})
	if want := sorted("die@10", "die@60"); !reflect.DeepEqual(got, want) {
		t.Errorf("a one-word literal: marked %v, want %v", got, want)
	}

	// A pattern is not a literal at all.
	got = markedOn(t, line(10, "Konto", "4815162342"), func(rd *Redactor) {
		rd.Pattern(regexp.MustCompile(`^\d{10}$`))
		rd.SetOCRWordFilter(never)
	})
	if want := sorted("4815162342@10"); !reflect.DeepEqual(got, want) {
		t.Errorf("a pattern: marked %v, want %v", got, want)
	}

	// A literal the engine put in a single box.
	got = markedOn(t, []OCRWord{
		{Text: "Ada Lovelace", X: 10, Y: 10, W: 90, H: 12, Confidence: 1},
	}, func(rd *Redactor) {
		rd.Text("Ada Lovelace")
		rd.SetOCRWordFilter(never)
	})
	if want := sorted("Ada Lovelace@10"); !reflect.DeepEqual(got, want) {
		t.Errorf("a literal in one box: marked %v, want %v", got, want)
	}
}

// TestOCRPhraseTakesShortWords: written out, a literal goes with the
// words too short to be looked for alone.
func TestOCRPhraseTakesShortWords(t *testing.T) {
	var words []OCRWord
	words = append(words, line(10, "Dear", "Al", "Gore,", "welcome")...)
	words = append(words, line(60, "Al", "fresco")...)
	got := markedOn(t, words, func(rd *Redactor) { rd.Text("Al Gore") })
	if want := sorted("Al@10", "Gore,@10"); !reflect.DeepEqual(got, want) {
		t.Errorf("marked %v, want %v", got, want)
	}
}

// TestOCRPhraseAcrossLines: an address block breaks a name wherever the
// column ends.
func TestOCRPhraseAcrossLines(t *testing.T) {
	var words []OCRWord
	words = append(words, line(10, "An", "die", "Stiftung", "für")...)
	words = append(words, line(60, "die", "Kinder", "AG", "in", "Bern")...)
	got := markedOn(t, words, func(rd *Redactor) {
		rd.Text(foundation)
		rd.SetOCRWordFilter(notFunctionWord)
	})
	// The "die" before the name is not part of it.
	want := sorted("Stiftung@10", "für@10", "die@60", "Kinder@60", "AG@60")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("marked %v, want %v", got, want)
	}
}

// TestOCRPhraseNeedsTheWordsTogether: the same words scattered about are
// not the literal.
func TestOCRPhraseNeedsTheWordsTogether(t *testing.T) {
	var words []OCRWord
	words = append(words, line(10, "die", "Stiftung", "dankt", "für", "die", "Hilfe")...)
	words = append(words, line(60, "der", "AG")...)
	got := markedOn(t, words, func(rd *Redactor) {
		rd.Text(foundation)
		rd.SetOCRWordFilter(notFunctionWord)
	})
	if want := sorted("Stiftung@10"); !reflect.DeepEqual(got, want) {
		t.Errorf("marked %v, want %v", got, want)
	}
}

// TestOCRPhraseSpacing: a rule may space its words however its source
// did.
func TestOCRPhraseSpacing(t *testing.T) {
	for _, lit := range []string{
		"Stiftung für die  Kinder AG",
		"Stiftung für die Kinder\tAG",
	} {
		got := markedOn(t, letter()[:5], func(rd *Redactor) {
			rd.Text(lit)
			rd.SetOCRWordFilter(notFunctionWord)
		})
		want := sorted("Stiftung@10", "für@10", "die@10", "Kinder@10", "AG@10")
		if !reflect.DeepEqual(got, want) {
			t.Errorf("literal %q: marked %v, want %v", lit, got, want)
		}
	}
}

// TestOCRReadingOrder: an engine reports in whatever order it likes, and
// boxes on one line rarely share a top edge.
func TestOCRReadingOrder(t *testing.T) {
	words := []OCRWord{
		{Text: "Kinder", X: 140, Y: 11, W: 48, H: 12},
		{Text: "zweite", X: 10, Y: 60, W: 48, H: 12},
		{Text: "für", X: 80, Y: 13, W: 24, H: 9},
		{Text: "Zeile", X: 70, Y: 58, W: 40, H: 14},
		{Text: "Stiftung", X: 10, Y: 10, W: 64, H: 14},
		{Text: "die", X: 110, Y: 9, W: 24, H: 13},
	}
	if got, want := readAcross(words), "Stiftung für die Kinder zweite Zeile"; got != want {
		t.Errorf("read as %q, want %q", got, want)
	}
	if got := readAcross(nil); got != "" {
		t.Errorf("no words read as %q", got)
	}
	// An engine that reports no boxes at all is taken at its word: the
	// order it gave is the only order there is.
	bare := []OCRWord{{Text: "Stiftung"}, {Text: "für"}, {Text: "die"}, {Text: "Kinder"}}
	if got, want := readAcross(bare), "Stiftung für die Kinder"; got != want {
		t.Errorf("without boxes: read as %q, want %q", got, want)
	}

	// And so a literal is found however the words arrived.
	rd := Redact(&Reader{})
	rd.Text("Stiftung für die Kinder")
	covered := rd.ocrPhrases(words)
	for i, w := range words {
		if in := strings.Contains("Stiftung für die Kinder", w.Text); (covered[i] != "") != in {
			t.Errorf("%q covered by %q", w.Text, covered[i])
		}
	}
}

// TestOCRPhraseToken: a word that went with its literal carries the
// literal's token, where by itself it would have found none.
func TestOCRPhraseToken(t *testing.T) {
	r, _ := NewReader(scanDoc(t, 400, 200))
	rd := Redact(r)
	rd.SetOCR(&stubOCR{gone: true, words: letter()})
	rd.SetOCRWordFilter(notFunctionWord)
	rd.SetLabel("[REDACTED]")
	rd.Substitute(foundation, "[[ORG_1]]")
	if _, err := rd.Marks(); err != nil {
		t.Fatal(err)
	}
	if len(rd.bars[0]) != 6 {
		t.Fatalf("got %d bars, want 6", len(rd.bars[0]))
	}
	for _, bar := range rd.bars[0] {
		if bar.label != "[[ORG_1]]" {
			t.Errorf("a bar carries %q, want the literal's token", bar.label)
		}
	}
	// By itself a function word has no claim on the token.
	if got := rd.tokenFor("die"); got != "[REDACTED]" {
		t.Errorf("token for a lone function word = %q", got)
	}
	if got := rd.tokenFor("Kinder"); got != "[[ORG_1]]" {
		t.Errorf("token for a lone telling word = %q", got)
	}
}

// inkOCR reads words off the pixels the way an engine does: a word whose
// box has been painted over is no longer there to read. Boxes are given
// for an image of the stated size and scaled to whatever is handed in.
type inkOCR struct {
	w, h  int
	words []OCRWord
}

func (e *inkOCR) Recognize(img image.Image) ([]OCRWord, error) {
	b := img.Bounds()
	var out []OCRWord
	for _, word := range e.words {
		x := b.Min.X + (word.X+word.W/2)*b.Dx()/e.w
		y := b.Min.Y + (word.Y+word.H/2)*b.Dy()/e.h
		if r, g, bl, _ := img.At(x, y).RGBA(); r>>8 < 40 && g>>8 < 40 && bl>>8 < 40 {
			continue
		}
		out = append(out, word)
	}
	return out, nil
}

// TestOCRPhraseAfterRemoval: taking a word out brings its neighbours
// together. If they then spell a literal, the read-back will find it, so
// the plan must have taken them too — or a document is refused over
// words the plan never looked at.
func TestOCRPhraseAfterRemoval(t *testing.T) {
	var words []OCRWord
	words = append(words, line(10, "Dank", "für", "Kinder", "die", "Hilfe")...)
	words = append(words, line(60, "für", "uns", "die", "Hilfe")...)
	setup := func(rd *Redactor) {
		rd.Text("Kinder")
		rd.Text("für die") // only ever matches as a run: both words are the filter's
		rd.SetOCRWordFilter(notFunctionWord)
	}
	got := markedOn(t, words, setup)
	want := sorted("für@10", "Kinder@10", "die@10")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("marked %v, want %v", got, want)
	}

	// And written against an engine that reads the pixels, the document
	// passes its own check.
	const w, h = 400, 200
	r, _ := NewReader(scanDoc(t, w, h))
	rd := Redact(r)
	rd.SetOCR(&inkOCR{w: w, h: h, words: words})
	setup(rd)
	out := redactTo(t, rd)

	r2, _ := NewReader(out)
	imgs, err := r2.PageImages(0)
	if err != nil || len(imgs) != 1 {
		t.Fatalf("the image is gone: %v", err)
	}
	pixels, err := imgs[0].Decode()
	if err != nil {
		t.Fatal(err)
	}
	left, _ := (&inkOCR{w: w, h: h, words: words}).Recognize(pixels)
	var texts []string
	for _, word := range left {
		texts = append(texts, word.Text)
	}
	if got, want := strings.Join(texts, " "), "Dank Hilfe für uns die Hilfe"; got != want {
		t.Errorf("still readable: %q, want %q", got, want)
	}
}

// readAcross is the words of an image as they are read straight across.
func readAcross(words []OCRWord) string {
	var s []string
	for _, i := range ocrReadings(words)[0] {
		s = append(s, words[i].Text)
	}
	return strings.Join(s, " ")
}

// saveAndRead redacts a scan carrying words against an engine that reads
// pixels, and returns what that engine still reads in the saved file.
func saveAndRead(t *testing.T, w, h int, words []OCRWord, setup func(*Redactor, PageSize)) (string, error) {
	t.Helper()
	r, err := NewReader(scanDoc(t, w, h))
	if err != nil {
		t.Fatal(err)
	}
	page, _ := r.PageSize(0)
	rd := Redact(r)
	rd.SetOCR(&inkOCR{w: w, h: h, words: words})
	setup(rd, page)
	var buf bytes.Buffer
	if _, err := rd.WriteTo(&buf); err != nil {
		return "", err
	}
	r2, _ := NewReader(buf.Bytes())
	imgs, err := r2.PageImages(0)
	if err != nil || len(imgs) != 1 {
		t.Fatalf("the image is gone: %v", err)
	}
	pixels, err := imgs[0].Decode()
	if err != nil {
		t.Fatal(err)
	}
	left, _ := (&inkOCR{w: w, h: h, words: words}).Recognize(pixels)
	var s []string
	for _, word := range left {
		s = append(s, word.Text)
	}
	return strings.Join(s, " "), nil
}

// typeSize is a size of type in pixels: character width, word gap, cap
// height, x-height, descender and line pitch.
type typeSize struct{ charW, gap, capH, xH, desc, pitch int }

// typeset boxes text the way an engine boxes real type: a word with no
// capital or ascender starts lower, a word with a descender ends lower,
// and on a scan turned by deg degrees every box slides down by the
// tangent of the angle times how far along the line it is.
func typeset(lines []string, deg float64, ts typeSize) []OCRWord {
	tan := math.Tan(deg * math.Pi / 180)
	var out []OCRWord
	for l, text := range lines {
		x := 300
		for _, word := range strings.Fields(text) {
			w := ts.charW * len([]rune(word))
			y, h := 300+l*ts.pitch, ts.capH
			if !strings.ContainsAny(word, "ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÜbdfhkltijäöüß0123456789") {
				y, h = y+ts.capH-ts.xH, ts.xH
			}
			if strings.ContainsAny(word, "gjpqy,;") {
				h += ts.desc
			}
			y += int(math.Round(float64(x+w/2) * tan))
			out = append(out, OCRWord{Text: word, X: x, Y: y, W: w, H: h, Confidence: 1})
			x += w + ts.gap
		}
	}
	return out
}

// A letter at 300 dpi in 11 point, its lines 1800 pixels long.
var letterType = typeSize{charW: 16, gap: 12, capH: 33, xH: 23, desc: 9, pitch: 55}

// TestOCRPhraseOnATiltedScan: turned a degree and a half, the far end of
// one line is level with the near end of the next. The lines must not
// run into each other, or a literal written out along one of them is no
// longer read as written out.
func TestOCRPhraseOnATiltedScan(t *testing.T) {
	lines := []string{
		"Sehr geehrte Damen und Herren, wir haben Ihr Schreiben vom dritten Maerz dieses Jahres erhalten und danken Ihnen sehr",
		"herzlich im Namen der Stiftung für die Kinder AG und von Frau Li Na für die grosszügige Spende an unsere Projekte hier.",
	}
	for _, deg := range []float64{0, 1, 1.5, 3, -1.5} {
		words := typeset(lines, deg, letterType)
		if got, want := readAcross(words), strings.Join(lines, " "); got != want {
			t.Errorf("%v degrees: read as\n  %s\nwant\n  %s", deg, got, want)
		}
		left, err := saveAndRead(t, 2480, 700, words, func(rd *Redactor, _ PageSize) {
			rd.Text(foundation)
			rd.Text("Li Na")
			rd.SetOCRWordFilter(notFunctionWord)
		})
		if err != nil {
			t.Fatalf("%v degrees: %v", deg, err)
		}
		want := lines[0] + " herzlich im Namen der und von Frau für die grosszügige Spende an unsere Projekte hier."
		if left != want {
			t.Errorf("%v degrees: still readable\n  %s\nwant\n  %s", deg, left, want)
		}
	}
}

// TestOCRPhraseShortLiteralOnATiltedScan: a name of two short words has
// nothing but the run to be found by, whatever stands on the line above.
func TestOCRPhraseShortLiteralOnATiltedScan(t *testing.T) {
	for shift := 0; shift < 12; shift++ {
		lines := []string{
			strings.Repeat("S", shift+1) + " geehrte Damen und Herren, wir haben Ihr Schreiben vom dritten Maerz dieses Jahres erhalten und danken sehr",
			"herzlich im Namen der Stiftung für die Kinder AG und von Frau Li Na für die grosszügige Spende an unsere Projekte hier.",
		}
		for _, deg := range []float64{0, 1.5} {
			rd := Redact(&Reader{})
			rd.Text("Li Na")
			goes, _ := rd.ocrMatches(typeset(lines, deg, letterType), nil)
			n := 0
			for _, g := range goes {
				if g {
					n++
				}
			}
			if n != 2 {
				t.Errorf("%v degrees, line above shifted by %d: %d words go, want 2", deg, shift, n)
			}
		}
	}
}

// TestOCRPhraseIgnoresASpeck: one stray box at the end of a line — an
// apostrophe, a fleck of dust — is not a word of it and must not change
// how the line is read.
func TestOCRPhraseIgnoresASpeck(t *testing.T) {
	words := []OCRWord{
		{Text: "Herr", X: 10, Y: 100, W: 40, H: 20, Confidence: 1},
		{Text: "Hans", X: 60, Y: 100, W: 40, H: 20, Confidence: 1},
		{Text: "von", X: 110, Y: 106, W: 30, H: 14, Confidence: 1}, // no ascender
		{Text: "Arx", X: 150, Y: 100, W: 30, H: 20, Confidence: 1},
		{Text: "schreibt", X: 190, Y: 100, W: 80, H: 20, Confidence: 1},
		{Text: "'", X: 300, Y: 102, W: 3, H: 3, Confidence: 1},
	}
	for n, want := range map[int]string{5: "Herr schreibt", 6: "Herr schreibt '"} {
		left, err := saveAndRead(t, 400, 200, words[:n], func(rd *Redactor, _ PageSize) {
			rd.Text("Hans von Arx")
			rd.SetOCRWordFilter(notFunctionWord)
		})
		if err != nil {
			t.Fatalf("%d boxes: %v", n, err)
		}
		if left != want {
			t.Errorf("%d boxes: still readable %q, want %q", n, left, want)
		}
	}
}

// TestOCRPhraseDownAColumn: a letterhead sets the address beside the
// date. Read straight across, the date comes between the two lines of
// the name; read down the column, it does not.
func TestOCRPhraseDownAColumn(t *testing.T) {
	var words []OCRWord
	words = append(words, line(10, "Stiftung", "für", "die")...)
	words = append(words, line(28, "Kinder", "AG")...)
	words = append(words, line(46, "Postfach", "für", "die", "Post")...)
	words = append(words,
		OCRWord{Text: "Bern,", X: 250, Y: 10, W: 40, H: 12, Confidence: 1},
		OCRWord{Text: "3.", X: 296, Y: 10, W: 16, H: 12, Confidence: 1},
		OCRWord{Text: "Maerz", X: 318, Y: 10, W: 40, H: 12, Confidence: 1},
		OCRWord{Text: "Ihr", X: 250, Y: 28, W: 24, H: 12, Confidence: 1},
		OCRWord{Text: "Zeichen", X: 280, Y: 28, W: 56, H: 12, Confidence: 1})
	left, err := saveAndRead(t, 400, 200, words, func(rd *Redactor, _ PageSize) {
		rd.Text(foundation)
		rd.SetOCRWordFilter(notFunctionWord)
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "Postfach für die Post Bern, 3. Maerz Ihr Zeichen"; left != want {
		t.Errorf("still readable %q, want %q", left, want)
	}
}

// TestOCRPhraseAcrossColumns is the signature line the filter was made
// for: two parties on one baseline, and a literal that runs from the end
// of the first into the second. It is read straight across.
func TestOCRPhraseAcrossColumns(t *testing.T) {
	words := line(10, "Fuer", "die", "acme", "Digital", "Solutions", "GmbH")
	right := line(10, "Fuer", "die", "Beispiel", "AG")
	for i := range right {
		right[i].X += 420
	}
	words = append(words, right...)
	words = append(words, line(60, "Wir", "informieren", "die", "andere", "Partei")...)
	left, err := saveAndRead(t, 800, 200, words, func(rd *Redactor, _ PageSize) {
		rd.Text("Digital Solutions GmbH Fuer die Beispiel AG")
		rd.SetOCRWordFilter(func(w string) bool {
			return len(w) > 2 && w != "Fuer" && w != "die" && w != "GmbH" && w != "Solutions"
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "Fuer die acme Wir informieren die andere Partei"; left != want {
		t.Errorf("still readable %q, want %q", left, want)
	}
}

// TestOCRPhraseBesideAnArea: an Area takes a word out as surely as a
// rule does, and its neighbours close up just the same.
func TestOCRPhraseBesideAnArea(t *testing.T) {
	const w, h = 400, 200
	words := line(10, "Frau", "Li", "Stempel", "Na", "schreibt")
	for _, c := range []struct {
		area bool
		want string
	}{
		{false, "Frau Li Stempel Na schreibt"},
		{true, "Frau schreibt"},
	} {
		left, err := saveAndRead(t, w, h, words, func(rd *Redactor, page PageSize) {
			rd.Text("Li Na")
			if c.area {
				u := words[2]
				rd.Area(0, float64(u.X)/w*page.W, float64(u.Y)/h*page.H,
					float64(u.W)/w*page.W, float64(u.H)/h*page.H)
			}
		})
		if err != nil {
			t.Errorf("area=%v: %v", c.area, err)
			continue
		}
		if left != c.want {
			t.Errorf("area=%v: still readable %q, want %q", c.area, left, c.want)
		}
	}
}

// TestOCRPhraseTokenFromAnotherRule: a word covered by a Text literal
// still takes the token of a Substitute that names it.
func TestOCRPhraseTokenFromAnotherRule(t *testing.T) {
	for _, substituteFirst := range []bool{false, true} {
		r, _ := NewReader(scanDoc(t, 400, 200))
		rd := Redact(r)
		rd.SetOCR(&stubOCR{gone: true, words: line(10, "Ada", "Lovelace")})
		if substituteFirst {
			rd.Substitute("Lovelace", "[[NAME_1]]")
			rd.Text("Ada Lovelace")
		} else {
			rd.Text("Ada Lovelace")
			rd.Substitute("Lovelace", "[[NAME_1]]")
		}
		if _, err := rd.Marks(); err != nil {
			t.Fatal(err)
		}
		if len(rd.bars[0]) != 2 {
			t.Fatalf("got %d bars, want 2", len(rd.bars[0]))
		}
		if got := rd.bars[0][1].label; got != "[[NAME_1]]" {
			t.Errorf("substitute first=%v: the bar over Lovelace carries %q", substituteFirst, got)
		}
	}
}

// laterOCR reports one set of words the first time it is asked and
// another from then on, standing for an image before and after scrubbing.
type laterOCR struct {
	first, then []OCRWord
	calls       int
}

func (l *laterOCR) Recognize(image.Image) ([]OCRWord, error) {
	l.calls++
	if l.calls == 1 {
		return l.first, nil
	}
	return l.then, nil
}

// TestOCRWordFilterReadBack: the check after writing judges by the same
// rule as the search. A word the filter let stand is not a leak; a word
// it would have taken, still readable, is.
func TestOCRWordFilterReadBack(t *testing.T) {
	sentence := line(60, "Wir", "danken", "für", "die", "Hilfe")
	for _, c := range []struct {
		name    string
		then    []OCRWord
		refused bool
	}{
		{"the sentence is still there", sentence, false},
		{"a telling word is still there", line(110, "Kinder"), true},
		{"the literal is still there", line(10, "Stiftung", "für", "die", "Kinder", "AG"), true},
	} {
		r, _ := NewReader(scanDoc(t, 400, 200))
		rd := Redact(r)
		rd.SetOCR(&laterOCR{first: letter(), then: c.then})
		rd.SetOCRWordFilter(notFunctionWord)
		rd.Text(foundation)
		var buf bytes.Buffer
		_, err := rd.WriteTo(&buf)
		if c.refused {
			if err == nil || !strings.Contains(err.Error(), "still be read in an image") {
				t.Errorf("%s: err = %v, want a refusal", c.name, err)
			}
			if buf.Len() != 0 {
				t.Errorf("%s: a refused document was written", c.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

// TestOCRMatchesAnyRuleFiltered is TestOCRMatchesAnyRule with a filter
// in place.
func TestOCRMatchesAnyRuleFiltered(t *testing.T) {
	rd := Redact(&Reader{})
	rd.Text("Hans von Arx")
	rd.Text(foundation)
	rd.SetOCRWordFilter(notFunctionWord)
	cases := map[string]bool{
		"Hans":      true,
		"Arx":       true,
		"Arx.":      true,  // punctuation the engine attached
		"von":       false, // the filter's
		"die":       false,
		"für":       false,
		"AG":        false,
		"Stiftung":  true,
		"Kinder":    true,
		"Kindern":   false, // not a word of it
		foundation:  true,  // the whole literal in one box
		"Hans von":  false, // part of one, in one box
		"vonseiten": false,
	}
	for text, want := range cases {
		if got := rd.matchesAnyRule(text); got != want {
			t.Errorf("matchesAnyRule(%q) = %v, want %v", text, got, want)
		}
	}
}
