package gopdf

import (
	"bytes"
	"strings"
	"testing"
)

// wrappedApartDoc draws a value once on one line and once broken over two
// lines that are two text objects of their own — set as a table cell or
// a column sets them — which the flow engine does not take for one
// paragraph.
func wrappedApartDoc(t *testing.T) []byte {
	content := "BT /F1 10 Tf 1 0 0 1 72 700 Tm (Stocks located in the \\223Northeast Heating Oil) Tj ET\n" +
		"BT /F1 10 Tf 1 0 0 1 72 688 Tm (Reserve\\224 are not included. For details see) Tj ET\n" +
		"BT /F1 10 Tf 1 0 0 1 72 640 Tm (the \\223Northeast Heating Oil Reserve\\224 report.) Tj ET\n"
	return rawPageDoc(t, content)
}

// TestPseudonymizeValueWrappedApartIsReplaced: the value broken over two
// lines the flow engine does not join is replaced all the same — the
// token where it begins, the rest taken out — instead of the copy being
// withheld; the words around it stay.
func TestPseudonymizeValueWrappedApartIsReplaced(t *testing.T) {
	src := wrappedApartDoc(t)
	if got := flatPageText(t, src); strings.Count(got, "Northeast Heating Oil Reserve") != 2 {
		t.Fatalf("fixture: %q", got)
	}
	var out bytes.Buffer
	if _, err := Pseudonymize(NewReaderOrFail(t, src), &out, []Pseudonym{
		{From: "Northeast Heating Oil Reserve", To: "[REDACTED]", FitWidth: true},
	}); err != nil {
		t.Fatal(err)
	}
	got := flatPageText(t, out.Bytes())
	for _, gone := range []string{"Northeast", "Heating", "Reserve"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q is still read: %q", gone, got)
		}
	}
	if n := strings.Count(got, "[REDACTED]"); n != 2 {
		t.Errorf("%d tokens, want 2: %q", n, got)
	}
	for _, kept := range []string{"Stocks located in the", "are not included. For details see", "report."} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q was lost: %q", kept, got)
		}
	}
}

// TestFittedPlanTakesAValueAcrossALineBreak: asked to (the second try),
// the fitted plan takes a value a line break runs through: the token
// where it begins, the rest of it on the next line taken out, the words
// around it in their places. Not asked, it leaves the page to the flow
// engine, as before.
func TestFittedPlanTakesAValueAcrossALineBreak(t *testing.T) {
	src := wrappedDoc(t)
	subs := []Pseudonym{{From: "Marialuisa Vanetti", To: "[REDACTED]", FitWidth: true}}
	for _, across := range []bool{false, true} {
		u := Update(NewReaderOrFail(t, src))
		page, err := u.Page(0)
		if err != nil {
			t.Fatal(err)
		}
		counts, ok := page.replacePageFitted(subs, matchWords, across)
		if !across {
			if ok {
				t.Fatal("the plan took a value across a line break without being asked")
			}
			continue
		}
		if !ok || counts["Marialuisa Vanetti"] != 2 {
			t.Fatalf("not planned: ok=%v counts=%v", ok, counts)
		}
		var out bytes.Buffer
		if _, err := u.WriteTo(&out); err != nil {
			t.Fatal(err)
		}
		got := flatPageText(t, out.Bytes())
		if strings.Contains(got, "Marialuisa") || strings.Contains(got, "Vanetti") {
			t.Errorf("the name is still read: %q", got)
		}
		if n := strings.Count(got, "[REDACTED]"); n != 2 {
			t.Errorf("%d tokens, want 2: %q", n, got)
		}
		for _, kept := range []string{"La cliente", "autorizza il bonifico", "la stessa signora", "conferma di nuovo a Lugano."} {
			if !strings.Contains(got, kept) {
				t.Errorf("%q was lost: %q", kept, got)
			}
		}
	}
}

// TestValueAcrossTwoPiecesEndingInASpace: a heading drawn as two pieces,
// the first ending in a space (" UNITED " and "STATES OF AMERICA "), is
// one value, for the fitted tokens and for the bars alike; joining the
// pieces with a second space spelled "UNITED  STATES", which no value
// does, and the heading stayed while the rest of the page was replaced.
func TestValueAcrossTwoPiecesEndingInASpace(t *testing.T) {
	// As a real filing set it: a centred heading whose spaces are
	// stretched by word spacing, so the pen after " UNITED " is past
	// where "STATES" begins and the two are read as two chains.
	content := "BT /F1 1 Tf -0.0089 Tc 11.9839 Tw 12 0 0 12 72 695.04 Tm ( UNITED )Tj 16.306 0 Td (STATES)Tj " +
		"-0.0079 Tc 0.0004 Tw 3.784 0 Td ( OF AMERICA )Tj ET\n" +
		"BT /F1 12 Tf 0 Tw 1 0 0 1 72 660 Tm (In the Matter of Marco Rossi, adviser.) Tj ET\n"
	src := rawPageDoc(t, content)
	var out bytes.Buffer
	if _, err := Pseudonymize(NewReaderOrFail(t, src), &out, []Pseudonym{
		{From: "UNITED STATES OF AMERICA", To: "[REDACTED]", FitWidth: true},
		{From: "Marco Rossi", To: "[REDACTED]", FitWidth: true},
	}); err != nil {
		t.Fatal(err)
	}
	assertGone(t, out.Bytes(), "UNITED", "STATES", "AMERICA", "Rossi")
	if got := flatPageText(t, out.Bytes()); !strings.Contains(got, "In the Matter of") {
		t.Errorf("the words around were lost: %q", got)
	}
	rd := Redact(NewReaderOrFail(t, src))
	rd.Text("UNITED STATES OF AMERICA")
	assertGone(t, redactTo(t, rd), "UNITED", "STATES", "AMERICA")
}
