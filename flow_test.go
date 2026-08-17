package gopdf

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

// flowDoc renders a paragraph of the given lines and returns the file.
func flowDoc(t *testing.T, lines ...string) []byte {
	t.Helper()
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 11)
	y := 100.0
	for _, l := range lines {
		page.Text(60, y, l)
		y += 14
	}
	return docBytes(t, doc)
}

// editFlows opens a file and returns its page-one paragraphs.
func editFlows(t *testing.T, data []byte) (*Document, *EditablePage, []*Flow) {
	t.Helper()
	r, err := NewReader(data)
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	e, err := doc.EditPage(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	return doc, e, e.Flows()
}

func TestFlowGroupsAParagraph(t *testing.T) {
	src := flowDoc(t,
		"The quick brown fox jumps over the lazy",
		"dog and then keeps running until it is",
		"quite out of breath.")
	_, _, flows := editFlows(t, src)
	if len(flows) != 1 {
		t.Fatalf("got %d flows, want 1", len(flows))
	}
	f := flows[0]
	if f.LineCount() != 3 {
		t.Errorf("LineCount = %d, want 3", f.LineCount())
	}
	if !strings.HasPrefix(f.Text(), "The quick brown fox") {
		t.Errorf("Text = %q", f.Text())
	}
	if !strings.HasSuffix(f.Text(), "out of breath.") {
		t.Errorf("Text = %q", f.Text())
	}
	if f.LineHeight < 13 || f.LineHeight > 15 {
		t.Errorf("LineHeight = %v, want about 14", f.LineHeight)
	}
	if f.Width <= 0 {
		t.Errorf("Width = %v", f.Width)
	}
}

// TestFlowGrowsAndShrinks is the point of the engine: the paragraph takes
// however many lines the new text needs.
func TestFlowGrowsAndShrinks(t *testing.T) {
	src := flowDoc(t,
		"Alpha beta gamma delta epsilon zeta eta",
		"theta iota kappa lambda mu nu xi.")

	// Much longer.
	_, e, flows := editFlows(t, src)
	long := strings.TrimSpace(strings.Repeat("expanded wording that goes on ", 6))
	if err := flows[0].SetText(long); err != nil {
		t.Fatal(err)
	}
	if d := flows[0].LineDelta(); d <= 0 {
		t.Errorf("LineDelta = %d, want a paragraph that grew", d)
	}
	out := saveDoc(t, e)
	text := extractAll(t, out)
	if !strings.Contains(collapse(text), collapse(long)) {
		t.Errorf("the longer text did not survive:\n%q", text)
	}

	// Much shorter.
	_, e2, flows2 := editFlows(t, src)
	if err := flows2[0].SetText("Short."); err != nil {
		t.Fatal(err)
	}
	if d := flows2[0].LineDelta(); d >= 0 {
		t.Errorf("LineDelta = %d, want a paragraph that shrank", d)
	}
	out2 := saveDoc(t, e2)
	got := collapse(extractAll(t, out2))
	if !strings.Contains(got, "Short.") {
		t.Errorf("the shorter text is missing: %q", got)
	}
	if strings.Contains(got, "lambda") {
		t.Errorf("text from the old paragraph survived: %q", got)
	}
}

// TestFlowKeepsMixedStyling is the other half: a bold word inside a
// sentence stays bold when the sentence is rewritten around it.
func TestFlowKeepsMixedStyling(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 11)
	page.Text(60, 100, "Payment of ")
	w := Helvetica.TextWidth("Payment of ", 11)
	page.SetFont(HelveticaBold, 11)
	page.Text(60+w, 100, "EUR 1,200")
	w += HelveticaBold.TextWidth("EUR 1,200", 11)
	page.SetFont(Helvetica, 11)
	page.Text(60+w, 100, " is due on Friday.")
	src := docBytes(t, doc)

	_, e, flows := editFlows(t, src)
	if len(flows) != 1 {
		t.Fatalf("a mixed-style line should be one flow, got %d", len(flows))
	}
	f := flows[0]
	spans := f.Spans()
	if len(spans) != 3 {
		t.Fatalf("got %d spans, want 3: %+v", len(spans), spans)
	}
	if spans[1].Text != "EUR 1,200" {
		t.Errorf("middle span = %q", spans[1].Text)
	}
	boldName := spans[1].FontName
	if boldName == spans[0].FontName {
		t.Fatal("the fixture is not actually mixed")
	}

	// Replace inside the bold span; it must stay bold.
	n, err := f.Replace("EUR 1,200", "EUR 27,450.99")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("Replace reported %d occurrences, want 1", n)
	}
	got := f.Spans()
	var found bool
	for _, s := range got {
		if strings.Contains(s.Text, "27,450.99") {
			found = true
			if s.FontName != boldName {
				t.Errorf("the replacement is in %q, want the bold font %q",
					s.FontName, boldName)
			}
		}
	}
	if !found {
		t.Fatalf("the replacement is missing: %+v", got)
	}
	// And the surrounding text kept its own font.
	for _, s := range got {
		if strings.Contains(s.Text, "is due on Friday") && s.FontName == boldName {
			t.Error("the trailing text became bold")
		}
	}

	out := saveDoc(t, e)
	text := collapse(extractAll(t, out))
	if !strings.Contains(text, "Payment of EUR 27,450.99 is due on Friday.") {
		t.Errorf("rendered text = %q", text)
	}
	if !bytes.Contains(out, []byte("/F2")) && !bytes.Contains(out, []byte("Bold")) {
		t.Error("the bold font is no longer referenced")
	}
}

