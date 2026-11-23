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

// A hyphen that ends a line stays where a paragraph is laid out again,
// and so does the word before it: "Veltranet-" over "supported" is a
// compound, not a divided word. Set whole as "Veltranetsupported" once a
// replacement before it made the paragraph be laid out again, it hid the
// marked "Veltranet" from the replacement after it, and the copy was
// refused.
func TestFlowKeepsALineEndHyphenWhenLaidOutAgain(t *testing.T) {
	src := hyphenatedDoc(t, "Contractualisation for the Veltranet-", "supported system.")
	_, e, flows := editFlows(t, src)
	if n, err := flows[0].Replace("Contractualisation", "X"); err != nil || n != 1 {
		t.Fatalf("first replacement: %d, %v", n, err)
	}
	n, err := flows[0].Replace("Veltranet", "[[PII_ORG_1]]")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("the value before the line end hyphen was not found after the first replacement: %q", flows[0].joinedText())
	}
	got := collapse(extractAll(t, saveDoc(t, e)))
	if strings.Contains(got, "Veltranet") {
		t.Errorf("the value survived: %q", got)
	}
	if !strings.Contains(got, "supported") {
		t.Errorf("the rest of the compound went with it: %q", got)
	}

	// And as the first replacement of its paragraph.
	src = hyphenatedDoc(t, "Contractualisation for the Veltranet-", "supported system.")
	_, e, flows = editFlows(t, src)
	if n, err := flows[0].Replace("Veltranet", "[[PII_ORG_1]]"); err != nil || n != 1 {
		t.Fatalf("the value before the line end hyphen: %d, %v", n, err)
	}
	if got := collapse(extractAll(t, saveDoc(t, e))); strings.Contains(got, "Veltranet") {
		t.Errorf("the value survived as the first replacement: %q", got)
	}
}
