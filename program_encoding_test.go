package gopdf

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

// streamObj writes a stream object holding data as it is.
func streamObj(dict string, data []byte) string {
	return fmt.Sprintf("<< %s /Length %d >>\nstream\n%s\nendstream", dict, len(data), data)
}

// onePage returns a one-page document whose page draws content with the
// fonts given (/F1, /F2 …), each an object of its own after the page.
func onePage(content string, fonts []string, more map[int]string) []byte {
	var res strings.Builder
	objs := map[int]string{
		1: `<< /Type /Catalog /Pages 2 0 R >>`,
		2: `<< /Type /Pages /Kids [3 0 R] /Count 1 >>`,
		4: streamObj("", []byte(content)),
	}
	for i, f := range fonts {
		fmt.Fprintf(&res, "/F%d %d 0 R ", i+1, 10+i)
		objs[10+i] = f
	}
	objs[3] = `<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << ` + res.String() + `>> >> >>`
	for n, o := range more {
		objs[n] = o
	}
	return rawPDFOf(objs, "")
}

// Converters re-encode a font's glyphs to low codes and name them by
// their Windows codes: "G84" is T, "MT32" a space, "G151" an em dash.
// Read so, the body of a report and a line of a journal's back page read
// as the text every viewer shows; read as nothing they were control
// characters, and no value in them was found by the redactor or by the
// check after it.
func TestNumberedGlyphNamesReadAsTheCharactersTheyNumber(t *testing.T) {
	content := strings.Join([]string{
		`BT /F1 12 Tf 1 0 0 1 72 700 Tm (\001\002\003\004\005\006\005\001\002\003\004) Tj ET`,
		`BT /F2 12 Tf 1 0 0 1 72 680 Tm <020304040506070806> Tj ET`,
		`BT /F3 12 Tf 1 0 0 1 72 660 Tm (\001\002\003) Tj ET`,
		`BT /F4 12 Tf 1 0 0 1 72 640 Tm (\001\002\003\004) Tj ET`,
	}, "\n") + "\n"
	got := extractAll(t, onePage(content, []string{
		`<< /Type /Font /Subtype /Type1 /BaseFont /AAAAAA+Serif01 /Encoding << /Differences [1 /G84 /G104 /G105 /G115 /G32 /G151] >> >>`,
		`<< /Type /Font /Subtype /Type1 /BaseFont /BBBBBB+Sans-Condensed /Encoding << /Differences [2 /MT70 /MT117 /MT108 /MT32 /MT116 /MT101 /MT120] >> >>`,
		// A TrueType font's numbered names are glyph numbers.
		`<< /Type /Font /Subtype /TrueType /BaseFont /CCCCCC+Sans /Encoding << /Differences [1 /g84 /g104 /g105] >> >>`,
		// Names of two kinds are no numbering a converter made.
		`<< /Type /Font /Subtype /Type1 /BaseFont /DDDDDD+Serif /Encoding << /Differences [1 /G84 /X104 /G105 /K115] >> >>`,
	}, nil))
	for _, want := range []string{"This — This", "Full text"} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in %q", want, got)
		}
	}
	// The TrueType font and the font of mixed names read nothing of
	// "This": only the first line has it, twice.
	if n := strings.Count(got, "Thi"); n != 2 {
		t.Errorf("numbered names read where they should not be (%d times \"Thi\"): %q", n, got)
	}
}

func TestNumberedNameRunes(t *testing.T) {
	if got := numberedNameRunes(map[int]string{1: "G84", 2: "G104", 3: "G105"}); len(got) != 3 || got[1] != 'T' || got[3] != 'i' {
		t.Errorf("one prefix: %q", got)
	}
	if got := numberedNameRunes(map[int]string{1: "G84", 2: "G104"}); got != nil {
		t.Errorf("two names are no numbering: %q", got)
	}
	if got := numberedNameRunes(map[int]string{1: "G84", 2: "X104", 3: "G105", 4: "K115"}); got != nil {
		t.Errorf("mixed prefixes: %q", got)
	}
	// Out of the Windows encoding's printable range: nothing.
	if got := numberedNameRunes(map[int]string{1: "G07", 2: "G12", 3: "G29"}); len(got) != 0 {
		t.Errorf("control codes: %q", got)
	}
}

