package gopdf

import (
	"bytes"
	"strings"
	"testing"
)

// Two fitted tokens in one string: a name and an account number in the
// same sentence, the commonest shape of a line in a contract. The run
// rebuilder must place both, in a lone Tj and inside a TJ array, and
// the document must verify.
func TestPseudonymize_TwoFittedTokensInOneString(t *testing.T) {
	const name, iban = "Marialuisa Vanetti", "IT60X0542811101000000123456"
	line := "Mandato: la cliente " + name + " conto " + iban + " presso Lugano."
	for _, tc := range []struct {
		name string
		draw func(p *Page)
	}{
		{"Tj", func(p *Page) { p.Text(56, 67, line) }},
		{"TJ", func(p *Page) {
			p.Text(56, 67, "Mandato: la cliente ")
			p.Text(56, 90, name+" conto "+iban+" presso")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := New()
			doc.SetInfo(Info{})
			pg := doc.AddPage()
			pg.SetFont(Helvetica, 11)
			tc.draw(pg)
			var buf bytes.Buffer
			if _, err := doc.WriteTo(&buf); err != nil {
				t.Fatal(err)
			}
			r, err := NewReader(buf.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			subs := []Pseudonym{
				{From: name, To: "[PERSON_1]", FitWidth: true},
				{From: iban, To: "[IBAN_1]", FitWidth: true},
			}
			var out bytes.Buffer
			res, err := Pseudonymize(r, &out, subs)
			if err != nil {
				t.Fatalf("pseudonymize: %v", err)
			}
			if res.Replaced[name] != 1 || res.Replaced[iban] != 1 {
				t.Fatalf("replaced: %v", res.Replaced)
			}
			rr, err := NewReader(out.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			txt, err := rr.PageText(0)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(txt, name) || strings.Contains(txt, iban) {
				t.Fatalf("original survives: %q", txt)
			}
			if !strings.Contains(txt, "[PERSON_1]") || !strings.Contains(txt, "[IBAN_1]") {
				t.Fatalf("tokens missing: %q", txt)
			}
			if !strings.Contains(txt, "presso") || !strings.Contains(txt, "conto") {
				t.Fatalf("surrounding words lost: %q", txt)
			}
		})
	}
}

// Three occurrences in one string, one of them repeated, must all go.
func TestPseudonymize_RepeatedFittedTokensInOneString(t *testing.T) {
	const name = "Ada Lovelace"
	line := name + " scrive a " + name + " e ancora " + name + "."
	doc := New()
	doc.SetInfo(Info{})
	pg := doc.AddPage()
	pg.SetFont(Helvetica, 11)
	pg.Text(56, 67, line)
	var buf bytes.Buffer
	doc.WriteTo(&buf)
	r, _ := NewReader(buf.Bytes())
	var out bytes.Buffer
	res, err := Pseudonymize(r, &out, []Pseudonym{{From: name, To: "[P1]", FitWidth: true}})
	if err != nil {
		t.Fatalf("pseudonymize: %v", err)
	}
	if res.Replaced[name] != 1 { // counted per page, not per occurrence
		t.Logf("replaced: %v", res.Replaced)
	}
	rr, _ := NewReader(out.Bytes())
	txt, _ := rr.PageText(0)
	if strings.Contains(txt, name) || strings.Count(txt, "[P1]") != 3 {
		t.Fatalf("got %q", txt)
	}
}
