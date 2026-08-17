package gopdf

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// taggedDoc builds a document with a structure tree: a heading, two
// paragraphs, and a figure with alternate text — the shape a tagged
// document has, in miniature.
func taggedDoc(t *testing.T) []byte {
	t.Helper()
	doc := New()
	doc.Compress = false
	p := doc.AddPage()
	p.SetFont(Helvetica, 18)
	p.Text(72, 100, "Quarterly Report")
	p.SetFont(Helvetica, 12)
	p.Text(72, 140, "Revenue rose.")
	p.Text(72, 160, "Costs fell.")
	src := docBytes(t, doc)

	r := NewReaderOrFail(t, src)
	u := Update(r)
	pageRef, _ := r.PageRef(0)

	kid := func(role Name, extra Dict) Ref {
		d := Dict{"Type": Name("StructElem"), "S": role, "Pg": pageRef}
		for k, v := range extra {
			d[k] = v
		}
		return u.AddObject(d)
	}
	heading := kid("Head1", Dict{"T": String("Quarterly Report")})
	para1 := kid("P", Dict{"ActualText": String("Revenue rose.")})
	para2 := kid("P", Dict{"ActualText": String("Costs fell.")})
	figure := kid("Figure", Dict{"Alt": String("A chart of revenue, rising")})
	section := u.AddObject(Dict{
		"Type": Name("StructElem"), "S": Name("Sect"),
		"K": Array{heading, para1, para2, figure},
	})
	root := u.AddObject(Dict{
		"Type": Name("StructTreeRoot"),
		"K":    Array{section},
		// The document uses its own name for a heading and says what it
		// stands for.
		"RoleMap": Dict{"Head1": Name("H1")},
	})
	if err := u.SetCatalogEntry("StructTreeRoot", root); err != nil {
		t.Fatal(err)
	}
	if err := u.SetCatalogEntry("MarkInfo", Dict{"Marked": true}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestStructureTree(t *testing.T) {
	r := NewReaderOrFail(t, taggedDoc(t))

	if !r.Tagged() {
		t.Fatal("the document is not reported as carrying a tree")
	}
	if !r.MarkedTagged() {
		t.Error("the document does not report itself as tagged")
	}
	tree := r.Structure()
	if len(tree) != 1 {
		t.Fatalf("%d roots, want the one section", len(tree))
	}
	sect := tree[0]
	if sect.Type != "Sect" {
		t.Errorf("root type = %q", sect.Type)
	}
	if len(sect.Children) != 4 {
		t.Fatalf("%d children, want 4: %+v", len(sect.Children), sect.Children)
	}

	h := sect.Children[0]
	if h.Type != "Head1" {
		t.Errorf("the heading's own type = %q", h.Type)
	}
	// The document's own name is mapped to the standard one, which is
	// the whole point of the role map.
	if h.Role != "H1" {
		t.Errorf("the heading's role = %q, want H1 through the role map", h.Role)
	}
	if h.Title != "Quarterly Report" {
		t.Errorf("title = %q", h.Title)
	}
	if h.Page != 0 {
		t.Errorf("the heading is on page %d, want 0", h.Page)
	}

	fig := sect.Children[3]
	if fig.Role != "Figure" || fig.Alt != "A chart of revenue, rising" {
		t.Errorf("figure = %+v", fig)
	}
	// A role nothing maps stays as it is.
	if sect.Children[1].Role != "P" {
		t.Errorf("an unmapped role became %q", sect.Children[1].Role)
	}
	// The section has no page of its own and takes it from its content.
	if sect.Page != 0 {
		t.Errorf("the section reports page %d, want the page its content is on", sect.Page)
	}
}

func TestStructText(t *testing.T) {
	r := NewReaderOrFail(t, taggedDoc(t))
	got := r.StructText()
	// The order is the structure's, and the figure speaks through its
	// alternate text.
	want := "Revenue rose.\nCosts fell.\nA chart of revenue, rising"
	if got != want {
		t.Errorf("StructText() = %q, want %q", got, want)
	}
}

func TestStructOutline(t *testing.T) {
	r := NewReaderOrFail(t, taggedDoc(t))
	heads := r.StructOutline()
	if len(heads) != 1 {
		t.Fatalf("%d headings, want 1: %+v", len(heads), heads)
	}
	if heads[0].Level != 1 || heads[0].Text != "Quarterly Report" || heads[0].Page != 0 {
		t.Errorf("heading = %+v", heads[0])
	}
}

func TestHeadingLevels(t *testing.T) {
	for _, c := range []struct {
		role string
		want int
	}{
		{"H", 1}, {"H1", 1}, {"H2", 2}, {"H6", 6},
		{"H7", 0}, {"H0", 0}, {"P", 0}, {"Head1", 0}, {"", 0}, {"Heading", 0},
	} {
		if got := headingLevel(c.role); got != c.want {
			t.Errorf("headingLevel(%q) = %d, want %d", c.role, got, c.want)
		}
	}
}

// TestUntaggedDocument: an untagged document has no structure to walk,
// and says so rather than inventing one.
func TestUntaggedDocument(t *testing.T) {
	doc := New()
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(72, 100, "plain text")
	r := NewReaderOrFail(t, docBytes(t, doc))

	if r.Tagged() {
		t.Error("an untagged document reports a tree")
	}
	if r.MarkedTagged() {
		t.Error("an untagged document reports itself as tagged")
	}
	if got := r.Structure(); got != nil {
		t.Errorf("Structure() = %+v", got)
	}
	if got := r.StructText(); got != "" {
		t.Errorf("StructText() = %q, want nothing", got)
	}
	if got := r.StructOutline(); got != nil {
		t.Errorf("StructOutline() = %+v", got)
	}
	// And PageText still works, which is what to use there.
	if txt, err := r.PageText(0); err != nil || !strings.Contains(txt, "plain text") {
		t.Errorf("page text: %q %v", txt, err)
	}
}

// TestStructureSurvivesACycle: an element that lists itself among its
// children must not send the walk round for ever.
func TestStructureSurvivesACycle(t *testing.T) {
	doc := New()
	doc.Compress = false
	doc.AddPage()
	r := NewReaderOrFail(t, docBytes(t, doc))
	u := Update(r)

	loop := u.AddObject(Dict{"Type": Name("StructElem"), "S": Name("P")})
	// Point it at itself, and at a second element that points back.
	other := u.AddObject(Dict{
		"Type": Name("StructElem"), "S": Name("P"), "K": Array{loop},
	})
	if err := u.SetObject(loop, Dict{
		"Type": Name("StructElem"), "S": Name("P"), "K": Array{loop, other},
	}); err != nil {
		t.Fatal(err)
	}
	root := u.AddObject(Dict{"Type": Name("StructTreeRoot"), "K": Array{loop}})
	if err := u.SetCatalogEntry("StructTreeRoot", root); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}

	done := make(chan int, 1)
	go func() {
		tree := NewReaderOrFail(t, buf.Bytes()).Structure()
		done <- len(tree)
	}()
	select {
	case n := <-done:
		if n == 0 {
			t.Error("the tree read as empty")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the walk did not finish")
	}
}

// TestStructureIgnoresContentReferences: a marked-content reference
// points at content rather than describing it, and is not an element.
func TestStructureIgnoresContentReferences(t *testing.T) {
	doc := New()
	doc.Compress = false
	doc.AddPage()
	r := NewReaderOrFail(t, docBytes(t, doc))
	u := Update(r)
	pageRef, _ := r.PageRef(0)

	para := u.AddObject(Dict{
		"Type": Name("StructElem"), "S": Name("P"), "Pg": pageRef,
		"ActualText": String("a paragraph"),
		// Its children are content references and an integer marked-content
		// identifier, none of which is an element.
		"K": Array{
			int64(0),
			Dict{"Type": Name("MCR"), "Pg": pageRef, "MCID": int64(0)},
			Dict{"Type": Name("OBJR"), "Obj": pageRef},
		},
	})
	root := u.AddObject(Dict{"Type": Name("StructTreeRoot"), "K": Array{para}})
	if err := u.SetCatalogEntry("StructTreeRoot", root); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}

	tree := NewReaderOrFail(t, buf.Bytes()).Structure()
	if len(tree) != 1 {
		t.Fatalf("%d elements, want the paragraph alone: %+v", len(tree), tree)
	}
	if len(tree[0].Children) != 0 {
		t.Errorf("content references were read as elements: %+v", tree[0].Children)
	}
	if got := NewReaderOrFail(t, buf.Bytes()).StructText(); got != "a paragraph" {
		t.Errorf("StructText() = %q", got)
	}
}

// TestStructActualTextWins: an element that says what it really reads as
// speaks for its children, which is how a ligature or a decorated word
// comes out as the word.
func TestStructActualTextWins(t *testing.T) {
	doc := New()
	doc.Compress = false
	doc.AddPage()
	r := NewReaderOrFail(t, docBytes(t, doc))
	u := Update(r)

	inner := u.AddObject(Dict{
		"Type": Name("StructElem"), "S": Name("Span"),
		"ActualText": String("should not appear"),
	})
	outer := u.AddObject(Dict{
		"Type": Name("StructElem"), "S": Name("P"),
		"ActualText": String("difficult"), "K": Array{inner},
	})
	root := u.AddObject(Dict{"Type": Name("StructTreeRoot"), "K": Array{outer}})
	if err := u.SetCatalogEntry("StructTreeRoot", root); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	if got := NewReaderOrFail(t, buf.Bytes()).StructText(); got != "difficult" {
		t.Errorf("StructText() = %q, want the outer element's own reading", got)
	}
}
