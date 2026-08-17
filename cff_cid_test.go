package gopdf

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// testCIDCFFPath returns a CID-keyed CFF font on this host, or skips.
//
// A CID-keyed font is organised by character identifier rather than by
// glyph name, which is what CJK and Indic fonts use and what the plain
// subsetter cannot reduce.
func testCIDCFFPath(t *testing.T) string {
	t.Helper()
	for _, p := range []string{
		"/System/Library/Fonts/Supplemental/NotoSansCanadianAboriginal-Regular.otf",
		"/System/Library/Fonts/KohinoorGujarati.ttc",
		"/System/Library/Fonts/AppleSDGothicNeo.ttc",
		"/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
	} {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		f, err := parseTTF(data)
		if err != nil || !f.cff {
			continue
		}
		if isCIDKeyedCFF(f.tables["CFF "]) {
			return p
		}
	}
	t.Skip("no CID-keyed CFF font available on this system")
	return ""
}

// isCIDKeyedCFF reports whether a CFF table is CID-keyed.
func isCIDKeyedCFF(cff []byte) bool {
	if len(cff) < 4 {
		return false
	}
	hdrSize := int(cff[2])
	nameIdx, err := parseCFFIndex(cff, hdrSize)
	if err != nil {
		return false
	}
	topIdx, err := parseCFFIndex(cff, nameIdx.end)
	if err != nil || len(topIdx.items) == 0 {
		return false
	}
	top, err := parseCFFDict(topIdx.items[0])
	if err != nil {
		return false
	}
	return dictEntry(top, cffOpROS) != nil || dictEntry(top, cffOpFDArray) != nil
}

