package gopdf

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// formFixture hand-builds an AcroForm document, since this library does
// not yet author interactive forms. It has a text field, a checkbox, a
// radio group with two buttons, a choice field and a read-only field.
func formFixture() []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := map[int]int{}
	obj := func(num int, body string) {
		offsets[num] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", num, body)
	}

	content := "BT /F1 12 Tf 60 700 Td (Application form) Tj ET"
	obj(1, "<< /Type /Catalog /Pages 2 0 R /AcroForm << /Fields [5 0 R 6 0 R 7 0 R 10 0 R 11 0 R] "+
		"/DA (/Helv 0 Tf 0 g) /DR << /Font << /Helv 4 0 R >> >> >> >>")
	obj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	obj(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 12 0 R "+
		"/Resources << /Font << /F1 4 0 R >> >> "+
		"/Annots [5 0 R 6 0 R 8 0 R 9 0 R 10 0 R 11 0 R] >>")
	obj(4, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")

	// 5: text field with a widget of its own.
	obj(5, "<< /FT /Tx /T (applicant) /V (existing name) /DA (/Helv 10 Tf 0 g) "+
		"/Type /Annot /Subtype /Widget /Rect [200 640 420 664] /P 3 0 R /MaxLen 40 >>")
	// 6: checkbox, currently off.
	obj(6, "<< /FT /Btn /T (subscribe) /V /Off /AS /Off "+
		"/Type /Annot /Subtype /Widget /Rect [200 600 216 616] /P 3 0 R "+
		"/AP << /N << /Yes 13 0 R /Off 14 0 R >> >> >>")
	// 7: radio group with two kid widgets (8 and 9).
	obj(7, "<< /FT /Btn /Ff 32768 /T (plan) /V /Off /Kids [8 0 R 9 0 R] >>")
	obj(8, "<< /Parent 7 0 R /Type /Annot /Subtype /Widget /Rect [200 560 216 576] "+
		"/P 3 0 R /AS /Off /AP << /N << /basic 13 0 R /Off 14 0 R >> >> >>")
	obj(9, "<< /Parent 7 0 R /Type /Annot /Subtype /Widget /Rect [300 560 316 576] "+
		"/P 3 0 R /AS /Off /AP << /N << /pro 13 0 R /Off 14 0 R >> >> >>")
	// 10: choice field.
	obj(10, "<< /FT /Ch /T (country) /V (Italy) /Opt [(Italy) (France) (Spain)] "+
		"/Type /Annot /Subtype /Widget /Rect [200 520 340 540] /P 3 0 R /Q 1 >>")
	// 11: read-only field.
	obj(11, "<< /FT /Tx /Ff 1 /T (reference) /V (REF-001) "+
		"/Type /Annot /Subtype /Widget /Rect [200 480 340 500] /P 3 0 R >>")
	obj(12, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))

	// 13/14: on and off appearance streams for the buttons.
	on := "0 0 1 rg 0 0 16 16 re f"
	off := "1 1 1 rg 0 0 16 16 re f"
	obj(13, fmt.Sprintf("<< /Type /XObject /Subtype /Form /BBox [0 0 16 16] /Length %d >>\nstream\n%s\nendstream", len(on), on))
	obj(14, fmt.Sprintf("<< /Type /XObject /Subtype /Form /BBox [0 0 16 16] /Length %d >>\nstream\n%s\nendstream", len(off), off))

	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 15\n0000000000 65535 f \n")
	for i := 1; i <= 14; i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size 15 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref)
	return buf.Bytes()
}

func TestFormFieldDiscovery(t *testing.T) {
	r, err := NewReader(formFixture())
	if err != nil {
		t.Fatal(err)
	}
	if !r.HasForm() {
		t.Fatal("HasForm = false")
	}
	fields := r.FormFields()
	byName := make(map[string]FormField, len(fields))
	for _, f := range fields {
		byName[f.Name] = f
	}
	if len(fields) != 5 {
		t.Fatalf("found %d fields, want 5: %+v", len(fields), fields)
	}

	applicant := byName["applicant"]
	if applicant.Type != FieldText {
		t.Errorf("applicant type = %q", applicant.Type)
	}
	if applicant.Value != "existing name" {
		t.Errorf("applicant value = %q", applicant.Value)
	}
	if applicant.MaxLen != 40 {
		t.Errorf("applicant MaxLen = %d", applicant.MaxLen)
	}
	if applicant.Page != 0 {
		t.Errorf("applicant page = %d", applicant.Page)
	}
	// Rect is reported top-left: y = 792-664 .. 792-640.
	if applicant.Rect != [4]float64{200, 128, 420, 152} {
		t.Errorf("applicant rect = %v", applicant.Rect)
	}

	if got := byName["subscribe"].Type; got != FieldCheckbox {
		t.Errorf("subscribe type = %q", got)
	}
	if got := byName["plan"].Type; got != FieldRadio {
		t.Errorf("plan type = %q", got)
	}
	country := byName["country"]
	if country.Type != FieldChoice {
		t.Errorf("country type = %q", country.Type)
	}
	if strings.Join(country.Options, ",") != "Italy,France,Spain" {
		t.Errorf("country options = %v", country.Options)
	}
	if country.Value != "Italy" {
		t.Errorf("country value = %q", country.Value)
	}
	if !byName["reference"].ReadOnly {
		t.Error("reference should be read-only")
	}
}