func TestFlowReplacePreservesNeighbours(t *testing.T) {
	src := flowDoc(t,
		"Invoice 2024-001 was issued to the client",
		"on the first of March and remains unpaid.")
	_, e, flows := editFlows(t, src)
	if _, err := flows[0].Replace("2024-001", "2026-114-REVISED"); err != nil {
		t.Fatal(err)
	}
	got := collapse(extractAll(t, saveDoc(t, e)))
	if !strings.Contains(got, "Invoice 2026-114-REVISED was issued") {
		t.Errorf("replacement not applied: %q", got)
	}
	if !strings.Contains(got, "remains unpaid.") {
		t.Errorf("the tail of the paragraph was lost: %q", got)
	}
}

func TestFlowReplaceEveryOccurrence(t *testing.T) {
	src := flowDoc(t,
		"Acme owns the site and Acme pays for it,",
		"so Acme carries the risk throughout.")
	_, e, flows := editFlows(t, src)
	n, err := flows[0].Replace("Acme", "Initech")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("replaced %d occurrences, want 3", n)
	}
	got := collapse(extractAll(t, saveDoc(t, e)))
	if strings.Contains(got, "Acme") {
		t.Errorf("an occurrence survived: %q", got)
	}
	if strings.Count(got, "Initech") != 3 {
		t.Errorf("want three replacements: %q", got)
	}
}

func TestFlowRewrapsToColumnWidth(t *testing.T) {
	src := flowDoc(t,
		"One two three four five six seven eight",
		"nine ten eleven twelve thirteen.")
	_, e, flows := editFlows(t, src)
	f := flows[0]
	width := f.Width
	if err := f.SetText(strings.TrimSpace(strings.Repeat("word ", 40))); err != nil {
		t.Fatal(err)
	}
	out := saveDoc(t, e)

	// Every rendered line must stay inside the original column.
	r2, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	doc2 := New()
	e2, err := doc2.EditPage(r2, 0)
	if err != nil {
		t.Fatal(err)
	}
	lines := 0
	for _, run := range e2.Runs() {
		if run.Text == "" {
			continue
		}
		lines++
		if run.Width > width+1 {
			t.Errorf("a line is %v wide, past the %v column: %q",
				run.Width, width, run.Text)
		}
	}
	if lines < 3 {
		t.Errorf("the text should have wrapped onto several lines, got %d", lines)
	}
}

func TestFlowMaxExtraLines(t *testing.T) {
	src := flowDoc(t, "A short paragraph on one line.")
	_, _, flows := editFlows(t, src)
	f := flows[0]
	f.SetMaxExtraLines(0)
	err := f.SetText(strings.TrimSpace(strings.Repeat("much longer text ", 20)))
	if err == nil {
		t.Fatal("growing past the cap should be refused")
	}
	if !strings.Contains(err.Error(), "more line") {
		t.Errorf("unhelpful error: %v", err)
	}
	// And the document must be untouched by the refusal.
	if f.LineDelta() != 0 {
		t.Errorf("a refused change reported a delta of %d", f.LineDelta())
	}
}

