package gopdf

import (
	"math"
	"testing"
	"unicode/utf8"
)

// A row of table headers drawn as one show-text array, a column's width
// between its words: each word is placed where the page draws it, and the
// space read for each gap starts where the word before it ends.
func TestAdvancesPlaceTheWordsOfOneArray(t *testing.T) {
	content := "BT /F1 10 Tf 1 0 0 1 100 700 Tm [(First) -3000 (Fourth) -2500 (First)] TJ ET\n"
	frags, err := NewReaderOrFail(t, rawPageDoc(t, content)).PageTextFragments(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 1 || frags[0].Text != "First Fourth First" {
		t.Fatalf("fragments %+v", frags)
	}
	f := frags[0]
	if len(f.Advances) != utf8.RuneCountInString(f.Text)+1 {
		t.Fatalf("%d advances for %d characters", len(f.Advances), utf8.RuneCountInString(f.Text))
	}
	first := Helvetica.TextWidth("First", 10)
	fourth := Helvetica.TextWidth("Fourth", 10)
	for _, c := range []struct {
		what string
		i    int
		want float64
	}{
		{"F of First", 0, 0},
		{"i of First", 1, Helvetica.TextWidth("F", 10)},
		{"the space after First", 5, first},
		{"F of Fourth", 6, first + 30},
		{"the space after Fourth", 12, first + 30 + fourth},
		{"F of the second First", 13, first + 30 + fourth + 25},
		{"the end", 18, first + 30 + fourth + 25 + first},
	} {
		if got := f.Advances[c.i]; math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: %v, want %v", c.what, got, c.want)
		}
	}
	if end := f.Advances[len(f.Advances)-1]; math.Abs(end-f.W) > 1e-9 {
		t.Errorf("the text ends at %v, its width is %v", end, f.W)
	}
}

// Character and word spacing move each character as they move the pen,
// horizontal scaling and the matrices scale them, and the last advance is
// the fragment's width.
func TestAdvancesFollowSpacingAndScaling(t *testing.T) {
	content := "q 2 0 0 2 0 0 cm BT /F1 10 Tf 2 Tc 5 Tw 50 Tz 1 0 0 1 30 300 Tm (a b) Tj ET Q\n"
	frags, err := NewReaderOrFail(t, rawPageDoc(t, content)).PageTextFragments(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 1 || frags[0].Text != "a b" {
		t.Fatalf("fragments %+v", frags)
	}
	f := frags[0]
	// In text space: a, then 2 of character spacing; the space, 2 and 5;
	// b and 2. Half as wide (Tz 50), twice as large (cm).
	a := Helvetica.TextWidth("a", 10) + 2
	sp := Helvetica.TextWidth(" ", 10) + 2 + 5
	b := Helvetica.TextWidth("b", 10) + 2
	want := []float64{0, a, a + sp, a + sp + b}
	for i := range want {
		want[i] *= 0.5 * 2
		if math.Abs(f.Advances[i]-want[i]) > 1e-9 {
			t.Errorf("advance %d: %v, want %v", i, f.Advances[i], want[i])
		}
	}
	if math.Abs(f.Advances[3]-f.W) > 1e-9 {
		t.Errorf("the text ends at %v, its width is %v", f.Advances[3], f.W)
	}
}

// A code that wrote nothing is the replacement character, placed where
// the code was drawn; a code that wrote two characters shares its width.
func TestAdvancesOfMissingAndJoinedCodes(t *testing.T) {
	missing := &TextRun{Text: "AB", codeText: []int{1, 0, 1},
		codeAt: []float64{0, 500, 800}, codeW: []float64{500, 300, 600}, emPts: 0.01}
	text := spacedFragmentText(missing)
	if text != "A�B" {
		t.Fatalf("text %q", text)
	}
	want := []float64{0, 5, 8, 14}
	got, _ := fragmentAdvances(missing, text)
	if len(got) != len(want) {
		t.Fatalf("advances %v, want %v", got, want)
	}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Errorf("missing: advance %d: %v, want %v", i, got[i], want[i])
		}
	}

	joined := &TextRun{Text: "fix", codeText: []int{2, 1},
		codeAt: []float64{0, 700}, codeW: []float64{600, 300}, emPts: 0.01}
	want = []float64{0, 3, 7, 10}
	got, _ = fragmentAdvances(joined, spacedFragmentText(joined))
	if len(got) != len(want) {
		t.Fatalf("advances %v, want %v", got, want)
	}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Errorf("joined: advance %d: %v, want %v", i, got[i], want[i])
		}
	}

	// Codes the run did not place, or that do not account for its text:
	// nothing is guessed.
	if got, w := fragmentAdvances(&TextRun{Text: "AB", codeText: []int{1, 1}}, "AB"); got != nil || w != nil {
		t.Errorf("unplaced codes: %v", got)
	}
	short := &TextRun{Text: "ABC", codeText: []int{1, 1}, codeAt: []float64{0, 1}, codeW: []float64{1, 1}, emPts: 1}
	if got, w := fragmentAdvances(short, "ABC"); got != nil || w != nil {
		t.Errorf("codes short of the text: %v", got)
	}
}

