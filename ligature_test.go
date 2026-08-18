package gopdf

import (
	"strings"
	"testing"
)

// A face that sets "fi" as one glyph may never draw a lone f, and a
// subset of it then carries no code for one. Its ToUnicode still says
// what the joined glyph means, so the text reads back correctly — but
// writing the same word again was refused, because the map was inverted
// one rune at a time and a two-rune entry has no single rune to key on.
// It is invertible as a run: to write "fi", draw the fi.

// ligFont is a font whose f exists only inside ligatures, which is the
// shape of the real ones this was found on.
func ligFont() *fontInfo {
	codes := map[uint32]string{
		0x0069: "i", 0x006E: "n", 0x0065: "e", 0x006F: "o",
		564: "fi", 605: "ffi", 909: "ff",
	}
	fi := &fontInfo{
		name: "C2_0", cid: true, embedded: true,
		decoder:  &fontDecoder{cid: true, toUnicode: codes},
		encode:   map[rune][]byte{},
		widths:   map[uint32]float64{},
		observed: map[uint32]bool{},
	}
	// Every code here is one the page really draws, which is what lets
	// the encoder write it back.
	for code := range codes {
		fi.observed[code] = true
	}
	return fi
}

func TestLigatureEncoding(t *testing.T) {
	fi := ligFont()
	got, err := fi.encodeText("fine")
	if err != nil {
		t.Fatalf("a word whose f is only in a ligature was refused: %v", err)
	}
	// "fi" is one code, then i-less "n" and "e" on their own.
	want := []byte{0x02, 0x34, 0x00, 0x6E, 0x00, 0x65}
	if string(got) != string(want) {
		t.Errorf("encodeText(\"fine\") = % X, want % X", got, want)
	}
}

// TestLigatureLongestWins: a font carrying both "ffi" and "fi" spells
// "office" with the three-letter glyph its producer used.
func TestLigatureLongestWins(t *testing.T) {
	fi := ligFont()
	got, err := fi.encodeText("offi")
	if err != nil {
		t.Fatal(err)
	}
	// o, then the ffi glyph — not o, ff, i or o, f, fi.
	want := []byte{0x00, 0x6F, 0x02, 0x5D}
	if string(got) != string(want) {
		t.Errorf("encodeText(\"offi\") = % X, want % X", got, want)
	}
}

// TestLigatureDoesNotDisplaceASingleGlyph is the guard against changing
// what already worked: where the font has a character on its own, it is
// written on its own, whatever ligatures exist around it.
func TestLigatureDoesNotDisplaceASingleGlyph(t *testing.T) {
	fi := ligFont()
	fi.decoder.toUnicode[0x0066] = "f" // this face does have a lone f
	fi.observed[0x0066] = true         // and the page draws it
	got, err := fi.encodeText("fine")
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x00, 0x66, 0x00, 0x69, 0x00, 0x6E, 0x00, 0x65}
	if string(got) != string(want) {
		t.Errorf("encodeText(\"fine\") = % X, want % X (single glyphs)", got, want)
	}
}

// TestLigatureStillRefusesWhatIsAbsent: the fallback must not paper over
// a character the font genuinely has not got.
func TestLigatureStillRefusesWhatIsAbsent(t *testing.T) {
	fi := ligFont()
	_, err := fi.encodeText("fix")
	if err == nil {
		t.Fatal("a character absent from the font was accepted")
	}
	if !strings.Contains(err.Error(), "'x'") {
		t.Errorf("the error should name the character it could not set: %v", err)
	}
	// A lone f, with no following letter to join to, is still absent.
	if _, err := fi.encodeText("of"); err == nil {
		t.Error("a trailing lone f was accepted by a font that has none")
	}
}

// TestLigatureWidthsComeFromTheGlyphDrawn: the ligature is one glyph, so
// the text measures as that glyph and not as its letters.
func TestLigatureWidthsComeFromTheGlyphDrawn(t *testing.T) {
	fi := ligFont()
	fi.widths = map[uint32]float64{564: 600, 0x6E: 500, 0x65: 500}
	codes, err := fi.encodeText("fine")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fi.stringWidth(codes, 0, 0, 1000), 1600.0; got != want {
		t.Errorf("stringWidth = %v, want %v (fi + n + e)", got, want)
	}
}

// TestLigatureEmpty covers the boundary the loop indexes on.
func TestLigatureEmpty(t *testing.T) {
	fi := ligFont()
	got, err := fi.encodeText("")
	if err != nil || len(got) != 0 {
		t.Errorf("encoding nothing gave % X, %v", got, err)
	}
	if n, _ := fi.ligatureAt([]rune("fi"), 1); n != 0 {
		t.Error("a ligature was matched starting inside itself")
	}
	if n, _ := fi.ligatureAt([]rune("f"), 0); n != 0 {
		t.Error("a ligature was matched with too little text left for it")
	}
}