func TestFlowReplaceMissingText(t *testing.T) {
	src := flowDoc(t, "Nothing to see here.")
	_, _, flows := editFlows(t, src)
	n, err := flows[0].Replace("absent", "x")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("matched %d times in text that lacks it", n)
	}
	if _, err := flows[0].Replace("", "x"); err == nil {
		t.Error("an empty search should be refused")
	}
}

func TestFlowSetSpansValidation(t *testing.T) {
	src := flowDoc(t, "Some text.")
	_, _, flows := editFlows(t, src)
	f := flows[0]
	if err := f.SetSpans([]FlowSpan{{Text: "no style here"}}); err == nil {
		t.Error("a span without a style should be refused")
	}
	if err := f.SetSpans(nil); err == nil {
		t.Error("emptying a paragraph should be refused")
	}
	// A span borrowing an existing style is accepted.
	spans := f.Spans()
	spans[0].Text = "Replaced entirely."
	if err := f.SetSpans(spans); err != nil {
		t.Fatal(err)
	}
}

func TestFlowPageLevelReplace(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 11)
	page.Text(60, 100, "The vendor shall deliver on time.")
	page.Text(60, 160, "The vendor accepts the terms.")
	src := docBytes(t, doc)

	_, e, _ := editFlows(t, src)
	n, err := e.ReplaceTextFlow("vendor", "supplier of record")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("rewrote %d paragraphs, want 2", n)
	}
	got := collapse(extractAll(t, saveDoc(t, e)))
	if strings.Contains(got, "vendor") {
		t.Errorf("an occurrence survived: %q", got)
	}
	if strings.Count(got, "supplier of record") != 2 {
		t.Errorf("want two replacements: %q", got)
	}
	if _, err := e.ReplaceTextFlow("", "x"); err == nil {
		t.Error("an empty search should be refused")
	}
}

// TestFlowInUpdate checks the same thing through the incremental-update
// path, where the original file is preserved around the edit.
func TestFlowInUpdate(t *testing.T) {
	src := flowDoc(t,
		"The agreement runs for twelve months from",
		"the date of signature by both parties.")
	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	u := Update(r)
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	flows := page.Flows()
	if len(flows) != 1 {
		t.Fatalf("got %d flows, want 1", len(flows))
	}
	if _, err := flows[0].Replace("twelve months", "thirty-six calendar months"); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	got := collapse(extractAll(t, buf.Bytes()))
	if !strings.Contains(got, "thirty-six calendar months") {
		t.Errorf("replacement missing: %q", got)
	}
	if strings.Contains(got, "twelve months") {
		t.Errorf("the original wording survived: %q", got)
	}
}

func TestFlowReplaceInSpansUnit(t *testing.T) {
	a := flowStyle{fontName: "F1", fontSizeRaw: 10, horizScale: 1}
	b := flowStyle{fontName: "F2", fontSizeRaw: 10, horizScale: 1}
	spans := []FlowSpan{
		{Text: "Total ", style: a},
		{Text: "500", style: b},
		{Text: " due", style: a},
	}
	out, n := replaceInSpans(spans, "500", "12,345", matchWords)
	if n != 1 {
		t.Fatalf("count = %d, want 1", n)
	}
	var text string
	for _, s := range out {
		text += s.Text
	}
	if text != "Total 12,345 due" {
		t.Errorf("text = %q", text)
	}
	for _, s := range out {
		if strings.Contains(s.Text, "12,345") && s.style.fontName != "F2" {
			t.Errorf("the replacement took style %q, want F2", s.style.fontName)
		}
	}

	// A replacement spanning a style boundary takes the style it starts in.
	out2, n2 := replaceInSpans(spans, "500 due", "nothing owed", matchWords)
	if n2 != 1 {
		t.Fatalf("count = %d, want 1", n2)
	}
	for _, s := range out2 {
		if strings.Contains(s.Text, "nothing owed") && s.style.fontName != "F2" {
			t.Errorf("a cross-boundary replacement took %q, want F2", s.style.fontName)
		}
	}
}

