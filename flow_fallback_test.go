package gopdf

import (
	"bytes"
	"strings"
	"testing"
)

// subsetFontDoc builds a document whose font declares only the glyphs its
// own text uses, as a subsetted font does. Anything else — a bracket, an
// underscore — cannot be set in it.
func subsetFontDoc(t *testing.T, text string, bold bool) []byte {
	t.Helper()
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	f := Helvetica
	if bold {
		f = HelveticaBold
	}
	page.SetFont(f, 11)
	page.Text(60, 100, text)
	src := docBytes(t, doc)
	return narrowFont(t, src, text, bold)
}

// narrowFont rewrites the document's font so that it is an embedded
// subset covering only the characters in text: the widths array is
// trimmed to them and a descriptor with a font file is attached, which is
// what makes canUse refuse anything else.
func narrowFont(t *testing.T, src []byte, text string, bold bool) []byte {
	t.Helper()
	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	u := Update(r)

	used := map[byte]bool{}
	for _, b := range winAnsiEncode(text) {
		used[b] = true
	}
	lo, hi := 255, 0
	for b := range used {
		if int(b) < lo {
			lo = int(b)
		}
		if int(b) > hi {
			hi = int(b)
		}
	}
	widths := Array{}
	for c := lo; c <= hi; c++ {
		if used[byte(c)] {
			widths = append(widths, float64(600))
			continue
		}
		widths = append(widths, float64(0))
	}
	// A font file makes the font embedded, so only observed codes count.
	prog := u.add(&rawStream{dict: Dict{"Length1": int64(4)}, data: []byte("fake")})
	base := "AAAAAA+Helvetica"
	flags := int64(32)
	if bold {
		base = "AAAAAA+Helvetica-Bold"
		flags = 32 | 1<<18
	}
	desc := u.add(Dict{
		"Type": Name("FontDescriptor"), "FontName": Name(base),
		"Flags": flags, "FontFile": Ref{Num: prog},
	})
	fontNum := u.add(Dict{
		"Type": Name("Font"), "Subtype": Name("TrueType"),
		"BaseFont": Name(base), "FirstChar": int64(lo), "LastChar": int64(hi),
		"Widths": widths, "Encoding": Name("WinAnsiEncoding"),
		"FontDescriptor": Ref{Num: desc},
	})

	pi := r.pages[0]
	res, _ := r.resolve(pi.resources).(Dict)
	newRes := cloneDict(res)
	fonts, _ := r.resolve(newRes["Font"]).(Dict)
	newFonts := cloneDict(fonts)
	for name := range newFonts {
		newFonts[name] = Ref{Num: fontNum}
	}
	newRes["Font"] = newFonts
	pd := cloneDict(pi.dict)
	pd["Resources"] = newRes
	num, _ := r.pageObjectNumber(0)
	u.set(num, pd)

	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

//  1. A token the document's own subset cannot set lands in a fallback
//     font, the rest of the line keeps the original, and the original text
//     is gone.
func TestFallbackSetsTokenTheSubsetCannot(t *testing.T) {
	src := subsetFontDoc(t, "The claimant Ada Lovelace attended.", false)
	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	u := Update(r)
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	n, err := page.ReplaceTextFlow("Ada Lovelace", "[[PII_NAME_1]]")
	if err != nil {
		t.Fatalf("the substitution should fall back, not fail: %v", err)
	}
	if n != 1 {
		t.Fatalf("replaced %d paragraphs, want 1", n)
	}
	var out bytes.Buffer
	if _, err := u.WriteTo(&out); err != nil {
		t.Fatal(err)
	}
	got := collapse(extractAll(t, out.Bytes()))
	if !strings.Contains(got, "[[PII_NAME_1]]") {
		t.Errorf("the token is missing: %q", got)
	}
	if strings.Contains(got, "Ada Lovelace") {
		t.Errorf("the original survived: %q", got)
	}
	if !strings.Contains(got, "The claimant") || !strings.Contains(got, "attended.") {
		t.Errorf("the text either side was lost: %q", got)
	}

	// 6. The fallback font is in the page resources under the
	//    collision-free prefix, and the token uses it.
	r2, _ := NewReader(out.Bytes())
	res, _ := r2.resolve(r2.pages[0].resources).(Dict)
	fonts, _ := r2.resolve(res["Font"]).(Dict)
	prefix := page.Page.resPrefix
	var fallbackName Name
	for name := range fonts {
		if strings.HasPrefix(string(name), prefix) {
			fallbackName = name
		}
	}
	if fallbackName == "" {
		t.Fatalf("no font under the prefix %q; resources are %v", prefix, fonts)
	}
	content, _ := r2.pageContent(r2.pages[0].dict)
	if !bytes.Contains(content, []byte("/"+string(fallbackName)+" ")) {
		t.Errorf("the token does not use the fallback font %q", fallbackName)
	}
}

//  2. Only inserted text may be restyled: the document's own text keeps
//     its font whatever happens.
func TestFallbackNeverRestylesOriginalText(t *testing.T) {
	src := subsetFontDoc(t, "The claimant Ada Lovelace attended.", false)
	r, _ := NewReader(src)
	u := Update(r)
	page, _ := u.Page(0)
	flows := page.Flows()
	if len(flows) != 1 {
		t.Fatalf("got %d flows", len(flows))
	}
	before := flows[0].Spans()[0].style
	if _, err := flows[0].Replace("Ada Lovelace", "[[PII_NAME_1]]"); err != nil {
		t.Fatal(err)
	}
	for _, s := range flows[0].Spans() {
		if s.inserted {
			continue
		}
		if !s.style.sameAs(before) {
			t.Errorf("original text %q was restyled from %q to %q",
				s.Text, before.fontName, s.style.fontName)
		}
	}
}

// mergeSpans must not let the inserted flag spread onto original text,
// which would hand it permission to be restyled.
func TestFallbackInsertedFlagDoesNotBleed(t *testing.T) {
	st := flowStyle{fontName: "F1", fontSizeRaw: 10, horizScale: 1}
	got := mergeSpans([]FlowSpan{
		{Text: "before ", style: st},
		{Text: "TOKEN", style: st, inserted: true},
		{Text: " after", style: st},
	})
	if len(got) != 3 {
		t.Fatalf("merged into %d spans, want 3: %+v", len(got), got)
	}
	if got[0].inserted || got[2].inserted {
		t.Errorf("the inserted flag spread onto original text: %+v", got)
	}
	if !got[1].inserted {
		t.Error("the inserted span lost its flag")
	}
	// Adjacent spans that agree still merge.
	same := mergeSpans([]FlowSpan{
		{Text: "a", style: st, inserted: true},
		{Text: "b", style: st, inserted: true},
	})
	if len(same) != 1 || same[0].Text != "ab" {
		t.Errorf("spans that agree should merge: %+v", same)
	}
}

//  3. A token holding a character even the fallback cannot set is still
//     refused, rather than drawn as a blank box.
func TestFallbackStillRefusesImpossibleToken(t *testing.T) {
	src := subsetFontDoc(t, "The claimant Ada Lovelace attended.", false)
	r, _ := NewReader(src)
	u := Update(r)
	page, _ := u.Page(0)
	// An arrow is outside cp1252, so no standard font can set it.
	_, err := page.ReplaceTextFlow("Ada Lovelace", "name → redacted")
	if err == nil {
		t.Fatal("a token Helvetica cannot set should be refused")
	}
	if !strings.Contains(err.Error(), "cannot") {
		t.Errorf("unhelpful error: %v", err)
	}
}

// 5. A bold source gets a bold fallback.
func TestFallbackMatchesTheFace(t *testing.T) {
	for _, c := range []struct {
		bold bool
		want string
	}{{false, "Helvetica"}, {true, "Helvetica-Bold"}} {
		src := subsetFontDoc(t, "Amount Ada Lovelace due", c.bold)
		r, _ := NewReader(src)
		u := Update(r)
		page, _ := u.Page(0)
		flows := page.Flows()
		if len(flows) == 0 {
			t.Fatal("no flows")
		}
		if got := standardFallback(flows[0].Spans()[0].style); got.name != c.want {
			t.Errorf("bold=%v chose %q, want %q", c.bold, got.name, c.want)
		}
		if _, err := flows[0].Replace("Ada Lovelace", "[[PII_1]]"); err != nil {
			t.Fatalf("bold=%v: %v", c.bold, err)
		}
		var out bytes.Buffer
		if _, err := u.WriteTo(&out); err != nil {
			t.Fatal(err)
		}
		r2, _ := NewReader(out.Bytes())
		if !bytes.Contains(out.Bytes(), []byte(c.want)) {
			t.Errorf("bold=%v: the output does not reference %q", c.bold, c.want)
		}
		if got := collapse(extractAll(t, out.Bytes())); !strings.Contains(got, "[[PII_1]]") {
			t.Errorf("bold=%v: token missing from %q", c.bold, got)
		}
		_ = r2
	}
}

func TestBuiltinFontInfo(t *testing.T) {
	fi := builtinFontInfo(Helvetica, "Gp0F1")
	codes, err := fi.encodeText("[[PII_NAME_1]]")
	if err != nil {
		t.Fatalf("Helvetica should set a bracket: %v", err)
	}
	if string(codes) != "[[PII_NAME_1]]" {
		t.Errorf("encoded to %q", codes)
	}
	// Widths must agree with the authoring metrics.
	want := Helvetica.TextWidth("Hello", 10)
	em := fi.stringWidth([]byte("Hello"), 0, 0, 10)
	if got := em / 1000 * 10; got < want-0.01 || got > want+0.01 {
		t.Errorf("width = %v, want %v", got, want)
	}
	// A rune outside cp1252 has no code.
	if _, err := fi.encodeText("→"); err == nil {
		t.Error("an arrow should not encode in WinAnsi")
	}
	// Nothing is embedded, so every code it knows is usable.
	if !fi.canUse('A') {
		t.Error("a standard font should be able to set A")
	}
}

func TestStandardFallbackFaces(t *testing.T) {
	mk := func(bold, italic bool) flowStyle {
		return flowStyle{font: &fontInfo{bold: bold, italic: italic}}
	}
	cases := []struct {
		style flowStyle
		want  string
	}{
		{mk(false, false), "Helvetica"},
		{mk(true, false), "Helvetica-Bold"},
		{mk(false, true), "Helvetica-Oblique"},
		{mk(true, true), "Helvetica-BoldOblique"},
		{flowStyle{}, "Helvetica"},
	}
	for _, c := range cases {
		if got := standardFallback(c.style); got.name != c.want {
			t.Errorf("got %q, want %q", got.name, c.want)
		}
	}
}

// --- hyphen at a line break ---

// hyphenatedDoc sets a word split across two lines, as justified text
// does: "Bian-" ends the first line and "chi" starts the second.
func hyphenatedDoc(t *testing.T, first, second string) []byte {
	t.Helper()
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 11)
	page.Text(60, 100, first)
	page.Text(60, 114, second)
	return docBytes(t, doc)
}