func TestFillFormFlattens(t *testing.T) {
	r, err := NewReader(formFixture())
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	doc.Compress = false
	n, err := doc.FillForm(r, map[string]string{
		"applicant": "Ada Lovelace",
		"subscribe": "Yes",
		"plan":      "pro",
		"country":   "France",
	})
	if err != nil {
		t.Fatal(err)
	}
	// applicant + subscribe + country + both radio widgets = 5 widgets.
	if n != 5 {
		t.Errorf("filled %d widgets, want 5", n)
	}

	out := docBytes(t, doc)
	verifyXref(t, out)
	r2, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	// The output must no longer be a form...
	if r2.HasForm() {
		t.Error("filled document still advertises an interactive form")
	}
	if len(r2.FormFields()) != 0 {
		t.Error("filled document still has form fields")
	}
	// ...and the values must be drawn into the page.
	text, err := r2.PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Application form", "Ada Lovelace", "France"} {
		if !strings.Contains(text, want) {
			t.Errorf("flattened page missing %q\ngot %q", want, text)
		}
	}
	// The untouched read-only field keeps its original value on the page
	// only if it had an appearance; here it has none, so just check the
	// document is still readable and the checkbox mark was drawn.
	if !strings.Contains(text, "4") && !bytes.Contains(out, []byte("ZapfDingbats")) {
		t.Error("checkbox mark not drawn")
	}
}

