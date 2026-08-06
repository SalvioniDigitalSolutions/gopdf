package gopdf

import (
	"bytes"
	"strings"
	"testing"
)

func updatedBytes(t *testing.T, u *Updater) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestUpdatePreservesOriginalBytes is the whole point of an incremental
// update: the original file must appear verbatim at the head of the
// output, so nothing the library does not model can be lost.
func TestUpdatePreservesOriginalBytes(t *testing.T) {
	src := invoiceFixture(t)
	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	u := Update(r)
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	n, err := page.ReplaceText("DRAFT", "FINAL")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("replaced %d runs, want 1", n)
	}
	out := updatedBytes(t, u)

	if !bytes.HasPrefix(out, src) {
		t.Fatal("the original bytes were not preserved at the head of the file")
	}
	if len(out) <= len(src) {
		t.Fatal("nothing was appended")
	}
	// The update must chain onto the original cross-reference section.
	if !bytes.Contains(out[len(src):], []byte("/Prev ")) {
		t.Error("appended trailer has no /Prev link to the original xref")
	}
	if bytes.Count(out, []byte("%%EOF")) < 2 {
		t.Error("expected the original and the appended EOF markers")
	}
}

func TestUpdateReplaceText(t *testing.T) {
	src := invoiceFixture(t)
	r, _ := NewReader(src)
	u := Update(r)
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{
		{"DRAFT", "APPROVED"},
		{"Acme Corporation", "Globex Ltd"},
		{"1950.00", "2075.50"},
	} {
		if _, err := page.ReplaceText(pair[0], pair[1]); err != nil {
			t.Fatalf("%q: %v", pair[0], err)
		}
	}
	out := updatedBytes(t, u)

	r2, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	if r2.NumPages() != 1 {
		t.Fatalf("page count changed to %d", r2.NumPages())
	}
	text, err := r2.PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Status: APPROVED", "Customer: Globex Ltd", "2075.50"} {
		if !strings.Contains(text, want) {
			t.Errorf("updated page missing %q\ngot: %s", want, text)
		}
	}
	for _, gone := range []string{"DRAFT", "Acme", "1950.00"} {
		if strings.Contains(text, gone) {
			t.Errorf("updated page still shows %q", gone)
		}
	}
}

// TestUpdateKeepsUnmodelledObjects proves the fidelity claim: an object
// the library has no concept of survives an update untouched.
func TestUpdateKeepsUnmodelledObjects(t *testing.T) {
	doc := New()
	doc.Compress = false
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(60, 80, "original text")
	base := docBytes(t, doc)

	// Splice in an object this library neither writes nor understands,
	// registered in the catalog, and rebuild the xref by re-reading.
	marker := []byte("<</Type/CustomThing/Note(do not lose me)>>")
	custom := append([]byte("\n99 0 obj\n"), append(marker, []byte("\nendobj\n")...)...)
	eof := bytes.LastIndex(base, []byte("xref\n"))
	if eof < 0 {
		t.Fatal("no xref in the fixture")
	}
	// Append as its own incremental section so the file stays valid.
	withCustom := append(append([]byte(nil), base...), custom...)
	offset := len(base) + 1
	xrefAt := len(withCustom)
	withCustom = append(withCustom, []byte(
		"xref\n99 1\n"+pad10(offset)+" 00000 n \ntrailer\n<< /Size 100 /Root 1 0 R /Prev "+
			itoa(startXrefOf(t, base))+" >>\nstartxref\n"+itoa(xrefAt)+"\n%%EOF\n")...)

	r, err := NewReader(withCustom)
	if err != nil {
		t.Fatal(err)
	}
	obj, err := r.object(99)
	if err != nil || obj == nil {
		t.Fatalf("fixture object not readable: %v", err)
	}

	u := Update(r)
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := page.ReplaceText("original", "modified"); err != nil {
		t.Fatal(err)
	}
	out := updatedBytes(t, u)

	if !bytes.Contains(out, marker) {
		t.Error("an object the library does not model was lost by the update")
	}
	r2, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	if obj, err := r2.object(99); err != nil || obj == nil {
		t.Errorf("custom object unreadable after the update: %v", err)
	}
	text, _ := r2.PageText(0)
	if !strings.Contains(text, "modified text") {
		t.Errorf("edit did not apply: %q", text)
	}
}

func pad10(v int) string {
	s := itoa(v)
	for len(s) < 10 {
		s = "0" + s
	}
	return s
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}

func startXrefOf(t *testing.T, data []byte) int {
	t.Helper()
	i := bytes.LastIndex(data, []byte("startxref"))
	if i < 0 {
		t.Fatal("no startxref")
	}
	p := &parser{data: data, pos: i + len("startxref")}
	v, err := p.expectInt()
	if err != nil {
		t.Fatal(err)
	}
	return int(v)
}