func TestFlowJoinsHyphenAtLineBreak(t *testing.T) {
	src := hyphenatedDoc(t, "Contract with Marco Bian-", "chi of this parish.")
	_, _, flows := editFlows(t, src)
	if len(flows) != 1 {
		t.Fatalf("got %d flows, want 1", len(flows))
	}
	f := flows[0]
	if got := f.joinedText(); !strings.Contains(got, "Bianchi") {
		t.Errorf("the split word was not joined: %q", got)
	}
	// The paragraph's own text keeps the hyphen; only the reading joins.
	if !strings.Contains(f.Text(), "Bian-") {
		t.Errorf("the original text was altered: %q", f.Text())
	}
}

func TestFlowReplacesSplitWord(t *testing.T) {
	src := hyphenatedDoc(t, "Contract with Marco Bian-", "chi of this parish.")
	_, e, flows := editFlows(t, src)
	n, err := flows[0].Replace("Bianchi", "[[PII_NAME_1]]")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("replaced %d, want 1", n)
	}
	got := collapse(extractAll(t, saveDoc(t, e)))
	if !strings.Contains(got, "[[PII_NAME_1]]") {
		t.Errorf("the token is missing: %q", got)
	}
	if strings.Contains(got, "Bian-") || strings.Contains(got, "Bianchi") {
		t.Errorf("part of the split word survived: %q", got)
	}
	if !strings.Contains(got, "Contract with Marco") || !strings.Contains(got, "of this parish.") {
		t.Errorf("the text around it was lost: %q", got)
	}
}

