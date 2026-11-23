package gopdf

import (
	"strings"
	"testing"
)

// A word divided at a line end is found after another value of its
// paragraph has been replaced. The first replacement kept the space
// between the lines as a line end only at the very start of the
// paragraph: every other one became an ordinary space, "Bian-" and
// "chi" two words, and the name was found by nothing after it.
func TestFlowFindsADividedWordAfterAnotherReplacement(t *testing.T) {
	src := hyphenatedDoc(t, "Contract with Marco Bian-", "chi of this parish.")
	_, e, flows := editFlows(t, src)
	if n, err := flows[0].Replace("Contract", "[[PII_DOC_1]]"); err != nil || n != 1 {
		t.Fatalf("first replacement: %d, %v", n, err)
	}
	n, err := flows[0].Replace("Bianchi", "[[PII_NAME_1]]")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("the divided name was not found after the first replacement: %q", flows[0].joinedText())
	}
	got := collapse(extractAll(t, saveDoc(t, e)))
	if strings.Contains(got, "Bian") {
		t.Errorf("part of the divided name survived: %q", got)
	}
}

// Where a paragraph laid out again sets both halves of a divided word on
// one line, the word is whole: "Bianchi", not "Bian- chi".
func TestFlowSetsADividedWordWholeWhereItsHalvesMeet(t *testing.T) {
	src := hyphenatedDoc(t, "Contractualisation for Marco Bian-", "chi of this parish.")
	_, e, flows := editFlows(t, src)
	if n, err := flows[0].Replace("Contractualisation", "X"); err != nil || n != 1 {
		t.Fatalf("replacement: %d, %v", n, err)
	}
	got := collapse(extractAll(t, saveDoc(t, e)))
	if strings.Contains(got, "Bian- chi") || strings.Contains(got, "Bian-chi") {
		t.Errorf("the divided word was set as two: %q", got)
	}
	if !strings.Contains(got, "Bianchi") {
		t.Errorf("the divided word is not whole where its halves meet: %q", got)
	}
}
