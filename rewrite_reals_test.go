package gopdf

// rewrite_reals_test.go — a rewritten file keeps the source's numbers.

import (
	"bytes"
	"testing"
)

// A form drawn through a fine matrix is drawn at its size after a
// rewrite: written to three decimals, 0.002468 became 0.002 and the
// form shrank by a fifth.
func TestRewriteKeepsTheSourcesNumbers(t *testing.T) {
	src := kfix{
		res:     "/XObject << /Fm1 10 0 R >>",
		content: "q 1 0 0 1 20 20 cm /Fm1 Do Q\n",
		objs: map[int]string{10: kstream("/Type /XObject /Subtype /Form /BBox [0 0 405.119995 28.799999] /Matrix [0.002468 0 0 0.034722 0 0]",
			"0 0 1 rg 0 0 40000 1000 re f")},
	}.pdf()
	r := NewReaderOrFail(t, src)
	var rw bytes.Buffer
	if _, err := Rewrite(r, &rw); err != nil {
		t.Fatal(err)
	}
	var kept bytes.Buffer
	if _, err := KeepOnly(NewReaderOrFail(t, src), &kept, KeepPolicy{}); err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string][]byte{"Rewrite": rw.Bytes(), "KeepOnly": kept.Bytes()} {
		cp := NewReaderOrFail(t, out)
		res, _ := cp.resolve(cp.PageDict(0)["Resources"]).(Dict)
		xo, _ := cp.resolve(res["XObject"]).(Dict)
		form, _ := cp.resolve(xo["Fm1"]).(*rawStream)
		if form == nil {
			t.Fatalf("%s: the form is gone", name)
		}
		m, _ := cp.resolve(form.dict["Matrix"]).(Array)
		box, _ := cp.resolve(form.dict["BBox"]).(Array)
		if len(m) != 6 || m[0] != 0.002468 || m[3] != 0.034722 || len(box) != 4 || box[2] != 405.119995 {
			t.Errorf("%s wrote matrix %v and box %v", name, m, box)
		}
		a, _ := r.RenderPage(0, RenderOpts{DPI: 40, IncludeVector: true})
		b, _ := cp.RenderPage(0, RenderOpts{DPI: 40, IncludeVector: true})
		if d := pixelDiffOf(a, b); d != 0 {
			t.Errorf("%s draws %d pixels differently", name, d)
		}
	}
	if got := exactReal(-0.0, 64); got != "0" {
		t.Errorf("-0 written as %q", got)
	}
	if got := exactReal(1e-7, 64); got != "0.0000001" {
		t.Errorf("1e-7 written as %q", got)
	}
}
