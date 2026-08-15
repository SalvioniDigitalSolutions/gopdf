package gopdf

import (
	"math"
	"os"
	"testing"
	"time"
)

// outlineBounds is the box a glyph's contours occupy, in font units.
func outlineBounds(g *glyphOutline) (minX, minY, maxX, maxY float64) {
	minX, minY = math.Inf(1), math.Inf(1)
	maxX, maxY = math.Inf(-1), math.Inf(-1)
	for _, c := range g.contours {
		for _, p := range c.pts {
			minX, maxX = math.Min(minX, p.x), math.Max(maxX, p.x)
			minY, maxY = math.Min(minY, p.y), math.Max(maxY, p.y)
		}
	}
	return
}

func TestTrueTypeOutlines(t *testing.T) {
	data, err := os.ReadFile(testFontPath(t))
	if err != nil {
		t.Fatal(err)
	}
	f, err := parseTTF(data)
	if err != nil {
		t.Fatal(err)
	}
	em := float64(f.unitsPerEm)

	// A capital H is the easiest shape to be sure about: one contour,
	// sitting on the baseline, about a cap height tall.
	gid := f.cmap['H']
	if gid == 0 {
		t.Fatal("the font has no H")
	}
	g := f.outline(gid)
	if g == nil || len(g.contours) != 1 {
		t.Fatalf("H has %v contours, want 1", contourCount(g))
	}
	minX, minY, maxX, maxY := outlineBounds(g)
	if minY < -em*0.02 || minY > em*0.02 {
		t.Errorf("H sits at y=%.0f, not on the baseline", minY)
	}
	if h := maxY - minY; h < em*0.5 || h > em*0.85 {
		t.Errorf("H is %.0f units tall in a %.0f em", h, em)
	}
	if w := maxX - minX; w < em*0.3 || w > em*0.9 {
		t.Errorf("H is %.0f units wide in a %.0f em", w, em)
	}

	// An O has two contours: the outside and the counter inside it.
	if gid := f.cmap['O']; gid != 0 {
		if g := f.outline(gid); g == nil || len(g.contours) != 2 {
			t.Errorf("O has %v contours, want 2 (the bowl and its counter)",
				contourCount(g))
		}
	}
	// A space has no outline at all, which is not a failure.
	if gid := f.cmap[' ']; gid != 0 && f.outline(gid) != nil {
		t.Error("a space has an outline")
	}
	// And a glyph the font does not have must not panic or invent one.
	if f.outline(uint16(f.numGlyphs+50)) != nil {
		t.Error("a glyph past the end of the font produced an outline")
	}
}

// TestCompositeGlyph checks an accented letter, which is stored as
// references to the letter and the accent rather than as its own points.
func TestCompositeGlyph(t *testing.T) {
	data, err := os.ReadFile(testFontPath(t))
	if err != nil {
		t.Fatal(err)
	}
	f, err := parseTTF(data)
	if err != nil {
		t.Fatal(err)
	}
	gid := f.cmap['Ä']
	if gid == 0 {
		t.Skip("the font has no A dieresis")
	}
	if raw := f.glyphData(gid); len(raw) < 10 || int16(be16(raw, 0)) >= 0 {
		t.Skip("A dieresis is not a composite in this font")
	}
	g := f.outline(gid)
	if g == nil {
		t.Fatal("the composite produced no outline")
	}
	plain := f.outline(f.cmap['A'])
	if plain == nil {
		t.Fatal("no A")
	}
	// The accented letter is the letter plus the two dots.
	if len(g.contours) <= len(plain.contours) {
		t.Errorf("A dieresis has %d contours and A has %d; the accent is missing",
			len(g.contours), len(plain.contours))
	}
	_, _, _, plainTop := outlineBounds(plain)
	_, _, _, top := outlineBounds(g)
	if top <= plainTop {
		t.Errorf("A dieresis reaches y=%.0f and A reaches y=%.0f; the accent is not above",
			top, plainTop)
	}
}

