package gopdf

import (
	"bytes"
	"strings"
	"testing"
)

// patternTextReader returns a page with two boxes filled by a tiling
// pattern whose cell writes a name found nowhere else on the page.
func patternTextReader(t *testing.T) *Reader {
	t.Helper()
	f := newXFormFixture(t)
	pattern := f.u.AddObject(NewStream(Dict{
		"Type":        Name("Pattern"),
		"PatternType": int64(1),
		"PaintType":   int64(1),
		"TilingType":  int64(1),
		"BBox":        Array{float64(0), float64(0), float64(200), float64(30)},
		"XStep":       float64(200),
		"YStep":       float64(30),
		"Resources":   Dict{"Font": f.resources["Font"]},
	}, []byte("BT /F1 9 Tf 0.6 g 4 10 Td (Kopie Hans Aeschlimann) Tj ET")))
	shading := f.u.AddObject(Dict{"Type": Name("Pattern"), "PatternType": int64(2)})
	f.resources["Pattern"] = Dict{"P1": pattern, "Sh": shading}
	f.content = append(f.content,
		"/Pattern cs /P1 scn 72 300 400 90 re f",
		"/Sh scn 72 500 100 20 re f",
		"/Pattern CS /P1 SCN 72 600 400 30 re S")
	return f.reader()
}

// streamsHolding returns how many streams of a file hold a text, each
// decoded as the file says.
func streamsHolding(t *testing.T, data []byte, text string) int {
	t.Helper()
	r, err := NewReader(data)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	r.Walk(func(_ Ref, obj any) bool {
		if stm, ok := obj.(*Stream); ok {
			if body, derr := stm.Data(); derr == nil && bytes.Contains(body, []byte(text)) {
				n++
			}
		}
		return true
	})
	return n
}

// What the cell of a tiling pattern writes is on the page wherever the
// pattern is painted. It is read with the page's text, once.
func TestPatternTextIsRead(t *testing.T) {
	r := patternTextReader(t)
	text, err := r.PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(text, "Kopie Hans Aeschlimann"); n != 1 {
		t.Errorf("the page's text holds the cell's line %d times, want once: %q", n, text)
	}
	if !strings.Contains(text, "A page with forms") {
		t.Errorf("the page's own text is gone: %q", text)
	}
	frags, err := r.PageTextFragments(0)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, f := range frags {
		if f.Text == "Kopie Hans Aeschlimann" {
			found++
			if f.FontSize != 9 {
				t.Errorf("the cell's line is reported at %.1f points, want 9", f.FontSize)
			}
		}
	}
	if found != 1 {
		t.Errorf("the cell's line is %d of the page's pieces of text, want one", found)
	}
}

// A redaction takes a name out of the cell too: every tile loses it,
// the rest of the cell stays, and no bar is painted for it — there is
// no one place on the page where it stood.
func TestRedactReachesPatternText(t *testing.T) {
	r := patternTextReader(t)
	rd := Redact(r)
	rd.Text("Hans Aeschlimann")
	marks, err := rd.Marks()
	if err != nil {
		t.Fatal(err)
	}
	if len(marks) != 1 || marks[0].Kind != RedactText || marks[0].Text != "Hans Aeschlimann" {
		t.Errorf("marks: %+v", marks)
	}
	out := redactTo(t, rd)
	if got := extractAll(t, out); strings.Contains(got, "Aeschlimann") || !strings.Contains(got, "Kopie") {
		t.Errorf("after the redaction the page reads %q", got)
	}
	if n := streamsHolding(t, out, "Aeschlimann"); n != 0 {
		t.Errorf("%d streams of the output still hold the name", n)
	}
	r2, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	content, err := r2.pageContent(r2.pages[0].dict)
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(content, []byte(" re f")); n != 2 {
		t.Errorf("the page fills %d rectangles, want its own two and no bar: %s", n, content)
	}
}

// A substitution writes its token into the cell.
func TestPseudonymizeReachesPatternText(t *testing.T) {
	r := patternTextReader(t)
	var buf bytes.Buffer
	res, err := Pseudonymize(r, &buf, []Pseudonym{{From: "Hans Aeschlimann", To: "[[PII_PERSON_001]]", FitWidth: true, MinScale: 0.18}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Replaced["Hans Aeschlimann"] == 0 {
		t.Error("nothing is reported replaced")
	}
	got := extractAll(t, buf.Bytes())
	if strings.Contains(got, "Aeschlimann") || !strings.Contains(got, "Kopie [[PII_PERSON_001]]") {
		t.Errorf("after the substitution the page reads %q", got)
	}
	if n := streamsHolding(t, buf.Bytes(), "Aeschlimann"); n != 0 {
		t.Errorf("%d streams of the output still hold the name", n)
	}
}

// An area elsewhere on the page leaves the cell as it was.
func TestRedactAreaLeavesPatternText(t *testing.T) {
	r := patternTextReader(t)
	rd := Redact(r)
	rd.Area(0, 60, 40, 200, 30) // the page's own line, not the cell
	out := redactTo(t, rd)
	if got := extractAll(t, out); !strings.Contains(got, "Kopie Hans Aeschlimann") {
		t.Errorf("an area elsewhere changed the cell: %q", got)
	}
}

// A pattern whose cell paints with the pattern itself is read once and
// left: the scan ends.
func TestPatternTextSurvivesACycle(t *testing.T) {
	f := newXFormFixture(t)
	ref := f.u.AddObject(nil)
	cell := NewStream(Dict{
		"Type":        Name("Pattern"),
		"PatternType": int64(1),
		"PaintType":   int64(1),
		"TilingType":  int64(1),
		"BBox":        Array{float64(0), float64(0), float64(200), float64(30)},
		"XStep":       float64(200),
		"YStep":       float64(30),
		"Resources":   Dict{"Font": f.resources["Font"], "Pattern": Dict{"P1": ref}},
	}, []byte("/Pattern cs /P1 scn BT /F1 9 Tf 4 10 Td (Im Kreis) Tj ET"))
	if err := f.u.SetObject(ref, cell); err != nil {
		t.Fatal(err)
	}
	f.resources["Pattern"] = Dict{"P1": ref}
	f.content = append(f.content, "/Pattern cs /P1 scn 72 300 400 90 re f")
	r := f.reader()
	text, err := r.PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(text, "Im Kreis") != 1 {
		t.Errorf("a cell that paints with itself reads %q", text)
	}
	if _, err := r.PageTextFragments(0); err != nil {
		t.Fatal(err)
	}
}
