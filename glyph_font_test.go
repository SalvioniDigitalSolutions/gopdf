package gopdf

import (
	"bytes"
	"os"
	"testing"
)

// The way from a character code to a glyph goes differently for each kind
// of font, and the branches were only ever exercised end to end through
// rendering — where a wrong answer looks like a slightly wrong letter and
// nothing fails. They are driven directly here.

func TestNamedGlyphFromPostTable(t *testing.T) {
	data, err := os.ReadFile(testFontPath(t))
	if err != nil {
		t.Fatal(err)
	}
	f, err := parseTTF(data)
	if err != nil {
		t.Fatal(err)
	}
	g := &glyphFont{ttf: f}

	// A name the font's own post table spells out.
	if len(f.glyphNames) > 0 {
		for name, want := range f.glyphNames {
			got, ok := g.namedGlyph(name)
			if !ok || got != want {
				t.Errorf("the font's own name %q resolved to %d (%v), want %d",
					name, got, ok, want)
			}
			break
		}
	}
	// A name nothing knows.
	if _, ok := g.namedGlyph("definitelyNotAGlyphName"); ok {
		t.Error("an unknown name resolved to a glyph")
	}
}

// TestNamedGlyphByNumber covers the convention a subsetter uses when it
// names a glyph after its own index — which is what "g3" means, and the
// case that had a Calibri subset drawing the wrong letters.
func TestNamedGlyphByNumber(t *testing.T) {
	g := &glyphFont{}
	for _, c := range []struct {
		name string
		want uint16
		ok   bool
	}{
		{"g3", 3, true},
		{"g0", 0, true},
		{"glyph12", 12, true},
		{"index7", 7, true},
		{"cid44", 44, true},
		{"G9", 9, true},
		{"g65535", 65535, true},
		// Not the convention, or out of range.
		{"g65536", 0, false},
		{"g-1", 0, false},
		{"g", 0, false},
		{"gx", 0, false},
		{"A", 0, false},
		{"space", 0, false},
		{"", 0, false},
	} {
		got, ok := g.namedGlyph(c.name)
		if ok != c.ok {
			t.Errorf("namedGlyph(%q) resolved = %v, want %v", c.name, ok, c.ok)
			continue
		}
		if ok && got != c.want {
			t.Errorf("namedGlyph(%q) = %d, want %d", c.name, got, c.want)
		}
	}
	// The font's own table wins over the convention, since a font that
	// names a glyph is telling you which one it means.
	g2 := &glyphFont{ttf: &ttfFont{glyphNames: map[string]uint16{"g3": 99}}}
	if got, ok := g2.namedGlyph("g3"); !ok || got != 99 {
		t.Errorf("the post table did not win: got %d (%v), want 99", got, ok)
	}
}

func TestEncodingNames(t *testing.T) {
	doc := New()
	doc.Compress = false
	doc.AddPage()
	r := NewReaderOrFail(t, docBytes(t, doc))

	// A /Differences array: a starting code, then names, then a new code.
	names := encodingNames(r, Dict{"Differences": Array{
		int64(1), Name("g3"), Name("g4"),
		int64(65), Name("A"),
		int64(200), Name("eacute"),
	}})
	for code, want := range map[uint32]string{
		1: "g3", 2: "g4", 65: "A", 200: "eacute",
	} {
		if names[code] != want {
			t.Errorf("code %d is named %q, want %q", code, names[code], want)
		}
	}
	if len(names) != 4 {
		t.Errorf("%d names, want 4: %v", len(names), names)
	}
	// A base encoding with no differences names nothing.
	if got := encodingNames(r, Name("WinAnsiEncoding")); got != nil {
		t.Errorf("a base encoding produced names: %v", got)
	}
	if got := encodingNames(r, Dict{}); got != nil {
		t.Errorf("an encoding with no differences produced names: %v", got)
	}
	if got := encodingNames(r, nil); got != nil {
		t.Errorf("no encoding produced names: %v", got)
	}
}

