package gopdf

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
)

const pseudoName = "Ada Lovelace"

// hidingPlacesDoc plants the same name everywhere a PDF can keep one:
// drawn on the page, in the metadata, in an annotation, a bookmark, a
// form field and an XMP packet.
func hidingPlacesDoc(t *testing.T) []byte {
	t.Helper()
	doc := New()
	doc.SetInfo(Info{
		Title:    "File on " + pseudoName,
		Author:   pseudoName,
		Subject:  "Concerning " + pseudoName,
		Keywords: pseudoName + ", claimant",
	})
	page := doc.AddPage()
	page.SetFont(Helvetica, 11)
	page.Text(60, 100, "The claimant "+pseudoName+" attended on 3 May")
	page.Text(60, 114, "and the matter was resolved in full.")
	page.AddNote(300, 100, "Spoke to "+pseudoName+" by telephone", NoteOptions{
		Author: pseudoName,
	})
	doc.AddOutline(nil, "Statement of "+pseudoName, page, 100)
	page.AddTextField("claimant", 60, 200, 200, 20, FieldOptions{
		Value: pseudoName,
	})
	src := docBytes(t, doc)
	return withXMP(t, src, "<x:xmpmeta><dc:creator>"+pseudoName+"</dc:creator></x:xmpmeta>")
}

