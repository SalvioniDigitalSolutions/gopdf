package gopdf

import (
	"bytes"
	"strings"
	"testing"
)

// Values that overlap.
//
// A review marks what it finds, and two findings can hold the same
// words: a person's name and the wider phrase a model drew around it,
// each with a token of its own; or two that cross. The fitted engine
// gave the place to the longer one and still counted the other as
// replaced, so the read-back looked for a token nobody had written and
// withheld the file — "the document's font encodes it as something
// else", on a page set in Helvetica.

func overlapDoc(t *testing.T, lines ...string) []byte {
	t.Helper()
	doc := New()
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	for i, l := range lines {
		p.Text(60, float64(90+16*i), l)
	}
	return docBytes(t, doc)
}

func pseudonymizeText(t *testing.T, src []byte, subs []Pseudonym) (string, error) {
	t.Helper()
	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := Pseudonymize(r, &out, subs); err != nil {
		return "", err
	}
	return flatPageText(t, out.Bytes()), nil
}

func TestPseudonymizeAValueInsideALongerOne(t *testing.T) {
	src := overlapDoc(t, "Vertrag zwischen Marco Bianchi und der Beispiel Treuhand AG", "Herr Bianchi unterschreibt.")
	for _, fit := range []bool{true, false} {
		got, err := pseudonymizeText(t, src, []Pseudonym{
			{From: "Marco Bianchi und der Beispiel Treuhand AG", To: "[[ORG_1]]", FitWidth: fit, MinScale: 0.18},
			{From: "Marco Bianchi", To: "[[PERSON_1]]", FitWidth: fit, MinScale: 0.18},
			{From: "Treuhand AG", To: "[REDACTED]", FitWidth: fit},
			{From: "Bianchi", To: "[[PERSON_2]]", FitWidth: fit, MinScale: 0.18},
		})
		if err != nil {
			t.Fatalf("fit=%v: refused: %v", fit, err)
		}
		for _, w := range []string{"Marco", "Bianchi", "Treuhand", "Beispiel"} {
			if strings.Contains(got, w) {
				t.Errorf("fit=%v: %q is still on the page: %q", fit, w, got)
			}
		}
		// The longer value's token where it stood, the shorter one's
		// where that one stands alone.
		if !strings.Contains(got, "Vertrag zwischen [[ORG_1]]") || !strings.Contains(got, "Herr [[PERSON_2]] unterschreibt.") {
			t.Errorf("fit=%v: the page reads %q", fit, got)
		}
		if strings.Contains(got, "[[PERSON_1]]") {
			t.Errorf("fit=%v: a token was written for a value that stood nowhere by itself: %q", fit, got)
		}
	}
}

func TestPseudonymizeValuesThatCross(t *testing.T) {
	src := overlapDoc(t, "Kontakt Anna Keller: anna.keller@example.com oder Telefon.")
	got, err := pseudonymizeText(t, src, []Pseudonym{
		{From: "anna.keller@example.com", To: "[[EMAIL_1]]", FitWidth: true, MinScale: 0.18},
		{From: "Keller: anna", To: "[REDACTED]", FitWidth: true},
		{From: "Anna Keller", To: "[[PERSON_1]]", FitWidth: true, MinScale: 0.18},
	})
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	for _, w := range []string{"Anna", "Keller", "anna", "keller", "example.com"} {
		if strings.Contains(got, w) {
			t.Errorf("%q is still on the page: %q", w, got)
		}
	}
	if !strings.Contains(got, "[[EMAIL_1]]") || !strings.HasPrefix(got, "Kontakt") || !strings.Contains(got, "oder Telefon.") {
		t.Errorf("the page reads %q", got)
	}
}

// The crossing that leaves a remainder: the longer value takes the
// middle, and what the shorter one holds outside it must go as well.
func TestPseudonymizeACrossedValueLeavesNoRemainder(t *testing.T) {
	src := overlapDoc(t, "Zeugin Anna Keller Treuhand Bern erschien.")
	// Both engines: the fitted one, and the paragraph one a page falls
	// back to.
	for _, fit := range []bool{true, false} {
		got, err := pseudonymizeText(t, src, []Pseudonym{
			{From: "Keller Treuhand Bern", To: "[[ORG_1]]", FitWidth: fit, MinScale: 0.18},
			{From: "Anna Keller", To: "[[PERSON_1]]", FitWidth: fit, MinScale: 0.18},
		})
		if err != nil {
			t.Fatalf("fit=%v: refused: %v", fit, err)
		}
		for _, w := range []string{"Anna", "Keller", "Treuhand", "Bern"} {
			if strings.Contains(got, w) {
				t.Errorf("fit=%v: %q is still on the page: %q", fit, w, got)
			}
		}
		if !strings.Contains(got, "Zeugin") || !strings.Contains(got, "[[ORG_1]]") || !strings.Contains(got, "erschien.") {
			t.Errorf("fit=%v: the page reads %q", fit, got)
		}
	}
}

func TestFreeRanges(t *testing.T) {
	hits := []fitHit{{at: [2]int{5, 10}}, {at: [2]int{14, 16}}}
	for _, c := range []struct {
		at   [2]int
		want [][2]int
	}{
		{[2]int{0, 4}, [][2]int{{0, 4}}},
		{[2]int{0, 8}, [][2]int{{0, 5}}},
		{[2]int{6, 9}, nil},
		{[2]int{8, 15}, [][2]int{{10, 14}}},
		{[2]int{3, 20}, [][2]int{{3, 5}, {10, 14}, {16, 20}}},
	} {
		got := freeRanges(hits, c.at)
		if len(got) != len(c.want) {
			t.Errorf("%v: got %v, want %v", c.at, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%v: got %v, want %v", c.at, got, c.want)
			}
		}
	}
}