func TestGIDForACompositeFont(t *testing.T) {
	// A composite font with no map: the code is the glyph.
	g := &glyphFont{cid: true, ttf: &ttfFont{numGlyphs: 100}}
	if got, ok := g.gid(42); !ok || got != 42 {
		t.Errorf("identity mapping gave %d (%v), want 42", got, ok)
	}

	// With a /CIDToGIDMap the map decides.
	g.cidToGID = []uint16{0, 7, 9, 11}
	for code, want := range map[uint32]uint16{0: 0, 1: 7, 2: 9, 3: 11} {
		if got, ok := g.gid(code); !ok || got != want {
			t.Errorf("code %d mapped to %d (%v), want %d", code, got, ok, want)
		}
	}
	// A code past the end of the map has no glyph, rather than a wrong
	// one.
	if _, ok := g.gid(4); ok {
		t.Error("a code past the end of the map resolved to a glyph")
	}

	// A CID-keyed CFF finds the glyph carrying the identifier.
	g2 := &glyphFont{cid: true, cff: &cffOutlines{
		charstrings: make([][]byte, 4),
		charsetCIDs: []uint16{0, 100, 200, 300},
	}}
	for cid, want := range map[uint32]uint16{100: 1, 200: 2, 300: 3} {
		if got, ok := g2.gid(cid); !ok || got != want {
			t.Errorf("CID %d led to glyph %d (%v), want %d", cid, got, ok, want)
		}
	}
	if _, ok := g2.gid(999); ok {
		t.Error("a CID the font does not carry resolved to a glyph")
	}
}

func TestGIDForASimpleFont(t *testing.T) {
	// A text font: the code becomes a character through the encoding, and
	// the character is looked up in the font's map.
	enc := &[256]rune{}
	enc['A'] = 'A'
	enc[200] = 'é'
	g := &glyphFont{
		ttf:      &ttfFont{numGlyphs: 50, cmap: map[rune]uint16{'A': 11, 'é': 22}},
		encoding: enc,
	}
	if got, ok := g.gid('A'); !ok || got != 11 {
		t.Errorf("A resolved to %d (%v), want 11", got, ok)
	}
	if got, ok := g.gid(200); !ok || got != 22 {
		t.Errorf("code 200 resolved to %d (%v), want 22 through the encoding", got, ok)
	}
	// A code the map does not mention has no glyph. Using it as an index
	// would draw some other letter, which is worse than drawing none.
	if _, ok := g.gid('Z'); ok {
		t.Error("a code the font's map does not mention resolved to a glyph")
	}

	// A symbol font's map is keyed at F000, and fonts disagree about
	// whether the plain code is there too.
	sym := &glyphFont{
		symbolic: true,
		ttf:      &ttfFont{numGlyphs: 50, cmap: map[rune]uint16{0xF041: 5}},
	}
	if got, ok := sym.gid('A'); !ok || got != 5 {
		t.Errorf("a symbol font resolved A to %d (%v), want 5 at F041", got, ok)
	}

	// A name the document gave wins over everything, because it names
	// the glyph rather than describing it.
	named := &glyphFont{
		ttf:      &ttfFont{numGlyphs: 50, cmap: map[rune]uint16{'A': 11}},
		encoding: enc,
		names:    map[uint32]string{'A': "g33"},
	}
	if got, ok := named.gid('A'); !ok || got != 33 {
		t.Errorf("the /Differences name gave %d (%v), want glyph 33", got, ok)
	}
}

// TestGIDWithNoCharacterMap: a subsetter that wrote no map meant the code
// to be the glyph index, and that is what viewers do.
func TestGIDWithNoCharacterMap(t *testing.T) {
	g := &glyphFont{ttf: &ttfFont{numGlyphs: 20}}
	if got, ok := g.gid(7); !ok || got != 7 {
		t.Errorf("with no map, code 7 gave %d (%v), want glyph 7", got, ok)
	}
	// Past the end of the font there is no glyph.
	if _, ok := g.gid(20); ok {
		t.Error("a code past the last glyph resolved to one")
	}
	// A bare CFF simple font is addressed by name, which needs the
	// built-in encodings this package does not carry.
	bare := &glyphFont{cff: &cffOutlines{charstrings: make([][]byte, 10)}}
	if _, ok := bare.gid(5); ok {
		t.Error("a bare CFF simple font resolved a code without a name")
	}
	if bare.addressable() {
		t.Error("a bare CFF simple font reports itself as addressable")
	}
}

