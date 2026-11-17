package gopdf

import (
	"fmt"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// encodingFixture draws, with fonts that name no encoding of their own:
// a Type 1 standard font's em dash, en dash and quotes (StandardEncoding);
// a Wingdings bullet set before a word with a narrow gap, and a
// ZapfDingbats one glued to a word (pictograph fonts); a TrueType font's
// é (WinAnsi, as before); a symbolic Type 1 font (WinAnsi, as before);
// and an encoding that names StandardEncoding itself.
func encodingFixture() []byte {
	content := strings.Join([]string{
		`BT /F1 12 Tf 1 0 0 1 72 700 Tm (A. Background\320Alien Property) Tj ET`,
		`BT /F1 12 Tf 1 0 0 1 72 680 Tm (\140\140hold\047\047 from 1998\2611999) Tj ET`,
		// As a real file set it: the bullet's advance and the step to the
		// text leave 3 points, under half of Arial's space at 24 points.
		`BT /F2 24 Tf 1 0 0 1 72 620 Tm -0.306 Tc (\330) Tj 21.75 0 Td /F3 24 Tf 0 Tc (DOE approved method) Tj ET`,
		`BT /F4 12 Tf 1 0 0 1 72 580 Tm (l) Tj /F1 12 Tf (Marco wrote) Tj ET`,
		`BT /F3 12 Tf 1 0 0 1 72 560 Tm (caf\351 au lait) Tj ET`,
		`BT /F5 12 Tf 1 0 0 1 72 540 Tm (sym\351) Tj ET`,
		`BT /F6 12 Tf 1 0 0 1 72 520 Tm (Part I\320Giulia Ferrari) Tj ET`,
		`BT /F1 12 Tf 1 0 0 1 72 500 Tm (Meeting with Marco\240Bianchi today.) Tj ET`,
	}, "\n") + "\n"
	objs := map[int]string{
		1: `<< /Type /Catalog /Pages 2 0 R >>`,
		2: `<< /Type /Pages /Kids [3 0 R] /Count 1 >>`,
		3: `<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R ` +
			`/Resources << /Font << /F1 5 0 R /F2 6 0 R /F3 7 0 R /F4 8 0 R /F5 9 0 R /F6 11 0 R >> >> >>`,
		4:  fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content),
		5:  `<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman >>`,
		6:  `<< /Type /Font /Subtype /TrueType /BaseFont /CFAPJI+Wingdings /FirstChar 216 /LastChar 216 /Widths [794] /FontDescriptor 12 0 R >>`,
		7:  `<< /Type /Font /Subtype /TrueType /BaseFont /Arial /FirstChar 32 /LastChar 32 /Widths [278] >>`,
		8:  `<< /Type /Font /Subtype /Type1 /BaseFont /ZapfDingbats >>`,
		9:  `<< /Type /Font /Subtype /Type1 /BaseFont /MathSymbols /FontDescriptor 10 0 R >>`,
		10: `<< /Type /FontDescriptor /FontName /MathSymbols /Flags 4 /FontBBox [0 0 1000 1000] /ItalicAngle 0 /Ascent 800 /Descent -200 /CapHeight 700 /StemV 80 >>`,
		11: `<< /Type /Font /Subtype /Type1 /BaseFont /Melior /Encoding << /BaseEncoding /StandardEncoding /Differences [65 /A] >> >>`,
		12: `<< /Type /FontDescriptor /FontName /CFAPJI+Wingdings /Flags 4 /FontBBox [-250 -200 1748 886] /ItalicAngle 0 /Ascent 886 /Descent -200 /CapHeight 886 /StemV 161 >>`,
	}
	return rawPDFOf(objs, "")
}

// TestFontsWithoutEncodingReadAsViewersShowThem: a Type 1 font that names
// no encoding is read in StandardEncoding, its dash being a dash and not a
// letter; a pictograph font's codes are no letters.
func TestFontsWithoutEncodingReadAsViewersShowThem(t *testing.T) {
	got := extractAll(t, encodingFixture())
	for _, want := range []string{
		"Background—Alien Property",
		"‘‘hold’’ from 1998–1999",
		"café au lait", // TrueType: WinAnsi, as before
		"symé",         // a symbolic Type 1 font: WinAnsi, as before
		"Part I—Giulia Ferrari",
		"Marco\u00a0Bianchi", // a code StandardEncoding leaves empty: WinAnsi's reading
	} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in %q", want, got)
		}
	}
	for _, word := range []string{"DOE approved", "Marco wrote"} {
		i := strings.Index(got, word)
		if i < 0 {
			t.Fatalf("%q not read: %q", word, got)
		}
		if r, _ := utf8.DecodeLastRuneInString(got[:i]); unicode.IsLetter(r) || unicode.IsDigit(r) {
			t.Errorf("the bullet before %q reads as %q, a letter", word, r)
		}
	}
	if !strings.ContainsRune(got, 0xF0D8) || !strings.ContainsRune(got, 0xF06C) {
		t.Errorf("the pictographs do not read as their private-use characters: %q", got)
	}
}

// TestValuesBesideADashOrABulletAreRedacted: the values the fixture sets
// against a dash or a bullet are found as words and removed, and the
// text around them stays.
func TestValuesBesideADashOrABulletAreRedacted(t *testing.T) {
	r, err := NewReader(encodingFixture())
	if err != nil {
		t.Fatal(err)
	}
	rd := Redact(r)
	for _, v := range []string{"Alien Property", "DOE", "Marco", "Giulia Ferrari"} {
		rd.Text(v)
	}
	out := redactTo(t, rd)
	text := extractAll(t, out)
	for _, gone := range []string{"Alien", "Property", "DOE", "Marco", "Giulia", "Ferrari"} {
		if strings.Contains(text, gone) {
			t.Errorf("%q is still read in %q", gone, text)
		}
	}
	for _, kept := range []string{"Background—", "approved method", "wrote", "Part I—"} {
		if !strings.Contains(text, kept) {
			t.Errorf("%q was lost: %q", kept, text)
		}
	}
}

// TestDashesQuotesAndSlashesAreWordBoundaries: a value set right after
// or before a dash of any length, a quote, a guillemet or a slash is a
// word of its own, for the redactor and for the check alike.
func TestDashesQuotesAndSlashesAreWordBoundaries(t *testing.T) {
	for _, sep := range []string{"—", "–", "‒", "―", "“", "”", "‘", "’", "«", "»", "/", ""} {
		text := "Background" + sep + "Alien Property" + sep + "Custodian"
		if got := literalRanges(text, "Alien Property", matchWords); len(got) != 1 {
			t.Errorf("%q: the value beside %q is not found as a word (%v)", text, sep, got)
		}
	}
	// And a letter still joins: no value inside a longer word.
	if got := literalRanges("BackgroundÐAlien", "Alien", matchWords); len(got) != 0 {
		t.Errorf("a value inside a word was found: %v", got)
	}
}
