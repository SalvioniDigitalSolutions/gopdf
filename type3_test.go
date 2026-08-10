package gopdf

import (
	"fmt"
	"strings"
	"testing"
)

// type3Doc builds a document using a Type 3 font: glyphs drawn as little
// content streams, widths measured in the font's own glyph space.
//
// unitsPerEm sets the font matrix, so a test can check that widths are
// scaled by it rather than assumed to be in 1/1000 em.
func type3Doc(t *testing.T, unitsPerEm float64, text string) []byte {
	t.Helper()
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 10) // ensures a resource dictionary exists

	// Each glyph is a filled square, one per letter used.
	glyph := fmt.Sprintf("%s 0 d0 0 0 %s %s re f\n",
		fl(unitsPerEm*0.6), fl(unitsPerEm*0.6), fl(unitsPerEm*0.6))
	var procs, diffs, widths strings.Builder
	diffs.WriteString("[ 65")
	seen := map[rune]bool{}
	codes := map[rune]byte{}
	next := byte(65)
	for _, r := range text {
		if r == ' ' || seen[r] {
			continue
		}
		seen[r] = true
		codes[r] = next
		next++
	}
	// Emit the glyph names in code order.
	names := make([]string, 0, len(codes))
	order := make([]rune, 0, len(codes))
	for r := range codes {
		order = append(order, r)
	}
	for i := 0; i < len(order); i++ {
		for j := i + 1; j < len(order); j++ {
			if codes[order[j]] < codes[order[i]] {
				order[i], order[j] = order[j], order[i]
			}
		}
	}
	for _, r := range order {
		name := fmt.Sprintf("g%c", r)
		names = append(names, name)
		diffs.WriteString(" /" + name)
		procs.WriteString(fmt.Sprintf("/%s << >>\n", name))
		widths.WriteString(fl(unitsPerEm*0.6) + " ")
	}
	diffs.WriteString(" ]")

	// Build the objects by hand: the writer has no Type 3 API.
	var content strings.Builder
	content.WriteString("BT /T3 20 Tf 1 0 0 1 60 700 Tm (")
	for _, r := range text {
		if r == ' ' {
			continue
		}
		content.WriteByte(codes[r])
	}
	content.WriteString(") Tj ET\n")
	page.prelude = []byte(content.String())

	src := docBytes(t, doc)
	return injectType3(t, src, unitsPerEm, names, widths.String(), diffs.String(), glyph)
}

// injectType3 rewrites a document to carry a Type 3 font named /T3.
func injectType3(t *testing.T, src []byte, unitsPerEm float64,
	names []string, widths, diffs, glyph string) []byte {
	t.Helper()
	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	u := Update(r)

	// One shared procedure stream for every glyph keeps the fixture small.
	procNum := u.add(&rawStream{dict: Dict{}, data: []byte(glyph)})
	procs := Dict{}
	for _, n := range names {
		procs[Name(n)] = Ref{Num: procNum}
	}
	m := 1 / unitsPerEm
	fontNum := u.add(Dict{
		"Type":       Name("Font"),
		"Subtype":    Name("Type3"),
		"FontBBox":   Array{float64(0), float64(0), unitsPerEm, unitsPerEm},
		"FontMatrix": Array{m, float64(0), float64(0), m, float64(0), float64(0)},
		"CharProcs":  procs,
		"Encoding":   Dict{"Type": Name("Encoding"), "Differences": parseDiffs(t, diffs)},
		"FirstChar":  int64(65),
		"LastChar":   int64(65 + len(names) - 1),
		"Widths":     parseWidths(t, widths),
	})

	// Hang it off the page's resources.
	pi := r.pages[0]
	res, _ := r.resolve(pi.resources).(Dict)
	newRes := cloneDict(res)
	fonts, _ := r.resolve(newRes["Font"]).(Dict)
	newFonts := cloneDict(fonts)
	newFonts["T3"] = Ref{Num: fontNum}
	newRes["Font"] = newFonts

	pageDict := cloneDict(pi.dict)
	pageDict["Resources"] = newRes
	num, _ := r.pageObjectNumber(0)
	u.set(num, pageDict)

	return updateBytes(t, u)
}

func parseWidths(t *testing.T, s string) Array {
	t.Helper()
	var out Array
	for _, f := range strings.Fields(s) {
		var v float64
		fmt.Sscanf(f, "%g", &v)
		out = append(out, v)
	}
	return out
}

func parseDiffs(t *testing.T, s string) Array {
	t.Helper()
	var out Array
	for _, f := range strings.Fields(strings.Trim(s, "[] ")) {
		if strings.HasPrefix(f, "/") {
			out = append(out, Name(f[1:]))
			continue
		}
		var v int64
		fmt.Sscanf(f, "%d", &v)
		out = append(out, v)
	}
	return out
}

func updateBytes(t *testing.T, u *Updater) []byte {
	t.Helper()
	var b strings.Builder
	if _, err := u.WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	return []byte(b.String())
}