func TestFlowSplitKeepingBreaks(t *testing.T) {
	got := splitKeepingBreaks("a b\nc")
	want := []string{"a", " ", "b", "\n", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

func TestFlowMergeSpans(t *testing.T) {
	a := flowStyle{fontName: "F1", fontSizeRaw: 10}
	b := flowStyle{fontName: "F2", fontSizeRaw: 10}
	in := []FlowSpan{
		{Text: "one", style: a}, {Text: " two", style: a},
		{Text: "three", style: b}, {Text: "four", style: b},
	}
	got := mergeSpans(in)
	if len(got) != 2 {
		t.Fatalf("merged into %d spans, want 2: %+v", len(got), got)
	}
	if got[0].Text != "one two" || got[1].Text != "threefour" {
		t.Errorf("merged wrongly: %+v", got)
	}
}

// saveDoc writes an edited document and returns the bytes.
func saveDoc(t *testing.T, e *EditablePage) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := e.Page.doc.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// collapse squeezes whitespace so extracted text can be compared without
// caring where the line breaks landed.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// cascadeDoc builds three paragraphs stacked down the page.
func cascadeDoc(t *testing.T) []byte {
	t.Helper()
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 11)
	page.Text(60, 100, "First paragraph mentions ACME twice over")
	page.Text(60, 114, "these two lines of running text.")
	page.Text(60, 200, "Second paragraph sits well below the")
	page.Text(60, 214, "first and should move as a unit.")
	page.Text(60, 300, "Third paragraph is the last one here.")
	return docBytes(t, doc)
}

// yOf returns the baseline of the run whose text contains want.
func yOf(t *testing.T, data []byte, want string) float64 {
	t.Helper()
	for text, pos := range runPositions(t, data) {
		if strings.Contains(text, want) {
			return pos[1]
		}
	}
	t.Fatalf("no run containing %q; runs are %v", want, runPositions(t, data))
	return 0
}

// TestFlowCascadePushesFollowingContent is the property that makes a
// growing paragraph usable: everything below it moves out of the way.
func TestFlowCascadePushesFollowingContent(t *testing.T) {
	src := cascadeDoc(t)
	beforeSecond := yOf(t, src, "Second paragraph")
	beforeThird := yOf(t, src, "Third paragraph")

	_, e, flows := editFlows(t, src)
	lineHeight := flows[0].LineHeight
	if lineHeight <= 0 {
		t.Fatalf("the first paragraph has no leading: %v", lineHeight)
	}
	n, err := e.ReplaceTextFlow("ACME",
		"a considerably longer corporate name that forces a rewrap")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rewrote %d paragraphs, want 1", n)
	}
	delta := flows[0].LineDelta()
	if delta <= 0 {
		t.Fatalf("the paragraph did not grow: delta=%d", delta)
	}

	out := saveDoc(t, e)
	want := float64(delta) * lineHeight
	for _, c := range []struct {
		name   string
		before float64
	}{{"Second paragraph", beforeSecond}, {"Third paragraph", beforeThird}} {
		got := yOf(t, out, c.name) - c.before
		if math.Abs(got-want) > 0.6 {
			t.Errorf("%s moved by %.2f, want %.2f", c.name, got, want)
		}
	}
}

func TestFlowCascadePullsUpWhenShrinking(t *testing.T) {
	src := cascadeDoc(t)
	beforeSecond := yOf(t, src, "Second paragraph")

	_, _, flows := editFlows(t, src)
	lineHeight := flows[0].LineHeight
	// SetText on its own does not cascade; the page-level call does.
	_, e2, flows2 := editFlows(t, src)
	if _, err := e2.ReplaceTextFlow(
		"First paragraph mentions ACME twice over these two lines of running text.",
		"Tiny."); err != nil {
		t.Fatal(err)
	}
	delta := flows2[0].LineDelta()
	if delta >= 0 {
		t.Fatalf("the paragraph did not shrink: delta=%d", delta)
	}
	got := yOf(t, saveDoc(t, e2), "Second paragraph") - beforeSecond
	want := float64(delta) * lineHeight
	if math.Abs(got-want) > 0.6 {
		t.Errorf("the following paragraph moved by %.2f, want %.2f", got, want)
	}
}

