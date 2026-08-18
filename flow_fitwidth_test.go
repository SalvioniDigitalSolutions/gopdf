package gopdf

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

// The promise of FitWidth is narrow and checkable: the page comes back
// with its line breaks where they were, and the token is the width of
// the words it replaced. Both halves have to hold, and the second is
// what makes the first true — a test that only counted lines would pass
// on a token that had been quietly dropped.

const fitTo = "[REDACTED]"

// fitDoc lays out a paragraph with the name near the end of the first
// line, so a longer token has to push a word over unless it is fitted.
func fitDoc(t *testing.T, name string) []byte {
	t.Helper()
	return flowDoc(t,
		"The contract was signed by "+name+" and countersigned",
		"the following morning by the second party, who had",
		"waited most of the week for it.")
}

// fitLine is one baseline of the page: where it sits, where it starts,
// and which words landed on it.
type fitLine struct {
	y, x float64
	text string
}

// fitLines reports the page's lines. The words are the part that matters
// — this paragraph is left-aligned, so every line begins at the margin
// whether it re-wrapped or not, and comparing only the starts would call
// a re-wrapped paragraph unchanged.
func fitLines(t *testing.T, data []byte) []fitLine {
	t.Helper()
	frags, err := NewReaderOrFail(t, data).PageTextFragments(0)
	if err != nil {
		t.Fatal(err)
	}
	var out []fitLine
	for _, f := range frags {
		if f.Text == "" {
			continue
		}
		if n := len(out); n > 0 && math.Abs(out[n-1].y-f.Y) < 0.5 {
			out[n-1].text += f.Text // same baseline, later fragment
			continue
		}
		out = append(out, fitLine{y: f.Y, x: f.X, text: f.Text})
	}
	for i := range out {
		out[i].text = strings.Join(strings.Fields(out[i].text), " ")
	}
	return out
}

// fitToken returns the fragment holding the token and the size the rest
// of the paragraph is set in.
func fitToken(t *testing.T, data []byte) (tok TextFragment, bodySize float64) {
	t.Helper()
	frags, err := NewReaderOrFail(t, data).PageTextFragments(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range frags {
		if strings.TrimSpace(f.Text) == fitTo {
			tok = f
			continue
		}
		if f.FontSize > bodySize {
			bodySize = f.FontSize
		}
	}
	if tok.Text == "" {
		t.Fatalf("the token %q is not on the page", fitTo)
	}
	return tok, bodySize
}

func pseudo(t *testing.T, src []byte, subs []Pseudonym) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := Pseudonymize(NewReaderOrFail(t, src), &buf, subs); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// "G. Verdi" is 61% of the width of "[REDACTED]", which lands above the
// floor, so this pair is the one that fits exactly.
const fitName = "G. Verdi"

// TestFitWidthKeepsTheLayout is the whole feature in one set: same
// lines, same starts, token present and set smaller.
func TestFitWidthKeepsTheLayout(t *testing.T) {
	src := fitDoc(t, fitName)
	want := fitLines(t, src)
	out := pseudo(t, src, []Pseudonym{{From: fitName, To: fitTo, FitWidth: true}})
	got := fitLines(t, out)

	if len(got) != len(want) {
		t.Fatalf("the paragraph re-wrapped: %d lines, want %d", len(got), len(want))
	}
	for i := range want {
		// Every line should hold the words it held, the swap aside.
		wantText := strings.ReplaceAll(want[i].text, fitName, fitTo)
		if got[i].text != wantText {
			t.Errorf("line %d re-wrapped:\n got  %q\n want %q", i, got[i].text, wantText)
		}
		if math.Abs(got[i].y-want[i].y) > 0.01 {
			t.Errorf("line %d baseline moved: %.2f, want %.2f", i, got[i].y, want[i].y)
		}
		if math.Abs(got[i].x-want[i].x) > 0.01 {
			t.Errorf("line %d starts at %.2f, want %.2f", i, got[i].x, want[i].x)
		}
	}
	tok, body := fitToken(t, out)
	if tok.FontSize >= body {
		t.Errorf("the token was not shrunk: %.2f against a body of %.2f",
			tok.FontSize, body)
	}
	// Pseudonymize checks this itself; a test should not take it on trust.
	text, err := NewReaderOrFail(t, out).PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, fitName) {
		t.Errorf("the original is still on the page: %q", text)
	}
}

