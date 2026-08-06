package gopdf

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// testFontPath returns a TrueType font available on the host, or skips.
func testFontPath(t *testing.T) string {
	t.Helper()
	candidates := []string{
		"/System/Library/Fonts/Supplemental/Arial.ttf",
		"/System/Library/Fonts/Supplemental/Verdana.ttf",
		"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
		"C:\\Windows\\Fonts\\arial.ttf",
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("no test font available on this system")
	return ""
}

// testOTFPath returns a CFF-based OpenType font available on the host.
func testOTFPath(t *testing.T) string {
	t.Helper()
	for _, p := range []string{
		"/System/Library/Fonts/Supplemental/STIXGeneral.otf",
		"/usr/share/fonts/opentype/urw-base35/NimbusRoman-Regular.otf",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("no OpenType/CFF font available on this system")
	return ""
}

// TestOpenTypeCFF covers fonts with PostScript outlines, which carry
// their glyphs in a CFF table instead of glyf/loca.
func TestOpenTypeCFF(t *testing.T) {
	font, err := LoadFont(testOTFPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if !font.ttf.cff {
		t.Fatal("font was not recognised as CFF-based")
	}
	if font.ttf.numGlyphs < 100 {
		t.Errorf("suspiciously few glyphs: %d", font.ttf.numGlyphs)
	}
	if font.ttf.cmap['A'] == 0 {
		t.Error("no glyph for 'A'")
	}
	// Metrics still come from hmtx, so measurement works.
	if font.TextWidth("Handgloves", 12) <= 0 {
		t.Error("TextWidth returned nothing")
	}
	if font.TextWidth("W", 12) <= font.TextWidth("i", 12) {
		t.Error("glyph widths look wrong")
	}

	doc := New()
	page := doc.AddPage()
	page.SetFont(font, 14)
	page.Text(60, 80, "OpenType CFF — àéîöü")

	out := docBytes(t, doc)
	verifyXref(t, out)
	for _, want := range []string{
		"/Subtype /CIDFontType0", // CFF outlines, not TrueType
		"/FontFile3",
		"/Subtype /OpenType",
		"/Encoding /Identity-H",
		"/ToUnicode",
	} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("output missing %q", want)
		}
	}
	// CIDToGIDMap applies only to TrueType-outlined CID fonts.
	if bytes.Contains(out, []byte("/CIDToGIDMap")) {
		t.Error("CIDToGIDMap must not be written for a CFF font")
	}

	r, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	text, err := r.PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "OpenType CFF — àéîöü") {
		t.Errorf("extracted %q", text)
	}
}

