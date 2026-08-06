package gopdf

import (
	"bytes"
	"strings"
	"testing"
)

// authoredForm builds an interactive form from scratch.
func authoredForm(t *testing.T, compress bool) []byte {
	t.Helper()
	doc := New()
	doc.Compress = compress
	doc.SetInfo(Info{Title: "Membership application"})
	page := doc.AddPage()
	page.SetFont(HelveticaBold, 16)
	page.Text(60, 70, "Membership application")
	page.SetFont(Helvetica, 10)

	label := func(y float64, s string) { page.Text(60, y+13, s) }

	label(100, "Full name")
	if err := page.AddTextField("name", 160, 100, 240, 20, FieldOptions{
		Value: "", MaxLen: 60, Tooltip: "Your full legal name",
	}); err != nil {
		t.Fatal(err)
	}
	label(130, "Country")
	if err := page.AddChoiceField("country", 160, 130, 160, 20,
		[]string{"Italy", "France", "Spain"}, FieldOptions{Value: "Italy"}); err != nil {
		t.Fatal(err)
	}
	label(160, "Newsletter")
	if err := page.AddCheckbox("newsletter", 160, 160, 16, FieldOptions{
		Selected: true, Color: RGB(0, 90, 0),
	}); err != nil {
		t.Fatal(err)
	}
	label(190, "Plan")
	for i, plan := range []string{"basic", "pro"} {
		if err := page.AddRadioButton("plan", plan, 160+float64(i)*80, 190, 14,
			FieldOptions{Selected: plan == "pro"}); err != nil {
			t.Fatal(err)
		}
		page.Text(180+float64(i)*80, 201, plan)
	}
	label(220, "Notes")
	if err := page.AddTextField("notes", 160, 220, 240, 60, FieldOptions{
		Multiline: true, Background: &Color{245, 245, 245},
	}); err != nil {
		t.Fatal(err)
	}
	if err := page.AddTextField("reference", 160, 300, 160, 20, FieldOptions{
		Value: "REF-2026", ReadOnly: true, Align: AlignRight,
	}); err != nil {
		t.Fatal(err)
	}
	return docBytes(t, doc)
}