func TestFlowCascadeLeavesContentAboveAlone(t *testing.T) {
	src := cascadeDoc(t)
	beforeFirst := yOf(t, src, "First paragraph")

	_, e, _ := editFlows(t, src)
	if _, err := e.ReplaceTextFlow("Second paragraph sits well below the first and should move as a unit.",
		strings.TrimSpace(strings.Repeat("grown ", 30))); err != nil {
		t.Fatal(err)
	}
	out := saveDoc(t, e)
	if got := yOf(t, out, "First paragraph"); math.Abs(got-beforeFirst) > 0.01 {
		t.Errorf("the paragraph above moved from %.2f to %.2f", beforeFirst, got)
	}
}

// TestFlowRepeatedReplace covers editing the same page twice, which used
// to stack two edits on the same operators and fail.
func TestFlowRepeatedReplace(t *testing.T) {
	src := cascadeDoc(t)
	_, e, _ := editFlows(t, src)
	if _, err := e.ReplaceTextFlow("ACME", "Initech Corporation of Delaware"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ReplaceTextFlow("Second paragraph", "Clause two"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ReplaceTextFlow("Third", "Final"); err != nil {
		t.Fatal(err)
	}
	got := collapse(extractAll(t, saveDoc(t, e)))
	for _, want := range []string{"Initech Corporation of Delaware", "Clause two", "Final paragraph"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	for _, gone := range []string{"ACME", "Second paragraph", "Third paragraph"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q survived: %q", gone, got)
		}
	}
}

// TestFlowRepeatedReplaceKeepsSpacing checks that editing twice does not
// double-apply the displacement.
func TestFlowRepeatedReplaceKeepsSpacing(t *testing.T) {
	src := cascadeDoc(t)
	beforeThird := yOf(t, src, "Third paragraph")

	_, e, flows := editFlows(t, src)
	lineHeight := flows[0].LineHeight
	if _, err := e.ReplaceTextFlow("ACME", "a much longer replacement name indeed"); err != nil {
		t.Fatal(err)
	}
	delta := flows[0].LineDelta()
	// A second replacement that changes no lengths must move nothing.
	if _, err := e.ReplaceTextFlow("Third", "Final"); err != nil {
		t.Fatal(err)
	}
	got := yOf(t, saveDoc(t, e), "Final paragraph") - beforeThird
	want := float64(delta) * lineHeight
	if math.Abs(got-want) > 0.6 {
		t.Errorf("after two edits the paragraph moved %.2f, want %.2f", got, want)
	}
}

func TestFlowMixedStyleWrapUsesEachFont(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 10)
	page.Text(60, 100, "small ")
	w := Helvetica.TextWidth("small ", 10)
	page.SetFont(HelveticaBold, 10)
	page.Text(60+w, 100, "BOLDER")
	src := docBytes(t, doc)

	_, _, flows := editFlows(t, src)
	f := flows[0]
	spans := f.Spans()
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2", len(spans))
	}
	// The same text measures differently in each span's font.
	// Helvetica and Helvetica-Bold share many widths; "i" is one that
	// differs (222 against 278 per mille).
	wr, okr := spans[0].style.advance("iiii")
	wb, okb := spans[1].style.advance("iiii")
	if !okr || !okb {
		t.Fatal("both styles should be able to measure text")
	}
	if wb <= wr {
		t.Errorf("bold %v should be wider than regular %v", wb, wr)
	}
}

func TestFlowSingleLineGrowsWithSaneLeading(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 12)
	page.Text(60, 100, "One line only here.")
	src := docBytes(t, doc)

	_, _, flows := editFlows(t, src)
	f := flows[0]
	// A lone line has no measured leading; the fallback must be sane.
	if f.LineHeight < 12 || f.LineHeight > 16 {
		t.Errorf("LineHeight = %v, want about 14.4 for 12pt text", f.LineHeight)
	}
	if f.leadingTS >= 0 {
		t.Errorf("leadingTS = %v, want a negative step so later lines sit lower", f.leadingTS)
	}
}

