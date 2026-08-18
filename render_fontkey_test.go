package gopdf

import (
	"bytes"
	"image"
	"os"
	"strings"
	"testing"
)

// A form XObject carries its own /Font dictionary, and producers reuse
// the same short names inside it: /TT0 on the page and /TT0 in a form
// are routinely two different faces. The renderer cached fonts by that
// name, so whichever was seen first answered for both — and the second
// one drew with widths that did not fit the codes it was drawing, which
// on a real document meant an advance of zero and every glyph of a
// heading stacked in one place.

// inkColumns reports which pixel columns carry ink, which is enough to
// tell three glyphs side by side from three glyphs on top of each other.
func inkColumns(img image.Image) []bool {
	b := img.Bounds()
	cols := make([]bool, b.Dx())
	for x := b.Min.X; x < b.Max.X; x++ {
		for y := b.Min.Y; y < b.Max.Y; y++ {
			if r, _, _, a := img.At(x, y).RGBA(); a > 0x4000 && r < 0x8000 {
				cols[x-b.Min.X] = true
				break
			}
		}
	}
	return cols
}

// clusters counts runs of inked columns and returns the span from the
// first inked column to the last.
func clusters(cols []bool) (n, span, first int) {
	first, last := -1, -1
	for x, on := range cols {
		if !on {
			continue
		}
		if first < 0 {
			first = x
		}
		if last < 0 || x > last {
			last = x
		}
	}
	for x := 0; x < len(cols); x++ {
		if cols[x] && (x == 0 || !cols[x-1]) {
			n++
		}
	}
	if first < 0 {
		return 0, 0, 0
	}
	return n, last - first + 1, first
}

// type0Doc builds a page drawing three CIDs through an Identity-H font
// whose /W array gives them widths of 500, 600 and 700 — deliberately
// not the widths the embedded program carries, so the test can tell
// which of the two the renderer stepped by.
func type0Doc(t *testing.T, size float64, extra ...string) ([]byte, []uint16) {
	t.Helper()
	prog, err := os.ReadFile(testFontPath(t))
	if err != nil {
		t.Fatal(err)
	}
	// Glyph 1 is .null and glyph 3 a space in most faces, so the CIDs
	// are looked up rather than assumed: three letters that draw ink.
	ttf, err := parseTTF(prog)
	if err != nil {
		t.Fatal(err)
	}
	var cids []uint16
	for _, r := range []rune{'A', 'B', 'C'} {
		gid, ok := ttf.cmap[r]
		if !ok || gid == 0 {
			t.Skipf("the test font has no glyph for %q", r)
		}
		cids = append(cids, gid)
	}
	var codes []byte
	for _, c := range cids {
		codes = append(codes, byte(c>>8), byte(c))
	}
	doc := New()
	doc.Compress = false
	p := doc.AddPage()
	p.prelude = []byte("BT /GF1 " + fl(size) + " Tf 1 0 0 1 50 700 Tm <" +
		hexOf(codes) + "> Tj ET\n" + strings.Join(extra, ""))
	r := NewReaderOrFail(t, docBytes(t, doc))

	u := Update(r)
	file := u.AddObject(NewStream(Dict{"Length1": len(prog)}, prog))
	fd := u.AddObject(Dict{
		"Type": Name("FontDescriptor"), "FontName": Name("AAAAAA+Test"),
		"Flags": 4, "ItalicAngle": 0, "Ascent": 750, "Descent": -250,
		"CapHeight": 700, "StemV": 80,
		"FontBBox":  Array{-500, -300, 1300, 1000},
		"FontFile2": file,
	})
	cidFont := u.AddObject(Dict{
		"Type": Name("Font"), "Subtype": Name("CIDFontType2"),
		"BaseFont": Name("AAAAAA+Test"),
		"CIDSystemInfo": Dict{
			"Registry": String("Adobe"), "Ordering": String("Identity"), "Supplement": 0,
		},
		"FontDescriptor": fd,
		// Widths given per CID, deliberately unlike the widths the
		// embedded program carries, so the test can tell which of the
		// two the renderer stepped by.
		"W": Array{
			int(cids[0]), Array{500},
			int(cids[1]), Array{600},
			int(cids[2]), Array{700},
		},
		"CIDToGIDMap": Name("Identity"),
	})
	f0 := u.AddObject(Dict{
		"Type": Name("Font"), "Subtype": Name("Type0"),
		"BaseFont": Name("AAAAAA+Test"), "Encoding": Name("Identity-H"),
		"DescendantFonts": Array{cidFont},
	})
	if err := u.SetPageEntry(0, "Resources", Dict{"Font": Dict{"GF1": f0}}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), cids
}

func hexOf(b []byte) string {
	const digits = "0123456789ABCDEF"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0xF])
	}
	return string(out)
}

// TestRenderType0AdvancesFromW: three CIDs must come out side by side,
// spanning the width the /W array gives them and not the width the
// embedded program would have chosen.
func TestRenderType0AdvancesFromW(t *testing.T) {
	const size, dpi = 40, 72
	src, _ := type0Doc(t, size)
	img, err := NewReaderOrFail(t, src).RenderPage(0,
		RenderOpts{DPI: dpi, IncludeText: true})
	if err != nil {
		t.Fatal(err)
	}
	n, span, _ := clusters(inkColumns(img))
	if n < 2 {
		t.Fatalf("the glyphs are stacked: %d inked cluster(s)", n)
	}
	// The pen steps 500 and 600 thousandths before the last glyph, which
	// then draws its own shape: the inked span reaches from the first
	// glyph's left edge to somewhere inside the third.
	stepped := (500 + 600) / 1000.0 * size * dpi / 72
	if float64(span) <= stepped {
		t.Errorf("the ink spans %d px, which is less than the %.0f px the "+
			"/W array steps before the last glyph even starts", span, stepped)
	}
	total := (500 + 600 + 700) / 1000.0 * size * dpi / 72
	if float64(span) > total*1.35 {
		t.Errorf("the ink spans %d px against a /W total of %.0f px; the "+
			"advances are not coming from the document", span, total)
	}
}

