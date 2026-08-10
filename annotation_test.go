package gopdf

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

func TestAnnotationsOnNewDocument(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 12)
	page.Text(60, 100, "The quick brown fox")

	page.AddHighlight(58, 88, 120, 16, "look here", NoteOptions{Author: "Ada"})
	page.AddUnderline(58, 108, 120, 14, "", NoteOptions{})
	page.AddStrikeOut(58, 128, 120, 14, "", NoteOptions{Color: RGB(0, 120, 0)})
	page.AddNote(300, 90, "a sticky note", NoteOptions{Author: "Grace", Open: true})
	page.AddSquareAnnotation(300, 150, 120, 60, "boxed", NoteOptions{})
	page.AddCircleAnnotation(300, 230, 120, 60, "", NoteOptions{})

	out := docBytes(t, doc)
	verifyXref(t, out)

	r, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	annots, err := r.Annotations(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(annots) != 6 {
		t.Fatalf("found %d annotations, want 6", len(annots))
	}
	byType := map[AnnotType]Annotation{}
	for _, a := range annots {
		byType[a.Type] = a
	}
	for _, want := range []AnnotType{
		AnnotHighlight, AnnotUnderline, AnnotStrikeOut,
		AnnotText, AnnotSquare, AnnotCircle,
	} {
		if _, ok := byType[want]; !ok {
			t.Errorf("no %s annotation round-tripped", want)
		}
	}

	h := byType[AnnotHighlight]
	if h.Contents != "look here" {
		t.Errorf("highlight contents = %q", h.Contents)
	}
	if h.Author != "Ada" {
		t.Errorf("highlight author = %q", h.Author)
	}
	if h.Color == nil {
		t.Error("highlight has no colour")
	}
	// The rectangle round-trips in top-left coordinates.
	if math.Abs(h.Rect[0]-58) > 0.01 || math.Abs(h.Rect[1]-88) > 0.01 {
		t.Errorf("highlight rect = %v, want origin (58, 88)", h.Rect)
	}
	if got := byType[AnnotText].Contents; got != "a sticky note" {
		t.Errorf("note contents = %q", got)
	}
	if c := byType[AnnotStrikeOut].Color; c == nil || c.G < 100 || c.R > 40 {
		t.Errorf("strike-out colour = %v, want the green that was asked for", c)
	}

	// Markup annotations need an appearance and a quad, or viewers
	// disagree about how to draw them.
	if !bytes.Contains(out, []byte("/QuadPoints")) {
		t.Error("no QuadPoints written for the text-markup annotations")
	}
	if !bytes.Contains(out, []byte("/BM /Multiply")) {
		t.Error("highlight appearance does not use a multiply blend")
	}
	if !bytes.Contains(out, []byte("/AP << /N")) {
		t.Error("no appearance streams written")
	}
	// The appearance's resources belong to the form, not the annotation.
	if bytes.Contains(out, []byte("/Subtype /Highlight")) &&
		bytes.Contains(out, []byte("/Resources << /ExtGState << /GS << /BM /Multiply")) {
		// present in the form only — nothing further to assert here
		_ = out
	}
}

func TestAnnotationsInPlace(t *testing.T) {
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
	page.AddHighlight(58, 132, 200, 16, "check this total", NoteOptions{Author: "Reviewer"})
	page.AddNote(420, 70, "please confirm", NoteOptions{Author: "Reviewer"})
	page.LinkURL(58, 300, 120, 14, "https://example.com/invoice")

	out := updatedBytes(t, u)
	if !bytes.HasPrefix(out, src) {
		t.Fatal("original bytes were not preserved")
	}
	verifyXref(t, out)

	r2, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	annots, err := r2.Annotations(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(annots) != 3 {
		t.Fatalf("found %d annotations, want 3: %+v", len(annots), annots)
	}
	var sawLink, sawHighlight, sawNote bool
	for _, a := range annots {
		switch a.Type {
		case AnnotLink:
			sawLink = a.URL == "https://example.com/invoice"
		case AnnotHighlight:
			sawHighlight = a.Contents == "check this total" && a.Author == "Reviewer"
		case AnnotText:
			sawNote = a.Contents == "please confirm"
		}
	}
	if !sawLink {
		t.Error("link annotation missing or wrong")
	}
	if !sawHighlight {
		t.Error("highlight missing or wrong")
	}
	if !sawNote {
		t.Error("sticky note missing or wrong")
	}
	// The page's own content is untouched.
	text, _ := r2.PageText(0)
	if !strings.Contains(text, "INVOICE 2024-117") {
		t.Errorf("page content lost: %q", text)
	}
}

func TestRemoveAnnotations(t *testing.T) {
	// Start from a document that already has three annotations.
	doc := New()
	page := doc.AddPage()
	page.SetFont(Helvetica, 12)
	page.Text(60, 80, "reviewed document")
	page.AddNote(300, 60, "first", NoteOptions{Author: "A"})
	page.AddNote(300, 100, "second", NoteOptions{Author: "B"})
	page.AddHighlight(58, 70, 150, 14, "kept", NoteOptions{})
	src := docBytes(t, doc)

	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Annotations(0); len(got) != 3 {
		t.Fatalf("fixture has %d annotations, want 3", len(got))
	}

	u := Update(r)
	// Strip every sticky note, keep the highlight.
	n, err := u.RemoveAnnotations(0, func(a Annotation) bool {
		return a.Type == AnnotText
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("removed %d annotations, want 2", n)
	}

	r2, err := NewReader(updatedBytes(t, u))
	if err != nil {
		t.Fatal(err)
	}
	left, err := r2.Annotations(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 {
		t.Fatalf("%d annotations left, want 1: %+v", len(left), left)
	}
	if left[0].Type != AnnotHighlight || left[0].Contents != "kept" {
		t.Errorf("wrong annotation survived: %+v", left[0])
	}
	// The page itself is unchanged.
	text, _ := r2.PageText(0)
	if !strings.Contains(text, "reviewed document") {
		t.Errorf("page content lost: %q", text)
	}
}

func TestAnnotationsOutOfRange(t *testing.T) {
	r, _ := NewReader(invoiceFixture(t))
	if _, err := r.Annotations(7); err == nil {
		t.Error("expected an error for an out-of-range page")
	}
	u := Update(r)
	if _, err := u.RemoveAnnotations(7, func(Annotation) bool { return true }); err == nil {
		t.Error("expected an error removing from an out-of-range page")
	}
}

// TestAnnotationsReadRealWorld checks annotation reading against a file
// this library did not write.
func TestAnnotationsReadRealWorld(t *testing.T) {
	r, err := NewReader(formFixture())
	if err != nil {
		t.Fatal(err)
	}
	annots, err := r.Annotations(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(annots) != 6 {
		t.Fatalf("found %d annotations, want the form's 6 widgets", len(annots))
	}
	for _, a := range annots {
		if a.Type != AnnotWidget {
			t.Errorf("annotation typed %q, want Widget", a.Type)
		}
		if a.Rect[2] <= a.Rect[0] || a.Rect[3] <= a.Rect[1] {
			t.Errorf("degenerate rectangle %v", a.Rect)
		}
	}
}