func TestFlowUpdatePathCascades(t *testing.T) {
	src := cascadeDoc(t)
	beforeSecond := yOf(t, src, "Second paragraph")

	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	u := Update(r)
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	lineHeight := page.Flows()[0].LineHeight
	if _, err := page.ReplaceTextFlow("ACME", "a far longer name that will wrap the line"); err != nil {
		t.Fatal(err)
	}
	delta := page.Flows()[0].LineDelta()
	if delta <= 0 {
		t.Fatalf("delta = %d, want growth", delta)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	got := yOf(t, buf.Bytes(), "Second paragraph") - beforeSecond
	if math.Abs(got-float64(delta)*lineHeight) > 0.6 {
		t.Errorf("moved %.2f, want %.2f", got, float64(delta)*lineHeight)
	}
}

func TestFlowContinuesFlowRules(t *testing.T) {
	mk := func(y, x, size float64) flowLine {
		run := &TextRun{Y: y, X: x, FontSize: size, Text: "x"}
		return flowLine{runs: []*TextRun{run}, y: y}
	}
	first := mk(100, 60, 11)
	// A consistent second line joins and sets the leading.
	if lead, ok := continuesFlow(first, first, mk(114, 60, 11), 0); !ok || lead != 14 {
		t.Errorf("a normal second line should join with leading 14, got %v %v", lead, ok)
	}
	// A different size is a heading, not a continuation.
	if _, ok := continuesFlow(first, first, mk(114, 60, 15), 0); ok {
		t.Error("a line in a different size should not join")
	}
	// A different left edge is a different column.
	if _, ok := continuesFlow(first, first, mk(114, 200, 11), 0); ok {
		t.Error("a line at a different x should not join")
	}
	// Too far below is a separate paragraph.
	if _, ok := continuesFlow(first, first, mk(200, 60, 11), 0); ok {
		t.Error("a distant line should not join")
	}
	// Once a leading is established, a different one ends the paragraph.
	if _, ok := continuesFlow(first, mk(114, 60, 11), mk(140, 60, 11), 14); ok {
		t.Error("an inconsistent leading should end the paragraph")
	}
}

func TestFlowWrapRespectsExplicitNewlines(t *testing.T) {
	src := flowDoc(t, "Alpha beta gamma delta epsilon zeta.")
	_, e, flows := editFlows(t, src)
	if err := flows[0].SetText("First line.\nSecond line.\nThird line."); err != nil {
		t.Fatal(err)
	}
	out := saveDoc(t, e)
	r2, _ := NewReader(out)
	doc2 := New()
	e2, err := doc2.EditPage(r2, 0)
	if err != nil {
		t.Fatal(err)
	}
	var lines int
	for _, run := range e2.Runs() {
		if strings.TrimSpace(run.Text) != "" {
			lines++
		}
	}
	if lines != 3 {
		t.Errorf("got %d rendered lines, want 3 (one per explicit break)", lines)
	}
}

// TestFlowLeadingFromPageGeometry is the regression for a document that
// positions each line with its own transform. Deriving the step from the
// difference between two text matrices gave zero there, and every
// wrapped line landed on the same baseline.
func TestFlowLeadingFromPageGeometry(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 10)
	// Each line inside its own transform, so the text matrices match
	// while the lines are plainly apart on the page.
	for i, s := range []string{"First line of the paragraph", "second line of it"} {
		page.Push()
		page.Translate(0, float64(i)*13)
		page.Text(60, 100, s)
		page.Pop()
	}
	src := docBytes(t, doc)

	_, e, flows := editFlows(t, src)
	if len(flows) != 1 {
		t.Fatalf("got %d flows, want 1", len(flows))
	}
	f := flows[0]
	if f.leadingTS >= 0 {
		t.Errorf("leadingTS = %v, want a negative step", f.leadingTS)
	}
	if err := f.SetText(strings.TrimSpace(strings.Repeat("wrapping text here ", 8))); err != nil {
		t.Fatal(err)
	}
	// Every emitted line must sit on its own baseline.
	out := saveDoc(t, e)
	seen := map[float64]bool{}
	r2, _ := NewReader(out)
	doc2 := New()
	e2, err := doc2.EditPage(r2, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, run := range e2.Runs() {
		if strings.TrimSpace(run.Text) == "" {
			continue
		}
		n++
		if seen[math.Round(run.Y*10)] {
			t.Errorf("two lines share the baseline %.1f", run.Y)
		}
		seen[math.Round(run.Y*10)] = true
	}
	if n < 3 {
		t.Errorf("expected the text to wrap onto several lines, got %d", n)
	}
}

// TestFlowSpaceCharacterFallback is the regression for documents that
// write every gap as a non-breaking space: their fonts carry no U+0020,
// and inserting one encoded to nothing and ran the words together.
func TestFlowSpaceCharacterFallback(t *testing.T) {
	ascii := flowStyle{font: &fontInfo{
		encode: map[rune][]byte{'a': {'a'}, ' ': {' '}},
		built:  true,
	}, fontSizeRaw: 10, horizScale: 1}
	nbspOnly := flowStyle{font: &fontInfo{
		encode: map[rune][]byte{'a': {'a'}, ' ': {0xA0}},
		built:  true,
	}, fontSizeRaw: 10, horizScale: 1}
	none := flowStyle{font: &fontInfo{
		encode: map[rune][]byte{'a': {'a'}},
		built:  true,
	}, fontSizeRaw: 10, horizScale: 1}

	if got := ascii.spaceText(); got != " " {
		t.Errorf("an ordinary space should be preferred, got %q", got)
	}
	if got := nbspOnly.spaceText(); got != " " {
		t.Errorf("a font with only U+00A0 should use it, got %q", got)
	}
	if got := none.spaceText(); got != "" {
		t.Errorf("a font with no space should report none, got %q", got)
	}
}

// TestFlowRefusesWithoutASpaceGlyph checks that a paragraph whose font
// cannot set a space is declined rather than run together.
func TestFlowRefusesWithoutASpaceGlyph(t *testing.T) {
	f := &Flow{widthTS: 1000, maxExtra: -1}
	st := flowStyle{font: &fontInfo{
		encode: map[rune][]byte{'a': {'a'}, 'b': {'b'}},
		built:  true,
	}, fontSizeRaw: 10, horizScale: 1}
	_, err := f.wrap([]FlowSpan{{Text: "a b", style: st}})
	if err == nil {
		t.Fatal("wrapping two words with no space glyph should be refused")
	}
	if !strings.Contains(err.Error(), "space") {
		t.Errorf("the error should say why: %v", err)
	}
	// A single word needs no space and is fine.
	if _, err := f.wrap([]FlowSpan{{Text: "ab", style: st}}); err != nil {
		t.Errorf("a single word should not need a space: %v", err)
	}
}

func TestFlowTextScale(t *testing.T) {
	if got := textScale(&TextRun{FontSize: 24, fontSizeRaw: 12}); got != 2 {
		t.Errorf("textScale = %v, want 2", got)
	}
	// A degenerate run must not divide by zero.
	if got := textScale(&TextRun{}); got != 1 {
		t.Errorf("textScale of an empty run = %v, want 1", got)
	}
}

// TestFlowReadsGapsInsideOneOperation: a run can carry word breaks of
// its own, where one show-text operation draws "9.2.1" and then moves
// the pen before "Messaggio". A reader sees two words there, and a
// replacement of the second must find it.
func TestFlowReadsGapsInsideOneOperation(t *testing.T) {
	doc := New()
	doc.Compress = false
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.op("BT /F1 12 Tf 72 700 Td [(9.2.1) -2000 (Messaggio) -2000 (concernente)] TJ ET")
	src := docBytes(t, doc)

	r := NewReaderOrFail(t, src)
	u := Update(r)
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	n, err := page.ReplaceTextFlow("Messaggio", "[[TOKEN_1]]")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("replaced %d, want the one word after the gap", n)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	got, err := NewReaderOrFail(t, buf.Bytes()).PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "[[TOKEN_1]]") {
		t.Errorf("the replacement is not on the page: %q", got)
	}
	if strings.Contains(got, "Messaggio") {
		t.Errorf("the original survived: %q", got)
	}
	for _, keep := range []string{"9.2.1", "concernente"} {
		if !strings.Contains(got, keep) {
			t.Errorf("%q was lost: %q", keep, got)
		}
	}
}

