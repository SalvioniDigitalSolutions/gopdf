package gopdf

import (
	"bytes"
	"testing"
)

// TestDocumentSpellingsOfSpacedValues: the spellings a document gives a
// value of several words — two spaces, a non-breaking space, a tab —
// are found, each as the page holds it; not over a line break, and not
// inside a longer word.
func TestDocumentSpellingsOfSpacedValues(t *testing.T) {
	content := "BT /F1 12 Tf 1 0 0 1 72 700 Tm (File No.  3-45678 for Marco \240Rossi) Tj " +
		"0 -20 Td (and XNo.  3-45678 and Marco) Tj 0 -20 Td (Rossi again) Tj ET\n"
	r := NewReaderOrFail(t, rawPageDoc(t, content))
	got := documentSpellings(r, []string{"No. 3-45678", "Marco Rossi", "single"})
	if len(got["No. 3-45678"]) != 1 || got["No. 3-45678"][0] != "No.  3-45678" {
		t.Errorf("No.: %q", got["No. 3-45678"])
	}
	if len(got["Marco Rossi"]) != 1 || got["Marco Rossi"][0] != "Marco \u00a0Rossi" {
		t.Errorf("Marco Rossi: %q", got["Marco Rossi"])
	}
}

// TestPseudonymizeValueSetWithTwoSpaces: the page sets a value with two
// spaces between its words; it is replaced, and the copy is not refused.
func TestPseudonymizeValueSetWithTwoSpaces(t *testing.T) {
	content := "BT /F1 12 Tf 1 0 0 1 72 700 Tm (File No.  3-45678 and Marco  Rossi) Tj ET\n"
	src := rawPageDoc(t, content)
	for _, fit := range []bool{false, true} {
		var out bytes.Buffer
		if _, err := Pseudonymize(NewReaderOrFail(t, src), &out, []Pseudonym{
			{From: "No. 3-45678", To: "[REDACTED]", FitWidth: fit},
			{From: "Marco Rossi", To: "[REDACTED]", FitWidth: fit},
		}); err != nil {
			t.Fatalf("fit=%v: %v", fit, err)
		}
		assertGone(t, out.Bytes(), "45678", "Rossi")
	}
	// And the bar engine, with its own read-back.
	rd := Redact(NewReaderOrFail(t, src))
	rd.Text("No. 3-45678")
	rd.Text("Marco Rossi")
	assertGone(t, redactTo(t, rd), "45678", "Rossi")
}
