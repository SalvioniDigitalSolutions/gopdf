package gopdf

import (
	"math"
	"strings"
	"testing"
)

// A key-reversible marker is long by construction — "[[PII_LOCATION_001]]"
// against a place name of seven letters — and the default floor of 45%
// will not take it. Refusing to go smaller means the paragraph re-wraps,
// which moves everything below it past whatever is painted at fixed
// coordinates. A caller who would rather have a small marker than a
// moved page says so with MinScale.

const marker = "[[PII_LOCATION_001]]"

// TestMinScaleLetsAMarkerFit is the case the field exists for: a token
// far too long for the default floor, in a paragraph that must not move.
func TestMinScaleLetsAMarkerFit(t *testing.T) {
	src := leaderPage(t, "Locarno")

	// At the default floor the token cannot be brought down far enough,
	// so the paragraph engine takes it and rewrites the page.
	def := fitExact(t, src, []Pseudonym{
		{From: "Locarno", To: marker, FitWidth: true},
	})
	const untouched = "(Davanti a me notaio, oggi.) Tj"
	if bytesHas(pageContent(t, def), untouched) {
		t.Fatal("the default floor took this token, so the fixture cannot " +
			"show what MinScale changes")
	}

	// With room to shrink, the same substitution is spliced in place.
	out := fitExact(t, src, []Pseudonym{
		{From: "Locarno", To: marker, FitWidth: true, MinScale: 0.18},
	})
	if !bytesHas(pageContent(t, out), untouched) {
		t.Errorf("the page was re-laid-out even with MinScale set:\n%s",
			pageContent(t, out))
	}
	before, after := lineYs(t, src), lineYs(t, out)
	if len(after) != len(before) {
		t.Fatalf("the paragraph re-wrapped: %d lines, want %d", len(after), len(before))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("line %d moved from %.4f to %.4f", i, before[i], after[i])
		}
	}
	// And the marker is on the page, whole, for the key to reverse.
	if got := joinFrags(fragsOf(t, out)); !strings.Contains(got, marker) {
		t.Errorf("the marker did not arrive: %q", got)
	}
}

// TestMinScaleSetsTheSize: the token is set at the scale the width
// demands, not at the floor, whenever the width demands more than it.
func TestMinScaleSetsTheSize(t *testing.T) {
	src := leaderPage(t, "Locarno")
	out := fitExact(t, src, []Pseudonym{
		{From: "Locarno", To: marker, FitWidth: true, MinScale: 0.05},
	})
	var tok TextFragment
	body := 0.0
	for _, f := range fragsOf(t, out) {
		if strings.Contains(f.Text, marker) {
			tok = f
		} else if f.FontSize > body {
			body = f.FontSize
		}
	}
	if tok.Text == "" {
		t.Fatalf("the marker is not on the page: %q", joinFrags(fragsOf(t, out)))
	}
	// The ratio of the two widths, which is where the size should land —
	// well above the floor that was allowed.
	want := body * Helvetica.TextWidth("Locarno", body) /
		Helvetica.TextWidth(marker, body)
	if math.Abs(tok.FontSize-want) > 0.05 {
		t.Errorf("marker set at %.3f, want %.3f", tok.FontSize, want)
	}
	if tok.FontSize <= body*0.05 {
		t.Errorf("the marker fell to the floor at %.3f; the floor is a limit, "+
			"not a target", tok.FontSize)
	}
}

func TestPseudonymFloor(t *testing.T) {
	for _, c := range []struct {
		in, want float64
	}{
		{0, fitWidthFloor},    // unset keeps the default
		{-1, fitWidthFloor},   // and so does nonsense
		{0.18, 0.18},          // what a caller asks for
		{0.01, minFitScale},   // but not past the point of legibility
		{1.5, 1},              // and never an enlargement
		{1, 1},                //
		{0.45, fitWidthFloor}, //
	} {
		if got := (Pseudonym{MinScale: c.in}).floor(); got != c.want {
			t.Errorf("MinScale %v gave a floor of %v, want %v", c.in, got, c.want)
		}
	}
}

// TestVariantsCarryTheWholeMapping guards the mistake this was found
// through. A mapping is expanded into the spellings a document might
// have used, and the expansion once named the fields it copied — so
// FitWidth was dropped, and later MinScale, and the engine quietly used
// its defaults for every substitution anyone made.
func TestVariantsCarryTheWholeMapping(t *testing.T) {
	in := Pseudonym{
		From: "Via Franzoni 25", To: marker, FitWidth: true, MinScale: 0.18,
	}
	got := expandAllVariants([]Pseudonym{in})
	if len(got) < 2 {
		t.Fatalf("the mapping expanded to %d spellings, want more than one", len(got))
	}
	for _, v := range got {
		if v.To != in.To || !v.FitWidth || v.MinScale != in.MinScale {
			t.Errorf("the %q spelling lost part of the mapping: %+v", v.From, v)
		}
	}
}

func bytesHas(b []byte, s string) bool { return strings.Contains(string(b), s) }
