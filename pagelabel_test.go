package gopdf

import (
	"bytes"
	"strings"
	"testing"
)

func TestPageLabelFormatting(t *testing.T) {
	for _, c := range []struct {
		style PageLabelStyle
		n     int
		want  string
	}{
		{LabelDecimal, 1, "1"},
		{LabelDecimal, 42, "42"},
		{LabelRomanLower, 1, "i"},
		{LabelRomanLower, 4, "iv"},
		{LabelRomanLower, 9, "ix"},
		{LabelRomanLower, 14, "xiv"},
		{LabelRomanUpper, 1990, "MCMXC"},
		{LabelRomanUpper, 3999, "MMMCMXCIX"},
		// Past the numerals a page count is just a count.
		{LabelRomanUpper, 4000, "4000"},
		{LabelLettersUpper, 1, "A"},
		{LabelLettersUpper, 26, "Z"},
		// The specification repeats the letter rather than carrying:
		// after Z comes AA, then BB.
		{LabelLettersUpper, 27, "AA"},
		{LabelLettersUpper, 28, "BB"},
		{LabelLettersLower, 53, "aaa"},
		{LabelNone, 5, ""},
		{LabelDecimal, 0, ""},
	} {
		if got := formatLabel(c.style, c.n); got != c.want {
			t.Errorf("formatLabel(%q, %d) = %q, want %q", c.style, c.n, got, c.want)
		}
	}
}

// TestPageLabelsRoundTrip writes the numbering a book has — roman front
// matter, then decimal from one — and reads it back.
func TestPageLabelsRoundTrip(t *testing.T) {
	doc := New()
	doc.Compress = false
	for i := 0; i < 8; i++ {
		p := doc.AddPage()
		p.SetFont(Helvetica, 12)
		p.Text(72, 100, "page")
	}
	doc.SetPageLabels([]PageLabelRange{
		{From: 4, Style: LabelDecimal, Start: 1},
		{From: 0, Style: LabelRomanLower, Start: 1},
		{From: 6, Style: LabelDecimal, Prefix: "A-", Start: 1},
	})
	src := docBytes(t, doc)

	r := NewReaderOrFail(t, src)
	ranges := r.PageLabels()
	if len(ranges) != 3 {
		t.Fatalf("%d ranges, want 3: %+v", len(ranges), ranges)
	}
	if ranges[0].From != 0 || ranges[0].Style != LabelRomanLower {
		t.Errorf("the ranges are not in page order: %+v", ranges)
	}
	want := []string{"i", "ii", "iii", "iv", "1", "2", "A-1", "A-2"}
	for i, w := range want {
		if got := r.PageLabel(i); got != w {
			t.Errorf("page %d is labelled %q, want %q", i, got, w)
		}
	}
	// A page past the end has no label rather than a wrong one.
	if got := r.PageLabel(99); got != "" {
		t.Errorf("a page that does not exist is labelled %q", got)
	}
	verifyXref(t, src)
}

// TestPageLabelsWithNoLabels: a document that says nothing about its
// numbering is numbered from one, which is what a viewer shows.
func TestPageLabelsWithNoLabels(t *testing.T) {
	doc := New()
	doc.AddPage()
	doc.AddPage()
	r := NewReaderOrFail(t, docBytes(t, doc))
	if got := r.PageLabels(); got != nil {
		t.Errorf("a document with no labels reports %+v", got)
	}
	if got := r.PageLabel(1); got != "2" {
		t.Errorf("the second page is labelled %q, want 2", got)
	}
}

// TestPageLabelsBeforeTheFirstRange: pages ahead of the first range are
// numbered plainly rather than borrowing a scheme that does not cover
// them.
func TestPageLabelsBeforeTheFirstRange(t *testing.T) {
	doc := New()
	for i := 0; i < 4; i++ {
		doc.AddPage()
	}
	doc.SetPageLabels([]PageLabelRange{{From: 2, Style: LabelRomanUpper, Start: 7}})
	r := NewReaderOrFail(t, docBytes(t, doc))
	for i, want := range []string{"1", "2", "VII", "VIII"} {
		if got := r.PageLabel(i); got != want {
			t.Errorf("page %d is labelled %q, want %q", i, got, want)
		}
	}
}

// TestPageLabelsOnAnExistingDocument sets the numbering through an
// incremental update.
func TestPageLabelsOnAnExistingDocument(t *testing.T) {
	doc := New()
	doc.Compress = false
	for i := 0; i < 3; i++ {
		p := doc.AddPage()
		p.SetFont(Helvetica, 12)
		p.Text(72, 100, "body")
	}
	r := NewReaderOrFail(t, docBytes(t, doc))

	u := Update(r)
	if err := u.SetPageLabels([]PageLabelRange{
		{From: 0, Style: LabelRomanLower},
		{From: 1, Style: LabelDecimal, Start: 5},
	}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	out := NewReaderOrFail(t, buf.Bytes())
	for i, want := range []string{"i", "5", "6"} {
		if got := out.PageLabel(i); got != want {
			t.Errorf("page %d is labelled %q, want %q", i, got, want)
		}
	}
	// The document still works.
	if txt, err := out.PageText(0); err != nil || !strings.Contains(txt, "body") {
		t.Errorf("page text: %q %v", txt, err)
	}

	// And they can be taken off again.
	u2 := Update(out)
	if err := u2.SetPageLabels(nil); err != nil {
		t.Fatal(err)
	}
	var buf2 bytes.Buffer
	if _, err := u2.WriteTo(&buf2); err != nil {
		t.Fatal(err)
	}
	if got := NewReaderOrFail(t, buf2.Bytes()).PageLabels(); got != nil {
		t.Errorf("the labels survived removal: %+v", got)
	}
}

// TestNumberTreeWithKids: a long document splits its label tree into
// branches, and reading only the root finds none of them.
func TestNumberTreeWithKids(t *testing.T) {
	doc := New()
	doc.Compress = false
	for i := 0; i < 4; i++ {
		doc.AddPage()
	}
	r := NewReaderOrFail(t, docBytes(t, doc))
	u := Update(r)
	leaf := func(from int, style PageLabelStyle) Ref {
		return u.AddObject(Dict{
			"Nums":   Array{int64(from), Dict{"S": Name(style)}},
			"Limits": Array{int64(from), int64(from)},
		})
	}
	root := u.AddObject(Dict{"Kids": Array{
		leaf(0, LabelRomanLower),
		u.AddObject(Dict{"Kids": Array{leaf(2, LabelDecimal)}}),
	}})
	if err := u.SetCatalogEntry("PageLabels", root); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	out := NewReaderOrFail(t, buf.Bytes())
	if got := out.PageLabels(); len(got) != 2 {
		t.Fatalf("found %d ranges through the tree, want 2: %+v", len(got), got)
	}
	for i, want := range []string{"i", "ii", "1", "2"} {
		if got := out.PageLabel(i); got != want {
			t.Errorf("page %d is labelled %q, want %q", i, got, want)
		}
	}
}
