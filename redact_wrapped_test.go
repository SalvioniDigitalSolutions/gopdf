package gopdf

import (
	"bytes"
	"strings"
	"testing"
)

// A value broken across a line.
//
// "Marialuisa" ends a line and "Vanetti" begins the next. Matching ran a
// line at a time, so neither engine removed it — the bar engine nothing
// of it, the fitted replacement every occurrence but that one — and the
// read-back, a line at a time as well, passed the page. These tests
// hold both engines to the page as a reader joins it.

// wrappedDoc writes a page where the name stands once whole and once
// across a line break, among other words.
func wrappedDoc(t *testing.T) []byte {
	t.Helper()
	doc := New()
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(60, 90, "La cliente Marialuisa Vanetti autorizza il bonifico e poi")
	p.Text(60, 106, "poi poi poi poi la stessa signora Marialuisa")
	p.Text(60, 122, "Vanetti conferma di nuovo a Lugano.")
	return docBytes(t, doc)
}

func flatPageText(t *testing.T, pdf []byte) string {
	t.Helper()
	r, err := NewReader(pdf)
	if err != nil {
		t.Fatal(err)
	}
	text, err := r.PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(strings.Fields(text), " ")
}

func TestRedactTextBrokenAcrossALine(t *testing.T) {
	src := wrappedDoc(t)
	if got := flatPageText(t, src); strings.Count(got, "Marialuisa Vanetti") != 2 {
		t.Fatalf("fixture: want the name twice in the page as read, got %q", got)
	}
	r, _ := NewReader(src)
	if text, _ := r.PageText(0); strings.Count(text, "Marialuisa Vanetti") != 1 {
		t.Fatalf("fixture: want the name once on one line and once across a break, got %q", text)
	}
	rd := Redact(r)
	rd.Text("Marialuisa Vanetti")
	out := redactTo(t, rd)
	got := flatPageText(t, out)
	for _, w := range []string{"Marialuisa", "Vanetti"} {
		if strings.Contains(got, w) {
			t.Errorf("%q is still on the page: %q", w, got)
		}
	}
	for _, w := range []string{"La cliente", "autorizza il bonifico", "la stessa signora", "conferma di nuovo a Lugano."} {
		if !strings.Contains(got, w) {
			t.Errorf("%q was lost: %q", w, got)
		}
	}
}

func TestPseudonymizeFittedTextBrokenAcrossALine(t *testing.T) {
	src := wrappedDoc(t)
	for _, sub := range []Pseudonym{
		{From: "Marialuisa Vanetti", To: "[REDACTED]"},
		{From: "Marialuisa Vanetti", To: "[REDACTED]", FitWidth: true},
		{From: "Marialuisa Vanetti", To: "PERSON_7f3a9c21", FitWidth: true, MinScale: 0.18},
	} {
		r, _ := NewReader(src)
		var out bytes.Buffer
		if _, err := Pseudonymize(r, &out, []Pseudonym{sub}); err != nil {
			t.Errorf("fit=%v %q: refused: %v", sub.FitWidth, sub.To, err)
			continue
		}
		got := flatPageText(t, out.Bytes())
		if strings.Contains(got, "Marialuisa") || strings.Contains(got, "Vanetti") {
			t.Errorf("fit=%v %q: the name is still on the page: %q", sub.FitWidth, sub.To, got)
		}
		if n := strings.Count(got, sub.To); n != 2 {
			t.Errorf("fit=%v: the token stands %d time(s), want 2: %q", sub.FitWidth, n, got)
		}
		if !strings.Contains(got, "conferma di nuovo a Lugano.") {
			t.Errorf("fit=%v: the words after the name were lost: %q", sub.FitWidth, got)
		}
	}
}

// The read-back itself: a page that still says the name across a line
// break is refused, not passed.
func TestReadBackSeesAValueAcrossALineBreak(t *testing.T) {
	src := wrappedDoc(t)
	r, _ := NewReader(src)
	where, err := findResidue(r, []Pseudonym{{From: "Marialuisa Vanetti", To: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if where == "" {
		t.Fatal("the read-back found nothing on a page that says the name twice")
	}
	// With the whole occurrence gone and the wrapped one left, it must
	// still be found.
	doc := New()
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(60, 90, "poi poi poi poi la stessa signora Marialuisa")
	p.Text(60, 106, "Vanetti conferma di nuovo a Lugano.")
	r2, _ := NewReader(docBytes(t, doc))
	where, err = findResidue(r2, []Pseudonym{{From: "Marialuisa Vanetti", To: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if where == "" {
		t.Fatal("the read-back passed a page that says the name across a line break")
	}
}