// TestRenderType0CountsAsDrawn: the report has to agree that these
// glyphs were painted, so a caller is not told a page is fine when it
// is a row of blots, nor that glyphs are missing when they are not.
func TestRenderType0CountsAsDrawn(t *testing.T) {
	src, _ := type0Doc(t, 24)
	_, rep, err := NewReaderOrFail(t, src).RenderPageDetail(0,
		RenderOpts{DPI: 72, IncludeText: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Glyphs != 3 {
		t.Errorf("report says %d glyphs drawn, want 3", rep.Glyphs)
	}
	if rep.Missing != 0 {
		t.Errorf("report says %d glyphs missing, want none", rep.Missing)
	}
}

// TestRenderFontNameCollision is the bug itself. Two fonts share the
// resource name /GF1 — one on the page, one inside a form it draws —
// and declare different widths for the same CIDs. Cached by name, the
// second one drew with the first one's metrics; on a real document that
// meant widths that did not cover the codes being drawn, an advance of
// zero apiece, and a heading painted as one blot of ink.
func TestRenderFontNameCollision(t *testing.T) {
	prog, err := os.ReadFile(testFontPath(t))
	if err != nil {
		t.Fatal(err)
	}
	// The page draws its own line and then invokes the form; /Fm0 is
	// resolved through the resources the update installs below.
	base, cids := type0Doc(t, 40, "q /Fm0 Do Q\n") // its /GF1 steps 500, 600, 700
	r := NewReaderOrFail(t, base)
	u := Update(r)

	// A second font under the same name, three times as wide.
	file := u.AddObject(NewStream(Dict{"Length1": len(prog)}, prog))
	fd := u.AddObject(Dict{
		"Type": Name("FontDescriptor"), "FontName": Name("BBBBBB+Test"),
		"Flags": 4, "Ascent": 750, "Descent": -250, "CapHeight": 700, "StemV": 80,
		"FontBBox": Array{-500, -300, 1300, 1000}, "FontFile2": file,
	})
	wide := u.AddObject(Dict{
		"Type": Name("Font"), "Subtype": Name("CIDFontType2"),
		"BaseFont": Name("BBBBBB+Test"),
		"CIDSystemInfo": Dict{
			"Registry": String("Adobe"), "Ordering": String("Identity"), "Supplement": 0,
		},
		"FontDescriptor": fd,
		"W": Array{
			int(cids[0]), Array{1500},
			int(cids[1]), Array{1800},
			int(cids[2]), Array{2100},
		},
		"CIDToGIDMap": Name("Identity"),
	})
	wide0 := u.AddObject(Dict{
		"Type": Name("Font"), "Subtype": Name("Type0"),
		"BaseFont": Name("BBBBBB+Test"), "Encoding": Name("Identity-H"),
		"DescendantFonts": Array{wide},
	})
	var codes []byte
	for _, c := range cids {
		codes = append(codes, byte(c>>8), byte(c))
	}
	form := u.AddObject(NewStream(Dict{
		"Type": Name("XObject"), "Subtype": Name("Form"),
		"BBox":      Array{0, 0, 595, 842},
		"Resources": Dict{"Font": Dict{"GF1": wide0}},
	}, []byte("BT /GF1 40 Tf 1 0 0 1 50 400 Tm <"+hexOf(codes)+"> Tj ET\n")))

	pageFonts, _ := r.resolve(r.InheritedPageValue(0, "Resources")).(Dict)
	fontDict, _ := r.resolve(pageFonts["Font"]).(Dict)
	if err := u.SetPageEntry(0, "Resources", Dict{
		"Font":    fontDict,
		"XObject": Dict{"Fm0": form},
	}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}

	img, err := NewReaderOrFail(t, buf.Bytes()).RenderPage(0,
		RenderOpts{DPI: 72, IncludeText: true})
	if err != nil {
		t.Fatal(err)
	}
	// Split the page at the two baselines and measure each line's ink.
	narrow := spanOfBand(img, 100, 180) // the page's own line, near y=700pt
	wideSpan := spanOfBand(img, 400, 480)
	if narrow == 0 || wideSpan == 0 {
		t.Fatalf("a line did not draw: narrow=%d wide=%d", narrow, wideSpan)
	}
	// The form's font steps three times as far, so its line must be
	// markedly wider. Cached by name, the two came out identical.
	if wideSpan <= narrow*2 {
		t.Errorf("the form's line spans %d px against the page's %d; the two "+
			"fonts share a resource name and the wrong metrics were used",
			wideSpan, narrow)
	}
}

// spanOfBand measures the inked width of a horizontal band of the page.
func spanOfBand(img image.Image, y0, y1 int) int {
	b := img.Bounds()
	cols := make([]bool, b.Dx())
	for y := y0; y < y1 && y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			if r, _, _, a := img.At(b.Min.X+x, b.Min.Y+y).RGBA(); a > 0x4000 && r < 0x8000 {
				cols[x] = true
			}
		}
	}
	_, span, _ := clusters(cols)
	return span
}