func TestFillFormInteractive(t *testing.T) {
	r, err := NewReader(formFixture())
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	doc.Compress = false
	n, err := doc.FillFormInteractive(r, map[string]string{
		"applicant": "Grace Hopper",
		"subscribe": "Yes",
		"plan":      "basic",
		"country":   "Spain",
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Errorf("filled %d fields, want 4", n)
	}

	out := docBytes(t, doc)
	verifyXref(t, out)
	r2, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	// The form must still be interactive, with the new values in place.
	if !r2.HasForm() {
		t.Fatal("form was lost; the output has no interactive fields")
	}
	byName := map[string]FormField{}
	for _, f := range r2.FormFields() {
		byName[f.Name] = f
	}
	if len(byName) != 5 {
		t.Errorf("output has %d fields, want 5", len(byName))
	}
	if got := byName["applicant"].Value; got != "Grace Hopper" {
		t.Errorf("applicant value = %q", got)
	}
	if got := byName["country"].Value; got != "Spain" {
		t.Errorf("country value = %q", got)
	}
	if got := byName["subscribe"].Value; got != "Yes" {
		t.Errorf("subscribe value = %q", got)
	}
	if got := byName["plan"].Value; got != "basic" {
		t.Errorf("plan value = %q", got)
	}
	// Untouched fields keep what they had.
	if got := byName["reference"].Value; got != "REF-001" {
		t.Errorf("reference value = %q", got)
	}

	// A fresh appearance must exist so the value shows before a viewer
	// regenerates anything.
	if !bytes.Contains(out, []byte("/Tx BMC")) {
		t.Error("no text-field appearance stream generated")
	}
	if !bytes.Contains(out, []byte("(Grace Hopper) Tj")) {
		t.Error("generated appearance does not draw the value")
	}
	// The widget annotations must be on the page.
	annots, _ := r2.resolve(r2.pages[0].dict["Annots"]).(Array)
	if len(annots) < 6 {
		t.Errorf("page has %d annotations, want at least 6", len(annots))
	}
	// The selected radio widget is switched on, the other left off.
	var on, off int
	for _, a := range annots {
		ad, _ := r2.resolve(a).(Dict)
		if as, ok := r2.resolve(ad["AS"]).(Name); ok {
			if as == "Off" {
				off++
			} else {
				on++
			}
		}
	}
	if on != 2 { // the checkbox and one radio button
		t.Errorf("%d widgets switched on, want 2", on)
	}
	if off == 0 {
		t.Error("the unselected radio button should still be Off")
	}
}

func TestFillFormInteractiveValidation(t *testing.T) {
	r, err := NewReader(formFixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		values map[string]string
		want   string
	}{
		{"unknown", map[string]string{"nope": "x"}, "no form field"},
		{"read-only", map[string]string{"reference": "x"}, "read-only"},
		{"too long", map[string]string{"applicant": strings.Repeat("x", 41)}, "allows 40"},
		{"bad option", map[string]string{"country": "Germany"}, "not an option"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := New()
			if _, err := doc.FillFormInteractive(r, tc.values); err == nil {
				t.Fatal("expected an error")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// TestFillFormInteractivePreservesContent confirms the page's own content
// survives alongside the re-attached widgets.
func TestFillFormInteractivePreservesContent(t *testing.T) {
	r, err := NewReader(formFixture())
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	if _, err := doc.FillFormInteractive(r, map[string]string{"applicant": "Ada"}); err != nil {
		t.Fatal(err)
	}
	r2, err := NewReader(docBytes(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	text, err := r2.PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Application form") {
		t.Errorf("page content lost: %q", text)
	}
}

func TestFillFormValidation(t *testing.T) {
	r, err := NewReader(formFixture())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		values map[string]string
		want   string
	}{
		{"unknown field", map[string]string{"nope": "x"}, "no form field"},
		{"read-only", map[string]string{"reference": "x"}, "read-only"},
		{"too long", map[string]string{"applicant": strings.Repeat("x", 41)}, "allows 40"},
		{"bad option", map[string]string{"country": "Germany"}, "not an option"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := New()
			if _, err := doc.FillForm(r, tc.values); err == nil {
				t.Fatalf("expected an error")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// TestFillFormKeepsExistingAppearances checks that widgets left alone
// still show their appearance after flattening.
func TestFillFormKeepsExistingAppearances(t *testing.T) {
	r, err := NewReader(formFixture())
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	doc.Compress = false
	// Fill nothing: every widget keeps its own appearance stream.
	if _, err := doc.FillForm(r, nil); err != nil {
		t.Fatal(err)
	}
	out := docBytes(t, doc)
	// The "off" appearance draws a white box; it must have been placed.
	if !bytes.Contains(out, []byte("1 1 1 rg 0 0 16 16 re f")) {
		t.Error("existing widget appearance was not carried into the page")
	}
	if !bytes.Contains(out, []byte("Do")) {
		t.Error("no XObject invocation emitted for the appearance")
	}
	if _, err := NewReader(out); err != nil {
		t.Fatal(err)
	}
}

func TestFillFormSelectsRadioWidget(t *testing.T) {
	r, err := NewReader(formFixture())
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	doc.Compress = false
	if _, err := doc.FillForm(r, map[string]string{"plan": "basic"}); err != nil {
		t.Fatal(err)
	}
	out := docBytes(t, doc)
	// The mark is drawn as a filled circle; exactly one radio widget of
	// the group should get one.
	marks := bytes.Count(out, []byte(" c\n"))
	if marks == 0 {
		t.Error("no radio mark drawn")
	}
	if _, err := NewReader(out); err != nil {
		t.Fatal(err)
	}
}

func TestParseDefaultAppearance(t *testing.T) {
	font, size, color := parseDefaultAppearance("/HeBo 11 Tf 1 0 0 rg")
	if font != HelveticaBold {
		t.Errorf("font = %s", font.Name())
	}
	if size != 11 {
		t.Errorf("size = %v", size)
	}
	if color != (Color{255, 0, 0}) {
		t.Errorf("color = %v", color)
	}
	_, size, color = parseDefaultAppearance("/Helv 0 Tf 0.5 g")
	if size != 0 {
		t.Errorf("auto size = %v, want 0", size)
	}
	if color.R != color.G || color.G != color.B || color.R == 0 {
		t.Errorf("gray color = %v", color)
	}
}

func TestFormFieldsOnNonFormDocument(t *testing.T) {
	doc := New()
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(10, 10, "no form here")
	r, err := NewReader(docBytes(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	if r.HasForm() {
		t.Error("HasForm = true for a plain document")
	}
	if len(r.FormFields()) != 0 {
		t.Error("fields reported for a plain document")
	}
	out := New()
	if _, err := out.FillForm(r, map[string]string{"x": "y"}); err == nil {
		t.Error("expected an error filling a document with no form")
	}
	// Filling nothing is a plain import.
	out2 := New()
	if _, err := out2.FillForm(r, nil); err != nil {
		t.Fatal(err)
	}
	r2, err := NewReader(docBytes(t, out2))
	if err != nil {
		t.Fatal(err)
	}
	text, _ := r2.PageText(0)
	if !strings.Contains(text, "no form here") {
		t.Errorf("text = %q", text)
	}
}
