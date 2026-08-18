package gopdf

import (
	"bytes"
	"strings"
	"testing"
)

// A fitted token claims exactly the width of the words it replaces, in
// both directions: a wider one is set smaller, a narrower one is padded.
// When every token on a page fits, the page is edited in place — the
// strings holding the tokens are rewritten and nothing else is, kerns
// included — so anything painted over the text at a fixed position is
// still over the same text afterwards.

// justifiedPage draws a line the way a word processor justifies one:
// each word its own string, with a kern between them carrying the extra
// space. A rewrite that reproduced the line from measurements would not
// give those kerns back.
func justifiedPage(t *testing.T, words ...string) []byte {
	t.Helper()
	var b strings.Builder
	b.WriteString("BT /F1 11 Tf 1 0 0 1 60 700 Tm [")
	for i, w := range words {
		if i > 0 {
			b.WriteString(" -260")
		}
		b.WriteString(" (" + w + ")")
	}
	b.WriteString(" ] TJ ET\n")
	// A rule drawn under the line, at a fixed place: it is what a
	// re-wrap would leave stranded.
	b.WriteString("q 0 0 0 RG 1 w 60 694 m 400 694 l S Q\n")
	return rawPageDoc(t, b.String())
}

func fitExact(t *testing.T, src []byte, subs []Pseudonym) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := Pseudonymize(NewReaderOrFail(t, src), &buf, subs); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestFitExactNarrowTokenIsPadded is the half that changed: a token
// narrower than what it replaces used to keep its size and let the line
// gain slack. It now claims the same width, so nothing after it moves.
func TestFitExactNarrowTokenIsPadded(t *testing.T) {
	src := justifiedPage(t, "Contratto", "con", "Edoardo", "Salvioni", "oggi")
	out := fitExact(t, src, []Pseudonym{{From: "Edoardo", To: "***", FitWidth: true}})

	before := fragsOf(t, src)
	after := fragsOf(t, out)
	if len(before) == 0 || len(after) == 0 {
		t.Fatal("nothing was drawn")
	}
	// The line still ends where it ended: the padding took up the slack.
	b, a := before[len(before)-1], after[len(after)-1]
	if got, want := a.X+a.W, b.X+b.W; got < want-0.05 || got > want+0.05 {
		t.Errorf("the line now ends at %.3f, want %.3f", got, want)
	}
	if !strings.Contains(joinFrags(after), "***") {
		t.Errorf("the token is not on the page: %q", joinFrags(after))
	}
}

// TestFitExactKeepsWhatFollows: the words after the token stay exactly
// where they were, which is the point of padding rather than closing up.
//
// Measured in ink rather than in reported positions: the whole line is
// one operation, so a fragment covers all of it and asking where a word
// inside it starts means interpolating, which for a proportional face is
// a guess. The pixels are not a guess.
func TestFitExactKeepsWhatFollows(t *testing.T) {
	src := justifiedPage(t, "Contratto", "con", "Edoardo", "Salvioni", "oggi")
	out := fitExact(t, src, []Pseudonym{{From: "Edoardo", To: "***", FitWidth: true}})

	before := inkCols(t, src)
	after := inkCols(t, out)
	if len(before) != len(after) {
		t.Fatalf("the renders differ in width: %d and %d", len(before), len(after))
	}
	// Where the token sits, the ink is expected to differ. Everything to
	// the right of it must not.
	last := 0
	for x := range before {
		if before[x] != after[x] {
			last = x
		}
	}
	if last == 0 {
		t.Fatal("nothing changed at all; the substitution did not happen")
	}
	// The token is one word of five; anything beyond the middle of the
	// line is text that followed it.
	for x := last + 1; x < len(before); x++ {
		if before[x] != after[x] {
			t.Fatalf("column %d changed after the last token column %d", x, last)
		}
	}
	tokenEnd := last
	tail := 0
	for x := tokenEnd + 1; x < len(before); x++ {
		if before[x] {
			tail++
		}
	}
	if tail < 20 {
		t.Fatalf("only %d inked columns follow the token; the fixture does "+
			"not show that following words held their place", tail)
	}
}