func TestAuthorFormRoundTrip(t *testing.T) {
	data := authoredForm(t, true)
	verifyXref(t, data)

	r, err := NewReader(data)
	if err != nil {
		t.Fatal(err)
	}
	if !r.HasForm() {
		t.Fatal("authored document is not an interactive form")
	}
	fields := r.FormFields()
	byName := map[string]FormField{}
	for _, f := range fields {
		byName[f.Name] = f
	}
	if len(fields) != 6 {
		t.Fatalf("found %d fields, want 6: %+v", len(fields), fields)
	}

	name := byName["name"]
	if name.Type != FieldText {
		t.Errorf("name type = %q", name.Type)
	}
	if name.MaxLen != 60 {
		t.Errorf("name MaxLen = %d", name.MaxLen)
	}
	if name.Page != 0 {
		t.Errorf("name page = %d", name.Page)
	}
	if name.Rect != [4]float64{160, 100, 400, 120} {
		t.Errorf("name rect = %v, want [160 100 400 120]", name.Rect)
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

	if got := byName["newsletter"]; got.Type != FieldCheckbox || got.Value != "Yes" {
		t.Errorf("newsletter = %+v", got)
	}
	if got := byName["plan"]; got.Type != FieldRadio || got.Value != "pro" {
		t.Errorf("plan = %+v", got)
	}
	if !byName["reference"].ReadOnly {
		t.Error("reference should be read-only")
	}
	if byName["reference"].Value != "REF-2026" {
		t.Errorf("reference value = %q", byName["reference"].Value)
	}
}

func TestAuthorFormAppearances(t *testing.T) {
	data := authoredForm(t, false)
	// Every widget needs an appearance, or viewers show empty controls.
	if !bytes.Contains(data, []byte("/Tx BMC")) {
		t.Error("no text-field appearance emitted")
	}
	if !bytes.Contains(data, []byte("(REF-2026) Tj")) {
		t.Error("the read-only field's value is not drawn")
	}
	if !bytes.Contains(data, []byte("(Italy) Tj")) {
		t.Error("the choice field's value is not drawn")
	}
	// The checkbox mark uses ZapfDingbats.
	if !bytes.Contains(data, []byte("ZapfDingbats")) {
		t.Error("check mark font not embedded in the resources")
	}
	if !bytes.Contains(data, []byte("/AS /Yes")) {
		t.Error("the selected checkbox is not switched on")
	}
	if !bytes.Contains(data, []byte("/AS /pro")) {
		t.Error("the selected radio button is not switched on")
	}
	if !bytes.Contains(data, []byte("/NeedAppearances false")) {
		t.Error("appearances should be declared complete")
	}
	// The form's default resources must list the fonts its /DA names.
	if !bytes.Contains(data, []byte("/DR << /Font <<")) {
		t.Error("no default resource dictionary written")
	}
}

// TestAuthorFormReadBackAndFill closes the loop: author a form, then fill
// it with the same library.
func TestAuthorFormReadBackAndFill(t *testing.T) {
	r, err := NewReader(authoredForm(t, true))
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	doc.Compress = false
	n, err := doc.FillFormInteractive(r, map[string]string{
		"name":    "Alan Turing",
		"country": "France",
		"plan":    "basic",
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("filled %d fields, want 3", n)
	}
	r2, err := NewReader(docBytes(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]FormField{}
	for _, f := range r2.FormFields() {
		byName[f.Name] = f
	}
	if got := byName["name"].Value; got != "Alan Turing" {
		t.Errorf("name = %q", got)
	}
	if got := byName["country"].Value; got != "France" {
		t.Errorf("country = %q", got)
	}
	if got := byName["plan"].Value; got != "basic" {
		t.Errorf("plan = %q", got)
	}

	// And flattening the authored form produces readable page text.
	flat := New()
	if _, err := flat.FillForm(r, map[string]string{"name": "Alan Turing"}); err != nil {
		t.Fatal(err)
	}
	r3, err := NewReader(docBytes(t, flat))
	if err != nil {
		t.Fatal(err)
	}
	if r3.HasForm() {
		t.Error("flattened output still has a form")
	}
	text, _ := r3.PageText(0)
	for _, want := range []string{"Membership application", "Alan Turing"} {
		if !strings.Contains(text, want) {
			t.Errorf("flattened text missing %q: %q", want, text)
		}
	}
}

func TestAuthorFormValidation(t *testing.T) {
	newPage := func() *Page { return New().AddPage() }

	if err := newPage().AddTextField("", 0, 0, 100, 20, FieldOptions{}); err == nil {
		t.Error("accepted a field with no name")
	}
	if err := newPage().AddTextField("a", 0, 0, 0, 20, FieldOptions{}); err == nil {
		t.Error("accepted an empty rectangle")
	}
	if err := newPage().AddTextField("a", 0, 0, 100, 20, FieldOptions{
		Value: "toolong", MaxLen: 3,
	}); err == nil {
		t.Error("accepted a value longer than MaxLen")
	}
	if err := newPage().AddChoiceField("a", 0, 0, 100, 20, nil, FieldOptions{}); err == nil {
		t.Error("accepted a choice field with no options")
	}
	if err := newPage().AddChoiceField("a", 0, 0, 100, 20,
		[]string{"x"}, FieldOptions{Value: "y"}); err == nil {
		t.Error("accepted a value that is not an option")
	}
	if err := newPage().AddRadioButton("g", "Off", 0, 0, 12, FieldOptions{}); err == nil {
		t.Error(`accepted "Off" as a radio value`)
	}

	// Duplicate names are refused.
	p := newPage()
	if err := p.AddTextField("dup", 0, 0, 50, 20, FieldOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := p.AddTextField("dup", 0, 40, 50, 20, FieldOptions{}); err == nil {
		t.Error("accepted a duplicate field name")
	}
	// A radio value may not repeat within its group.
	p2 := newPage()
	if err := p2.AddRadioButton("g", "one", 0, 0, 12, FieldOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := p2.AddRadioButton("g", "one", 0, 20, 12, FieldOptions{}); err == nil {
		t.Error("accepted a duplicate radio value")
	}
	// A radio group cannot collide with a text field of the same name.
	p3 := newPage()
	if err := p3.AddTextField("x", 0, 0, 50, 20, FieldOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := p3.AddRadioButton("x", "a", 0, 40, 12, FieldOptions{}); err == nil {
		t.Error("accepted a radio group clashing with a text field")
	}
}

// TestAuthorFormMultiPage puts fields on two pages and checks each page
// lists only its own widgets.
func TestAuthorFormMultiPage(t *testing.T) {
	doc := New()
	p1 := doc.AddPage()
	p2 := doc.AddPage()
	if err := p1.AddTextField("first", 50, 50, 100, 20, FieldOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := p2.AddTextField("second", 50, 50, 100, 20, FieldOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := p2.AddCheckbox("agree", 50, 90, 14, FieldOptions{}); err != nil {
		t.Fatal(err)
	}
	r, err := NewReader(docBytes(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	pages := map[string]int{}
	for _, f := range r.FormFields() {
		pages[f.Name] = f.Page
	}
	if pages["first"] != 0 {
		t.Errorf("first is on page %d, want 0", pages["first"])
	}
	if pages["second"] != 1 || pages["agree"] != 1 {
		t.Errorf("page-2 fields landed on %d and %d", pages["second"], pages["agree"])
	}
	a1, _ := r.resolve(r.pages[0].dict["Annots"]).(Array)
	a2, _ := r.resolve(r.pages[1].dict["Annots"]).(Array)
	if len(a1) != 1 || len(a2) != 2 {
		t.Errorf("annotation counts = %d and %d, want 1 and 2", len(a1), len(a2))
	}
}

func TestAuthorFormEncrypted(t *testing.T) {
	doc := New()
	doc.Encrypt("pw", "", AllowAll, AES128)
	page := doc.AddPage()
	if err := page.AddTextField("secret", 50, 50, 200, 20,
		FieldOptions{Value: "classified"}); err != nil {
		t.Fatal(err)
	}
	data := docBytes(t, doc)
	if bytes.Contains(data, []byte("classified")) {
		t.Error("the field value was written unencrypted")
	}
	r, err := NewReaderPassword(data, "pw")
	if err != nil {
		t.Fatal(err)
	}
	fields := r.FormFields()
	if len(fields) != 1 || fields[0].Value != "classified" {
		t.Errorf("decrypted fields = %+v", fields)
	}
}