func TestUpdatePageRotationAndInfo(t *testing.T) {
	src := invoiceFixture(t)
	r, _ := NewReader(src)
	u := Update(r)
	if err := u.SetPageRotation(0, 90); err != nil {
		t.Fatal(err)
	}
	u.SetInfo(Info{Title: "Rotated — updated", Author: "gopdf"})
	out := updatedBytes(t, u)

	r2, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	size, _ := r2.PageSize(0)
	orig, _ := r.PageSize(0)
	if size.W != orig.H || size.H != orig.W {
		t.Errorf("rotated size = %v, original was %v", size, orig)
	}
	if got := r2.Info().Title; got != "Rotated — updated" {
		t.Errorf("title = %q", got)
	}
	if got := r2.Info().Author; got != "gopdf" {
		t.Errorf("author = %q", got)
	}
}

func TestUpdateNoChangesIsIdentical(t *testing.T) {
	src := invoiceFixture(t)
	r, _ := NewReader(src)
	out := updatedBytes(t, Update(r))
	if !bytes.Equal(out, src) {
		t.Error("an update with no changes should reproduce the original exactly")
	}
}

func TestUpdateForm(t *testing.T) {
	r, err := NewReader(formFixture())
	if err != nil {
		t.Fatal(err)
	}
	u := Update(r)
	if err := u.SetFormValues(map[string]string{
		"applicant": "Katherine Johnson",
		"country":   "Spain",
		"subscribe": "Yes",
	}); err != nil {
		t.Fatal(err)
	}
	out := updatedBytes(t, u)
	if !bytes.HasPrefix(out, formFixture()) {
		t.Error("original bytes not preserved")
	}

	r2, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	if !r2.HasForm() {
		t.Fatal("the form was lost")
	}
	byName := map[string]FormField{}
	for _, f := range r2.FormFields() {
		byName[f.Name] = f
	}
	if got := byName["applicant"].Value; got != "Katherine Johnson" {
		t.Errorf("applicant = %q", got)
	}
	if got := byName["country"].Value; got != "Spain" {
		t.Errorf("country = %q", got)
	}
	if got := byName["subscribe"].Value; got != "Yes" {
		t.Errorf("subscribe = %q", got)
	}
	// Untouched fields keep their original values.
	if got := byName["reference"].Value; got != "REF-001" {
		t.Errorf("reference = %q", got)
	}
	// A fresh appearance must have been generated for the text field.
	if !bytes.Contains(out, []byte("(Katherine Johnson) Tj")) {
		t.Error("no appearance stream drawn for the filled value")
	}
}

func TestUpdateFormValidation(t *testing.T) {
	r, _ := NewReader(formFixture())
	for _, tc := range []struct{ name, field, value, want string }{
		{"unknown", "nope", "x", "no form field"},
		{"read-only", "reference", "x", "read-only"},
		{"bad option", "country", "Germany", "not an option"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := Update(r)
			err := u.SetFormValues(map[string]string{tc.field: tc.value})
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// TestUpdateXrefStreamFile updates a document whose cross-reference is a
// stream; the appended section must match that style.
func TestUpdateXrefStreamFile(t *testing.T) {
	src := buildXrefStreamPDF()
	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	if !r.xrefIsStream {
		t.Fatal("fixture should use a cross-reference stream")
	}
	u := Update(r)
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := page.ReplaceText("modern", "updated"); err != nil {
		t.Fatal(err)
	}
	out := updatedBytes(t, u)
	if !bytes.HasPrefix(out, src) {
		t.Error("original bytes not preserved")
	}
	if !bytes.Contains(out[len(src):], []byte("/Type /XRef")) {
		t.Error("appended section is not a cross-reference stream")
	}
	r2, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	text, err := r2.PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "updated xref") {
		t.Errorf("text = %q", text)
	}
}

func TestUpdateReflow(t *testing.T) {
	src := paragraphFixture(t)
	r, _ := NewReader(src)
	u := Update(r)
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	n, err := page.ReplaceTextReflow("internal business purposes only",
		"any lawful purpose whatsoever")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("reflowed %d paragraphs, want 1", n)
	}
	out := updatedBytes(t, u)
	r2, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := r2.PageText(0)
	flat := strings.Join(strings.Fields(text), " ")
	if !strings.Contains(flat, "any lawful purpose whatsoever") {
		t.Errorf("reflowed text = %q", flat)
	}
	if !strings.Contains(flat, "Terms of Service") {
		t.Error("the heading was lost")
	}
}

func TestUpdateOutOfRange(t *testing.T) {
	r, _ := NewReader(invoiceFixture(t))
	u := Update(r)
	if _, err := u.Page(9); err == nil {
		t.Error("expected an error for an out-of-range page")
	}
	if err := u.SetPageRotation(9, 90); err == nil {
		t.Error("expected an error rotating an out-of-range page")
	}
}