// withXMP attaches an uncompressed XMP metadata packet to the catalog.
func withXMP(t *testing.T, src []byte, xml string) []byte {
	t.Helper()
	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	u := Update(r)
	num := u.add(&rawStream{
		dict: Dict{"Type": Name("Metadata"), "Subtype": Name("XML")},
		data: []byte(xml),
	})
	rootRef := r.trailer["Root"].(Ref)
	root, _ := r.resolve(rootRef).(Dict)
	newRoot := cloneDict(root)
	newRoot["Metadata"] = Ref{Num: num}
	u.set(rootRef.Num, newRoot)
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestPseudonymizeLeavesNoTrace is the promise: after substitution the
// original must not be recoverable from anywhere in the file.
func TestPseudonymizeLeavesNoTrace(t *testing.T) {
	src := hidingPlacesDoc(t)
	// The fixture really does hide the name in all those places.
	if !bytes.Contains(src, []byte(pseudoName)) {
		t.Fatal("the fixture does not contain the name at all")
	}

	var out bytes.Buffer
	res, err := Pseudonymize(NewReaderOrFail(t, src), &out, []Pseudonym{
		{From: pseudoName, To: "[[PII_NAME_1]]"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total() == 0 {
		t.Error("nothing was reported as replaced")
	}
	got := out.Bytes()

	// 1. Not in the page text.
	text := extractAll(t, got)
	if strings.Contains(text, pseudoName) {
		t.Errorf("the name is still on the page: %q", text)
	}
	if !strings.Contains(text, "[[PII_NAME_1]]") {
		t.Errorf("the token is missing from the page: %q", text)
	}

	// 2. Not in the metadata.
	r := NewReaderOrFail(t, got)
	info := r.Info()
	for field, v := range map[string]string{
		"Title": info.Title, "Author": info.Author,
		"Subject": info.Subject, "Keywords": info.Keywords,
	} {
		if strings.Contains(v, pseudoName) {
			t.Errorf("the name survives in Info.%s: %q", field, v)
		}
	}

	// 3. Not in an annotation.
	annots, err := r.Annotations(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range annots {
		if strings.Contains(a.Contents, pseudoName) || strings.Contains(a.Author, pseudoName) {
			t.Errorf("the name survives in an annotation: %+v", a)
		}
	}

	// 4. Not in a form field value.
	for _, f := range r.FormFields() {
		if strings.Contains(f.Value, pseudoName) {
			t.Errorf("the name survives in field %q: %q", f.Name, f.Value)
		}
	}

	// 5. Not anywhere in the raw bytes, which covers the bookmark title
	//    and the XMP packet.
	if bytes.Contains(got, []byte(pseudoName)) {
		t.Error("the name survives somewhere in the raw file bytes")
	}

	// 6. No earlier revision to roll back to.
	if n := bytes.Count(got, []byte("%%EOF")); n != 1 {
		t.Errorf("the output has %d revisions; a rollback could recover the original", n)
	}
}

// TestPseudonymizeWithholdsOnResidue checks the safety net rather than
// trusting the substitution to be complete.
func TestPseudonymizeWithholdsOnResidue(t *testing.T) {
	src := hidingPlacesDoc(t)
	r := NewReaderOrFail(t, src)
	// A residue check that is asked about text nobody replaced must fire.
	where, err := findResidue(r, []Pseudonym{{From: pseudoName, To: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if where == "" {
		t.Fatal("the check found nothing in a document full of the name")
	}
	if !strings.Contains(where, pseudoName) {
		t.Errorf("the report should name what it found: %q", where)
	}
}

func TestPseudonymizeDifferentLengths(t *testing.T) {
	for _, token := range []string{"X", "[[PII_NAME_1]]",
		"[[a very much longer replacement token indeed]]"} {
		src := hidingPlacesDoc(t)
		var out bytes.Buffer
		if _, err := Pseudonymize(NewReaderOrFail(t, src), &out,
			[]Pseudonym{{From: pseudoName, To: token}}); err != nil {
			t.Fatalf("token %q: %v", token, err)
		}
		// A long token wraps, so compare with the line breaks squeezed out.
		text := extractAll(t, out.Bytes())
		if !strings.Contains(collapse(text), collapse(token)) {
			t.Errorf("token %q did not survive: %q", token, text)
		}
		if strings.Contains(text, pseudoName) {
			t.Errorf("with token %q the original survived", token)
		}
		// Text that was not marked must be intact.
		if !strings.Contains(collapse(text), "the matter was resolved in full.") {
			t.Errorf("with token %q the rest of the paragraph was lost: %q", token, text)
		}
	}
}

func TestPseudonymizeRedactedToken(t *testing.T) {
	src := hidingPlacesDoc(t)
	var out bytes.Buffer
	if _, err := Pseudonymize(NewReaderOrFail(t, src), &out,
		[]Pseudonym{{From: pseudoName, To: "[REDACTED]"}}); err != nil {
		t.Fatal(err)
	}
	text := extractAll(t, out.Bytes())
	if !strings.Contains(text, "[REDACTED]") {
		t.Errorf("the marker is missing: %q", text)
	}
	if bytes.Contains(out.Bytes(), []byte(pseudoName)) {
		t.Error("the original survives in the bytes")
	}
}

func TestPseudonymizeLongestFirst(t *testing.T) {
	doc := New()
	p := doc.AddPage()
	p.SetFont(Helvetica, 11)
	p.Text(60, 100, "Ada Lovelace and Ada Byron are the same person.")
	src := docBytes(t, doc)

	var out bytes.Buffer
	if _, err := Pseudonymize(NewReaderOrFail(t, src), &out, []Pseudonym{
		{From: "Ada", To: "[P1]"},
		{From: "Ada Lovelace", To: "[P2]"},
	}); err != nil {
		t.Fatal(err)
	}
	text := collapse(extractAll(t, out.Bytes()))
	// The longer rule must win where both could match.
	if !strings.Contains(text, "[P2] and [P1] Byron") {
		t.Errorf("mappings were applied in the wrong order: %q", text)
	}
}

func TestPseudonymizeValidation(t *testing.T) {
	src := hidingPlacesDoc(t)
	cases := []struct {
		name string
		subs []Pseudonym
	}{
		{"no substitutions", nil},
		{"empty original", []Pseudonym{{From: "", To: "x"}}},
		{"token contains the original", []Pseudonym{{From: "Ada", To: "Ada Lovelace"}}},
	}
	for _, c := range cases {
		if _, err := Pseudonymize(NewReaderOrFail(t, src), new(bytes.Buffer), c.subs); err == nil {
			t.Errorf("%s should be refused", c.name)
		}
	}
	if _, err := Pseudonymize(nil, new(bytes.Buffer), []Pseudonym{{From: "a", To: "b"}}); err == nil {
		t.Error("a nil document should be refused")
	}
}

func TestPseudonymizeFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := dir + "/in.pdf"
	dst := dir + "/out.pdf"
	if err := writeFileOrFail(t, src, hidingPlacesDoc(t)); err != nil {
		t.Fatal(err)
	}
	res, err := PseudonymizeFile(src, dst, []Pseudonym{{From: pseudoName, To: "[[P1]]"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pages != 1 {
		t.Errorf("touched %d pages, want 1", res.Pages)
	}
	got := readFileOrFail(t, dst)
	if bytes.Contains(got, []byte(pseudoName)) {
		t.Error("the name survives in the written file")
	}
	if !strings.Contains(extractAll(t, got), "[[P1]]") {
		t.Error("the token is missing from the written file")
	}
}

func TestPseudonymizeResultTotal(t *testing.T) {
	r := PseudonymizeResult{Replaced: map[string]int{"a": 2, "b": 3}}
	if r.Total() != 5 {
		t.Errorf("Total() = %d, want 5", r.Total())
	}
	if (PseudonymizeResult{}).Total() != 0 {
		t.Error("an empty result should total zero")
	}
}

func NewReaderOrFail(t *testing.T, b []byte) *Reader {
	t.Helper()
	r, err := NewReader(b)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func writeFileOrFail(t *testing.T, path string, b []byte) error {
	t.Helper()
	return os.WriteFile(path, b, 0o644)
}

func readFileOrFail(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

//  4. A document that writes non-breaking spaces and soft hyphens must
//     still match a mapping typed with an ordinary space and hyphen. Swiss
//     and German legal documents do this throughout.
func TestPseudonymizeCharacterVariants(t *testing.T) {
	cases := []struct {
		name    string
		written string // as the document draws it
		from    string // as the caller types it
	}{
		{"non-breaking space", "Ada Lovelace", "Ada Lovelace"},
		{"soft hyphen", "Basel­Stadt", "Basel-Stadt"},
		{"both", "Basel­Stadt Kanton", "Basel-Stadt Kanton"},
		{"plain, unchanged", "Ada Lovelace", "Ada Lovelace"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := New()
			p := doc.AddPage()
			p.SetFont(Helvetica, 11)
			p.Text(60, 100, "Concerning "+c.written+" of this parish.")
			src := docBytes(t, doc)
			if !strings.Contains(extractAll(t, src), c.written) {
				t.Fatalf("the fixture does not read back as written")
			}

			var out bytes.Buffer
			res, err := Pseudonymize(NewReaderOrFail(t, src), &out,
				[]Pseudonym{{From: c.from, To: "[[P1]]"}})
			if err != nil {
				t.Fatalf("the variant was not matched: %v", err)
			}
			if res.Total() == 0 {
				t.Fatal("nothing was replaced")
			}
			got := collapse(extractAll(t, out.Bytes()))
			if !strings.Contains(got, "[[P1]]") {
				t.Errorf("the token is missing: %q", got)
			}
			if strings.Contains(got, c.written) {
				t.Errorf("the original spelling survived: %q", got)
			}
			// And the check covers the variant, not just what was typed.
			if bytes.Contains(out.Bytes(), []byte(c.written)) {
				t.Error("the original spelling survives in the bytes")
			}
		})
	}
}

func TestExpandVariants(t *testing.T) {
	got := expandVariants(Pseudonym{From: "Basel-Stadt Kanton", To: "X"})
	forms := map[string]bool{}
	for _, v := range got {
		if v.To != "X" {
			t.Errorf("a variant lost its token: %+v", v)
		}
		if forms[v.From] {
			t.Errorf("duplicate variant %q", v.From)
		}
		forms[v.From] = true
	}
	for _, want := range []string{
		"Basel-Stadt Kanton", // as typed
		"Basel-Stadt Kanton", // nbsp
		"Basel­Stadt Kanton", // soft hyphen
		"Basel­Stadt Kanton", // both
	} {
		if !forms[want] {
			t.Errorf("missing variant %q; got %v", want, forms)
		}
	}
	// The caller's own spelling comes first, so ordering is stable.
	if got[0].From != "Basel-Stadt Kanton" {
		t.Errorf("first variant = %q", got[0].From)
	}
	// Nothing to vary expands to itself alone.
	if only := expandVariants(Pseudonym{From: "Ada", To: "X"}); len(only) != 1 {
		t.Errorf("expandVariants(%q) = %+v", "Ada", only)
	}
}

func TestExpandAllVariantsDedupes(t *testing.T) {
	got := expandAllVariants([]Pseudonym{
		{From: "Ada Lovelace", To: "[[P1]]"},
		{From: "Ada Lovelace", To: "[[P2]]"}, // a repeat must not win
	})
	seen := map[string]string{}
	for _, v := range got {
		if prev, dup := seen[v.From]; dup {
			t.Errorf("%q mapped twice: %q and %q", v.From, prev, v.To)
		}
		seen[v.From] = v.To
	}
	if seen["Ada Lovelace"] != "[[P1]]" {
		t.Errorf("the first mapping should win, got %q", seen["Ada Lovelace"])
	}
}

// TestPseudonymizeAnnotationAppearance covers the one combination that
// must never happen: text missed by the matcher and by the check. An
// annotation holds its words twice — as a string, and as drawing
// operators in an appearance stream — and scrubbing the string alone
// leaves the drawing showing the name.
func TestPseudonymizeAnnotationAppearance(t *testing.T) {
	src := annotWithAppearance(t, "Spoke to "+pseudoName+" today")

	// The fixture really does draw the name in an appearance.
	r0 := NewReaderOrFail(t, src)
	if got := r0.annotationText(0); !strings.Contains(got, pseudoName) {
		t.Fatalf("the fixture's note draws no text: %q", got)
	}

	var out bytes.Buffer
	if _, err := Pseudonymize(NewReaderOrFail(t, src), &out,
		[]Pseudonym{{From: pseudoName, To: "[[P1]]"}}); err != nil {
		t.Fatalf("the appearance should be handled, not refused: %v", err)
	}
	r := NewReaderOrFail(t, out.Bytes())
	if got := r.annotationText(0); strings.Contains(got, pseudoName) {
		t.Errorf("the name is still drawn by an annotation: %q", got)
	}
	if bytes.Contains(out.Bytes(), []byte(pseudoName)) {
		t.Error("the name survives in the bytes")
	}
}

// A stale appearance must be dropped when the strings behind it change,
// so a viewer draws the annotation from what it now says.
func TestPseudonymizeDropsStaleAppearance(t *testing.T) {
	doc := New()
	page := doc.AddPage()
	page.SetFont(Helvetica, 11)
	page.Text(60, 200, "See the note.")
	page.AddNote(60, 100, "About "+pseudoName, NoteOptions{})
	src := docBytes(t, doc)

	var out bytes.Buffer
	if _, err := Pseudonymize(NewReaderOrFail(t, src), &out,
		[]Pseudonym{{From: pseudoName, To: "[[P1]]"}}); err != nil {
		t.Fatal(err)
	}
	r := NewReaderOrFail(t, out.Bytes())
	annots, err := r.Annotations(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(annots) == 0 {
		t.Fatal("the annotation was removed entirely")
	}
	if !strings.Contains(annots[0].Contents, "[[P1]]") {
		t.Errorf("the note text was not substituted: %q", annots[0].Contents)
	}
	// And no appearance is left drawing the old wording.
	if got := r.annotationText(0); strings.Contains(got, pseudoName) {
		t.Errorf("a stale appearance survived: %q", got)
	}
}

func TestAnnotationTextReadsAppearances(t *testing.T) {
	doc := New()
	page := doc.AddPage()
	page.SetFont(Helvetica, 11)
	page.Text(60, 200, "Body text only.")
	page.AddNote(60, 100, "a note about something", NoteOptions{})
	src := docBytes(t, doc)
	r := NewReaderOrFail(t, src)

	// Whatever the note draws, it is not the page's own text.
	body, _ := r.PageText(0)
	if strings.Contains(body, "a note about") {
		t.Skip("this build draws notes into the page content")
	}
	if r.annotationText(-1) != "" || r.annotationText(99) != "" {
		t.Error("an out-of-range page should read as empty")
	}
}

// annotWithAppearance builds a FreeText annotation that both holds its
// words in /Contents and draws them in an appearance stream, which is how
// a real one arrives.
func annotWithAppearance(t *testing.T, note string) []byte {
	t.Helper()
	doc := New()
	page := doc.AddPage()
	page.SetFont(Helvetica, 11)
	page.Text(60, 200, "See the note.")
	src := docBytes(t, doc)

	r := NewReaderOrFail(t, src)
	u := Update(r)
	fontNum := u.add(Dict{
		"Type": Name("Font"), "Subtype": Name("Type1"),
		"BaseFont": Name("Helvetica"), "Encoding": Name("WinAnsiEncoding"),
	})
	ap := u.add(&rawStream{
		dict: Dict{
			"Type": Name("XObject"), "Subtype": Name("Form"),
			"BBox":      Array{float64(0), float64(0), float64(240), float64(30)},
			"Resources": Dict{"Font": Dict{"H": Ref{Num: fontNum}}},
		},
		data: []byte("BT /H 11 Tf 2 10 Td (" + note + ") Tj ET\n"),
	})
	annot := u.add(Dict{
		"Type": Name("Annot"), "Subtype": Name("FreeText"),
		"Rect":     Array{float64(60), float64(700), float64(300), float64(730)},
		"Contents": String(textStringBytes(note)),
		"AP":       Dict{"N": Ref{Num: ap}},
	})
	pi := r.pages[0]
	pd := cloneDict(pi.dict)
	annots, _ := r.resolve(pd["Annots"]).(Array)
	pd["Annots"] = append(append(Array{}, annots...), Ref{Num: annot})
	num, _ := r.pageObjectNumber(0)
	u.set(num, pd)

	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestSubstitutionWritesWhatItSays is the failure that only a check on
// the replacement can catch.
//
// A subset font puts its glyphs at whatever codes it likes and its
// ToUnicode says which. Writing a token by looking the characters up in
// a standard encoding drew "[[TOKEN_1]]" as "99PFKENJ1))": the original
// was genuinely gone, so every check that looked for the original
// passed, and what was left was a document quietly saying something
// else.
func TestSubstitutionWritesWhatItSays(t *testing.T) {
	// A font whose /Differences shuffles the codes, so a standard
	// encoding disagrees with the font's own map about what each means.
	src := shuffledEncodingDoc(t, "All rights reserved")

	r := NewReaderOrFail(t, src)
	before, err := r.PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(before, "All rights reserved") {
		t.Fatalf("the fixture does not read as expected: %q", before)
	}

	var buf bytes.Buffer
	res, err := Pseudonymize(r, &buf, []Pseudonym{{From: "rights", To: "[[TOKEN_1]]"}})
	if err != nil {
		t.Fatalf("the substitution was refused: %v", err)
	}
	if res.Total() != 1 {
		t.Fatalf("replaced %d, want 1", res.Total())
	}
	got, err := NewReaderOrFail(t, buf.Bytes()).PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "[[TOKEN_1]]") {
		t.Errorf("the token was not written as itself: %q", got)
	}
	if strings.Contains(got, "rights") {
		t.Errorf("the original survived: %q", got)
	}
}

// shuffledEncodingDoc builds a page set in a font that draws its glyphs
// at codes of its own choosing and says so only in /ToUnicode.
//
// The letters are shifted up by one, so the font's own map and the
// standard encoding disagree about what every code means. /ToUnicode
// covers the letters, digits and space and says nothing about the
// brackets and underscore a token is made of — which is the shape a
// subset font has, and the reason a token could come out as nonsense:
// with no map to consult, the standard encoding was believed, and the
// code it gave for "[" was a code this font draws a letter at.
func shuffledEncodingDoc(t *testing.T, text string) []byte {
	t.Helper()
	doc := New()
	doc.Compress = false
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(72, 100, "anchor")
	src := docBytes(t, doc)

	shift := func(b byte) byte { return b + 1 }
	mapped := func(b byte) bool {
		return b == ' ' || b >= '0' && b <= '9' ||
			b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z'
	}

	var cmap strings.Builder
	cmap.WriteString("/CIDInit /ProcSet findresource begin 12 dict begin begincmap\n" +
		"1 begincodespacerange <00> <FF> endcodespacerange\n")
	var entries int
	var body strings.Builder
	for c := byte(' '); c < 127; c++ {
		if !mapped(c) {
			continue
		}
		fmt.Fprintf(&body, "<%02X> <%04X>\n", shift(c), c)
		entries++
	}
	fmt.Fprintf(&cmap, "%d beginbfchar\n%s endbfchar\n", entries, body.String())
	cmap.WriteString("endcmap CMapName currentdict /CMap defineresource pop end end\n")

	r := NewReaderOrFail(t, src)
	u := Update(r)
	tu := u.AddObject(NewStream(Dict{}, []byte(cmap.String())))
	font := u.AddObject(Dict{
		"Type": Name("Font"), "Subtype": Name("Type1"),
		"BaseFont":  Name("Helvetica"),
		"Encoding":  Name("WinAnsiEncoding"),
		"ToUnicode": tu,
	})
	res, _ := r.InheritedPageValue(0, "Resources").(Dict)
	merged := res.Clone()
	fonts, _ := r.Resolve(merged["Font"]).(Dict)
	f2 := fonts.Clone()
	if f2 == nil {
		f2 = Dict{}
	}
	f2["Fsh"] = font
	merged["Font"] = f2
	if err := u.SetPageEntry(0, "Resources", merged); err != nil {
		t.Fatal(err)
	}

	var enc strings.Builder
	for i := 0; i < len(text); i++ {
		enc.WriteByte(shift(text[i]))
	}
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	page.op("BT /Fsh 12 Tf 72 700 Td (%s) Tj ET", escapeString([]byte(enc.String())))
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
