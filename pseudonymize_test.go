package gopdf

import (
	"bytes"
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