// TestOpenTypeCFFNotSubset documents the current trade-off: CFF programs
// are embedded whole, because there is no charstring subsetter yet.
func TestOpenTypeCFFNotSubset(t *testing.T) {
	font, err := LoadFont(testOTFPath(t))
	if err != nil {
		t.Fatal(err)
	}
	sub, err := font.ttf.subset(map[uint16]bool{font.ttf.cmap['A']: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(sub) != len(font.ttf.program) {
		t.Errorf("CFF subset is %d bytes, expected the whole %d-byte program",
			len(sub), len(font.ttf.program))
	}
	// The embedded program must still parse as the same font.
	again, err := parseTTF(sub)
	if err != nil {
		t.Fatalf("embedded CFF program does not reparse: %v", err)
	}
	if again.numGlyphs != font.ttf.numGlyphs {
		t.Error("reparsed CFF font has a different glyph count")
	}
}

func TestParseTTF(t *testing.T) {
	font, err := LoadFont(testFontPath(t))
	if err != nil {
		t.Fatal(err)
	}
	ttf := font.ttf
	if ttf.unitsPerEm != 1000 && ttf.unitsPerEm != 2048 {
		t.Errorf("unexpected unitsPerEm %d", ttf.unitsPerEm)
	}
	if ttf.numGlyphs < 100 {
		t.Errorf("suspiciously few glyphs: %d", ttf.numGlyphs)
	}
	if gid := ttf.cmap['A']; gid == 0 {
		t.Error("no glyph for 'A'")
	}
	if font.Name() == "" || font.Name() == "Embedded" {
		t.Errorf("PostScript name not parsed, got %q", font.Name())
	}
	// 'A' should be wider than 'i' in any proportional font.
	if font.TextWidth("A", 12) <= font.TextWidth("i", 12) {
		t.Error("glyph widths look wrong")
	}
	if font.TextWidth("Hello", 12) <= 0 {
		t.Error("TextWidth must be positive")
	}
}

func TestSubsetIsValidFont(t *testing.T) {
	font, err := LoadFont(testFontPath(t))
	if err != nil {
		t.Fatal(err)
	}
	used := map[uint16]bool{}
	for _, r := range "Hello, Wörld! ééé" {
		used[font.ttf.cmap[r]] = true
	}
	sub, err := font.ttf.subset(used)
	if err != nil {
		t.Fatal(err)
	}
	if len(sub) >= len(font.ttf.tables["glyf"])+4096 {
		t.Errorf("subset (%d bytes) not smaller than original glyf table (%d bytes)",
			len(sub), len(font.ttf.tables["glyf"]))
	}
	// The subset must itself parse as a TrueType font with identical
	// glyph IDs and metrics.
	reparsed, err := parseTTF(sub)
	if err != nil {
		t.Fatalf("subset does not reparse: %v", err)
	}
	if reparsed.numGlyphs != font.ttf.numGlyphs {
		t.Errorf("subset numGlyphs = %d, want %d", reparsed.numGlyphs, font.ttf.numGlyphs)
	}
	for gid := range used {
		orig := font.ttf.glyphData(gid)
		got := reparsed.glyphData(gid)
		if len(got) < len(orig) || !bytes.Equal(got[:len(orig)], orig) {
			t.Errorf("glyph %d data differs in subset", gid)
		}
		if reparsed.advances[gid] != font.ttf.advances[gid] {
			t.Errorf("glyph %d advance differs in subset", gid)
		}
	}
	// An unused glyph must be empty.
	unused := font.ttf.cmap['Z']
	if used[unused] {
		t.Fatal("test assumption broken: Z is in the used set")
	}
	if len(reparsed.glyphData(unused)) != 0 {
		t.Error("unused glyph kept its outline data")
	}
}

func TestEmbeddedFontDocument(t *testing.T) {
	font, err := LoadFont(testFontPath(t))
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	page := doc.AddPage()
	page.SetFont(font, 14)
	page.Text(72, 72, "Unicode: Ωмир — čeština")

	var buf bytes.Buffer
	if _, err := doc.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"/Subtype /Type0", "/Encoding /Identity-H", "/Subtype /CIDFontType2",
		"/FontFile2", "/ToUnicode", "/CIDToGIDMap /Identity", "+" + font.Name(),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}
}

func TestEmbeddedFontUnusedStillValid(t *testing.T) {
	// A registered but never-used embedded font must still serialize.
	font, err := LoadFont(testFontPath(t))
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	page := doc.AddPage()
	page.SetFont(font, 14)
	page.SetFont(Helvetica, 10)
	page.Text(72, 72, "standard font only")
	var buf bytes.Buffer
	if _, err := doc.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
}

func TestFeatureDocument(t *testing.T) {
	doc := New()
	p1 := doc.AddPage()
	p2 := doc.AddPage()

	p1.SetFont(Helvetica, 12)
	p1.Push()
	p1.SetAlpha(0.5, 1)
	p1.Rect(10, 10, 50, 50, Fill)
	p1.Pop()
	p1.RoundedRect(10, 80, 60, 30, 8, Stroke)
	p1.Push()
	p1.ClipRect(0, 0, 100, 100)
	p1.Circle(100, 100, 30, Fill)
	p1.Pop()
	p1.LinkURL(10, 10, 50, 20, "https://example.com/a(b)")
	p1.LinkPage(10, 40, 50, 20, p2, 200)

	ch := doc.AddOutline(nil, "Chapter 1", p1, 0)
	doc.AddOutline(ch, "Section 1.1", p2, 100)
	doc.AddOutline(nil, "Chapter 2", p2, 0)

	var buf bytes.Buffer
	if _, err := doc.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"/ExtGState", "/ca 0.5 /CA 1",
		"/Subtype /Link", `/URI (https://example.com/a\(b\))`,
		"/Type /Outlines", "(Chapter 1)", "(Section 1.1)", "/Count 3",
		"/PageMode /UseOutlines",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}

	// The xref must still be self-consistent with annots and outlines.
	verifyXref(t, buf.Bytes())
}

func TestTextWrapped(t *testing.T) {
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Courier, 10) // 6pt per character at size 10
	// 13 chars fit per 80pt line: "aaaa bbbb" stays, "cccc" wraps.
	y := page.TextWrapped(10, 100, 80, 14, "aaaa bbbb cccc dddd\n\nlast")
	// Lines: "aaaa bbbb", "cccc dddd", blank, "last".
	if want := 100.0 + 4*14; y != want {
		t.Errorf("next baseline = %v, want %v", y, want)
	}
	var buf bytes.Buffer
	if _, err := doc.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"(aaaa bbbb) Tj", "(cccc dddd) Tj", "(last) Tj"} {
		if !strings.Contains(out, want) {
			t.Errorf("content missing %q", want)
		}
	}
}
