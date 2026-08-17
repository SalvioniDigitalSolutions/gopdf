package gopdf

import (
	"bytes"
	"strings"
	"testing"
)

// TestPDFAWritesWhatTheProfileNeeds builds a conforming document and
// checks it against the profile.
func TestPDFAWritesWhatTheProfileNeeds(t *testing.T) {
	font, err := LoadFont(testFontPath(t))
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	doc.Compress = false
	doc.SetInfo(Info{Title: "An archival document", Author: "Ada Lovelace"})
	doc.SetPDFA(PDFA2b)
	p := doc.AddPage()
	p.SetFont(font, 14)
	p.Text(72, 100, "Kept for fifty years.")
	src := docBytes(t, doc)

	r := NewReaderOrFail(t, src)
	if issues := r.CheckPDFA(PDFA2b); len(issues) != 0 {
		for _, i := range issues {
			t.Errorf("the document it wrote does not meet the profile: %s", i)
		}
	}
	// The identification has to be in the packet, since that is where a
	// reader looks to learn what the file claims.
	x := r.XMP()
	if !strings.Contains(string(x.Raw), "pdfaid:part") {
		t.Error("the metadata does not identify the conformance level")
	}
	if !strings.Contains(string(x.Raw), ">2<") {
		t.Errorf("the part is not 2: %s", firstBytes(x.Raw))
	}
	// And the output intent has to be there.
	if _, ok := r.Resolve(r.Catalog()["OutputIntents"]).(Array); !ok {
		t.Error("no output intent was written")
	}
	if txt, err := r.PageText(0); err != nil || !strings.Contains(txt, "fifty years") {
		t.Errorf("page text: %q %v", txt, err)
	}
	verifyXref(t, src)
}

// TestPDFARefusesWhatItCannotClaim: a document that asked for the
// profile and cannot meet it must not be written at all.
func TestPDFARefusesWhatItCannotClaim(t *testing.T) {
	// A standard font is not embedded, which is the usual reason a
	// document fails.
	doc := New()
	doc.SetPDFA(PDFA2b)
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(72, 100, "not embedded")
	var buf bytes.Buffer
	_, err := doc.WriteTo(&buf)
	if err == nil {
		t.Fatal("a document with an unembedded font was written as PDF/A")
	}
	if !strings.Contains(err.Error(), "embed") {
		t.Errorf("the error does not explain itself: %v", err)
	}
	if buf.Len() != 0 {
		t.Error("a document that failed the check was written anyway")
	}

	// Encryption is the other thing this package can do that the profile
	// cannot have.
	font, err := LoadFont(testFontPath(t))
	if err != nil {
		t.Fatal(err)
	}
	doc2 := New()
	doc2.SetPDFA(PDFA2b)
	doc2.Encrypt("", "owner", AllowPrint, AES256)
	p2 := doc2.AddPage()
	p2.SetFont(font, 12)
	p2.Text(72, 100, "encrypted")
	var buf2 bytes.Buffer
	if _, err := doc2.WriteTo(&buf2); err == nil {
		t.Error("an encrypted document was written as PDF/A")
	} else if !strings.Contains(err.Error(), "encrypt") {
		t.Errorf("the error does not explain itself: %v", err)
	}
}

// TestCheckPDFAFindsWhatIsWrong points the checker at documents that
// break the profile in each of the ways it knows about.
func TestCheckPDFAFindsWhatIsWrong(t *testing.T) {
	// A plain document: no packet, no intent, a font it does not carry.
	doc := New()
	doc.Compress = false
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(72, 100, "plain")
	r := NewReaderOrFail(t, docBytes(t, doc))

	issues := r.CheckPDFA(PDFA2b)
	if len(issues) == 0 {
		t.Fatal("a plain document passed the profile")
	}
	var rules []string
	for _, i := range issues {
		rules = append(rules, i.Rule)
	}
	joined := strings.Join(rules, "; ")
	for _, want := range []string{"XMP", "output intent", "embedded"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the check did not mention %q: %s", want, joined)
		}
	}
	// Each issue says where it is and reads as a sentence.
	for _, i := range issues {
		if i.String() == "" {
			t.Error("an issue has no description")
		}
	}
}