func TestCFFOutlines(t *testing.T) {
	data, err := os.ReadFile(testOTFPath(t))
	if err != nil {
		t.Fatal(err)
	}
	f, err := parseTTF(data)
	if err != nil {
		t.Fatal(err)
	}
	if !f.cff {
		t.Skip("this font is not CFF-based")
	}
	c, err := parseCFFOutlines(f.tables["CFF "])
	if err != nil {
		t.Fatal(err)
	}
	if c.numGlyphs() < 100 {
		t.Errorf("the font defines %d glyphs", c.numGlyphs())
	}
	// The font matrix is what turns charstring units into ems, and it is
	// a thousandth for all but a handful of fonts.
	if c.fontMatrix[0] <= 0 || c.fontMatrix[0] > 0.01 {
		t.Errorf("font matrix scale = %g, want about 0.001", c.fontMatrix[0])
	}

	gid := f.cmap['H']
	if gid == 0 {
		t.Fatal("the font has no H")
	}
	g := c.outline(gid)
	if g == nil || len(g.contours) == 0 {
		t.Fatal("H produced no outline")
	}
	minX, minY, maxX, maxY := outlineBounds(g)
	em := 1 / c.fontMatrix[0]
	if minY < -em*0.03 || minY > em*0.03 {
		t.Errorf("H sits at y=%.0f, not on the baseline", minY)
	}
	if h := maxY - minY; h < em*0.5 || h > em*0.85 {
		t.Errorf("H is %.0f units tall in a %.0f em", h, em)
	}
	if w := maxX - minX; w < em*0.3 || w > em*0.9 {
		t.Errorf("H is %.0f units wide", w)
	}
	// The O of a CFF font is two contours too, drawn with curves rather
	// than the quadratics TrueType uses.
	if gid := f.cmap['O']; gid != 0 {
		if g := c.outline(gid); g == nil || len(g.contours) != 2 {
			t.Errorf("O has %v contours, want 2", contourCount(g))
		}
	}
	if c.outline(uint16(c.numGlyphs()+50)) != nil {
		t.Error("a glyph past the end produced an outline")
	}
}

// TestCharstringGarbageIsSurvivable: a charstring is a program, and a
// broken one must stop rather than run away.
func TestCharstringGarbageIsSurvivable(t *testing.T) {
	c := &cffOutlines{fontMatrix: matrix{0.001, 0, 0, 0.001, 0, 0}}
	for _, cs := range [][]byte{
		{},
		{28},                 // a two-byte number with no bytes after it
		{255, 1},             // a five-byte number, truncated
		{12},                 // an escape with no operator
		{10},                 // callsubr with nothing on the stack
		{29},                 // callgsubr with nothing on the stack
		{19, 0xFF, 0xFF},     // a hintmask claiming stems it never declared
		{139, 139, 21, 0x0E}, // rmoveto then a stray byte
	} {
		c.charstrings = [][]byte{cs}
		func() {
			defer func() {
				if e := recover(); e != nil {
					t.Errorf("charstring %v panicked: %v", cs, e)
				}
			}()
			c.outline(0)
		}()
	}
	// A subroutine that calls itself must not run for ever.
	c.charstrings = [][]byte{{139, 10}}
	c.lsubrs = [][]byte{{139, 10}}
	done := make(chan bool, 1)
	go func() {
		c.outline(0)
		done <- true
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a self-calling subroutine did not stop")
	}
}

