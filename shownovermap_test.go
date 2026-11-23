package gopdf

import (
	"bytes"
	"strings"
	"testing"
)

// brokenMapDoc is a page in a font that names its encoding and sets
// letters, with a /ToUnicode map of the kind a newsletter's bold face
// carried: the full stop read as a space, the space as a tab, the "1" as
// a control character and capitals as small letters. symbolic marks the
// font as one that draws its glyphs at codes of its own.
func brokenMapDoc(symbolic bool) []byte {
	cmap := "/CIDInit /ProcSet findresource begin 12 dict begin begincmap\n" +
		"1 begincodespacerange <0000> <FFFF> endcodespacerange\n" +
		"9 beginbfchar\n<20> <0009>\n<2E> <0020>\n<31> <0018>\n<48> <0068>\n<49> <0069>\n<53> <0073>\n<61> <0061>\n<66> <0066>\n<80> <00660069>\nendbfchar\n" +
		"endcmap CMapName currentdict /CMap defineresource pop end end"
	flags := "32"
	if symbolic {
		flags = "4"
	}
	objs := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 400 200] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		4: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding /ToUnicode 6 0 R /FontDescriptor 7 0 R >>",
		5: kstream("", "BT /F1 12 Tf 20 150 Td (www.fda.gov IHS 1 \\200) Tj ET"),
		6: kstream("", cmap),
		7: "<< /Type /FontDescriptor /FontName /Helvetica /Flags " + flags + " /FontBBox [0 0 1000 1000] /ItalicAngle 0 /Ascent 718 /Descent -207 /CapHeight 718 /StemV 88 >>",
	}
	return rawPDFOf(objs, "")
}

// A map that says what the font cannot be drawing is overruled by the
// encoding the font names: the page reads as it is drawn. What a map is
// for — a ligature, here — is still read from the map.
func TestAMapThatCannotBeRightReadsAsDrawn(t *testing.T) {
	got, err := NewReaderOrFail(t, brokenMapDoc(false)).PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	if want := "www.fda.gov IHS 1 fi"; strings.TrimSpace(got) != want {
		t.Errorf("read %q, want %q", got, want)
	}

	// A symbol font draws what its map says: nothing is overruled.
	got, err = NewReaderOrFail(t, brokenMapDoc(true)).PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "www fda gov\tihs") {
		t.Errorf("a symbol font read %q, want its map's reading", got)
	}
}

// What the page shows is what a redaction finds: "IHS" is taken off the
// line it is drawn on, and the web address stays as it is drawn.
func TestARedactionFindsWhatABrokenMapHides(t *testing.T) {
	rd := Redact(NewReaderOrFail(t, brokenMapDoc(false)))
	rd.Text("IHS")
	var out bytes.Buffer
	if _, err := rd.WriteTo(&out); err != nil {
		t.Fatalf("redact: %v", err)
	}
	got, err := NewReaderOrFail(t, out.Bytes()).PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(got), "ihs") {
		t.Errorf("the marked value is still on the page: %q", got)
	}
	if !strings.Contains(got, "www.fda.gov") {
		t.Errorf("the address went with it: %q", got)
	}
}

func TestShownOverMap(t *testing.T) {
	cases := []struct {
		mapped string
		shown  rune
		want   string
	}{
		{" ", '.', "."},    // a full stop the map calls a space
		{"\t", ' ', " "},   // a space the map calls a tab
		{"\x18", '1', "1"}, // a digit the map calls a control character
		{"h", 'H', "H"},    // a capital the map calls a small letter
		{"A", 'a', "a"},    // and the other way round
		{"fi", 'f', "fi"},  // a ligature: the map knows
		{"Z", 'P', "Z"},    // another letter: the map is believed
		{" ", ' ', " "},    // two kinds of space: either will do
		{"’", '\'', "’"},   // a curly quote for a straight one
		{"x", 0, "x"},      // nothing named
	}
	for _, c := range cases {
		if got := shownOverMap(c.mapped, c.shown); got != c.want {
			t.Errorf("shownOverMap(%q, %q) = %q, want %q", c.mapped, c.shown, got, c.want)
		}
	}
}
