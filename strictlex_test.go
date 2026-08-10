package gopdf

import (
	"fmt"
	"strings"
	"testing"
)

// Strict lexing of an emitted content stream.
//
// A splice that lands immediately after an operator can fuse with it:
// "Tc" and "1" become the token "Tc1", which every reader here tolerates
// and a strict parser rejects. The content is right and the file is not,
// and the cost falls on whoever tries to re-open the output to check it.
//
// The invariant is narrow enough to test directly: every keyword in a
// content stream must be an operator the specification defines. A fused
// one never is.

// assertStrictLex fails if a content stream holds a keyword that is not
// an operator, which is what a fused token looks like.
func assertStrictLex(t *testing.T, what string, content []byte) {
	t.Helper()
	if err := strictLex(content); err != nil {
		t.Errorf("%s: %v", what, err)
	}
}

func strictLex(content []byte) error {
	return strictLexContent(content)
}

// assertPagesLexStrictly checks every page of a written document.
func assertPagesLexStrictly(t *testing.T, what string, out []byte) {
	t.Helper()
	r, err := NewReader(out)
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	for i := 0; i < r.NumPages(); i++ {
		content, err := r.pageContent(r.pages[i].dict)
		if err != nil {
			t.Fatalf("%s page %d: %v", what, i+1, err)
		}
		assertStrictLex(t, fmt.Sprintf("%s page %d", what, i+1), content)
	}
}

// TestSpliceSeparator is the regression: a splice landing straight after
// an operator whose trailing space it consumed must not fuse with it.
func TestSpliceSeparator(t *testing.T) {
	cases := []struct {
		name string
		data string
		s    splice
		want string
	}{
		{
			name: "operator meets a number",
			data: "0.124 Tc[(x)] TJ",
			s:    splice{start: 8, end: 13, repl: []byte("1 0 0 1 5 5 Tm [(y)] TJ")},
			want: "0.124 Tc 1 0 0 1 5 5 Tm [(y)] TJ TJ",
		},
		{
			// "(a)" gives way to "5", which would otherwise sit against
			// the Tc before it and the Tj after it.
			name: "replacement meets regular characters on both sides",
			data: "0 Tc(a)Tj",
			s:    splice{start: 4, end: 7, repl: []byte("5")},
			want: "0 Tc 5 Tj",
		},
		{
			name: "delimiters already separate",
			data: "[(a)] TJ",
			s:    splice{start: 0, end: 5, repl: []byte("[(b)]")},
			want: "[(b)] TJ",
		},
		{
			// Removing what stood between two numbers must not let them
			// run into one.
			name: "an empty replacement must not fuse its neighbours",
			data: "10 20",
			s:    splice{start: 2, end: 3, repl: nil},
			want: "10 20",
		},
		{
			name: "an empty replacement between delimiters needs nothing",
			data: "[(a)] TJ",
			s:    splice{start: 1, end: 4, repl: nil},
			want: "[] TJ",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := applySplices([]byte(c.data), []splice{c.s})
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
			if err := strictLex(got); err != nil {
				t.Errorf("the result does not lex strictly: %v", err)
			}
		})
	}
}

func TestStrictLexCatchesFusedTokens(t *testing.T) {
	if err := strictLex([]byte("0.124 Tc1 0 0 1 70.944 16.8 Tm [(202)] TJ")); err == nil {
		t.Error("a fused Tc1 should be caught")
	}
	if err := strictLex([]byte("0.124 Tc 1 0 0 1 70.944 16.8 Tm [(202)] TJ")); err != nil {
		t.Errorf("well-formed content was rejected: %v", err)
	}
	// Inline image samples are not tokens and must not be judged.
	inline := "BI /W 2 /H 2 /BPC 8 /CS /G ID \x00\xffQq\x01 EI"
	if err := strictLex([]byte(inline)); err != nil {
		t.Errorf("inline image data was judged as tokens: %v", err)
	}
}

// TestFlowOutputLexesStrictly exercises the path that showed the bug: a
// flow rewrite whose positioning splice lands after an operator.
func TestFlowOutputLexesStrictly(t *testing.T) {
	src := flowDoc(t,
		"The claimant Ada Lovelace attended on 3 May",
		"and the matter was resolved in full.")
	_, e, flows := editFlows(t, src)
	if _, err := flows[0].Replace("Ada Lovelace", "[[PII_NAME_1]]"); err != nil {
		t.Fatal(err)
	}
	assertPagesLexStrictly(t, "flow rewrite", saveDoc(t, e))
}

func TestRedactOutputLexesStrictly(t *testing.T) {
	src := redactFixture(t)
	r, _ := NewReader(src)
	rd := Redact(r)
	rd.Text("Ada Lovelace")
	rd.SetLabel("[REDACTED]")
	assertPagesLexStrictly(t, "redaction", redactTo(t, rd))
}

func TestPseudonymizeOutputLexesStrictly(t *testing.T) {
	src := hidingPlacesDoc(t)
	var out strings.Builder
	if _, err := Pseudonymize(NewReaderOrFail(t, src), &out,
		[]Pseudonym{{From: pseudoName, To: "[[P1]]"}}); err != nil {
		t.Fatal(err)
	}
	assertPagesLexStrictly(t, "pseudonymization", []byte(out.String()))
}

func TestReflowOutputLexesStrictly(t *testing.T) {
	src := flowDoc(t,
		"Alpha beta gamma delta epsilon zeta eta",
		"theta iota kappa lambda mu nu xi omicron.")
	r, _ := NewReader(src)
	doc := New()
	e, err := doc.EditPage(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	e.SetMaxExtraLines(2)
	if _, err := e.ReplaceTextReflow("gamma", "gamma and more besides"); err != nil {
		t.Fatal(err)
	}
	assertPagesLexStrictly(t, "reflow", saveDoc(t, e))
}

func TestUpdateOutputLexesStrictly(t *testing.T) {
	src := flowDoc(t, "Invoice 2024-001 issued to the client today")
	r, _ := NewReader(src)
	u := Update(r)
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := page.ReplaceText("2024-001", "2026-114"); err != nil {
		t.Fatal(err)
	}
	if _, err := page.ReplaceTextFlow("client", "client of record"); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if _, err := u.WriteTo(&out); err != nil {
		t.Fatal(err)
	}
	assertPagesLexStrictly(t, "incremental update", []byte(out.String()))
}
