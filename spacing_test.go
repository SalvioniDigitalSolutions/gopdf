package gopdf

import (
	"strings"
	"testing"
)

// rawPageDoc builds a one-page file from a literal content stream, so a
// test can reproduce exactly how a producer laid its text out.
func rawPageDoc(t *testing.T, content string) []byte {
	t.Helper()
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 10) // registers the font as /F1
	page.prelude = []byte(content)
	return docBytes(t, doc)
}

// TestSpacingKeepsWordsWhole is the regression for troff and TeX output,
// which positions fragments of a single word separately. Treating every
// forward move as a space broke "BASH" into "BA SH".
func TestSpacingKeepsWordsWhole(t *testing.T) {
	// "BA" then a move of exactly its own width, then "SH(1)".
	w := Helvetica.TextWidth("BA", 10)
	content := "BT /F1 10 Tf 1 0 0 1 60 700 Tm (BA) Tj " +
		fl(w) + " 0 Td (SH\\(1\\)) Tj ET\n"
	got := extractAll(t, rawPageDoc(t, content))
	if !strings.Contains(got, "BASH(1)") {
		t.Errorf("the word was broken up: %q", got)
	}
}

// TestSpacingFindsRealGaps is the other side: a gap wide enough to read
// as a space must still produce one.
func TestSpacingFindsRealGaps(t *testing.T) {
	w := Helvetica.TextWidth("one", 10) + Helvetica.TextWidth("   ", 10)
	content := "BT /F1 10 Tf 1 0 0 1 60 700 Tm (one) Tj " +
		fl(w) + " 0 Td (two) Tj ET\n"
	got := extractAll(t, rawPageDoc(t, content))
	if !strings.Contains(got, "one two") {
		t.Errorf("a real gap did not become a space: %q", got)
	}
}

// TestSpacingKerningIsNotASpace covers a TJ array whose kerns are large
// enough to have tripped the old fixed threshold.
func TestSpacingKerningIsNotASpace(t *testing.T) {
	content := "BT /F1 10 Tf 1 0 0 1 60 700 Tm " +
		"[(A) -120 (V) -150 (A) -110 (T) -130 (A)] TJ ET\n"
	got := extractAll(t, rawPageDoc(t, content))
	if !strings.Contains(got, "AVATA") {
		t.Errorf("kerning inside a word became spaces: %q", got)
	}
}

// TestSpacingWideKernIsASpace checks a kern big enough to be a word gap.
func TestSpacingWideKernIsASpace(t *testing.T) {
	content := "BT /F1 10 Tf 1 0 0 1 60 700 Tm [(one) -400 (two)] TJ ET\n"
	got := extractAll(t, rawPageDoc(t, content))
	if !strings.Contains(got, "one two") {
		t.Errorf("a wide kern should read as a space: %q", got)
	}
}

// TestSpacingRotatedText is the regression for a booklet imposition,
// where advancing the pen changed the page y and every fragment looked
// like a new line.
func TestSpacingRotatedText(t *testing.T) {
	// A 90-degree text matrix: the baseline runs up the page.
	content := "BT /F1 10 Tf 0 1 -1 0 300 400 Tm (side) Tj " +
		fl(Helvetica.TextWidth("side", 10)) + " 0 Td (ways) Tj ET\n"
	got := extractAll(t, rawPageDoc(t, content))
	if strings.Count(got, "\n") > 2 {
		t.Errorf("rotated text was broken across lines: %q", got)
	}
	if !strings.Contains(got, "sideways") {
		t.Errorf("rotated text did not join: %q", got)
	}
}

// TestSpacingNewLine checks that a genuine line break still shows up.
func TestSpacingNewLine(t *testing.T) {
	content := "BT /F1 10 Tf 1 0 0 1 60 700 Tm (first) Tj " +
		"1 0 0 1 60 680 Tm (second) Tj ET\n"
	got := extractAll(t, rawPageDoc(t, content))
	if !strings.Contains(got, "first\nsecond") {
		t.Errorf("a new baseline should start a new line: %q", got)
	}
}

// TestSpacingTextSuppliesItsOwn checks that a fragment already beginning
// with whitespace is not given another space.
func TestSpacingTextSuppliesItsOwn(t *testing.T) {
	w := Helvetica.TextWidth("one", 10) + Helvetica.TextWidth("    ", 10)
	content := "BT /F1 10 Tf 1 0 0 1 60 700 Tm (one) Tj " +
		fl(w) + " 0 Td ( two) Tj ET\n"
	got := extractAll(t, rawPageDoc(t, content))
	if strings.Contains(got, "one  two") {
		t.Errorf("a doubled space was inserted: %q", got)
	}
}

func TestSpacingNeedsSpaceUnit(t *testing.T) {
	cases := []struct {
		text string
		gap  float64
		want bool
	}{
		{"a", 0, false},
		{"a", -3, false},  // the pen went backwards
		{"a", 1.0, false}, // kerning, well under a space
		{"a", 2.5, false}, // still inside the plateau
		{"a", 4.0, true},  // a real gap
		{" a", 9, false},  // the text carries its own space
		{" a", 9, false},  // including a non-breaking one
	}
	for _, c := range cases {
		if got := needsSpace(c.text, c.gap, 5); got != c.want {
			t.Errorf("needsSpace(%q, %v, 5) = %v, want %v", c.text, c.gap, got, c.want)
		}
	}
	// A font that reports no space width must not divide by zero.
	if needsSpace("a", 0.1, 0) {
		t.Error("a tiny gap should not be a space even without metrics")
	}
}

func TestSpacingBaselineDir(t *testing.T) {
	if x, y := baselineDir(matrix{1, 0, 0, 1, 0, 0}); x != 1 || y != 0 {
		t.Errorf("identity direction = %v,%v", x, y)
	}
	if x, y := baselineDir(matrix{0, 2, -2, 0, 0, 0}); x != 0 || y != 1 {
		t.Errorf("quarter-turn direction = %v,%v", x, y)
	}
	if x, y := baselineDir(matrix{}); x != 1 || y != 0 {
		t.Errorf("a degenerate matrix should fall back to %v,%v", x, y)
	}
}