func TestSubsetCIDKeyedCFF(t *testing.T) {
	path := testCIDCFFPath(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := parseTTF(data)
	if err != nil {
		t.Fatal(err)
	}
	cff := f.tables["CFF "]
	if len(cff) == 0 {
		t.Fatal("no CFF table")
	}

	// Keep a handful of glyphs, as a document using a few characters of
	// a large font does.
	keep := map[uint16]bool{}
	for _, r := range "abcABC123" {
		if gid := f.cmap[r]; gid != 0 {
			keep[gid] = true
		}
	}
	if len(keep) < 3 {
		// A font with no Latin still has glyphs; take some by index.
		for gid := uint16(1); gid < 12 && int(gid) < f.numGlyphs; gid++ {
			keep[gid] = true
		}
	}

	out, err := subsetCFF(cff, keep, f.numGlyphs)
	if err != nil {
		t.Fatalf("subsetting %s: %v", path, err)
	}
	if len(out) == 0 {
		t.Fatal("the subset is empty")
	}
	// The point of the exercise: a font of a dozen glyphs should not
	// carry the whole original.
	if len(out) >= len(cff) {
		t.Errorf("the subset is %d bytes and the original %d; nothing was saved",
			len(out), len(cff))
	}
	t.Logf("%s: %d bytes reduced to %d (%.1f%% smaller)", path,
		len(cff), len(out), 100*(1-float64(len(out))/float64(len(cff))))

	// It must still be a CID-keyed CFF, and still readable.
	if !isCIDKeyedCFF(out) {
		t.Error("the subset is no longer CID-keyed")
	}
	sub, err := parseCFFOutlines(out)
	if err != nil {
		t.Fatalf("the subset does not parse: %v", err)
	}
	if sub.numGlyphs() != f.numGlyphs {
		t.Errorf("the subset has %d glyphs, want the original's %d — glyph "+
			"numbers have to be preserved for the document to reach them",
			sub.numGlyphs(), f.numGlyphs)
	}

	// Every glyph that was kept must still draw the same outline, and
	// the ones dropped must draw nothing.
	orig, err := parseCFFOutlines(cff)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for gid := range keep {
		want, got := orig.outline(gid), sub.outline(gid)
		if want == nil {
			continue
		}
		if got == nil {
			t.Fatalf("glyph %d lost its outline in the subset", gid)
		}
		if len(got.contours) != len(want.contours) {
			t.Fatalf("glyph %d has %d contours in the subset and %d before",
				gid, len(got.contours), len(want.contours))
		}
		wx0, wy0, wx1, wy1 := outlineBounds(want)
		gx0, gy0, gx1, gy1 := outlineBounds(got)
		if wx0 != gx0 || wy0 != gy0 || wx1 != gx1 || wy1 != gy1 {
			t.Fatalf("glyph %d changed shape: %v,%v,%v,%v became %v,%v,%v,%v",
				gid, wx0, wy0, wx1, wy1, gx0, gy0, gx1, gy1)
		}
		checked++
	}
	if checked == 0 {
		t.Error("no kept glyph had an outline to compare")
	}
	// A glyph nothing kept draws nothing.
	dropped := 0
	for gid := uint16(1); int(gid) < f.numGlyphs && dropped < 5; gid++ {
		if keep[gid] || orig.outline(gid) == nil {
			continue
		}
		if sub.outline(gid) != nil {
			t.Errorf("glyph %d was not dropped", gid)
		}
		dropped++
	}
}

// TestSubsetCIDKeyedCFFKeepsFDSelect: a font covering several scripts
// hints them through different private dictionaries, and which glyph
// uses which has to survive or the hinting is applied to the wrong
// glyphs.
func TestSubsetCIDKeyedCFFKeepsFDSelect(t *testing.T) {
	path := testCIDCFFPath(t)
	data, _ := os.ReadFile(path)
	f, err := parseTTF(data)
	if err != nil {
		t.Fatal(err)
	}
	orig, err := parseCFFOutlines(f.tables["CFF "])
	if err != nil {
		t.Fatal(err)
	}
	if orig.fdSelect == nil {
		t.Skip("this font has a single private dictionary")
	}

	keep := map[uint16]bool{}
	// Take glyphs from as many different dictionaries as the font has.
	seen := map[uint8]bool{}
	for gid, fd := range orig.fdSelect {
		if !seen[fd] {
			seen[fd] = true
			keep[uint16(gid)] = true
		}
	}
	out, err := subsetCFF(f.tables["CFF "], keep, f.numGlyphs)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := parseCFFOutlines(out)
	if err != nil {
		t.Fatal(err)
	}
	if sub.fdSelect == nil {
		t.Fatal("the subset lost its glyph-to-dictionary map")
	}
	if len(sub.fdSelect) != len(orig.fdSelect) {
		t.Fatalf("the map covers %d glyphs, want %d",
			len(sub.fdSelect), len(orig.fdSelect))
	}
	for gid := range orig.fdSelect {
		if sub.fdSelect[gid] != orig.fdSelect[gid] {
			t.Fatalf("glyph %d moved from dictionary %d to %d",
				gid, orig.fdSelect[gid], sub.fdSelect[gid])
		}
	}
	if len(sub.fdSubrs) != len(orig.fdSubrs) {
		t.Errorf("the subset has %d font dictionaries, want %d",
			len(sub.fdSubrs), len(orig.fdSubrs))
	}
	t.Logf("%d dictionaries and %d glyphs preserved", len(sub.fdSubrs), len(sub.fdSelect))
}

// TestSubsetCIDKeyedCFFKeepsCIDs: the charset of a CID-keyed font is the
// character identifier of each glyph, and a document asks for glyphs by
// that number. Pruning it as if it held names would make the glyphs
// unreachable.
func TestSubsetCIDKeyedCFFKeepsCIDs(t *testing.T) {
	path := testCIDCFFPath(t)
	data, _ := os.ReadFile(path)
	f, _ := parseTTF(data)
	orig, err := parseCFFOutlines(f.tables["CFF "])
	if err != nil {
		t.Fatal(err)
	}
	if orig.charsetCIDs == nil {
		t.Skip("this font uses an identity charset")
	}
	keep := map[uint16]bool{1: true, 2: true, 3: true}
	out, err := subsetCFF(f.tables["CFF "], keep, f.numGlyphs)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := parseCFFOutlines(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(sub.charsetCIDs) != len(orig.charsetCIDs) {
		t.Fatalf("the charset covers %d glyphs, want %d",
			len(sub.charsetCIDs), len(orig.charsetCIDs))
	}
	for gid := range orig.charsetCIDs {
		if sub.charsetCIDs[gid] != orig.charsetCIDs[gid] {
			t.Fatalf("glyph %d carried CID %d and now carries %d", gid,
				orig.charsetCIDs[gid], sub.charsetCIDs[gid])
		}
	}
	// And the CID a document would ask for still finds its glyph.
	for gid := uint16(1); gid <= 3; gid++ {
		cid := orig.charsetCIDs[gid]
		got, ok := sub.gidForCID(cid)
		if !ok || got != gid {
			t.Errorf("CID %d led to glyph %d (%v), want %d", cid, got, ok, gid)
		}
	}
}

func TestBuildFDSelect(t *testing.T) {
	// A map of runs comes back as the same map.
	for _, in := range [][]uint8{
		{0},
		{0, 0, 0},
		{0, 1, 1, 2, 2, 2},
		{3, 3, 0, 0, 1},
	} {
		data := buildFDSelect(in)
		got := parseFDSelect(data, 0, len(in))
		if got == nil {
			t.Fatalf("%v did not read back", in)
		}
		for i := range in {
			if got[i] != in[i] {
				t.Fatalf("%v read back as %v", in, got)
			}
		}
	}
	if buildFDSelect(nil) != nil {
		t.Error("an empty map produced a table")
	}
}

// TestSubsetCIDCFFInADocument embeds a CID-keyed font in a real document
// and checks the result is a document.
func TestSubsetCIDCFFInADocument(t *testing.T) {
	path := testCIDCFFPath(t)
	font, err := LoadFont(path)
	if err != nil {
		t.Skipf("the font does not load for embedding: %v", err)
	}
	doc := New()
	doc.Compress = false
	p := doc.AddPage()
	p.SetFont(font, 18)
	p.Text(72, 100, "Subset test")
	src := docBytes(t, doc)

	r := NewReaderOrFail(t, src)
	if txt, err := r.PageText(0); err != nil || !strings.Contains(txt, "Subset test") {
		t.Errorf("page text: %q %v", txt, err)
	}
	verifyXref(t, src)

	// The embedded program has to parse as the font it claims to be.
	var found bool
	r.Walk(func(_ Ref, obj any) bool {
		d, ok := obj.(Dict)
		if !ok || d["Type"] != Name("FontDescriptor") {
			return true
		}
		for _, key := range []Name{"FontFile3", "FontFile2"} {
			stm, ok := r.Resolve(d[key]).(*Stream)
			if !ok {
				continue
			}
			prog, err := stm.Data()
			if err != nil {
				t.Errorf("the embedded program does not decode: %v", err)
				return false
			}
			if _, err := parseTTF(prog); err != nil {
				t.Errorf("the embedded program does not parse: %v", err)
			}
			found = true
			// And it should be a fraction of the original.
			if len(prog) >= len(mustRead(t, path)) {
				t.Errorf("the embedded program is %d bytes and the font is %d",
					len(prog), len(mustRead(t, path)))
			}
			t.Logf("embedded %d bytes of a %d-byte font", len(prog), len(mustRead(t, path)))
		}
		return false
	})
	if !found {
		t.Error("no embedded font program was found")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

var _ = bytes.Contains
