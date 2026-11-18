package gopdf

import (
	"bytes"
	"strings"
	"testing"
)

// A value standing whole in one string the page draws is found there,
// as the checks that read each drawn string apart find it.

// wordsApartDoc sets each word with its own positioning and a gap of
// half a space: the page shows the words apart, the text reads them as
// one ("thatFerrarisuffered"), and the checks that read the drawn
// strings apart read "Ferrari".
func wordsApartDoc(t *testing.T) []byte {
	t.Helper()
	const size = 12.0
	gap := Helvetica.TextWidth(" ", size) * 0.5
	doc := New()
	p := doc.AddPage()
	p.SetFont(Helvetica, size)
	x := 72.0
	for _, w := range []string{"finding", "that", "Ferrari", "suffered", "from", "it."} {
		p.Text(x, 100, w)
		x += Helvetica.TextWidth(w, size) + gap
	}
	p.Text(72, 130, "Signed in Lugano.")
	return docBytes(t, doc)
}

// piecesDoc draws a word in two touching strings, the value standing
// whole in the first: "on Egypt" and "ian Qurans."
func piecesDoc(t *testing.T) []byte {
	t.Helper()
	const size = 12.0
	doc := New()
	p := doc.AddPage()
	p.SetFont(Helvetica, size)
	p.Text(72, 100, "mimic those on Egypt")
	p.Text(72+Helvetica.TextWidth("mimic those on Egypt", size), 100, "ian Qurans.")
	return docBytes(t, doc)
}

// glyphsDoc draws a line a letter at a time, each letter its own string.
func glyphsDoc(t *testing.T, line string) []byte {
	t.Helper()
	const size = 12.0
	doc := New()
	p := doc.AddPage()
	p.SetFont(Helvetica, size)
	x := 72.0
	for _, r := range line {
		p.Text(x, 100, string(r))
		x += Helvetica.TextWidth(string(r), size)
	}
	return docBytes(t, doc)
}

func replaced(t *testing.T, src []byte, from string) string {
	t.Helper()
	var out bytes.Buffer
	if _, err := Pseudonymize(NewReaderOrFail(t, src), &out, []Pseudonym{{From: from, To: "[X]", FitWidth: true}}); err != nil {
		t.Fatalf("refused: %v", err)
	}
	got, _ := NewReaderOrFail(t, out.Bytes()).PageText(0)
	return got
}

func barred(t *testing.T, src []byte, from string) string {
	t.Helper()
	rd := Redact(NewReaderOrFail(t, src))
	rd.Text(from)
	got, _ := NewReaderOrFail(t, redactTo(t, rd)).PageText(0)
	return got
}