// inkCols reports which pixel columns of the rendered page carry ink.
func inkCols(t *testing.T, data []byte) []bool {
	t.Helper()
	// The fixture's face is one of the standard fourteen, which a
	// document does not carry, so the renderer needs a stand-in to have
	// any outlines to draw at all.
	img, err := NewReaderOrFail(t, data).RenderPage(0, RenderOpts{
		DPI: 150, IncludeText: true, IncludeVector: true,
		SubstituteFont: SystemFonts(),
	})
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	out := make([]bool, b.Dx())
	// Only the band the text sits in. The rule beneath it runs the width
	// of the line and would mark every column inked whatever the text
	// did, which would make this measure agree with anything.
	top, bottom := 270, 304
	for x := b.Min.X; x < b.Max.X; x++ {
		for y := top; y < bottom && y < b.Dy(); y++ {
			if r, _, _, a := img.At(b.Min.X+x-b.Min.X, b.Min.Y+y).RGBA(); a > 0x4000 && r < 0x8000 {
				out[x-b.Min.X] = true
				break
			}
		}
	}
	return out
}

// TestFitExactLeavesTheRestOfThePageAlone is the payoff, and the spec's
// bonus assertion: the content stream outside the rewritten string is
// unchanged, byte for byte — kerns, rule and all.
func TestFitExactLeavesTheRestOfThePageAlone(t *testing.T) {
	src := justifiedPage(t, "Contratto", "con", "Edoardo", "Salvioni", "oggi")
	out := fitExact(t, src, []Pseudonym{{From: "Edoardo", To: "***", FitWidth: true}})

	// The rule is drawn by operators of its own and must survive intact.
	const rule = "60 694 m 400 694 l S"
	if !bytes.Contains(pageContent(t, out), []byte(rule)) {
		t.Errorf("the rule under the line was not left alone:\n%s",
			pageContent(t, out))
	}
	// And the kerns that justify the line are still there.
	if n := bytes.Count(pageContent(t, out), []byte("-260")); n < 3 {
		t.Errorf("only %d of the line's kerns survived:\n%s", n,
			pageContent(t, out))
	}
}

// TestFitExactWideTokenStillShrinks: the other direction is unchanged.
func TestFitExactWideTokenStillShrinks(t *testing.T) {
	src := justifiedPage(t, "Contratto", "con", "Edo", "Salvioni", "oggi")
	out := fitExact(t, src, []Pseudonym{
		{From: "Edo", To: "[REDACTED]", FitWidth: true},
	})
	var tok TextFragment
	body := 0.0
	for _, f := range fragsOf(t, out) {
		if strings.Contains(f.Text, "[REDACTED]") {
			tok = f
		} else if f.FontSize > body {
			body = f.FontSize
		}
	}
	if tok.Text == "" {
		t.Fatalf("the token is not on the page: %q", joinFrags(fragsOf(t, out)))
	}
	if tok.FontSize >= body {
		t.Errorf("a token wider than what it replaced was not shrunk: %.2f vs %.2f",
			tok.FontSize, body)
	}
}

// TestFitExactSeveralTokensOneRun: two names in one string. Each is
// rewritten from the text as it then stands, so the first is not undone
// by the second.
func TestFitExactSeveralTokensOneRun(t *testing.T) {
	src := justifiedPage(t, "da", "Lugano", "in", "Locarno", "oggi")
	out := fitExact(t, src, []Pseudonym{
		{From: "Lugano", To: "***", FitWidth: true},
		{From: "Locarno", To: "****", FitWidth: true},
	})
	text := joinFrags(fragsOf(t, out))
	for _, gone := range []string{"Lugano", "Locarno"} {
		if strings.Contains(text, gone) {
			t.Errorf("%q survived: %q", gone, text)
		}
	}
	for _, want := range []string{"***", "****"} {
		if !strings.Contains(text, want) {
			t.Errorf("%q was not written: %q", want, text)
		}
	}
}

// --- helpers ---

