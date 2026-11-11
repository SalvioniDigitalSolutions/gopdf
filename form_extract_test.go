package gopdf

import (
	"fmt"
	"strings"
	"testing"
)

// xformFixture is a one-page document being fitted with forms by hand:
// the writer only ever lists a form it also draws, and these tests are
// about the difference.
type xformFixture struct {
	t *testing.T
	r *Reader
	u *Updater
	// xobjects and resources are what the page will list.
	xobjects  Dict
	resources Dict
	content   []string
	annots    Array
}

func newXFormFixture(t *testing.T) *xformFixture {
	t.Helper()
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 10)
	page.Text(60, 60, "A page with forms")
	r, err := NewReader(docBytes(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	res, _ := r.Resolve(r.InheritedPageValue(0, "Resources")).(Dict)
	return &xformFixture{t: t, r: r, u: Update(r), xobjects: Dict{}, resources: res.Clone()}
}

// form adds a form XObject. A nil resources leaves the form without a
// /Resources entry at all.
func (f *xformFixture) form(resources Dict, content string) Ref {
	d := Dict{
		"Type":    Name("XObject"),
		"Subtype": Name("Form"),
		"BBox":    Array{float64(0), float64(0), float64(100), float64(100)},
	}
	if resources != nil {
		d["Resources"] = resources
	}
	return f.u.AddObject(NewStream(d, []byte(content)))
}

// reader writes the fixture out and opens it.
func (f *xformFixture) reader() *Reader {
	f.t.Helper()
	res := f.resources.Clone()
	if len(f.xobjects) > 0 {
		res["XObject"] = f.xobjects
	}
	if err := f.u.SetPageEntry(0, "Resources", res); err != nil {
		f.t.Fatal(err)
	}
	if len(f.content) > 0 {
		extra := f.u.AddObject(NewStream(Dict{}, []byte(strings.Join(f.content, "\n"))))
		contents := Array{f.r.PageDict(0)["Contents"], extra}
		if err := f.u.SetPageEntry(0, "Contents", contents); err != nil {
			f.t.Fatal(err)
		}
	}
	if len(f.annots) > 0 {
		if err := f.u.SetPageEntry(0, "Annots", f.annots); err != nil {
			f.t.Fatal(err)
		}
	}
	r, err := NewReader(updatedBytes(f.t, f.u))
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

// wantForms checks that a page runs exactly the given forms.
func wantForms(t *testing.T, r *Reader, page int, want map[string]Ref) []FormRef {
	t.Helper()
	got, err := r.PageForms(page)
	if err != nil {
		t.Fatalf("PageForms: %v", err)
	}
	have := map[int]bool{}
	for _, f := range got {
		if f.Page != page {
			t.Errorf("form %d reports page %d, want %d", f.ObjectNumber(), f.Page, page)
		}
		if have[f.ObjectNumber()] {
			t.Errorf("form %d is listed twice", f.ObjectNumber())
		}
		have[f.ObjectNumber()] = true
	}
	for label, ref := range want {
		if !have[ref.Num] {
			t.Errorf("%s (object %d) is not listed, and the page runs it", label, ref.Num)
		}
		delete(have, ref.Num)
	}
	for num := range have {
		t.Errorf("object %d is listed, and nothing runs it", num)
	}
	return got
}

// A page lists two forms and invokes one. Only that one runs.
func TestPageFormsListsWhatThePageInvokes(t *testing.T) {
	f := newXFormFixture(t)
	used := f.form(Dict{}, "0 0 10 10 re f")
	unused := f.form(Dict{}, "0 0 20 20 re f")
	f.xobjects["Used"], f.xobjects["Unused"] = used, unused
	f.content = []string{"q /Used Do Q", "q /Used Do Q"}
	r := f.reader()

	got := wantForms(t, r, 0, map[string]Ref{"the invoked form": used})
	if len(got) != 1 || got[0].Name != "Used" {
		t.Errorf("got %+v, want the one form, by the name it was invoked by", got)
	}
	if _, err := r.PageForms(1); err == nil {
		t.Error("a page that does not exist reported no error")
	}
	if _, err := r.PageForms(-1); err == nil {
		t.Error("a negative page reported no error")
	}
}

// A page that draws nothing but text runs no form.
func TestPageFormsOfAPlainPage(t *testing.T) {
	r := newXFormFixture(t).reader()
	wantForms(t, r, 0, nil)
}

// Forms invoke forms. The chain is followed to its end — further than
// the text and image readers go, because the question here is whether
// ANYTHING can reach a form — and a form listed along the way without
// being invoked is still not run.
func TestPageFormsFollowsNestedForms(t *testing.T) {
	f := newXFormFixture(t)
	const depth = maxFormDepth + 8
	want := map[string]Ref{}
	// Built innermost first: each form lists and invokes the next one
	// in, and lists a bystander it never invokes.
	inner := f.form(Dict{}, "0 0 1 1 re f")
	want["the innermost form"] = inner
	var bystanders []Ref
	for i := 0; i < depth; i++ {
		by := f.form(Dict{}, "0 0 2 2 re f")
		bystanders = append(bystanders, by)
		inner = f.form(Dict{"XObject": Dict{"Next": inner, "By": by}}, "q /Next Do Q")
		want[fmt.Sprintf("form %d levels out", i+1)] = inner
	}
	f.xobjects["Outer"] = inner
	f.content = []string{"/Outer Do"}
	r := f.reader()

	got := wantForms(t, r, 0, want)
	for _, g := range got {
		for _, by := range bystanders {
			if g.ObjectNumber() == by.Num {
				t.Fatalf("a form listed inside a form and never invoked is reported as run")
			}
		}
	}
}

// A form without /Resources takes its names from whoever invoked it.
func TestPageFormsFormWithoutResources(t *testing.T) {
	f := newXFormFixture(t)
	leaf := f.form(Dict{}, "0 0 5 5 re f")
	bare := f.form(nil, "/Leaf Do")
	f.xobjects["Bare"], f.xobjects["Leaf"] = bare, leaf
	f.content = []string{"/Bare Do"}
	wantForms(t, f.reader(), 0, map[string]Ref{"the form without resources": bare, "the form it finds on the page": leaf})
}

// Names are local. A form that brings its own /Fm0 and invokes it does
// not thereby invoke the different /Fm0 the page lists.
func TestPageFormsNameCollisionStaysLocal(t *testing.T) {
	f := newXFormFixture(t)
	pageFm0 := f.form(Dict{}, "0 0 7 7 re f")
	innerFm0 := f.form(Dict{}, "0 0 8 8 re f")
	outer := f.form(Dict{"XObject": Dict{"Fm0": innerFm0}}, "/Fm0 Do")
	f.xobjects["Fm0"], f.xobjects["Outer"] = pageFm0, outer
	f.content = []string{"/Outer Do"}
	wantForms(t, f.reader(), 0, map[string]Ref{"the outer form": outer, "its own Fm0": innerFm0})
}

// A form whose own resources lack a name it invokes: a strict viewer
// draws nothing, a forgiving one looks further out. The list sides with
// the forgiving one.
func TestPageFormsLooksOutwardsForAMissingName(t *testing.T) {
	f := newXFormFixture(t)
	onPage := f.form(Dict{}, "0 0 9 9 re f")
	outer := f.form(Dict{"XObject": Dict{}}, "/OnPage Do")
	f.xobjects["OnPage"], f.xobjects["Outer"] = onPage, outer
	f.content = []string{"/Outer Do"}
	wantForms(t, f.reader(), 0, map[string]Ref{"the outer form": outer, "the form found on the page": onPage})
}

// Forms that invoke each other in a circle are each run once.
func TestPageFormsSurvivesACycle(t *testing.T) {
	f := newXFormFixture(t)
	// The two forms name each other through one shared dictionary, and
	// a third names itself through its own.
	shared := f.u.AddObject(Dict{})
	a := f.u.AddObject(NewStream(Dict{"Subtype": Name("Form"), "Resources": shared}, []byte("/B Do")))
	b := f.u.AddObject(NewStream(Dict{"Subtype": Name("Form"), "Resources": shared}, []byte("/A Do")))
	if err := f.u.SetObject(shared, Dict{"XObject": Dict{"A": a, "B": b}}); err != nil {
		t.Fatal(err)
	}
	self := f.u.AddObject(NewStream(Dict{"Subtype": Name("Form")}, []byte("/Self Do")))
	f.xobjects["A"], f.xobjects["Self"] = a, self
	f.content = []string{"/A Do /Self Do"}
	wantForms(t, f.reader(), 0, map[string]Ref{"A": a, "B": b, "the form that invokes itself": self})
}

// A glyph of a Type 3 font is a content stream, and may invoke a form.
// A font without resources of its own finds the form on the page.
func TestPageFormsFollowsType3Glyphs(t *testing.T) {
	for _, own := range []bool{false, true} {
		name := "page-resources"
		if own {
			name = "font-resources"
		}
		t.Run(name, func(t *testing.T) {
			f := newXFormFixture(t)
			glyphForm := f.form(Dict{}, "0 0 600 600 re f")
			bystander := f.form(Dict{}, "0 0 1 1 re f")
			invoking := f.u.AddObject(NewStream(Dict{}, []byte("600 0 d0 /Glyph Do")))
			plain := f.u.AddObject(NewStream(Dict{}, []byte("600 0 d0 0 0 600 600 re f")))
			font := Dict{
				"Type":       Name("Font"),
				"Subtype":    Name("Type3"),
				"FontBBox":   Array{float64(0), float64(0), float64(1000), float64(1000)},
				"FontMatrix": Array{0.001, float64(0), float64(0), 0.001, float64(0), float64(0)},
				"CharProcs":  Dict{"a": invoking, "b": plain},
				"Encoding":   Dict{"Type": Name("Encoding"), "Differences": Array{int64(97), Name("a"), Name("b")}},
				"FirstChar":  int64(97),
				"LastChar":   int64(98),
				"Widths":     Array{int64(600), int64(600)},
			}
			if own {
				font["Resources"] = Dict{"XObject": Dict{"Glyph": glyphForm}}
				f.xobjects["Glyph"] = bystander // same name, another form
			} else {
				f.xobjects["Glyph"] = glyphForm
				f.xobjects["Other"] = bystander
			}
			fonts, _ := f.r.Resolve(f.resources["Font"]).(Dict)
			fonts = fonts.Clone()
			fonts["T3"] = f.u.AddObject(font)
			f.resources["Font"] = fonts
			wantForms(t, f.reader(), 0, map[string]Ref{"the form a glyph invokes": glyphForm})
		})
	}
}

// A tiling pattern is a content stream with resources of its own.
func TestPageFormsFollowsTilingPatterns(t *testing.T) {
	f := newXFormFixture(t)
	tile := f.form(Dict{}, "0 0 4 4 re f")
	pattern := f.u.AddObject(NewStream(Dict{
		"Type":        Name("Pattern"),
		"PatternType": int64(1),
		"PaintType":   int64(1),
		"TilingType":  int64(1),
		"BBox":        Array{float64(0), float64(0), float64(8), float64(8)},
		"XStep":       float64(8),
		"YStep":       float64(8),
		"Resources":   Dict{"XObject": Dict{"Tile": tile}},
	}, []byte("/Tile Do")))
	f.resources["Pattern"] = Dict{"P1": pattern}
	wantForms(t, f.reader(), 0, map[string]Ref{"the form a pattern's cell invokes": tile})
}

// The group of a soft mask is a form that runs without a name, and may
// invoke others.
func TestPageFormsFollowsSoftMaskGroups(t *testing.T) {
	f := newXFormFixture(t)
	inner := f.form(Dict{}, "0 0 3 3 re f")
	group := f.form(Dict{"XObject": Dict{"Inner": inner}}, "/Inner Do")
	f.resources["ExtGState"] = Dict{"GS1": Dict{
		"Type":  Name("ExtGState"),
		"SMask": Dict{"Type": Name("Mask"), "S": Name("Luminosity"), "G": group},
	}}
	got := wantForms(t, f.reader(), 0, map[string]Ref{"the mask's group": group, "the form the group invokes": inner})
	for _, g := range got {
		if g.ObjectNumber() == group.Num && g.Name != "" {
			t.Errorf("the mask's group reports the name %q; nothing names it", g.Name)
		}
	}
}

// An annotation's appearance is a form drawn over the page: every state
// of it, and whatever those invoke. An appearance without resources
// finds its names on the page.
func TestPageFormsFollowsAnnotationAppearances(t *testing.T) {
	f := newXFormFixture(t)
	onPage := f.form(Dict{}, "0 0 6 6 re f")
	bystander := f.form(Dict{}, "0 0 1 1 re f")
	f.xobjects["OnPage"], f.xobjects["Bystander"] = onPage, bystander

	stamp := f.form(nil, "/OnPage Do")
	inState := f.form(Dict{}, "0 0 2 2 re f")
	on := f.form(Dict{"XObject": Dict{"In": inState}}, "/In Do")
	off := f.form(Dict{}, "")
	down := f.form(Dict{}, "0 0 1 1 re S")
	annot := func(ap Dict) Ref {
		return f.u.AddObject(Dict{
			"Type":    Name("Annot"),
			"Subtype": Name("Stamp"),
			"Rect":    Array{float64(10), float64(10), float64(110), float64(110)},
			"AP":      ap,
		})
	}
	f.annots = Array{
		annot(Dict{"N": stamp}),
		annot(Dict{"N": Dict{"On": on, "Off": off}, "D": down}),
	}
	got := wantForms(t, f.reader(), 0, map[string]Ref{
		"the stamp's appearance":        stamp,
		"the form it finds on the page": onPage,
		"the On state":                  on,
		"the Off state":                 off,
		"the pressed look":              down,
		"the form the On state invokes": inState,
	})
	for _, g := range got {
		if g.ObjectNumber() == stamp.Num && g.Name != "" {
			t.Errorf("an appearance reports the name %q; nothing names it", g.Name)
		}
	}
}

// A form two pages list is run by the page that invokes it, and only
// by that one.
func TestPageFormsIsPerPage(t *testing.T) {
	doc := New()
	doc.Compress = false
	for i := 0; i < 2; i++ {
		page := doc.AddPage()
		page.SetFont(Helvetica, 10)
		page.Text(60, 60, "page")
	}
	base, err := NewReader(docBytes(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	u := Update(base)
	form := u.AddObject(NewStream(Dict{"Subtype": Name("Form")}, []byte("0 0 1 1 re f")))
	res, _ := base.Resolve(base.InheritedPageValue(0, "Resources")).(Dict)
	res = res.Clone()
	res["XObject"] = Dict{"Shared": form}
	shared := u.AddObject(res)
	for page := 0; page < 2; page++ {
		if err := u.SetPageEntry(page, "Resources", shared); err != nil {
			t.Fatal(err)
		}
	}
	invoke := u.AddObject(NewStream(Dict{}, []byte("/Shared Do")))
	if err := u.SetPageEntry(1, "Contents", Array{base.PageDict(1)["Contents"], invoke}); err != nil {
		t.Fatal(err)
	}
	r, err := NewReader(updatedBytes(t, u))
	if err != nil {
		t.Fatal(err)
	}
	wantForms(t, r, 0, nil)
	wantForms(t, r, 1, map[string]Ref{"the shared form": form})
}

// The bytes of an inline image are not tokens. A "(" among them opens a
// string for the lexer, a ")" in a later image closes it, and whatever
// stood between the two is swallowed; an invocation there must be found
// all the same.
func TestPageFormsSeesPastAnInlineImage(t *testing.T) {
	f := newXFormFixture(t)
	after := f.form(Dict{}, "0 0 5 5 re f")
	f.xobjects["After"] = after
	image := func(pixels string) string {
		return "q 10 0 0 10 0 0 cm BI /W 4 /H 1 /BPC 8 /CS /G ID " + pixels + " EI Q"
	}
	f.content = []string{image("\x28\x10\x20\xff"), "/After Do", image("\x29\x10\x20\xff")}

	// The premise: read as tokens alone, the invocation is lost.
	tokensSeeIt := false
	var last any
	for _, tok := range tokenizeContent([]byte(strings.Join(f.content, "\n"))) {
		if op, isOp := tok.val.(opKeyword); isOp {
			if n, ok := last.(Name); ok && op == "Do" && n == "After" {
				tokensSeeIt = true
			}
			last = nil
			continue
		}
		last = tok.val
	}
	if tokensSeeIt {
		t.Skip("fixture drift: the lexer now reads past this inline image, so the test no longer shows the second reading at work")
	}
	wantForms(t, f.reader(), 0, map[string]Ref{"the form invoked after the image": after})
}

func TestContentDoNames(t *testing.T) {
	for _, c := range []struct {
		content string
		want    string
	}{
		{"", ""},
		{"/Fm1 Do", "Fm1"},
		{"q /Fm1 Do Q /Fm2 Do /Fm1 Do", "Fm1 Fm2"},
		{"/Fm1\r\n\tDo", "Fm1"},
		{"/Fm1 % a remark\nDo", "Fm1"},
		{"/F#6d1 Do", "Fm1"},
		{"/FmDo", ""},
		{"/Fm1 Dot", ""},
		{"/Fm1 Tf", ""},
		{"1 0 0 1 0 0 cm Do", ""},
		{"/A Do[/B Do]", "A B"},
		{"BI /W 1 /H 1 ID ( EI /Late Do BI /W 1 /H 1 ID ) EI", "Late"},
		{"(/InText Do) Tj", "InText"}, // a name too many, by design
	} {
		names, cut := contentDoNames([]byte(c.content))
		if cut {
			t.Errorf("%q: reported as too large", c.content)
		}
		var got []string
		for _, n := range names {
			got = append(got, string(n))
		}
		if strings.Join(got, " ") != c.want {
			t.Errorf("%q: got %q, want %q", c.content, strings.Join(got, " "), c.want)
		}
	}
}

// A form that cannot be decoded may invoke anything. If the page runs
// it, the list is not known and PageForms says so; if nothing runs it,
// what it would have invoked does not matter.
func TestPageFormsRefusesWhatItCannotRead(t *testing.T) {
	broken := func(f *xformFixture) Ref {
		return f.u.AddObject(NewStream(Dict{
			"Subtype": Name("Form"),
			"Filter":  Name("FlateDecode"),
		}, []byte("this is not deflate data")))
	}

	f := newXFormFixture(t)
	f.xobjects["Broken"] = broken(f)
	f.content = []string{"/Broken Do"}
	if got, err := f.reader().PageForms(0); err == nil {
		t.Errorf("a page that runs an undecodable form reported %d form(s) and no error", len(got))
	}

	f = newXFormFixture(t)
	fine := f.form(Dict{}, "0 0 1 1 re f")
	f.xobjects["Broken"], f.xobjects["Fine"] = broken(f), fine
	f.content = []string{"/Fine Do"}
	wantForms(t, f.reader(), 0, map[string]Ref{"the form the page invokes": fine})
}

// A page with resources of its own, under a page-tree node that has
// some too: a strict viewer reads the page's and stops, another merges
// the two. A form only the node lists, invoked by the page, is run for
// the second kind — so it is listed.
func TestPageFormsLooksInThePageTreeAbove(t *testing.T) {
	f := newXFormFixture(t)
	above := f.form(Dict{}, "0 0 3 3 re f")
	unused := f.form(Dict{}, "0 0 4 4 re f")
	parent, isRef := f.r.PageDict(0)["Parent"].(Ref)
	if !isRef {
		t.Fatal("fixture: the page has no parent node")
	}
	node, _ := f.r.Object(parent).(Dict)
	node = node.Clone()
	node["Resources"] = Dict{"XObject": Dict{"Above": above, "Unused": unused}}
	if err := f.u.SetObject(parent, node); err != nil {
		t.Fatal(err)
	}
	f.content = []string{"/Above Do"}
	wantForms(t, f.reader(), 0, map[string]Ref{"the form the node above lists": above})
}

// A form without resources, invoked from inside a form that has them,
// names something both that form and the page list — differently. One
// reading of the rule gives it the invoking form's, the other the
// page's; both are followed, and a third form neither reading reaches
// is not.
func TestPageFormsFormWithoutResourcesInsideAForm(t *testing.T) {
	f := newXFormFixture(t)
	fromOuter := f.form(Dict{}, "0 0 1 1 re f")
	fromPage := f.form(Dict{}, "0 0 2 2 re f")
	neither := f.form(Dict{}, "0 0 3 3 re f")
	bare := f.form(nil, "/Leaf Do")
	outer := f.form(Dict{"XObject": Dict{"Bare": bare, "Leaf": fromOuter, "Other": neither}}, "/Bare Do")
	f.xobjects["Outer"], f.xobjects["Leaf"] = outer, fromPage
	f.content = []string{"/Outer Do"}
	wantForms(t, f.reader(), 0, map[string]Ref{
		"the outer form":                outer,
		"the form without resources":    bare,
		"the Leaf the outer form lists": fromOuter,
		"the Leaf the page lists":       fromPage,
	})
}