// TestGIDForASubstitutedFont: a stand-in knows nothing of the original's
// glyph numbering, so the code has to become a character first.
func TestGIDForASubstitutedFont(t *testing.T) {
	cmap := map[rune]uint16{'A': 3, 'é': 4, '€': 5}

	// Through /ToUnicode, which is the only route for a composite font.
	cid := &glyphFont{
		cid: true, substituted: true,
		ttf:       &ttfFont{numGlyphs: 50, cmap: cmap},
		toUnicode: map[uint32]string{1: "A", 2: "é"},
	}
	if got, ok := cid.gid(1); !ok || got != 3 {
		t.Errorf("code 1 gave %d (%v), want glyph 3 through ToUnicode", got, ok)
	}
	// A code the map does not cover means nothing in the substitute.
	if _, ok := cid.gid(9); ok {
		t.Error("a code with no ToUnicode entry resolved in a substitute")
	}

	// Through the encoding, for a simple font.
	enc := &[256]rune{}
	enc[65] = 'A'
	simple := &glyphFont{
		substituted: true,
		ttf:         &ttfFont{numGlyphs: 50, cmap: cmap},
		encoding:    enc,
	}
	if got, ok := simple.gid(65); !ok || got != 3 {
		t.Errorf("code 65 gave %d (%v), want glyph 3 through the encoding", got, ok)
	}
	// A simple font falls back to the code as a character.
	plain := &glyphFont{
		substituted: true,
		ttf:         &ttfFont{numGlyphs: 50, cmap: cmap},
	}
	if got, ok := plain.gid('A'); !ok || got != 3 {
		t.Errorf("code A gave %d (%v), want glyph 3 as a character", got, ok)
	}
	// And a character the substitute does not have.
	if _, ok := plain.gid('Z'); ok {
		t.Error("a character the substitute lacks resolved to a glyph")
	}
}

// TestLoadCIDToGIDFromAStream covers the map given as data rather than as
// the name Identity.
func TestLoadCIDToGIDFromAStream(t *testing.T) {
	doc := New()
	doc.Compress = false
	doc.AddPage()
	r := NewReaderOrFail(t, docBytes(t, doc))
	u := Update(r)
	// Two bytes per CID, big-endian: CID 0 to glyph 0, 1 to 5, 2 to 9.
	stm := u.AddObject(NewStream(Dict{}, []byte{0, 0, 0, 5, 0, 9}))
	var buf bytes.Buffer
	if err := u.SetCatalogEntry("Lang", String("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	out := NewReaderOrFail(t, buf.Bytes())

	g := &glyphFont{cid: true}
	g.loadCIDToGID(out, Dict{"CIDToGIDMap": stm})
	if len(g.cidToGID) != 3 {
		t.Fatalf("the map has %d entries, want 3: %v", len(g.cidToGID), g.cidToGID)
	}
	for cid, want := range map[uint32]uint16{0: 0, 1: 5, 2: 9} {
		got, ok := g.gid(cid)
		if !ok || got != want {
			t.Errorf("CID %d gave %d (%v), want %d", cid, got, ok, want)
		}
	}

	// The name Identity leaves the map absent, which means the code is
	// the glyph.
	g2 := &glyphFont{cid: true}
	g2.loadCIDToGID(out, Dict{"CIDToGIDMap": Name("Identity")})
	if g2.cidToGID != nil {
		t.Errorf("Identity produced a map: %v", g2.cidToGID)
	}
}

func TestGlyphOutlineSource(t *testing.T) {
	// A font with neither kind of outline has none to give.
	if (&glyphFont{}).outline(0) != nil {
		t.Error("a font with no program produced an outline")
	}
	// CFF outlines are preferred where both are present, since an
	// OpenType font's shapes are in its CFF table and its glyf is empty.
	data, err := os.ReadFile(testOTFPath(t))
	if err != nil {
		t.Fatal(err)
	}
	f, err := parseTTF(data)
	if err != nil || !f.cff {
		t.Skip("no CFF-based font available")
	}
	cff, err := parseCFFOutlines(f.tables["CFF "])
	if err != nil {
		t.Fatal(err)
	}
	g := &glyphFont{ttf: f, cff: cff}
	gid := f.cmap['H']
	if gid == 0 {
		t.Skip("the font has no H")
	}
	if g.outline(gid) == nil {
		t.Error("an OpenType font's outline came back empty")
	}
}