func fragsOf(t *testing.T, data []byte) []TextFragment {
	t.Helper()
	f, err := NewReaderOrFail(t, data).PageTextFragments(0)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func joinFrags(fs []TextFragment) string {
	var b strings.Builder
	for _, f := range fs {
		b.WriteString(f.Text)
	}
	return b.String()
}

// pageContent returns page one's content stream, decoded.
func pageContent(t *testing.T, data []byte) []byte {
	t.Helper()
	r := NewReaderOrFail(t, data)
	stm, ok := r.resolve(r.PageDict(0)["Contents"]).(*rawStream)
	if !ok {
		t.Fatal("the page has no single content stream")
	}
	out, err := r.decodeStream(stm.dict, stm.data)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// splitWordPage draws a line the way the documents that prompted this
// draw one: a word broken into several strings with kerns between them,
// which is how a producer sets tracking. The token replacing it has to
// account for those kerns, because on the page they are part of its
// width.
func splitWordPage(t *testing.T) []byte {
	t.Helper()
	var b strings.Builder
	b.WriteString("BT /F1 11 Tf 1 0 0 1 60 700 Tm [ (in) -260 " +
		"(Lo) 12 (c) 8 (a) 10 (r) 6 (no) -260 (oggi) ] TJ ET\n")
	return rawPageDoc(t, b.String())
}

// TestFitExactTokenAcrossStrings: the occurrence spans five strings and
// the kerns between them. Replacing it has to leave the words on either
// side where they were.
func TestFitExactTokenAcrossStrings(t *testing.T) {
	src := splitWordPage(t)
	if got := joinFrags(fragsOf(t, src)); !strings.Contains(got, "Locarno") {
		t.Fatalf("the fixture does not read as a word: %q", got)
	}
	out := fitExact(t, src, []Pseudonym{{From: "Locarno", To: "***", FitWidth: true}})

	text := joinFrags(fragsOf(t, out))
	if strings.Contains(text, "Locarno") {
		t.Errorf("the original survived: %q", text)
	}
	if !strings.Contains(text, "***") {
		t.Errorf("the token was not written: %q", text)
	}
	// The line still ends where it did: the kerns inside the replaced
	// word were counted, so the token claimed their width too.
	before, after := fragsOf(t, src), fragsOf(t, out)
	b, a := before[len(before)-1], after[len(after)-1]
	if got, want := a.X+a.W, b.X+b.W; got < want-0.05 || got > want+0.05 {
		t.Errorf("the line ends at %.3f, want %.3f: the kerns inside the "+
			"replaced word were not counted", got, want)
	}
	// The kerns outside the replaced word are still in the stream.
	if n := bytes.Count(pageContent(t, out), []byte("-260")); n != 2 {
		t.Errorf("%d of the two outer kerns survived:\n%s", n, pageContent(t, out))
	}
}

// TestFitExactWidthCountsInnerKerns pins the arithmetic hitWidth does.
//
// A TJ number is subtracted from the displacement, so the positive ones
// this fixture sets its word with draw it tighter than its glyphs alone
// would be. Either way the token has to claim the width the word really
// took, not the width its letters add up to.
func TestFitExactWidthCountsInnerKerns(t *testing.T) {
	src := splitWordPage(t)
	r := NewReaderOrFail(t, src)
	u := Update(r)
	p, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range p.runs {
		if !strings.Contains(run.Text, "Locarno") {
			continue
		}
		st := styleOf(run)
		// The run's own text has no spaces — they are inferred from the
		// pen moves — so the word is located by offset rather than by a
		// bounded match, which would find no boundary to match on.
		i := strings.Index(run.Text, "Locarno")
		if i < 0 {
			t.Fatalf("the run does not hold the word: %q", run.Text)
		}
		at := [][2]int{{i, i + len("Locarno")}}
		pi := 0
		for i, pc := range run.pieces {
			if pc.to > at[0][0] {
				pi = i
				break
			}
		}
		glyphs, _ := st.advance("Locarno")
		got, ok := hitWidth(run, st, at[0], pi)
		if !ok {
			t.Fatal("the width could not be measured")
		}
		if got == glyphs {
			t.Errorf("width %.4f is the glyphs' own, so the tracking inside "+
				"the word was not counted at all", got)
		}
		// 12+8+10+6 thousandths of an em, subtracted, at this size.
		want := glyphs - 36.0/1000*st.fontSizeRaw*st.horizScale
		if got < want-0.001 || got > want+0.001 {
			t.Errorf("width = %.4f, want %.4f", got, want)
		}
		return
	}
	t.Fatal("no run holds the word")
}

// splitAcrossOpsPage draws a name in two operations — the producer
// changed face part way through it, which is how a company name set half
// in bold reaches the page. The two halves read as one name and have to
// be replaced as one.
func splitAcrossOpsPage(t *testing.T) []byte {
	t.Helper()
	head := Helvetica.TextWidth("con Salvioni ", 11)
	var b strings.Builder
	b.WriteString("BT /F1 11 Tf 1 0 0 1 60 700 Tm (con Salvioni ) Tj ET\n")
	b.WriteString("BT /F1 11 Tf 1 0 0 1 " + fl(60+head) + " 700 Tm (Digital Sagl oggi) Tj ET\n")
	return rawPageDoc(t, b.String())
}

// TestFitExactAcrossOperations: a name split across two show-text
// operations used to send the whole page to the paragraph engine, and
// with it every other substitution on that page. The first operation
// takes the token, at the width the name covered on the page; the second
// gives up its share and keeps its own width, so what follows stays put.
func TestFitExactAcrossOperations(t *testing.T) {
	src := splitAcrossOpsPage(t)
	if got := joinFrags(fragsOf(t, src)); !strings.Contains(got, "Salvioni Digital Sagl") {
		t.Fatalf("the fixture does not read as one name: %q", got)
	}
	out := fitExact(t, src, []Pseudonym{
		{From: "Salvioni Digital Sagl", To: "***", FitWidth: true},
	})

	text := joinFrags(fragsOf(t, out))
	if strings.Contains(text, "Salvioni Digital Sagl") {
		t.Errorf("the original survived: %q", text)
	}
	if !strings.Contains(text, "***") {
		t.Errorf("the token was not written: %q", text)
	}
	if !strings.Contains(text, "oggi") {
		t.Errorf("the word after the name was lost: %q", text)
	}
	// The word after the name is where it was: the second operation
	// keeps its own width even though its head was taken away.
	before, after := fragsOf(t, src), fragsOf(t, out)
	b, a := before[len(before)-1], after[len(after)-1]
	if got, want := a.X+a.W, b.X+b.W; got < want-0.05 || got > want+0.05 {
		t.Errorf("the line ends at %.3f, want %.3f", got, want)
	}
}

// leaderPage sets a line whose dash leader fills the measure exactly, so
// the smallest disagreement between the advance the document accounted
// for and the one a re-wrap computes flips the leader onto a line of its
// own. A paragraph on that knife edge is the reason the wrap is not
// recomputed at all when every token fits: the question never has to be
// asked, so it cannot be answered differently.
func leaderPage(t *testing.T, name string) []byte {
	t.Helper()
	const size = 11
	head := name + ", il 12 (dodici) marzo 2026 (due mila venti sei)."
	// Fill what is left of a 475pt measure with the leader, to the point
	// where one more dash would not fit.
	dashes := ""
	for Helvetica.TextWidth(head+dashes+"-", size) <= 475 {
		dashes += "-"
	}
	var b strings.Builder
	b.WriteString("BT /F1 11 Tf 1 0 0 1 60 700 Tm (" + head + dashes + ") Tj ET\n")
	b.WriteString("BT /F1 11 Tf 1 0 0 1 60 686 Tm (Davanti a me notaio, oggi.) Tj ET\n")
	return rawPageDoc(t, b.String())
}

// TestFitExactLeaderDoesNotWrap: the line is full to the last dash, and
// a token of exactly the same width as the name must leave it that way.
// A line gained here would push every line below it down the page, and
// anything painted at fixed coordinates would be left behind.
func TestFitExactLeaderDoesNotWrap(t *testing.T) {
	src := leaderPage(t, "Locarno")
	out := fitExact(t, src, []Pseudonym{
		{From: "Locarno", To: "*****", FitWidth: true},
	})

	before, after := lineYs(t, src), lineYs(t, out)
	if len(after) != len(before) {
		t.Fatalf("the paragraph re-wrapped: %d lines, want %d", len(after), len(before))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("line %d moved from %.4f to %.4f", i, before[i], after[i])
		}
	}
	if got := joinFrags(fragsOf(t, out)); !strings.Contains(got, "*****") ||
		strings.Contains(got, "Locarno") {
		t.Errorf("the substitution did not happen: %q", got)
	}
	// Line count and baselines are necessary but not sufficient: a
	// re-wrap that happened to land on the same lines would pass them.
	// The line the substitution did not touch has to come through as the
	// operators the document wrote, or the page went round the paragraph
	// engine after all and only luck put it back.
	const untouched = "(Davanti a me notaio, oggi.) Tj"
	if !bytes.Contains(pageContent(t, out), []byte(untouched)) {
		t.Errorf("the untouched line was rewritten, so the page was "+
			"re-laid-out:\n%s", pageContent(t, out))
	}
}

// lineYs returns the distinct baselines of page one, in order.
func lineYs(t *testing.T, data []byte) []float64 {
	t.Helper()
	var ys []float64
	for _, f := range fragsOf(t, data) {
		if strings.TrimSpace(f.Text) == "" {
			continue
		}
		if n := len(ys); n > 0 && ys[n-1] == f.Y {
			continue
		}
		ys = append(ys, f.Y)
	}
	return ys
}
