package gopdf

import (
	"bytes"
	"testing"
)

// layeredDoc draws a black bar on a layer and a second one outside any
// layer, so a render says which of them was painted.
func layeredDoc(t *testing.T, on bool) []byte {
	t.Helper()
	doc := New()
	doc.Compress = false
	l, err := doc.AddLayer("Draft stamp", on)
	if err != nil {
		t.Fatal(err)
	}
	p := doc.AddPage()
	p.SetFillColor(Black)
	p.BeginLayer(l)
	p.Rect(100, 100, 300, 100, Fill)
	p.EndLayer()
	p.Rect(100, 400, 50, 50, Fill) // always visible
	return docBytes(t, doc)
}

func TestLayerAuthoring(t *testing.T) {
	src := layeredDoc(t, true)
	r := NewReaderOrFail(t, src)

	layers := r.Layers()
	if len(layers) != 1 {
		t.Fatalf("%d layers, want 1: %+v", len(layers), layers)
	}
	if layers[0].Name != "Draft stamp" {
		t.Errorf("layer name = %q", layers[0].Name)
	}
	if !layers[0].On {
		t.Error("the layer should start visible")
	}
	verifyXref(t, src)
}

// TestLayerVisibilityIsHonoured: the two halves have to meet. A layer
// written as off must be the layer the renderer declines to paint.
func TestLayerVisibilityIsHonoured(t *testing.T) {
	inkOf := func(src []byte) float64 {
		r := NewReaderOrFail(t, src)
		img, err := r.RenderPage(0, RenderOpts{DPI: 72, IncludeVector: true})
		if err != nil {
			t.Fatal(err)
		}
		return inkFraction(img)
	}
	on, off := inkOf(layeredDoc(t, true)), inkOf(layeredDoc(t, false))
	if on <= 0 || off <= 0 {
		t.Fatalf("ink on=%.4f off=%.4f; the fixture drew nothing", on, off)
	}
	if off >= on {
		t.Errorf("the switched-off layer was painted: %.4f against %.4f", off, on)
	}
	// What remains is the bar drawn outside the layer.
	if off < 0.001 {
		t.Errorf("switching the layer off removed the rest of the page too (%.4f)", off)
	}
}

// TestLayerToggleOnAnExistingDocument switches a layer through an
// incremental update.
func TestLayerToggleOnAnExistingDocument(t *testing.T) {
	src := layeredDoc(t, true)
	r := NewReaderOrFail(t, src)

	u := Update(r)
	if err := u.SetLayerVisible("Draft stamp", false); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	out := NewReaderOrFail(t, buf.Bytes())
	layers := out.Layers()
	if len(layers) != 1 {
		t.Fatalf("%d layers after the update", len(layers))
	}
	if layers[0].On {
		t.Error("the layer is still reported as visible")
	}
	img, err := out.RenderPage(0, RenderOpts{DPI: 72, IncludeVector: true})
	if err != nil {
		t.Fatal(err)
	}
	before, err := r.RenderPage(0, RenderOpts{DPI: 72, IncludeVector: true})
	if err != nil {
		t.Fatal(err)
	}
	if inkFraction(img) >= inkFraction(before) {
		t.Error("the page still paints as much with the layer off")
	}

	// And back on again.
	u2 := Update(out)
	if err := u2.SetLayerVisible("Draft stamp", true); err != nil {
		t.Fatal(err)
	}
	var buf2 bytes.Buffer
	if _, err := u2.WriteTo(&buf2); err != nil {
		t.Fatal(err)
	}
	back := NewReaderOrFail(t, buf2.Bytes())
	if l := back.Layers(); len(l) != 1 || !l[0].On {
		t.Errorf("the layer did not come back on: %+v", l)
	}
	img2, err := back.RenderPage(0, RenderOpts{DPI: 72, IncludeVector: true})
	if err != nil {
		t.Fatal(err)
	}
	if !near(uint8(inkFraction(img2)*255), uint8(inkFraction(before)*255), 2) {
		t.Errorf("the page does not paint as it did: %.4f against %.4f",
			inkFraction(img2), inkFraction(before))
	}
}

func TestLayerErrors(t *testing.T) {
	doc := New()
	if _, err := doc.AddLayer("", true); err == nil {
		t.Error("a layer with no name was accepted")
	}
	// Ending a layer that was never begun writes nothing rather than an
	// unbalanced EMC, which would break every reader after it.
	p := doc.AddPage()
	p.EndLayer()
	p.EndLayer()
	src := docBytes(t, doc)
	if bytes.Contains(src, []byte("EMC")) {
		t.Error("an unmatched EndLayer wrote an EMC")
	}
	if _, err := NewReader(src); err != nil {
		t.Fatalf("the document does not parse: %v", err)
	}

	// Toggling a layer of a document that has none is an error, not a
	// silent no-op.
	r := NewReaderOrFail(t, src)
	if err := Update(r).SetLayerVisible("nothing", false); err == nil {
		t.Error("toggling a layer of a document with none was allowed")
	}
	// And a name nothing matches.
	r2 := NewReaderOrFail(t, layeredDoc(t, true))
	if err := Update(r2).SetLayerVisible("no such layer", false); err == nil {
		t.Error("an unknown layer name was accepted")
	}
}

// TestLayerNesting: two layers on one page each get their own resource
// name and bracket their own content.
func TestLayerNesting(t *testing.T) {
	doc := New()
	doc.Compress = false
	a, _ := doc.AddLayer("English", true)
	b, _ := doc.AddLayer("French", false)
	p := doc.AddPage()
	p.SetFillColor(Black)
	p.BeginLayer(a)
	p.Rect(100, 100, 100, 50, Fill)
	p.EndLayer()
	p.BeginLayer(b)
	p.Rect(100, 200, 100, 50, Fill)
	p.EndLayer()
	src := docBytes(t, doc)

	r := NewReaderOrFail(t, src)
	layers := r.Layers()
	if len(layers) != 2 {
		t.Fatalf("%d layers, want 2: %+v", len(layers), layers)
	}
	byName := map[string]bool{}
	for _, l := range layers {
		byName[l.Name] = l.On
	}
	if !byName["English"] || byName["French"] {
		t.Errorf("visibility is wrong: %+v", byName)
	}
	// Only the visible one is painted.
	img, err := r.RenderPage(0, RenderOpts{DPI: 72, IncludeVector: true})
	if err != nil {
		t.Fatal(err)
	}
	// Each bar is 100x50 points on a 595x842 page; one of them is about
	// a hundredth of it.
	ink := inkFraction(img)
	if ink < 0.005 || ink > 0.015 {
		t.Errorf("%.4f of the page is painted; expected one bar of two", ink)
	}
}