func drawnStrings(t *testing.T, src []byte) []string {
	t.Helper()
	frags, err := NewReaderOrFail(t, src).PageTextFragments(0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range frags {
		out = append(out, f.Text)
	}
	return out
}

func TestAValueSetApartAsItsOwnStringIsFound(t *testing.T) {
	src := wordsApartDoc(t)
	if got, _ := NewReaderOrFail(t, src).PageText(0); !strings.Contains(got, "thatFerrarisuffered") {
		t.Fatalf("fixture: the text no longer reads the words as one: %q", got)
	}
	for name, got := range map[string]string{"replaced": replaced(t, src, "Ferrari"), "barred": barred(t, src, "Ferrari")} {
		if strings.Contains(got, "Ferrari") {
			t.Errorf("%s: the name is still read: %q", name, got)
		}
		if !strings.Contains(got, "that") || !strings.Contains(got, "suffered") || !strings.Contains(got, "Signed in Lugano.") {
			t.Errorf("%s: more than the name went: %q", name, got)
		}
	}
	for _, s := range drawnStrings(t, mustReplace(t, src, "Ferrari")) {
		if strings.Contains(s, "Ferrari") {
			t.Errorf("a drawn string still holds the name: %q", s)
		}
	}
}

func mustReplace(t *testing.T, src []byte, from string) []byte {
	t.Helper()
	var out bytes.Buffer
	if _, err := Pseudonymize(NewReaderOrFail(t, src), &out, []Pseudonym{{From: from, To: "[X]", FitWidth: true}}); err != nil {
		t.Fatalf("refused: %v", err)
	}
	return out.Bytes()
}

func TestAValueWholeInOneOfAWordsPiecesIsFound(t *testing.T) {
	src := piecesDoc(t)
	if got, _ := NewReaderOrFail(t, src).PageText(0); !strings.Contains(got, "Egyptian") {
		t.Fatalf("fixture: the text no longer reads the word whole: %q", got)
	}
	for name, got := range map[string]string{"replaced": replaced(t, src, "Egypt"), "barred": barred(t, src, "Egypt")} {
		if strings.Contains(got, "Egypt") {
			t.Errorf("%s: the value is still read: %q", name, got)
		}
		if !strings.Contains(got, "mimic those on") || !strings.Contains(got, "ian Qurans.") {
			t.Errorf("%s: more than the value went: %q", name, got)
		}
	}
}

// A value spread over several strings — a line drawn a letter at a time
// — is not found inside a longer word: no check reads it there either.
func TestAValueInsideAWordDrawnLetterByLetterStays(t *testing.T) {
	src := glyphsDoc(t, "The Annual report")
	if got, _ := NewReaderOrFail(t, src).PageText(0); !strings.Contains(got, "Annual") {
		t.Fatalf("fixture: %q", got)
	}
	var out bytes.Buffer
	if _, err := Pseudonymize(NewReaderOrFail(t, src), &out, []Pseudonym{{From: "Ann", To: "[X]", FitWidth: true}}); err != nil {
		t.Fatalf("refused: %v", err)
	}
	if !bytes.Equal(out.Bytes(), src) {
		if got, _ := NewReaderOrFail(t, out.Bytes()).PageText(0); !strings.Contains(got, "The Annual report") {
			t.Errorf("a value inside a word was taken out: %q", got)
		}
	}
	if got := barred(t, src, "Ann"); !strings.Contains(got, "Annual") {
		t.Errorf("bars: a value inside a word was taken out: %q", got)
	}
	// And a value inside a longer word of one string stays too.
	doc := New()
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(72, 100, "Maestro Rossini wrote it.")
	p.Text(72+Helvetica.TextWidth("Maestro Rossini wrote it.", 12), 100, "Done")
	src = docBytes(t, doc)
	if got := replaced(t, src, "Rossi"); !strings.Contains(got, "Rossini") {
		t.Errorf("Rossi was found inside Rossini: %q", got)
	}
}

// valueHyphenAtBreakDoc sets a value whose own hyphen ends a line:
// "ITC-" at the end of one, "18" at the start of the next.
func valueHyphenAtBreakDoc(t *testing.T) []byte {
	t.Helper()
	doc := New()
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(72, 100, "recording were accomplished using an Instrutech ITC-")
	p.Text(72, 116, "18 interface controlled through a Labview program.")
	p.Text(72, 140, "The ITC-18 was calibrated first.")
	return docBytes(t, doc)
}

// A value whose own hyphen ends a line is found over the break: the
// page's text joins the lines without it ("ITC18"), the check that
// reads the file's strings reads "ITC-18".
func TestAValueWhoseOwnHyphenEndsALineIsFound(t *testing.T) {
	src := valueHyphenAtBreakDoc(t)
	for name, got := range map[string]string{"replaced": replaced(t, src, "ITC-18"), "barred": barred(t, src, "ITC-18")} {
		flat := strings.Join(strings.Fields(got), " ")
		if strings.Contains(flat, "ITC") || strings.Contains(flat, "18 interface") {
			t.Errorf("%s: the value is still read: %q", name, got)
		}
		for _, kept := range []string{"recording were accomplished using an Instrutech", "interface controlled through a Labview program.", "was calibrated first."} {
			if !strings.Contains(flat, kept) {
				t.Errorf("%s: %q went too: %q", name, kept, got)
			}
		}
	}
	// A word divided at a line end is still matched joined.
	doc := New()
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(72, 100, "The tenant Marco Bian-")
	p.Text(72, 116, "chi signed the lease.")
	if got := replaced(t, docBytes(t, doc), "Bianchi"); strings.Contains(got, "Bian") || strings.Contains(got, "chi signed") {
		t.Errorf("a divided name is still read: %q", got)
	}
}

// Two words drawn apart by a gap a reader takes for a space — a sixth
// of the size, under this package's rule of seven tenths of a space —
// are read as two when a value is sought with a space between them:
// pdftotext reads "Corporal Keller" there, this package's text
// "CorporalKeller", and the name was left for the outside reader.
func TestAValueOverANarrowGapBetweenTwoStringsIsFound(t *testing.T) {
	const size = 12.0
	doc := New()
	p := doc.AddPage()
	p.SetFont(Helvetica, size)
	x := 72.0
	for _, w := range []string{"apartment,", "Corporal", "Keller", "repeatedly"} {
		p.Text(x, 100, w)
		gap := Helvetica.TextWidth(" ", size)
		if w == "Corporal" {
			gap = 2.0
		}
		x += Helvetica.TextWidth(w, size) + gap
	}
	src := docBytes(t, doc)
	if got, _ := NewReaderOrFail(t, src).PageText(0); !strings.Contains(got, "CorporalKeller") {
		t.Fatalf("fixture: the text no longer joins the two words: %q", got)
	}
	for name, got := range map[string]string{"replaced": replaced(t, src, "Corporal Keller"), "barred": barred(t, src, "Corporal Keller")} {
		if strings.Contains(got, "Corporal") || strings.Contains(got, "Keller") {
			t.Errorf("%s: the name is still read: %q", name, got)
		}
		if !strings.Contains(got, "apartment,") || !strings.Contains(got, "repeatedly") {
			t.Errorf("%s: more than the name went: %q", name, got)
		}
	}
	// A value spelled without the space is found as the chain reads it.
	if got := replaced(t, src, "CorporalKeller"); strings.Contains(got, "Corporal") {
		t.Errorf("the tight spelling: %q", got)
	}
}
