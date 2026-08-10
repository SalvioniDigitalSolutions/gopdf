package gopdf

import (
	"bytes"
	"strings"
	"testing"
)

func TestLiteralRangesWordBoundaries(t *testing.T) {
	cases := []struct {
		text, lit string
		mode      matchMode
		want      int
	}{
		{"Sig. Rossi, avvocato", "Rossi", matchWords, 1},
		{"Rossini sang", "Rossi", matchWords, 0},    // inside a longer word
		{"Rossini sang", "Rossi", matchAnywhere, 1}, // unless asked
		{"Rossi", "Rossi", matchWords, 1},           // whole text
		{"di Rossi", "Rossi", matchWords, 1},        // at the end
		{"Rossi-Bianchi", "Rossi", matchWords, 1},   // punctuation is not a letter
		{"CHE-290.921.033", "290.921.033", matchWords, 1},
		{"5123 items", "123", matchWords, 0}, // digits count as word characters
		{"a 123 b", "123", matchWords, 1},
		{"aaa", "aa", matchWords, 0},        // flanked by a letter
		{"x@y.com", "y.com", matchWords, 1}, // @ is punctuation, so this matches
		{"ada@example.com", "ada@example.com", matchWords, 1},
	}
	for _, c := range cases {
		got := len(literalRanges(c.text, c.lit, c.mode))
		if got != c.want {
			t.Errorf("literalRanges(%q, %q, %v) found %d, want %d",
				c.text, c.lit, c.mode, got, c.want)
		}
	}
	if literalRanges("abc", "", matchWords) != nil {
		t.Error("an empty literal should match nothing")
	}
}

func TestWordBoundedEdges(t *testing.T) {
	if !wordBounded("Rossi", 0, 5) {
		t.Error("the whole text is bounded")
	}
	if wordBounded("Rossini", 0, 5) {
		t.Error("a following letter breaks the boundary")
	}
	if wordBounded("xRossi", 1, 6) {
		t.Error("a preceding letter breaks the boundary")
	}
	if !wordBounded("(Rossi)", 1, 6) {
		t.Error("brackets do not break a boundary")
	}
}

func TestIsWordRune(t *testing.T) {
	for _, r := range []rune{'a', 'Z', '5', 'é', 'ß'} {
		if !isWordRune(r) {
			t.Errorf("%q should be a word character", r)
		}
	}
	for _, r := range []rune{' ', '-', '.', '@', '\n', '('} {
		if isWordRune(r) {
			t.Errorf("%q should not be a word character", r)
		}
	}
}

// TestRedactWordBoundaries is the over-replacement regression: removing
// "Rossi" must not take "Rossini" with it.
func TestRedactWordBoundaries(t *testing.T) {
	doc := New()
	p := doc.AddPage()
	p.SetFont(Helvetica, 11)
	p.Text(60, 100, "Sig. Rossi met Rossini at the opera.")
	src := docBytes(t, doc)

	r, _ := NewReader(src)
	rd := Redact(r)
	rd.Text("Rossi")
	out := redactTo(t, rd)
	got := collapse(extractAll(t, out))
	if !strings.Contains(got, "Rossini") {
		t.Errorf("Rossini was removed along with Rossi: %q", got)
	}
	if strings.Contains(got, "Sig. Rossi met") {
		t.Errorf("Rossi was not removed: %q", got)
	}

	// And the old behaviour is available.
	r2, _ := NewReader(src)
	loose := Redact(r2)
	loose.MatchSubstrings(true)
	loose.Text("Rossi")
	got2 := collapse(extractAll(t, redactTo(t, loose)))
	if strings.Contains(got2, "Rossini") {
		t.Errorf("MatchSubstrings(true) should have taken Rossini too: %q", got2)
	}
}

func TestPseudonymizeWordBoundaries(t *testing.T) {
	doc := New()
	p := doc.AddPage()
	p.SetFont(Helvetica, 11)
	p.Text(60, 100, "Sig. Rossi met Rossini at the opera.")
	src := docBytes(t, doc)

	var out bytes.Buffer
	if _, err := Pseudonymize(NewReaderOrFail(t, src), &out,
		[]Pseudonym{{From: "Rossi", To: "[[P1]]"}}); err != nil {
		t.Fatal(err)
	}
	got := collapse(extractAll(t, out.Bytes()))
	if !strings.Contains(got, "Rossini") {
		t.Errorf("Rossini was replaced too: %q", got)
	}
	if !strings.Contains(got, "Sig. [[P1]] met") {
		t.Errorf("Rossi was not replaced: %q", got)
	}
}

// TestPseudonymizeBoundaryVariants checks the boundary rule holds across
// the non-breaking-space and soft-hyphen spellings too.
func TestPseudonymizeBoundaryVariants(t *testing.T) {
	doc := New()
	p := doc.AddPage()
	p.SetFont(Helvetica, 11)
	p.Text(60, 100, "Ada Lovelace and Adalovelacex are different.")
	src := docBytes(t, doc)

	var out bytes.Buffer
	if _, err := Pseudonymize(NewReaderOrFail(t, src), &out,
		[]Pseudonym{{From: "Ada Lovelace", To: "[[P1]]"}}); err != nil {
		t.Fatal(err)
	}
	got := collapse(extractAll(t, out.Bytes()))
	if !strings.Contains(got, "[[P1]]") {
		t.Errorf("the nbsp spelling was not matched: %q", got)
	}
	if !strings.Contains(got, "Adalovelacex") {
		t.Errorf("a longer word was caught up in it: %q", got)
	}
}
