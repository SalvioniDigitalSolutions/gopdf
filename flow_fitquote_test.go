package gopdf

import (
	"bytes"
	"strings"
	"testing"
)

// The ' operator is a Tj that first steps to the next line, and a
// header or a letterhead is often drawn exactly that way — one short
// line per operation. The fitted editor used to decline the operator
// outright, and one declined occurrence sends the WHOLE page to the
// paragraph engine, whose re-wrap merges a form's lines and shifts
// everything around them. The support case was an Italian lease whose
// every page carried its file number in a ' header.
func TestFitExactQuoteOperator(t *testing.T) {
	content := "BT /F1 11 Tf 12 TL 1 0 0 1 60 700 Tm " +
		"(Contratto di locazione) Tj (D45081 incarto) ' (Copia conduttore) ' ET\n" +
		// A rule at a fixed place below the text: a re-wrap would
		// leave it stranded, the in-place edit must not move it.
		"q 0 0 0 RG 1 w 60 660 m 400 660 l S Q\n"
	src := rawPageDoc(t, content)

	before := fragsOf(t, src)
	var buf bytes.Buffer
	res, err := Pseudonymize(NewReaderOrFail(t, src), &buf,
		[]Pseudonym{{From: "D45081", To: "[REF]", FitWidth: true}})
	if err != nil {
		t.Fatalf("pseudonymize: %v", err)
	}
	if res.Replaced["D45081"] != 1 {
		t.Fatalf("replaced: %v", res.Replaced)
	}
	out := buf.Bytes()

	rr := NewReaderOrFail(t, out)
	txt, err := rr.PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(txt, "D45081") {
		t.Fatalf("original survives: %q", txt)
	}
	for _, keep := range []string{"[REF]", "incarto", "Contratto di locazione", "Copia conduttore"} {
		if !strings.Contains(txt, keep) {
			t.Fatalf("%q missing from %q", keep, txt)
		}
	}

	// In place means in place: every fragment the edit did not touch
	// sits exactly where it sat, the ' lines included.
	after := fragsOf(t, out)
	byText := map[string][2]float64{}
	for _, f := range before {
		byText[f.Text] = [2]float64{f.X, f.Y}
	}
	for _, f := range after {
		was, ok := byText[f.Text]
		if !ok {
			continue // the rewritten fragment
		}
		if dx, dy := f.X-was[0], f.Y-was[1]; dx > 0.05 || dx < -0.05 || dy > 0.05 || dy < -0.05 {
			t.Fatalf("fragment %q moved from (%v,%v) to (%v,%v)", f.Text, was[0], was[1], f.X, f.Y)
		}
	}
}

// A token the floor cannot hold in a short original's room used to
// send the whole page to the paragraph engine. With a FitFallback it
// stays an exact in-place edit: the shorter marker goes in, scaled to
// the original's width, and nothing else on the page moves.
func TestFitFallbackTakenWhenFloorCannotHold(t *testing.T) {
	src := justifiedPage(t, "Via", "(viale,", "piazza)", "nr.", "10")
	before := fragsOf(t, src)

	var buf bytes.Buffer
	res, err := Pseudonymize(NewReaderOrFail(t, src), &buf, []Pseudonym{
		{From: "viale", To: "[REDACTED]", FitWidth: true, FitFallback: "[RED]"},
	})
	if err != nil {
		t.Fatalf("pseudonymize: %v", err)
	}
	if res.Replaced["viale"] != 1 {
		t.Fatalf("replaced: %v", res.Replaced)
	}
	out := buf.Bytes()
	txt, err := NewReaderOrFail(t, out).PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(txt, "viale") {
		t.Fatalf("original survives: %q", txt)
	}
	if !strings.Contains(txt, "[RED]") || strings.Contains(txt, "[REDACTED]") {
		t.Fatalf("fallback not taken: %q", txt)
	}
	// The page kept its exact layout: every untouched fragment where
	// it was.
	byText := map[string][2]float64{}
	for _, f := range before {
		byText[f.Text] = [2]float64{f.X, f.Y}
	}
	for _, f := range fragsOf(t, out) {
		was, ok := byText[f.Text]
		if !ok {
			continue
		}
		if dx, dy := f.X-was[0], f.Y-was[1]; dx > 0.05 || dx < -0.05 || dy > 0.05 || dy < -0.05 {
			t.Fatalf("fragment %q moved", f.Text)
		}
	}
}

// Room enough for the full token means the fallback stays unused.
func TestFitFallbackUnusedWhenTokenHolds(t *testing.T) {
	src := justifiedPage(t, "Contratto", "con", "Marialuisa", "Vanetti,", "oggi")
	var buf bytes.Buffer
	_, err := Pseudonymize(NewReaderOrFail(t, src), &buf, []Pseudonym{
		{From: "Marialuisa", To: "[PERSON_1]", FitWidth: true, FitFallback: "[P1]"},
	})
	if err != nil {
		t.Fatal(err)
	}
	txt, _ := NewReaderOrFail(t, buf.Bytes()).PageText(0)
	if !strings.Contains(txt, "[PERSON_1]") {
		t.Fatalf("full token not kept: %q", txt)
	}
}