// A hyphen in the middle of a line is real text and must not be joined
// away: "CHE-290" is one token, not "CHE290".
func TestFlowKeepsMidLineHyphen(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 11)
	page.Text(60, 100, "Registry CHE-290 applies here")
	page.Text(60, 114, "to every case of this kind.")
	src := docBytes(t, doc)

	_, _, flows := editFlows(t, src)
	if got := flows[0].joinedText(); strings.Contains(got, "CHE290") {
		t.Errorf("a mid-line hyphen was joined away: %q", got)
	}
	if got := flows[0].joinedText(); !strings.Contains(got, "CHE-290") {
		t.Errorf("the hyphen was lost: %q", got)
	}
}

func TestPseudonymizeSplitWordVerified(t *testing.T) {
	src := hyphenatedDoc(t, "Contract with Marco Bian-", "chi of this parish.")
	var out bytes.Buffer
	res, err := Pseudonymize(NewReaderOrFail(t, src), &out,
		[]Pseudonym{{From: "Bianchi", To: "[[PII_NAME_1]]"}})
	if err != nil {
		t.Fatalf("a split word should be matched and verified: %v", err)
	}
	if res.Total() == 0 {
		t.Error("nothing was replaced")
	}
	got := collapse(extractAll(t, out.Bytes()))
	if !strings.Contains(got, "[[PII_NAME_1]]") {
		t.Errorf("the token is missing: %q", got)
	}
}