// TestType3WidthsUseTheFontMatrix is the substance: a Type 3 font's
// widths are in its own glyph space, and must be scaled by the font
// matrix rather than assumed to be in 1/1000 em.
func TestType3WidthsUseTheFontMatrix(t *testing.T) {
	// Two fonts drawing the same glyphs, one with a 1000-unit em and one
	// with a 250-unit em. Both must measure the same on the page.
	// The em is chosen so its reciprocal survives the writer's three
	// decimal places, which is a limit of the fixture, not the reader.
	wide := type3Run(t, 1000)
	small := type3Run(t, 250)
	if wide <= 0 {
		t.Fatalf("no advance measured: %v", wide)
	}
	if d := wide - small; d < -0.01 || d > 0.01 {
		t.Errorf("a 1000-unit em measured %v and a 250-unit em %v; the font "+
			"matrix is not being applied", wide, small)
	}
	// 4 glyphs at 0.6 em, 20pt type.
	if want := 4 * 0.6 * 20.0; wide < want-0.5 || wide > want+0.5 {
		t.Errorf("advance = %v, want about %v", wide, want)
	}
}

// type3Run returns the measured advance of the text in a Type 3 fixture.
func type3Run(t *testing.T, unitsPerEm float64) float64 {
	t.Helper()
	data := type3Doc(t, unitsPerEm, "abcd")
	r, err := NewReader(data)
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	e, err := doc.EditPage(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range e.Runs() {
		if run.FontName == "T3" {
			return run.Width
		}
	}
	t.Fatalf("no run in the Type 3 font; runs are %+v", e.Runs())
	return 0
}

// TestType3GlyphCoverage checks that only codes with a procedure behind
// them are considered usable, so an edit cannot ask for a glyph the font
// does not draw.
func TestType3GlyphCoverage(t *testing.T) {
	data := type3Doc(t, 1000, "abc")
	r, err := NewReader(data)
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	e, err := doc.EditPage(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fi *fontInfo
	for _, run := range e.Runs() {
		if run.FontName == "T3" {
			fi = run.font
		}
	}
	if fi == nil {
		t.Fatal("no Type 3 run found")
	}
	if !fi.type3 {
		t.Error("the font was not recognised as Type 3")
	}
	if len(fi.procs) != 3 {
		t.Errorf("found %d glyph procedures, want 3", len(fi.procs))
	}
	for code := uint32(65); code < 68; code++ {
		if !fi.canUse(code) {
			t.Errorf("code %d has a procedure but is reported unusable", code)
		}
	}
	if fi.canUse(200) {
		t.Error("a code with no procedure should not be usable")
	}
}

func TestType3WidthScaleUnit(t *testing.T) {
	r := &Reader{}
	cases := []struct {
		matrix any
		want   float64
	}{
		{Array{0.001, 0.0, 0.0, 0.001, 0.0, 0.0}, 1},
		{Array{1.0 / 250, 0.0, 0.0, 1.0 / 250, 0.0, 0.0}, 1000.0 / 250},
		{Array{0.0, 0.0, 0.0, 0.0, 0.0, 0.0}, 1}, // degenerate
		{nil, 1},
		{Array{1.0, 2.0}, 1}, // wrong length
	}
	for _, c := range cases {
		got := type3WidthScale(r, Dict{"FontMatrix": c.matrix})
		if d := got - c.want; d < -1e-9 && d > 1e-9 {
			t.Errorf("type3WidthScale(%v) = %v, want %v", c.matrix, got, c.want)
		}
	}
}

// TestType3TextExtracts covers the gap that made Type 3 documents come
// out empty: their glyphs carry names like /ga that mean nothing to a
// name table, and taking those as "no character" blanked the code
// instead of leaving the base encoding in place.
func TestType3TextExtracts(t *testing.T) {
	data := type3Doc(t, 1000, "abcd")
	got := extractAll(t, data)
	// The fixture maps the four glyphs to codes 65..68, so the base
	// encoding reads them back as ABCD.
	if !strings.Contains(got, "ABCD") {
		t.Errorf("Type 3 text did not extract: %q", got)
	}
}

// TestUnknownGlyphNameKeepsBaseEncoding is the same rule stated directly:
// a /Differences entry naming something unrecognisable must not wipe the
// character out.
func TestUnknownGlyphNameKeepsBaseEncoding(t *testing.T) {
	r := &Reader{}
	enc := Dict{"Differences": Array{
		int64(65), Name("g17"), // unrecognisable: keep 'A'
		int64(67), Name("bullet"), // recognisable: becomes •
	}}
	table := simpleEncoding(r, enc)
	if table[65] != 'A' {
		t.Errorf("an unknown glyph name blanked code 65: %q", table[65])
	}
	if table[66] != 'B' {
		t.Errorf("an untouched code changed: %q", table[66])
	}
	if table[67] != '•' {
		t.Errorf("a known glyph name was not applied: %q", table[67])
	}
}