// TestFlowKeepsAKernedWordWhole is the other side: a tight kern inside a
// word is not a break, and splitting there would let a replacement match
// half a word.
func TestFlowKeepsAKernedWordWhole(t *testing.T) {
	doc := New()
	doc.Compress = false
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.op("BT /F1 12 Tf 72 700 Td [(Sunder) -20 (land)] TJ ET")
	src := docBytes(t, doc)

	r := NewReaderOrFail(t, src)
	u := Update(r)
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	n, err := page.ReplaceTextFlow("land", "[[X]]")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("replaced %d; a kern was treated as a word break", n)
	}
	// And the whole word is still replaceable.
	n2, err := page.ReplaceTextFlow("Sunderland", "[[TOWN]]")
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 1 {
		t.Errorf("replaced %d of the whole word, want 1", n2)
	}
}

// TestFlowRefusesToWriteBelowThePage is a silent failure turned into an
// error.
//
// A paragraph on the last line of a page, given a replacement several
// times longer, grew downwards past the bottom edge. The lines were in
// the file and on no page: PageText reported them, the substitution
// check verified them, and no reader could see any of it. That is worse
// than saying the text does not fit.
func TestFlowRefusesToWriteBelowThePage(t *testing.T) {
	doc := New()
	doc.Compress = false
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(72, 830, "Contact Ada Lovelace about it.")
	src := docBytes(t, doc)

	long := strings.Repeat("[[LONG_TOKEN]] ", 10)

	// Through the flow API directly.
	r := NewReaderOrFail(t, src)
	u := Update(r)
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := page.ReplaceTextFlow("Ada Lovelace", long); err == nil {
		t.Error("a paragraph was allowed to grow past the bottom of the page")
	} else if !strings.Contains(err.Error(), "fit") {
		t.Errorf("the error does not explain itself: %v", err)
	}

	// And through pseudonymization, which is where it mattered.
	var buf bytes.Buffer
	if _, err := Pseudonymize(NewReaderOrFail(t, src), &buf,
		[]Pseudonym{{From: "Ada Lovelace", To: long}}); err == nil {
		t.Error("a substitution wrote text below the page")
	}
	if buf.Len() != 0 {
		t.Error("a document that did not fit was written anyway")
	}

	// A replacement that does fit still works, on the same page.
	var ok bytes.Buffer
	if _, err := Pseudonymize(NewReaderOrFail(t, src), &ok,
		[]Pseudonym{{From: "Ada Lovelace", To: "[[NAME_1]]"}}); err != nil {
		t.Fatalf("a replacement that fits was refused: %v", err)
	}
	got, err := NewReaderOrFail(t, ok.Bytes()).PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "[[NAME_1]]") {
		t.Errorf("the fitting replacement is not on the page: %q", got)
	}
}