// A split word nobody asked to replace must still be seen by the check,
// or a survivor passes unnoticed.
func TestResidueSeesSplitWord(t *testing.T) {
	src := hyphenatedDoc(t, "Contract with Marco Bian-", "chi of this parish.")
	r := NewReaderOrFail(t, src)
	where, err := findResidue(r, []Pseudonym{{From: "Bianchi", To: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if where == "" {
		t.Error("the check did not see a word split across a line break")
	}
}

func TestDehyphenate(t *testing.T) {
	got := dehyphenate("Bian-\nchi and\nMarco")
	if !strings.Contains(got, "Bianchi") {
		t.Errorf("dehyphenate = %q", got)
	}
	// A dash that is not a word split stays, and so does the break.
	if got := dehyphenate("see —\nnext"); !strings.Contains(got, "—\nnext") {
		t.Errorf("a dash was treated as a word split: %q", got)
	}
	if got := dehyphenate("plain\ntext"); got != "plain\ntext" {
		t.Errorf("unhyphenated text changed: %q", got)
	}
}

// --- fragmented runs ---

// TestFlowInfersSpacesBetweenFragments covers a document that sets a line
// one fragment at a time, carrying the gaps by positioning rather than by
// space characters, as troff and TeX do.
func TestFlowInfersSpacesBetweenFragments(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 11)
	x := 60.0
	for _, word := range []string{"Contract", "with", "Marco", "Bianchi"} {
		page.Text(x, 100, word)
		x += Helvetica.TextWidth(word+" ", 11)
	}
	src := docBytes(t, doc)

	_, e, flows := editFlows(t, src)
	if len(flows) != 1 {
		t.Fatalf("got %d flows", len(flows))
	}
	if got := flows[0].Text(); !strings.Contains(got, "Contract with Marco Bianchi") {
		t.Errorf("gaps were not read as spaces: %q", got)
	}
	n, err := flows[0].Replace("Marco Bianchi", "[[PII_NAME_1]]")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("replaced %d, want 1", n)
	}
	got := collapse(extractAll(t, saveDoc(t, e)))
	if !strings.Contains(got, "Contract with [[PII_NAME_1]]") {
		t.Errorf("got %q", got)
	}
}

// --- rotated pages ---

func TestFlowOnRotatedText(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.Push()
	page.RotateAt(90, 300, 400)
	page.SetFont(Helvetica, 11)
	page.Text(200, 400, "Contract with Marco Bianchi here")
	page.Pop()
	src := docBytes(t, doc)

	_, e, flows := editFlows(t, src)
	if len(flows) == 0 {
		t.Fatal("rotated text produced no flows")
	}
	var target *Flow
	for _, f := range flows {
		if strings.Contains(f.Text(), "Bianchi") {
			target = f
		}
	}
	if target == nil {
		t.Fatalf("the rotated text was not grouped; flows are %d", len(flows))
	}
	if _, err := target.Replace("Marco Bianchi", "[[P1]]"); err != nil {
		t.Fatal(err)
	}
	got := collapse(extractAll(t, saveDoc(t, e)))
	if !strings.Contains(got, "[[P1]]") {
		t.Errorf("the rotated replacement is missing: %q", got)
	}
	if strings.Contains(got, "Marco Bianchi") {
		t.Errorf("the original survived: %q", got)
	}
}

// --- fitting ---

func TestFlowShrinkToFit(t *testing.T) {
	// A paragraph wide enough that a modest reduction makes the token fit.
	src := flowDoc(t, "Registry reference number for this case")
	_, _, flows := editFlows(t, src)
	f := flows[0]
	width := f.widthTS
	base := f.Spans()[0].style

	long := "[[PII_REG_NUMBER_0001_EXTENDED_FORM]]"
	f.SetShrinkToFit(true, 4)
	if err := f.SetSpans([]FlowSpan{{Text: long, style: base, inserted: true}}); err != nil {
		t.Fatal(err)
	}
	got := f.Spans()[0].style
	if got.fontSizeRaw >= base.fontSizeRaw {
		t.Errorf("the token was not shrunk: %v, original %v",
			got.fontSizeRaw, base.fontSizeRaw)
	}
	if w, ok := got.advance(long); !ok || w > width*1.02 {
		t.Errorf("the shrunk token still does not fit: %v > %v", w, width)
	}
}

// The floor is respected even when respecting it means the token still
// does not fit: type too small to read is not a fix.
func TestFlowShrinkToFitHonoursTheFloor(t *testing.T) {
	src := flowDoc(t, "Ref 7")
	_, _, flows := editFlows(t, src)
	f := flows[0]
	base := f.Spans()[0].style
	f.SetShrinkToFit(true, 6)
	if err := f.SetSpans([]FlowSpan{
		{Text: "[[PII_REG_NUMBER_0001]]", style: base, inserted: true},
	}); err != nil {
		t.Fatal(err)
	}
	if got := f.Spans()[0].style.fontSizeRaw; got < 6 {
		t.Errorf("shrunk to %v, below the floor of 6", got)
	}
}

// Shrinking is off unless asked for, and never touches the document's
// own text.
func TestFlowShrinkOnlyInsertedAndOptIn(t *testing.T) {
	src := flowDoc(t, "Registry reference number for this case")
	_, _, flows := editFlows(t, src)
	f := flows[0]
	base := f.Spans()[0].style
	long := "[[PII_REG_NUMBER_0001_EXTENDED_FORM]]"

	// Off by default.
	if err := f.SetSpans([]FlowSpan{{Text: long, style: base, inserted: true}}); err != nil {
		t.Fatal(err)
	}
	if got := f.Spans()[0].style.fontSizeRaw; got != base.fontSizeRaw {
		t.Errorf("shrinking happened without being asked: %v", got)
	}

	// And original text is left alone even when it is on.
	_, _, flows2 := editFlows(t, src)
	g := flows2[0]
	g.SetShrinkToFit(true, 4)
	spans := []FlowSpan{
		{Text: "kept ", style: base},
		{Text: long, style: base, inserted: true},
	}
	if err := g.SetSpans(spans); err != nil {
		t.Fatal(err)
	}
	for _, sp := range g.Spans() {
		if !sp.inserted && sp.style.fontSizeRaw != base.fontSizeRaw {
			t.Errorf("original text %q was resized to %v", sp.Text, sp.style.fontSizeRaw)
		}
	}
}

func TestFlowOverflowsPage(t *testing.T) {
	src := flowDoc(t, "One line only.")
	_, _, flows := editFlows(t, src)
	f := flows[0]
	if f.OverflowsPage(A4.H) {
		t.Error("a paragraph near the top should not overflow")
	}
	if f.OverflowsPage(0) {
		t.Error("an unknown page height cannot overflow")
	}
	// Grow it far past the page and it should say so.
	f.SetMaxExtraLines(-1)
	if err := f.SetText(strings.TrimSpace(strings.Repeat("filler words here ", 300))); err != nil {
		t.Fatal(err)
	}
	if !f.OverflowsPage(A4.H) {
		t.Errorf("a paragraph of %d lines should overflow the page", f.curLines)
	}
	if err := checkPageOverflow([]*Flow{f}, A4.H); err == nil {
		t.Error("checkPageOverflow should report it")
	}
}

// TestFallbackSetsTokenInOneFont is the regression for a token encoded
// against two tables at once. The decision to fall back was once made
// word by word, so a token could be set partly in the document's font and
// partly in another, and the join between them came out as the wrong
// character.
func TestFallbackSetsTokenInOneFont(t *testing.T) {
	// A subset that can set some of the token's characters but not all:
	// the letters are there, the brackets are not.
	src := subsetFontDoc(t, "The claimant Ada Lovelace expanded test", false)
	r, _ := NewReader(src)
	u := Update(r)
	page, _ := u.Page(0)
	flows := page.Flows()
	if len(flows) != 1 {
		t.Fatalf("got %d flows", len(flows))
	}
	token := "(expanded test)"
	if _, err := flows[0].Replace("Ada Lovelace", token); err != nil {
		t.Fatal(err)
	}
	// Every piece of the token must share one style.
	var styles []flowStyle
	for _, s := range flows[0].Spans() {
		if s.inserted {
			styles = append(styles, s.style)
		}
	}
	if len(styles) == 0 {
		t.Fatal("no inserted spans")
	}
	for _, st := range styles[1:] {
		if st.fontName != styles[0].fontName {
			t.Errorf("the token is split across fonts %q and %q",
				styles[0].fontName, st.fontName)
		}
	}
	var out bytes.Buffer
	if _, err := u.WriteTo(&out); err != nil {
		t.Fatal(err)
	}
	got := collapse(extractAll(t, out.Bytes()))
	if !strings.Contains(got, token) {
		t.Errorf("the token did not survive intact: %q", got)
	}
}