// A Type 1 font embedded with an encoding of its own — a report made on
// a Macintosh put the en dash at 0xD0 — is read in it: "1979–1996", and
// not "1979Ð1996", one word in which no year was found. The codes the
// program does not name keep the reading they had, and /Differences
// still has the last word.
func TestAnEmbeddedType1ProgramsOwnEncodingIsRead(t *testing.T) {
	clear := "%!PS-AdobeFont-1.0: TestSerif 001.000\n" +
		"/FontName /TestSerif def\n" +
		"/Encoding 256 array\n0 1 255 {1 index exch /.notdef put} for\n" +
		"dup 49 /one put\ndup 57 /nine put\ndup 208 /endash put\ndup 209 /emdash put\n" +
		"readonly def\ncurrentfile eexec\n"
	program := append([]byte(clear), 0x8c, 0x1f, 0x00, 0xa5, 0x5e, 0x11, 0x42)
	content := `BT /F1 12 Tf 1 0 0 1 72 700 Tm (1979\3201996 caf\351 \200) Tj ET` + "\n"
	got := extractAll(t, onePage(content, []string{
		`<< /Type /Font /Subtype /Type1 /BaseFont /EEEEEE+TestSerif /FontDescriptor 20 0 R /Encoding << /Differences [128 /Adieresis] >> >>`,
	}, map[int]string{
		20: `<< /Type /FontDescriptor /FontName /EEEEEE+TestSerif /Flags 6 /FontBBox [0 0 1000 1000] /ItalicAngle 0 /Ascent 800 /Descent -200 /CapHeight 700 /StemV 80 /FontFile 21 0 R >>`,
		21: streamObj(fmt.Sprintf("/Length1 %d /Length2 %d /Length3 0", len(clear), len(program)-len(clear)), program),
	}))
	for _, want := range []string{"1979–1996", "café", "Ä"} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in %q", want, got)
		}
	}
}

// customEncodingCFF is a CFF font made for this test (fontTools): six
// glyphs — T, h, e, space and an en dash named uni2013, a name kept in
// the font's own strings — reached through an encoding of its own at
// codes 1 to 5.
const customEncodingCFF = "0100040100010101095465737453616e730001010115f81c02bd8bf856f8ec05cc0fd7108bf71c12db11000201010810756e69323031335465737453616e73000000003500490046000101870101010400060101040e1822252ff8880ef888bd16f824f8ec060ef888bd16f824f8ec060ef888bd16f824f8ec060ef8880ef888bd16f824f8ec060e"

func TestAnEmbeddedCFFProgramsOwnEncodingIsRead(t *testing.T) {
	program, err := hex.DecodeString(customEncodingCFF)
	if err != nil {
		t.Fatal(err)
	}
	names := cffEncodingNames(program)
	if names[1] != "T" || names[4] != "space" || names[5] != "uni2013" {
		t.Fatalf("the program's encoding: %v", names)
	}
	content := `BT /F1 12 Tf 1 0 0 1 72 700 Tm (\001\002\003\004\005\004\001\002\003) Tj ET` + "\n"
	got := extractAll(t, onePage(content, []string{
		`<< /Type /Font /Subtype /Type1 /BaseFont /FFFFFF+TestSans /FirstChar 1 /LastChar 5 /Widths [500 500 500 250 500] /FontDescriptor 20 0 R >>`,
	}, map[int]string{
		20: `<< /Type /FontDescriptor /FontName /FFFFFF+TestSans /Flags 4 /FontBBox [0 0 1000 1000] /ItalicAngle 0 /Ascent 800 /Descent -200 /CapHeight 700 /StemV 80 /FontFile3 21 0 R >>`,
		21: streamObj("/Subtype /Type1C", program),
	}))
	if !strings.Contains(got, "The – The") {
		t.Errorf("want %q in %q", "The – The", got)
	}
}
