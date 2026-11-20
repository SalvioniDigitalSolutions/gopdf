package gopdf

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

// redactMarks redacts the page content with what set asks for and returns
// the marks and the copy's text.
func redactMarks(t *testing.T, content string, set func(*Redactor)) ([]RedactionMark, string) {
	t.Helper()
	rd := Redact(NewReaderOrFail(t, rawPageDoc(t, content)))
	set(rd)
	var b bytes.Buffer
	if _, err := rd.WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	marks, err := rd.Marks()
	if err != nil {
		t.Fatal(err)
	}
	text, err := NewReaderOrFail(t, b.Bytes()).PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	return marks, text
}

// A line that parts its words with moves, as TeX sets them: the bar over
// a word stands where the word is drawn. Measured one character after
// another, it stood a space to the left for every word before it.
func TestBarStandsOverAWordOnALineSetWithMoves(t *testing.T) {
	content := "BT /F1 10 Tf 1 0 0 1 100 700 Tm [(Hello) -333 (big) -333 (wide) -333 (world)] TJ ET\n"
	marks, text := redactMarks(t, content, func(rd *Redactor) { rd.Text("world") })
	if strings.Contains(text, "world") {
		t.Fatalf("world still read: %q", text)
	}
	want := 100 + Helvetica.TextWidth("Hellobigwide", 10) + 3*3.33
	var found bool
	for _, m := range marks {
		if m.Kind == RedactText && m.Text == "world" {
			found = true
			if math.Abs(m.X-want) > 1e-6 || math.Abs(m.W-Helvetica.TextWidth("world", 10)) > 1e-6 {
				t.Errorf("bar at %.3f, %.3f wide; the word is at %.3f, %.3f wide", m.X, m.W, want, Helvetica.TextWidth("world", 10))
			}
		}
	}
	if !found {
		t.Fatalf("no mark for world: %+v", marks)
	}
}

// A row of table headers drawn as one array: an area over the middle
// column takes the middle word, and only it.
func TestAreaTakesTheWordItCoversInOneArray(t *testing.T) {
	content := "BT /F1 10 Tf 1 0 0 1 100 700 Tm [(First) -3000 (Fourth) -2500 (Third)] TJ ET\n"
	first := Helvetica.TextWidth("First", 10)
	fourth := Helvetica.TextWidth("Fourth", 10)
	x0 := 100 + first + 30 // where Fourth is drawn
	// The page is 842 high (A4 in this package's documents): the baseline
	// at 700 from the bottom is 142 from the top.
	y := 842.0 - 700
	_, text := redactMarks(t, content, func(rd *Redactor) { rd.Area(0, x0-1, y-9, fourth+2, 12) })
	if strings.Contains(text, "Fourth") {
		t.Errorf("Fourth still read: %q", text)
	}
	if !strings.Contains(text, "First") || !strings.Contains(text, "Third") {
		t.Errorf("a word the area does not cover went: %q", text)
	}
}

// A run whose words are parted by space characters is measured as it
// always was: its bars do not move.
func TestRunsWithoutMovesAreMeasuredAsBefore(t *testing.T) {
	content := "BT /F1 10 Tf 1 0 0 1 100 700 Tm (Hello big wide world) Tj ET\n" +
		"BT /F1 10 Tf 1 0 0 1 100 680 Tm [(Hello) -40 (big wide world)] TJ ET\n"
	marks, _ := redactMarks(t, content, func(rd *Redactor) { rd.Text("world") })
	// Measured one character after another, the kern of the second line
	// not counted, as it always was.
	want := map[float64]bool{
		100 + Helvetica.TextWidth("Hello big wide ", 10): true,
		100 + Helvetica.TextWidth("Hellobig wide ", 10):  true,
	}
	for _, m := range marks {
		if m.Kind == RedactText && m.Text == "world" {
			if !want[m.X] {
				t.Errorf("bar at %.4f, want one of %v as before", m.X, want)
			}
			delete(want, m.X)
		}
	}
	if len(want) != 0 {
		t.Errorf("no bar at %v: %+v", want, marks)
	}
}

// wordStarts returns where each word of a page's text pieces starts, by
// the pieces' own placing of their characters.
func wordStarts(t *testing.T, data []byte) map[string]float64 {
	t.Helper()
	frags, err := NewReaderOrFail(t, data).PageTextFragments(0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, f := range frags {
		rs := []rune(f.Text)
		if len(f.Advances) != len(rs)+1 {
			t.Fatalf("%q is not placed", f.Text)
		}
		for j := 0; j < len(rs); {
			if rs[j] == ' ' {
				j++
				continue
			}
			k := j
			for k < len(rs) && rs[k] != ' ' {
				k++
			}
			out[string(rs[j:k])] = f.X + f.Advances[j]
			j = k
		}
	}
	return out
}

// Taking a word out of a line set with moves for spaces keeps every move:
// what is kept stays where it was drawn, the line reads as it did less
// the word, and what is drawn after the line stays in place. Written as
// the kept codes one after another, "Hello big wide world" less "big"
// read "Hello wideworld", every word after the cut moved left.
func TestRemovingAWordKeepsTheMovesOfItsLine(t *testing.T) {
	line := "[(Hello) -333 (big) -333 (wide) -333 (world)] TJ"
	content := "BT /F1 10 Tf 1 0 0 1 100 700 Tm " + line + " (after) Tj ET\n"
	src := rawPageDoc(t, content)
	before := wordStarts(t, src)
	for _, c := range []struct {
		word, reads string
	}{
		{"big", "Hello wide world"},
		{"Hello", "big wide world"},
		{"world", "Hello big wide"},
	} {
		rd := Redact(NewReaderOrFail(t, src))
		rd.Text(c.word)
		var b bytes.Buffer
		if _, err := rd.WriteTo(&b); err != nil {
			t.Fatal(err)
		}
		text, err := NewReaderOrFail(t, b.Bytes()).PageText(0)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(text, c.reads) || strings.Contains(text, c.word) {
			t.Errorf("%s out: the copy reads %q, want %q", c.word, text, c.reads)
		}
		after := wordStarts(t, b.Bytes())
		for w, x := range before {
			if w == c.word {
				continue
			}
			if got, ok := after[w]; !ok || math.Abs(got-x) > 1e-6 {
				t.Errorf("%s out: %s drawn at %.4f, was at %.4f", c.word, w, got, x)
			}
		}
	}
}