// TestCheckPDFAFindsAScript: the profile exists to keep a document from
// reaching outside itself.
func TestCheckPDFAFindsAScript(t *testing.T) {
	doc := New()
	doc.Compress = false
	doc.AddPage()
	r := NewReaderOrFail(t, docBytes(t, doc))
	u := Update(r)
	if err := u.SetCatalogEntry("OpenAction", Dict{
		"S": Name("JavaScript"), "JS": String("app.alert('hello')"),
	}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	issues := NewReaderOrFail(t, buf.Bytes()).CheckPDFA(PDFA2b)
	found := false
	for _, i := range issues {
		if strings.Contains(i.Rule, "act on being opened") {
			found = true
		}
	}
	if !found {
		t.Errorf("an open action was not reported: %+v", issues)
	}
}

// TestCheckPDFAUnicodeLevel: the U level asks that every glyph maps to a
// character, so the text can be searched.
func TestCheckPDFAUnicodeLevel(t *testing.T) {
	font, err := LoadFont(testFontPath(t))
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	doc.Compress = false
	doc.SetPDFA(PDFA2u)
	p := doc.AddPage()
	p.SetFont(font, 12)
	p.Text(72, 100, "searchable")
	r := NewReaderOrFail(t, docBytes(t, doc))

	// An embedded font this package writes carries a ToUnicode map, so
	// the stricter level is met as well.
	if issues := r.CheckPDFA(PDFA2u); len(issues) != 0 {
		for _, i := range issues {
			t.Errorf("the U level is not met: %s", i)
		}
	}
	if !strings.Contains(string(r.XMP().Raw), ">U<") {
		t.Error("the conformance letter is not U")
	}

	// A font with no map and no standard encoding fails that level and
	// not the other.
	u := Update(r)
	bad := u.AddObject(Dict{
		"Type": Name("Font"), "Subtype": Name("Type1"),
		"BaseFont": Name("ABCDEF+Custom"),
		"Encoding": Dict{"Type": Name("Encoding"),
			"Differences": Array{int64(65), Name("A")}},
		"FontDescriptor": u.AddObject(Dict{
			"Type": Name("FontDescriptor"), "FontName": Name("ABCDEF+Custom"),
			"FontFile3": u.AddObject(NewStream(Dict{}, []byte("x"))),
		}),
	})
	res, _ := r.InheritedPageValue(0, "Resources").(Dict)
	merged := res.Clone()
	fonts, _ := r.Resolve(merged["Font"]).(Dict)
	f2 := fonts.Clone()
	if f2 == nil {
		f2 = Dict{}
	}
	f2["Fbad"] = bad
	merged["Font"] = f2
	if err := u.SetPageEntry(0, "Resources", merged); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	out := NewReaderOrFail(t, buf.Bytes())
	if issues := out.CheckPDFA(PDFA2u); len(issues) == 0 {
		t.Error("a font with no character map passed the U level")
	}
	// The B level does not ask for the map, so that font is fine there.
	for _, i := range out.CheckPDFA(PDFA2b) {
		if strings.Contains(i.Rule, "characters") {
			t.Errorf("the B level asked for a character map: %s", i)
		}
	}
}

// TestPDFAWithATypeThreeFont: a Type 3 font's glyphs are content streams
// in the file, so it is embedded by construction and must not be
// reported as missing.
func TestPDFAEmbeddingOfAType3Font(t *testing.T) {
	doc := New()
	doc.Compress = false
	doc.AddPage()
	r := NewReaderOrFail(t, docBytes(t, doc))
	u := Update(r)
	proc := u.AddObject(NewStream(Dict{}, []byte("0 0 750 750 d0 0 0 750 750 re f")))
	font := u.AddObject(Dict{
		"Type": Name("Font"), "Subtype": Name("Type3"),
		"FontBBox":   Array{0.0, 0.0, 750.0, 750.0},
		"FontMatrix": Array{0.001, 0.0, 0.0, 0.001, 0.0, 0.0},
		"CharProcs":  Dict{"square": proc},
		"Encoding": Dict{"Type": Name("Encoding"),
			"Differences": Array{int64(97), Name("square")}},
		"FirstChar": int64(97), "LastChar": int64(97),
		"Widths": Array{int64(750)},
	})
	res, _ := r.InheritedPageValue(0, "Resources").(Dict)
	merged := res.Clone()
	if merged == nil {
		merged = Dict{}
	}
	merged["Font"] = Dict{"F3": font}
	if err := u.SetPageEntry(0, "Resources", merged); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	for _, i := range NewReaderOrFail(t, buf.Bytes()).CheckPDFA(PDFA2b) {
		if strings.Contains(i.Rule, "embedded") {
			t.Errorf("a Type 3 font was reported as not embedded: %s", i)
		}
	}
}
