package gopdf

import (
	"bytes"
	"strings"
	"testing"
)

func TestGradientRect(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	if err := page.FillGradientRect(50, 50, 200, 100, GradientVertical,
		Stop(0, RGB(255, 0, 0)), Stop(1, RGB(0, 0, 255))); err != nil {
		t.Fatal(err)
	}
	out := docBytes(t, doc)
	verifyXref(t, out)
	for _, want := range []string{
		"/ShadingType 2", "/ColorSpace /DeviceRGB", "/Extend [true true]",
		"/FunctionType 2", "/Sh1 sh", "/Shading <<",
	} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("output missing %q", want)
		}
	}
	// The gradient must be clipped to the rectangle.
	if !bytes.Contains(out, []byte("re W n")) {
		t.Error("gradient not clipped to its rectangle")
	}
	if _, err := NewReader(out); err != nil {
		t.Fatal(err)
	}
}

func TestGradientRadial(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	if err := page.FillGradientCircle(150, 150, 60,
		Stop(0, White), Stop(1, RGB(20, 40, 120))); err != nil {
		t.Fatal(err)
	}
	out := docBytes(t, doc)
	if !bytes.Contains(out, []byte("/ShadingType 3")) {
		t.Error("no radial shading emitted")
	}
	// Six coordinates: concentric centres, then the inner and outer radii.
	// The centre y is flipped into PDF space: 841.89 - 150.
	if !bytes.Contains(out, []byte("/Coords [150 691.89 0 150 691.89 60]")) {
		t.Errorf("unexpected radial coordinates in:\n%s", firstShading(out))
	}
	if _, err := NewReader(out); err != nil {
		t.Fatal(err)
	}
}

func firstShading(out []byte) string {
	i := bytes.Index(out, []byte("/ShadingType"))
	if i < 0 {
		return "(none)"
	}
	end := i + 160
	if end > len(out) {
		end = len(out)
	}
	return string(out[i:end])
}

func TestGradientMultipleStops(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	err := page.FillGradientRect(0, 0, 300, 60, GradientHorizontal,
		Stop(0, RGB(255, 0, 0)),
		Stop(0.5, RGB(255, 255, 0)),
		Stop(1, RGB(0, 128, 0)))
	if err != nil {
		t.Fatal(err)
	}
	out := docBytes(t, doc)
	// Three stops need a stitching function over two sub-functions.
	if !bytes.Contains(out, []byte("/FunctionType 3")) {
		t.Error("multi-stop gradient did not use a stitching function")
	}
	if !bytes.Contains(out, []byte("/Bounds [0.5]")) {
		t.Errorf("unexpected bounds: %s", firstFunction(out))
	}
	if got := strings.Count(string(out), "/FunctionType 2"); got != 2 {
		t.Errorf("%d sub-functions, want 2", got)
	}
}

func firstFunction(out []byte) string {
	i := bytes.Index(out, []byte("/FunctionType 3"))
	if i < 0 {
		return "(none)"
	}
	end := i + 240
	if end > len(out) {
		end = len(out)
	}
	return string(out[i:end])
}

func TestGradientStopNormalization(t *testing.T) {
	// Out-of-order and partial ranges are repaired, not rejected.
	stops, err := normalizeStops([]GradientStop{
		{0.8, RGB(0, 0, 255)}, {0.2, RGB(255, 0, 0)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stops) != 4 {
		t.Fatalf("got %d stops, want 4 (padded to span 0..1)", len(stops))
	}
	if stops[0].Offset != 0 || stops[len(stops)-1].Offset != 1 {
		t.Errorf("ramp does not span the domain: %+v", stops)
	}
	if stops[0].Color != (Color{255, 0, 0}) {
		t.Errorf("stops were not sorted: %+v", stops)
	}
	// Clamping.
	stops, _ = normalizeStops([]GradientStop{{-1, Black}, {5, White}})
	if stops[0].Offset != 0 || stops[1].Offset != 1 {
		t.Errorf("offsets not clamped: %+v", stops)
	}
}

// TestClipPathOrdering guards a bug that let a gradient escape its clip:
// the clip operator must consume the path, so it has to be emitted with
// the path rather than after a paint operator has already ended it.
func TestClipPathOrdering(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.Push()
	page.Circle(100, 100, 40, ClipPath)
	page.Pop()
	page.Rect(10, 10, 20, 20, ClipPath)

	out := string(docBytes(t, doc))
	// The curve that closes the circle must be followed directly by the
	// clip operator, with no intervening paint.
	if !strings.Contains(out, "c W n") {
		t.Errorf("circle clip is not attached to its path:\n%s", out)
	}
	if !strings.Contains(out, "re W n") {
		t.Error("rectangle clip is not attached to its path")
	}
	// A stray path-ending operator before W would silently void the clip.
	if strings.Contains(out, "c n\n") || strings.Contains(out, "re n\n") {
		t.Error("path was ended before the clip operator")
	}
}

// TestGradientStaysInsideClip renders nothing outside the clipped shape:
// the shading operator must sit between the clip and the matching Q.
func TestGradientStaysInsideClip(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	if err := page.FillGradientCircle(150, 150, 60, Stop(0, White), Stop(1, Black)); err != nil {
		t.Fatal(err)
	}
	out := string(docBytes(t, doc))
	clip := strings.Index(out, "W n")
	paint := strings.Index(out, " sh")
	restore := strings.Index(out[paint:], "Q")
	if clip < 0 || paint < 0 || restore < 0 {
		t.Fatalf("expected clip, shading and restore operators:\n%s", out)
	}
	if clip > paint {
		t.Error("the gradient is painted before the clip is established")
	}
}

func TestGradientValidation(t *testing.T) {
	doc := New()
	page := doc.AddPage()
	if err := page.FillGradientRect(0, 0, 10, 10, GradientVertical); err == nil {
		t.Error("accepted a gradient with no stops")
	}
	if err := page.FillGradientRect(0, 0, 10, 10, GradientVertical, Stop(0, Black)); err == nil {
		t.Error("accepted a gradient with one stop")
	}
	if err := page.PaintRadialGradient(0, 0, 0, 0, Stop(0, Black), Stop(1, White)); err == nil {
		t.Error("accepted a zero outer radius")
	}
}

// TestGradientOnClippedPath paints a gradient through an arbitrary path
// clip rather than a rectangle.
func TestGradientOnClippedPath(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.Push()
	page.MoveTo(100, 100)
	page.LineTo(200, 100)
	page.LineTo(150, 200)
	page.ClosePath()
	page.Clip(false)
	if err := page.PaintLinearGradient(100, 100, 150, 200,
		Stop(0, RGB(250, 200, 0)), Stop(1, RGB(200, 0, 80))); err != nil {
		t.Fatal(err)
	}
	page.Pop()
	out := docBytes(t, doc)
	if !bytes.Contains(out, []byte("h\nW n")) && !bytes.Contains(out, []byte("W n")) {
		t.Error("path clip not emitted")
	}
	if _, err := NewReader(out); err != nil {
		t.Fatal(err)
	}
}