// TestFlowGrowsWithinThePage: a paragraph with room below it may still
// grow, which is the whole point of the flow engine.
func TestFlowGrowsWithinThePage(t *testing.T) {
	doc := New()
	doc.Compress = false
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(72, 100, "Contact Ada Lovelace about it.")
	src := docBytes(t, doc)

	long := strings.Repeat("[[LONG_TOKEN]] ", 10)
	var buf bytes.Buffer
	if _, err := Pseudonymize(NewReaderOrFail(t, src), &buf,
		[]Pseudonym{{From: "Ada Lovelace", To: long}}); err != nil {
		t.Fatalf("a paragraph with room below it was refused: %v", err)
	}
	out := NewReaderOrFail(t, buf.Bytes())
	size, err := out.PageSize(0)
	if err != nil {
		t.Fatal(err)
	}
	frags, err := out.PageTextFragments(0)
	if err != nil {
		t.Fatal(err)
	}
	grew := 0
	for _, f := range frags {
		if !strings.Contains(f.Text, "LONG_TOKEN") {
			continue
		}
		grew++
		if f.Y > size.H {
			t.Errorf("a line was written at y=%.1f, below the %.0f-point page",
				f.Y, size.H)
		}
	}
	if grew < 2 {
		t.Errorf("the paragraph did not grow: %d lines carry the token", grew)
	}
}
