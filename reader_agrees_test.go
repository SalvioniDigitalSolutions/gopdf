package gopdf

import (
	"bytes"
	"strings"
	"testing"
)

// The replacer finds what the check after it reads.

// headingInPiecesDoc sets a heading as a producer of letter-spaced
// headings did: in pieces, each placed a little before the previous one
// ends by the font's widths, on one baseline. The page's text reads it
// whole; the replacer's lines broke at every piece.
func headingInPiecesDoc(t *testing.T) []byte {
	w := func(s string) float64 { return Helvetica.TextWidth(s, 12) }
	x1 := 100.0
	x2 := x1 + w("DEPARTM") - 8
	x3 := x2 + w("ENT OF CO") - 8
	content := "BT /F1 12 Tf 1 0 0 1 " + fl(x1) + " 700 Tm (DEPARTM) Tj ET\n" +
		"BT /F1 12 Tf 1 0 0 1 " + fl(x2) + " 700 Tm (ENT OF CO) Tj ET\n" +
		"BT /F1 12 Tf 1 0 0 1 " + fl(x3) + " 700 Tm (MMERCE) Tj ET\n" +
		"BT /F1 12 Tf 1 0 0 1 72 660 Tm (Signed for the Department, Lugano.) Tj ET\n"
	return rawPageDoc(t, content)
}

// compoundAtBreakDoc breaks a hyphenated compound across a line:
// "NIST-" ends one line, "calibrated" starts the next.
func compoundAtBreakDoc(t *testing.T) []byte {
	doc := New()
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(72, 100, "The control was calibrated using a NIST-")
	p.Text(72, 116, "calibrated thermistor in the laboratory.")
	return docBytes(t, doc)
}

func TestReplacerReadsAHeadingSetInPieces(t *testing.T) {
	src := headingInPiecesDoc(t)
	if got := flatPageText(t, src); !strings.Contains(got, "DEPARTMENT OF COMMERCE") {
		t.Fatalf("fixture: the page's text does not read the heading whole: %q", got)
	}
	var out bytes.Buffer
	if _, err := Pseudonymize(NewReaderOrFail(t, src), &out, []Pseudonym{
		{From: "DEPARTMENT OF COMMERCE", To: "[REDACTED]", FitWidth: true},
	}); err != nil {
		t.Fatalf("refused: %v", err)
	}
	got := flatPageText(t, out.Bytes())
	if strings.Contains(got, "COMMERCE") || strings.Contains(got, "DEPARTM") {
		t.Errorf("the heading is still read: %q", got)
	}
	if !strings.Contains(got, "Signed for the Department, Lugano.") {
		t.Errorf("the line below was changed: %q", got)
	}
	rd := Redact(NewReaderOrFail(t, src))
	rd.Text("DEPARTMENT OF COMMERCE")
	if got := flatPageText(t, redactTo(t, rd)); strings.Contains(got, "COMMERCE") {
		t.Errorf("bars: the heading is still read: %q", got)
	}
}

func TestReplacerReadsAValueBeforeAHyphenatedBreak(t *testing.T) {
	src := compoundAtBreakDoc(t)
	var out bytes.Buffer
	// A token that fits where the value stood: the fitted engine
	// places it (a token too wide for a short value sends the page to
	// the flow engine, a case of its own).
	if _, err := Pseudonymize(NewReaderOrFail(t, src), &out, []Pseudonym{
		{From: "NIST", To: "[X]", FitWidth: true},
	}); err != nil {
		t.Fatalf("refused: %v", err)
	}
	r := NewReaderOrFail(t, out.Bytes())
	got, _ := r.PageText(0)
	if containsBounded(got, "NIST", matchWords) {
		t.Errorf("NIST is still read: %q", got)
	}
	if !strings.Contains(got, "calibrated thermistor") || !strings.Contains(got, "The control was calibrated using a") {
		t.Errorf("more than the value went: %q", got)
	}
	rd := Redact(NewReaderOrFail(t, src))
	rd.Text("NIST")
	got2, _ := NewReaderOrFail(t, redactTo(t, rd)).PageText(0)
	if containsBounded(got2, "NIST", matchWords) {
		t.Errorf("bars: NIST is still read: %q", got2)
	}
}