// TestFitWidthSizeIsTheWidthRatio pins the arithmetic. The size is not a
// guess that happened to fit; it is the one size at which the token is
// as wide as the name was.
func TestFitWidthSizeIsTheWidthRatio(t *testing.T) {
	out := pseudo(t, fitDoc(t, fitName),
		[]Pseudonym{{From: fitName, To: fitTo, FitWidth: true}})
	tok, body := fitToken(t, out)

	// Measured independently of the flow engine, through the same font
	// the fixture drew with.
	wFrom := Helvetica.TextWidth(fitName, body)
	wTo := Helvetica.TextWidth(fitTo, body)
	if want := body * wFrom / wTo; math.Abs(tok.FontSize-want) > 0.02 {
		t.Errorf("token size = %.3f, want %.3f (%.2fpt × %.2f/%.2f)",
			tok.FontSize, want, body, wFrom, wTo)
	}
	// And the point of that size: the token takes the width the name did.
	if math.Abs(tok.W-wFrom) > 0.02 {
		t.Errorf("token advance = %.3f, want %.3f", tok.W, wFrom)
	}
}

// TestFitWidthOffRewraps is the control. Without the flag the paragraph
// behaves as it always has, which is what makes the flag mean something.
func TestFitWidthOffRewraps(t *testing.T) {
	src := fitDoc(t, fitName)
	want := fitLines(t, src)
	out := pseudo(t, src, []Pseudonym{{From: fitName, To: fitTo}})
	got := fitLines(t, out)

	// Nothing was resized: with one size throughout, the whole line is a
	// single show-text operation, which is why the token is not a
	// fragment of its own here and fitToken is no use.
	frags, err := NewReaderOrFail(t, out).PageTextFragments(0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range frags {
		if strings.Contains(f.Text, fitTo) {
			found = true
		}
		if math.Abs(f.FontSize-11) > 0.01 {
			t.Errorf("%q was set at %.2f without FitWidth, want 11", f.Text, f.FontSize)
		}
	}
	if !found {
		t.Fatalf("the token %q is not on the page", fitTo)
	}

	same := len(got) == len(want)
	for i := 0; same && i < len(want); i++ {
		same = got[i].text == strings.ReplaceAll(want[i].text, fitName, fitTo)
	}
	if same {
		t.Error("the fixture does not re-wrap without FitWidth, so it cannot " +
			"show that FitWidth is what prevents it")
	}
}

// TestFitWidthFloorWins is the extreme the spec calls out: "Verdi" is
// 39% of the token's width, and holding the geometry would mean setting
// it at 39% of the body. It is set at the floor instead and the
// paragraph is allowed to re-wrap, because a token nobody can read has
// failed at the only thing it was for.
func TestFitWidthFloorWins(t *testing.T) {
	out := pseudo(t, fitDoc(t, "Verdi"),
		[]Pseudonym{{From: "Verdi", To: fitTo, FitWidth: true}})
	tok, body := fitToken(t, out)

	if want := body * fitWidthFloor; math.Abs(tok.FontSize-want) > 0.01 {
		t.Errorf("token size = %.3f, want the floor of %.3f", tok.FontSize, want)
	}
	// Wider than what it replaced, which is the whole reason it clamped.
	if wFrom := Helvetica.TextWidth("Verdi", body); tok.W <= wFrom {
		t.Errorf("token advance %.2f is not wider than %.2f; the floor did "+
			"not bite and this fixture proves nothing", tok.W, wFrom)
	}
}

// TestFitWidthNeverEnlarges: a token narrower than what it replaces
// keeps its size and the line gains slack.
func TestFitWidthNeverEnlarges(t *testing.T) {
	const long = "countersigned"
	out := pseudo(t, fitDoc(t, fitName),
		[]Pseudonym{{From: long, To: "[X]", FitWidth: true}})
	frags, err := NewReaderOrFail(t, out).PageTextFragments(0)
	if err != nil {
		t.Fatal(err)
	}
	var tok TextFragment
	body := 0.0
	for _, f := range frags {
		if strings.Contains(f.Text, "[X]") {
			tok = f
		}
		if f.FontSize > body {
			body = f.FontSize
		}
	}
	if tok.Text == "" {
		t.Fatal("the token is not on the page")
	}
	if math.Abs(tok.FontSize-body) > 0.01 {
		t.Errorf("a token narrower than what it replaced was resized to %.2f, "+
			"want the run's own %.2f", tok.FontSize, body)
	}
}

// TestFitWidthKeepsTheSpaceInFront: the token shrinks, the gap before it
// does not. Setting that space at the token's size closes the paragraph
// up by as much as the token was shrunk, and the word before it ends up
// touching the redaction.
func TestFitWidthKeepsTheSpaceInFront(t *testing.T) {
	out := pseudo(t, fitDoc(t, fitName),
		[]Pseudonym{{From: fitName, To: fitTo, FitWidth: true}})
	frags, err := NewReaderOrFail(t, out).PageTextFragments(0)
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range frags {
		if strings.TrimSpace(f.Text) != fitTo || i == 0 {
			continue
		}
		before := frags[i-1]
		gap := f.X - (before.X + before.W)
		if space := Helvetica.TextWidth(" ", before.FontSize); gap > 0.01 &&
			math.Abs(gap-space) > 0.01 {
			t.Errorf("the gap before the token is %.3f, want a full space of %.3f",
				gap, space)
		}
		if strings.HasSuffix(before.Text, " ") &&
			math.Abs(before.FontSize-11) > 0.01 {
			t.Errorf("the space before the token was set at %.2f, not the "+
				"document's 11", before.FontSize)
		}
		return
	}
	t.Fatal("the token is not on the page")
}

// TestFitWidthPerOccurrence: two occurrences in different sizes each get
// their own answer, since each is fitted against its own run.
func TestFitWidthPerOccurrence(t *testing.T) {
	doc := New()
	doc.Compress = false
	p := doc.AddPage()
	p.SetFont(Helvetica, 18)
	p.Text(60, 100, "Signed by G. Verdi in person")
	p.SetFont(Helvetica, 9)
	p.Text(60, 300, "Countersigned by G. Verdi as well")
	out := pseudo(t, docBytes(t, doc),
		[]Pseudonym{{From: fitName, To: fitTo, FitWidth: true}})

	frags, err := NewReaderOrFail(t, out).PageTextFragments(0)
	if err != nil {
		t.Fatal(err)
	}
	var sizes []float64
	for _, f := range frags {
		if strings.TrimSpace(f.Text) == fitTo {
			sizes = append(sizes, f.FontSize)
		}
	}
	if len(sizes) != 2 {
		t.Fatalf("found %d tokens, want 2: %v", len(sizes), sizes)
	}
	ratio := Helvetica.TextWidth(fitName, 100) / Helvetica.TextWidth(fitTo, 100)
	for i, base := range []float64{18, 9} {
		if want := base * ratio; math.Abs(sizes[i]-want) > 0.02 {
			t.Errorf("the %gpt occurrence fitted to %.3f, want %.3f",
				base, sizes[i], want)
		}
	}
	if sizes[0] == sizes[1] {
		t.Error("both occurrences got the same size, so they were not fitted " +
			"against their own runs")
	}
}

// TestReverseDropsFitWidth: putting the original text back has no width
// to fit, and carrying the flag over would shrink it against itself.
func TestReverseDropsFitWidth(t *testing.T) {
	for _, s := range Reverse([]Pseudonym{
		{From: "Ada Lovelace", To: "[[P1]]", FitWidth: true},
		{From: "Verdi", To: "[[P2]]", FitWidth: true},
	}) {
		if s.FitWidth {
			t.Errorf("Reverse kept FitWidth on %q → %q", s.From, s.To)
		}
	}
}

// TestFitWidthSurvivesVariantSpellings: a mapping is expanded into the
// spellings a document might have used instead, and each of those is the
// same substitution — so each must fit the same way.
func TestFitWidthSurvivesVariantSpellings(t *testing.T) {
	got := expandAllVariants([]Pseudonym{{From: "G. Verdi", To: fitTo, FitWidth: true}})
	if len(got) < 2 {
		t.Fatalf("the mapping expanded to %d spellings, want more than one", len(got))
	}
	for _, v := range got {
		if !v.FitWidth {
			t.Errorf("the %q spelling lost FitWidth", v.From)
		}
	}
}

// TestFitWidthWithCharacterSpacing is why the size is solved for rather
// than taken as a ratio of the two widths.
//
// Character spacing is an absolute number of points per glyph: it does
// not shrink when the font does. So the advance is not proportional to
// the size, it is affine in it, and a token with more characters than
// the name it replaces carries more of that fixed part. Scaling by
// wFrom/wTo leaves it too wide by the difference — here by about 10% of
// the name's own width, which is a visible shove.
func TestFitWidthWithCharacterSpacing(t *testing.T) {
	const tc = 1.5
	line := "The contract was signed by " + fitName + " and countersigned"
	src := rawPageDoc(t, "BT /F1 11 Tf "+fl(tc)+" Tc 1 0 0 1 60 700 Tm ("+
		line+") Tj ET\n")
	out := pseudo(t, src, []Pseudonym{{From: fitName, To: fitTo, FitWidth: true}})
	tok, _ := fitToken(t, out)

	// What the name took, spacing and all.
	wFrom := Helvetica.TextWidth(fitName, 11) + tc*float64(len([]rune(fitName)))
	if math.Abs(tok.W-wFrom) > 0.05 {
		t.Errorf("token advance = %.3f, want %.3f", tok.W, wFrom)
	}
	// And the size a plain ratio would have chosen, which does not fit.
	wTo := Helvetica.TextWidth(fitTo, 11) + tc*float64(len([]rune(fitTo)))
	naive := 11 * wFrom / wTo
	if math.Abs(tok.FontSize-naive) < 0.1 {
		t.Errorf("the size %.3f is the plain ratio %.3f, which does not account "+
			"for character spacing", tok.FontSize, naive)
	}
	if wNaive := Helvetica.TextWidth(fitTo, naive) +
		tc*float64(len([]rune(fitTo))); math.Abs(wNaive-wFrom) < 0.5 {
		t.Skip("this fixture's spacing is too small to tell the two apart")
	}
}

// TestFlowSetFitWidth drives the paragraph-level switch directly, for a
// caller working through Flows rather than through Pseudonymize.
func TestFlowSetFitWidth(t *testing.T) {
	src := fitDoc(t, fitName)
	_, _, flows := editFlows(t, src)
	if len(flows) != 1 {
		t.Fatalf("got %d flows, want 1", len(flows))
	}
	f := flows[0]
	before := f.LineCount()
	f.SetFitWidth(true)
	if n, err := f.Replace(fitName, fitTo); err != nil || n != 1 {
		t.Fatalf("Replace = %d, %v", n, err)
	}
	if got := f.LineCount(); got != before {
		t.Errorf("the paragraph went from %d lines to %d", before, got)
	}
	// The token is its own span now, set smaller than the text around it.
	var tokSize, bodySize float64
	for _, s := range f.Spans() {
		if strings.Contains(s.Text, fitTo) {
			tokSize = s.FontSize
		} else if s.FontSize > bodySize {
			bodySize = s.FontSize
		}
	}
	if tokSize == 0 {
		t.Fatalf("the token is not among the spans: %q", f.Text())
	}
	if tokSize >= bodySize {
		t.Errorf("FontSize on the token span is %.2f, not below the body's %.2f",
			tokSize, bodySize)
	}
}

// TestFitSizeRefusals covers the guards. Each of them stands in front of
// a division, and returning "no fit" is the only safe answer when the
// measurement that division needs is not available.
func TestFitSizeRefusals(t *testing.T) {
	// A real style to vary, taken from a document rather than invented.
	_, _, flows := editFlows(t, fitDoc(t, fitName))
	base := flows[0].Spans()[0].style
	if base.font == nil {
		t.Fatal("the fixture span has no font")
	}

	for _, c := range []struct {
		name string
		st   flowStyle
		text string
		want float64
	}{
		{"no size", withSize(base, 0), fitTo, 20},
		{"no width to hit", base, fitTo, 0},
		{"nothing to set", base, "", 20},
		{"already narrower", base, "i", 500},
		{"font cannot set it", base, "儀", 20},
	} {
		if got, ok := fitSize(c.st, c.text, c.want); ok {
			t.Errorf("%s: fitSize returned %v, want no fit", c.name, got)
		}
	}

	// A style whose glyphs have no width at all leaves nothing to trade
	// away, however much spacing is set around them.
	zero := base
	zero.charSpacing = 50
	if got, ok := fitSize(zero, " ", 1); ok && got <= 0 {
		t.Errorf("a zero-width run fitted to %v", got)
	}

	// And the ordinary case still works, so the guards are not simply
	// refusing everything.
	if _, ok := fitSize(base, fitTo, Helvetica.TextWidth(fitName, base.fontSizeRaw)); !ok {
		t.Error("fitSize refused a fit it should have made")
	}
}

func withSize(s flowStyle, size float64) flowStyle {
	s.fontSizeRaw = size
	return s
}