// Every fragment of a page of ordinary text is placed, its advances in
// order, and ends where its width says.
func TestAdvancesEndAtTheWidth(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(HelveticaBold, 18)
	page.Text(60, 80, "Heading")
	page.SetFont(Helvetica, 11)
	page.Text(60, 120, "Body text here, with (brackets) and a long tail of words")
	page.SetFont(Courier, 9)
	page.Text(60, 160, "fixed   width   columns")
	frags, err := NewReaderOrFail(t, docBytes(t, doc)).PageTextFragments(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range frags {
		if len(f.Advances) != utf8.RuneCountInString(f.Text)+1 {
			t.Fatalf("%q: %d advances", f.Text, len(f.Advances))
		}
		for i := 1; i < len(f.Advances); i++ {
			if f.Advances[i] < f.Advances[i-1] {
				t.Errorf("%q: advance %d goes back", f.Text, i)
			}
		}
		if end := f.Advances[len(f.Advances)-1]; math.Abs(end-f.W) > 1e-9 {
			t.Errorf("%q ends at %v, its width is %v", f.Text, end, f.W)
		}
	}
}

// A word ends where its last character does, not where the next one is
// drawn: a kern between a word and the spaces after it is in neither.
// A space put in for a gap spans the gap.
func TestWidthsEndWordsWhereTheyEnd(t *testing.T) {
	content := "BT /F1 10 Tf 1 0 0 1 100 700 Tm [(description) -9000 (   Unit) -3000 (of)] TJ ET\n"
	frags, err := NewReaderOrFail(t, rawPageDoc(t, content)).PageTextFragments(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 1 || frags[0].Text != "description   Unit of" {
		t.Fatalf("fragments %+v", frags)
	}
	f := frags[0]
	if len(f.Widths) != len(f.Advances)-1 {
		t.Fatalf("%d widths for %d advances", len(f.Widths), len(f.Advances))
	}
	desc := Helvetica.TextWidth("description", 10)
	if end := f.Advances[10] + f.Widths[10]; math.Abs(end-desc) > 1e-9 {
		t.Errorf("description ends at %v, want %v", end, desc)
	}
	if got := f.Advances[11]; math.Abs(got-(desc+90)) > 1e-9 {
		t.Errorf("the space after the kern starts at %v, want %v", got, desc+90)
	}
	unitEnd := desc + 90 + Helvetica.TextWidth("   Unit", 10)
	// The space put in before "of" spans the 30 points the array moved.
	if got := f.Advances[18]; math.Abs(got-unitEnd) > 1e-9 {
		t.Errorf("the space before of starts at %v, want %v", got, unitEnd)
	}
	if got := f.Widths[18]; math.Abs(got-30) > 1e-9 {
		t.Errorf("the space before of is %v wide, want the gap, 30", got)
	}
	if got := f.Advances[19]; math.Abs(got-(unitEnd+30)) > 1e-9 {
		t.Errorf("of starts at %v, want %v", got, unitEnd+30)
	}
}

// Without kerns each character ends where the next starts.
func TestWidthsOfOrdinaryText(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 11)
	page.Text(60, 120, "Body text here, with (brackets)")
	frags, err := NewReaderOrFail(t, docBytes(t, doc)).PageTextFragments(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range frags {
		for i := range f.Widths {
			if math.Abs(f.Advances[i]+f.Widths[i]-f.Advances[i+1]) > 1e-9 {
				t.Errorf("%q: character %d ends at %v, the next starts at %v", f.Text, i, f.Advances[i]+f.Widths[i], f.Advances[i+1])
			}
		}
	}
	joined := &TextRun{Text: "fix", codeText: []int{2, 1},
		codeAt: []float64{0, 700}, codeW: []float64{600, 300}, emPts: 0.01}
	_, w := fragmentAdvances(joined, spacedFragmentText(joined))
	if len(w) != 3 || math.Abs(w[0]-3) > 1e-9 || math.Abs(w[1]-3) > 1e-9 || math.Abs(w[2]-3) > 1e-9 {
		t.Errorf("joined widths %v, want 3 each", w)
	}
}
