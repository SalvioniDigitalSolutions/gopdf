package gopdf

import (
	"bytes"
	"runtime"
	"testing"
)

// A content stream arrives decompressed, so a small file can carry a
// very large one, and every editing path in this package begins by
// lexing it. The token list is the biggest thing that gets built from a
// hostile input before anything has looked at it, which makes its size
// the thing to bound.

// TestContentTokenLimit: the lexer stops at the cap and says it stopped.
// A prefix handed back as if it were the whole stream would be edited
// against operators that are not there.
func TestContentTokenLimit(t *testing.T) {
	// Two bytes per token, so the input is well past the cap.
	data := bytes.Repeat([]byte("1 "), maxContentTokens+1000)
	toks, truncated := tokenizeContentLimited(data)
	if !truncated {
		t.Error("a stream past the cap was not reported as truncated")
	}
	if len(toks) > maxContentTokens {
		t.Errorf("lexed %d tokens past a cap of %d", len(toks), maxContentTokens)
	}
	// And a stream inside the cap is not reported as truncated.
	small, truncated := tokenizeContentLimited([]byte("BT /F1 12 Tf (hi) Tj ET"))
	if truncated {
		t.Error("an ordinary stream was reported as truncated")
	}
	if len(small) == 0 {
		t.Error("an ordinary stream lexed to nothing")
	}
}

// TestContentTokenMemory holds the lexer to a budget.
//
// A token costs around ninety-six bytes once its value is boxed, so
// without a cap a stream of short tokens allocates some ninety times its
// own size: eight megabytes of "1 " once came to 788. The cap is what
// stops that, and this is the test that notices if it stops stopping.
func TestContentTokenMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates a few hundred megabytes")
	}
	data := bytes.Repeat([]byte("1 "), 4<<20) // 8 MB of trivial tokens

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	toks, _ := tokenizeContentLimited(data)
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(toks)

	const budgetMB = 512
	got := (after.TotalAlloc - before.TotalAlloc) / (1 << 20)
	if got > budgetMB {
		t.Errorf("lexing %d MB of trivial tokens allocated %d MB, over a "+
			"budget of %d; the cap is not holding", len(data)/(1<<20),
			got, budgetMB)
	}
	t.Logf("%d MB input -> %d tokens, %d MB allocated",
		len(data)/(1<<20), len(toks), got)
}

// TestContentTokenSpans: every span the lexer reports has to lie inside
// the input, because those spans are used to cut the stream up. One
// outside it would splice from somewhere else entirely.
func TestContentTokenSpans(t *testing.T) {
	for _, in := range []string{
		"", "   ", "BT /F1 12 Tf (hi) Tj ET", "[ (a) -20 (b) ] TJ",
		"(unterminated", "<0102", "<</A 1>> BDC", "%comment\n1 0 0 1 0 0 cm",
		"1.5.2 3 4", "(nested (parens) here) Tj", "<</K[<</X 1>>]>> BDC",
	} {
		for _, tok := range tokenizeContent([]byte(in)) {
			if tok.start < 0 || tok.end > len(in) || tok.start > tok.end {
				t.Errorf("%q: span [%d,%d) is outside %d bytes",
					in, tok.start, tok.end, len(in))
			}
		}
	}
}
