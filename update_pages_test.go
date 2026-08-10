package gopdf

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// numberedPages builds a document whose pages announce their own number,
// so reordering is easy to verify.
func numberedPages(t *testing.T, n int) []byte {
	t.Helper()
	doc := New()
	doc.Compress = false
	for i := 1; i <= n; i++ {
		p := doc.AddPage()
		p.SetFont(HelveticaBold, 24)
		p.Text(72, 100, fmt.Sprintf("PAGE %d", i))
	}
	return docBytes(t, doc)
}

// pageTexts returns the first line of each page, in document order.
func pageTexts(t *testing.T, data []byte) []string {
	t.Helper()
	r, err := NewReader(data)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, r.NumPages())
	for i := range out {
		text, err := r.PageText(i)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
	}
	return out
}

func TestUpdateRemovePage(t *testing.T) {
	src := numberedPages(t, 4)
	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	u := Update(r)
	if err := u.RemovePage(1); err != nil { // drop "PAGE 2"
		t.Fatal(err)
	}
	out := updatedBytes(t, u)
	if !bytes.HasPrefix(out, src) {
		t.Fatal("original bytes were not preserved")
	}
	verifyXref(t, out)

	got := pageTexts(t, out)
	want := []string{"PAGE 1", "PAGE 3", "PAGE 4"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("pages = %v, want %v", got, want)
	}
}

func TestUpdateMovePage(t *testing.T) {
	src := numberedPages(t, 4)
	r, _ := NewReader(src)
	u := Update(r)
	if err := u.MovePage(3, 0); err != nil { // last page to the front
		t.Fatal(err)
	}
	got := pageTexts(t, updatedBytes(t, u))
	want := []string{"PAGE 4", "PAGE 1", "PAGE 2", "PAGE 3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("pages = %v, want %v", got, want)
	}
}

func TestUpdateSetPageOrder(t *testing.T) {
	src := numberedPages(t, 5)
	r, _ := NewReader(src)
	u := Update(r)
	// Reverse, dropping the middle page.
	if err := u.SetPageOrder([]int{4, 3, 1, 0}); err != nil {
		t.Fatal(err)
	}
	got := pageTexts(t, updatedBytes(t, u))
	want := []string{"PAGE 5", "PAGE 4", "PAGE 2", "PAGE 1"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("pages = %v, want %v", got, want)
	}
}

// TestUpdatePageOrderKeepsInheritedAttributes flattens a tree whose pages
// inherit their box and resources from the root, which must survive.
func TestUpdatePageOrderKeepsInheritedAttributes(t *testing.T) {
	src := inheritedAttrsPDF()
	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	before, err := r.PageSize(0)
	if err != nil {
		t.Fatal(err)
	}
	beforeText, _ := r.PageText(1)

	u := Update(r)
	if err := u.SetPageOrder([]int{1, 0}); err != nil {
		t.Fatal(err)
	}
	r2, err := NewReader(updatedBytes(t, u))
	if err != nil {
		t.Fatal(err)
	}
	if r2.NumPages() != 2 {
		t.Fatalf("page count = %d, want 2", r2.NumPages())
	}
	after, err := r2.PageSize(0)
	if err != nil {
		t.Fatal(err)
	}
	// The reordered first page is the old second page, and it kept the
	// media box it used to inherit.
	if after != before {
		t.Errorf("page size changed from %v to %v after flattening", before, after)
	}
	afterText, _ := r2.PageText(0)
	if strings.TrimSpace(afterText) != strings.TrimSpace(beforeText) {
		t.Errorf("reordered page text = %q, want %q", afterText, beforeText)
	}
	// Resources were inherited too; the text needs its font to extract.
	if !strings.Contains(afterText, "second") {
		t.Errorf("inherited resources lost: %q", afterText)
	}
}

// inheritedAttrsPDF builds a document whose pages carry neither
// /MediaBox nor /Resources, inheriting both from the tree root.
func inheritedAttrsPDF() []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := map[int]int{}
	obj := func(num int, body string) {
		offsets[num] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", num, body)
	}
	c1 := "BT /F1 18 Tf 72 700 Td (first) Tj ET"
	c2 := "BT /F1 18 Tf 72 700 Td (second) Tj ET"

	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	// Both the box and the resources live on the root node only.
	obj(2, "<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 "+
		"/MediaBox [0 0 400 650] /Resources << /Font << /F1 5 0 R >> >> >>")
	obj(3, "<< /Type /Page /Parent 2 0 R /Contents 6 0 R >>")
	obj(4, "<< /Type /Page /Parent 2 0 R /Contents 7 0 R >>")
	obj(5, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	obj(6, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(c1), c1))
	obj(7, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(c2), c2))

	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 8\n0000000000 65535 f \n")
	for i := 1; i <= 7; i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size 8 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref)
	return buf.Bytes()
}

func TestUpdatePageOrderValidation(t *testing.T) {
	src := numberedPages(t, 3)
	r, _ := NewReader(src)

	cases := []struct {
		name string
		run  func(*Updater) error
		want string
	}{
		{"empty", func(u *Updater) error { return u.SetPageOrder(nil) }, "at least one page"},
		{"out of range", func(u *Updater) error { return u.SetPageOrder([]int{0, 9}) }, "out of range"},
		{"duplicate", func(u *Updater) error { return u.SetPageOrder([]int{0, 0}) }, "twice"},
		{"remove missing", func(u *Updater) error { return u.RemovePage(9) }, "not in the document"},
		{"move missing", func(u *Updater) error { return u.MovePage(9, 0) }, "not in the document"},
		{"move out of range", func(u *Updater) error { return u.MovePage(0, 9) }, "out of range"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run(Update(r))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}

	// Removing the last remaining page is refused.
	one := numberedPages(t, 1)
	r1, _ := NewReader(one)
	if err := Update(r1).RemovePage(0); err == nil {
		t.Error("expected an error removing the only page")
	}
}

// TestUpdatePageOrderWithEdits combines reordering with a text edit and
// drawing, which all have to survive together.
func TestUpdatePageOrderWithEdits(t *testing.T) {
	src := numberedPages(t, 3)
	r, _ := NewReader(src)
	u := Update(r)

	page, err := u.Page(2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := page.ReplaceText("PAGE 3", "PAGE C"); err != nil {
		t.Fatal(err)
	}
	page.SetFont(Helvetica, 10)
	page.Text(72, 200, "annotated last page")

	if err := u.MovePage(2, 0); err != nil {
		t.Fatal(err)
	}
	out := updatedBytes(t, u)
	verifyXref(t, out)

	got := pageTexts(t, out)
	if got[0] != "PAGE C" {
		t.Errorf("first page = %q, want the edited page moved to the front", got[0])
	}
	r2, _ := NewReader(out)
	full, _ := r2.PageText(0)
	if !strings.Contains(full, "annotated last page") {
		t.Errorf("drawing lost during reordering: %q", full)
	}
	if strings.Join(got[1:], ",") != "PAGE 1,PAGE 2" {
		t.Errorf("remaining pages = %v", got[1:])
	}
}
