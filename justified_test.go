package gopdf

import (
	"bytes"
	"strings"
	"testing"
)

// Justified text is set by drawing the space between two words and then
// moving the pen the rest of the way to the margin. Reading that move as
// a second word break puts two spaces between every pair of words on the
// page — which nothing notices until someone searches for a name, and
// then a two-word name is never found, because the document says
// "Edoardo  Carlo" and the caller asked for "Edoardo Carlo".

// justifiedDoc draws each word followed by its own space, then advances
// the pen a further amount, which is how a justified line is set.
func justifiedDoc(t *testing.T, extra float64, words ...string) []byte {
	t.Helper()
	const size = 11
	var b strings.Builder
	b.WriteString("BT /F1 11 Tf 1 0 0 1 60 700 Tm")
	for _, w := range words {
		// The word carries its own trailing space, and then the pen is
		// moved on: a TJ number is thousandths of an em, subtracted.
		adj := -extra * 1000 / size
		b.WriteString(" [(" + w + " ) " + fl(adj) + "] TJ")
	}
	b.WriteString(" ET\n")
	return rawPageDoc(t, b.String())
}

func TestJustifiedTextReadsWithSingleSpaces(t *testing.T) {
	src := justifiedDoc(t, 3.0, "Edoardo", "Carlo", "SALVIONI,", "di", "sesso")
	got, err := NewReaderOrFail(t, src).PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "  ") {
		t.Errorf("the justification was read as a second space: %q", got)
	}
	if want := "Edoardo Carlo SALVIONI, di sesso"; !strings.Contains(got, want) {
		t.Errorf("page text is %q, want it to contain %q", got, want)
	}
}

// TestJustifiedTextIsMatchable is the failure as a caller meets it: a
// name spelled with single spaces, which is the only way anyone would
// type it, against a document that draws it justified.
func TestJustifiedTextIsMatchable(t *testing.T) {
	src := justifiedDoc(t, 3.0, "Edoardo", "Carlo", "SALVIONI", "nato", "a", "Lugano")
	for _, needle := range []string{
		"Edoardo Carlo SALVIONI",
		"Carlo SALVIONI",
		"Edoardo Carlo",
		"Edoardo",
	} {
		var buf bytes.Buffer
		res, err := Pseudonymize(NewReaderOrFail(t, src), &buf,
			[]Pseudonym{{From: needle, To: "[REDACTED]"}})
		if err != nil {
			t.Errorf("%q: %v", needle, err)
			continue
		}
		if res.Replaced[needle] == 0 {
			t.Errorf("%q was not found, though the page shows it", needle)
		}
	}
}

// TestJustifiedGapStillMakesASpaceOnItsOwn: the rule only suppresses a
// space that follows one. A wide gap with no space before it is still a
// word break, which is what lets word-positioned text read as words.
func TestJustifiedGapStillMakesASpaceOnItsOwn(t *testing.T) {
	// Words drawn with no space of their own, separated by pen moves.
	var b strings.Builder
	b.WriteString("BT /F1 10 Tf 1 0 0 1 60 700 Tm")
	x := 60.0
	for _, w := range []string{"uno", "due", "tre"} {
		b.WriteString(" 1 0 0 1 " + fl(x) + " 700 Tm (" + w + ") Tj")
		x += Helvetica.TextWidth(w, 10) + Helvetica.TextWidth(" ", 10)
	}
	b.WriteString(" ET\n")
	got, err := NewReaderOrFail(t, rawPageDoc(t, b.String())).PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	if want := "uno due tre"; !strings.Contains(got, want) {
		t.Errorf("page text is %q, want it to contain %q", got, want)
	}
}