// TestQuadraticImpliedPoints checks the one thing a TrueType outline
// reader has to get right: two off-curve points in a row imply an
// on-curve point halfway between them.
func TestQuadraticImpliedPoints(t *testing.T) {
	// A square-ish contour of four off-curve points, which describes a
	// circle-like shape entirely out of implied midpoints.
	g := &glyphOutline{contours: []glyphContour{{pts: []glyphPoint{
		{x: 0, y: 100, on: false},
		{x: 100, y: 100, on: false},
		{x: 100, y: 0, on: false},
		{x: 0, y: 0, on: false},
	}}}}
	var p rasterPath
	g.appendTo(&p, matrix{1, 0, 0, 1, 0, 0})
	if len(p.subs) != 1 {
		t.Fatalf("%d subpaths, want 1", len(p.subs))
	}
	minX, minY, maxX, maxY, ok := p.bounds()
	if !ok {
		t.Fatal("the path has no extent")
	}
	// Every point is a control point, so the curve stays inside the box
	// they describe and reaches the midpoints of its sides.
	if minX < -1 || minY < -1 || maxX > 101 || maxY > 101 {
		t.Errorf("the curve leaves its control box: %.1f,%.1f to %.1f,%.1f",
			minX, minY, maxX, maxY)
	}
	if maxX-minX < 40 || maxY-minY < 40 {
		t.Errorf("the curve collapsed: %.1f by %.1f", maxX-minX, maxY-minY)
	}
	if !p.subs[0].closed {
		t.Error("the contour was left open")
	}
}

func TestFamilyOf(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Arial-BoldMT", "Arial"},
		{"BCDEEE+Cambria", "Cambria"},
		{"ABCDEF+Arial-ItalicMT", "Arial"},
		{"Helvetica", "Helvetica"},
		{"TimesNewRomanPSMT", "TimesNewRoman"},
		{"Foo,Bold", "Foo"},
	} {
		if got := familyOf(c.in); got != c.want {
			t.Errorf("familyOf(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestRankFontsPrefersThePlainFace is the bug a bare substring match
// walks into: "Arial Black" contains "arial" and matches a request for
// Arial as well as "Arial" itself, and sorts first.
func TestRankFontsPrefersThePlainFace(t *testing.T) {
	index := []string{
		"/f/Arial Black.ttf",
		"/f/Arial Bold.ttf",
		"/f/Arial Narrow.ttf",
		"/f/Arial.ttf",
		"/f/Zapfino.ttf",
	}
	first := func(req FontRequest) string {
		got := rankFonts(index, req)
		if len(got) == 0 {
			return ""
		}
		return got[0].path
	}
	if got := first(FontRequest{BaseFont: "ArialMT"}); got != "/f/Arial.ttf" {
		t.Errorf("plain Arial resolved to %s", got)
	}
	if got := first(FontRequest{BaseFont: "Arial-BoldMT", Bold: true}); got != "/f/Arial Bold.ttf" {
		t.Errorf("bold Arial resolved to %s", got)
	}
	// And a family nothing matches still returns candidates to try
	// rather than giving up.
	if got := rankFonts(index, FontRequest{BaseFont: "NoSuchFace"}); len(got) == 0 {
		t.Error("no candidates at all for an unknown family")
	}
}

func contourCount(g *glyphOutline) any {
	if g == nil {
		return "no"
	}
	return len(g.contours)
}

// TestSystemFontsIsSafeToShare: the obvious way to render pages in
// parallel is to build one substitution function and hand it to every
// goroutine, so it must survive that.
func TestSystemFontsIsSafeToShare(t *testing.T) {
	if testing.Short() {
		t.Skip("scans the machine's font directories")
	}
	sub := SystemFonts()
	names := []string{"Helvetica", "Times-Roman", "Courier", "Arial-BoldMT"}
	done := make(chan bool, len(names)*4)
	for i := 0; i < len(names)*4; i++ {
		go func(i int) {
			defer func() { done <- true }()
			sub(FontRequest{BaseFont: names[i%len(names)], Bold: i%2 == 0})
		}(i)
	}
	for i := 0; i < len(names)*4; i++ {
		select {
		case <-done:
		case <-time.After(60 * time.Second):
			t.Fatal("a lookup did not finish")
		}
	}
	// And the same request twice must give the same answer.
	a := sub(FontRequest{BaseFont: "Helvetica"})
	b := sub(FontRequest{BaseFont: "Helvetica"})
	if len(a) != len(b) {
		t.Errorf("the same request gave %d bytes then %d", len(a), len(b))
	}
}
