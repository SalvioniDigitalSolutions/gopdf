package gopdf

import (
	"bytes"
	"compress/zlib"
	"strings"
	"testing"
)

// deflated packs data the way a writer does, at the given level. At
// level 0 the bytes are stored as they are, so whatever the data spells
// stands in the stream to be read.
func deflated(t *testing.T, data []byte, level int) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := zlib.NewWriterLevel(&buf, level)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// stripsPage is a scan stored in strips, each written into the page's
// instructions as deflated grey: three strips of 8 by 2 samples, black,
// grey and white from the top, each 100 points wide and 20 high, and
// a line of text after them.
func stripsPage(t *testing.T, level int) []byte {
	t.Helper()
	var c strings.Builder
	for i, shade := range []byte{0x00, 0x80, 0xFF} {
		// Sixteen samples, among them what reads as the end of the
		// picture and as the start of a string that never closes.
		raw := bytes.Repeat([]byte{shade}, 16)
		if level == zlib.NoCompression {
			copy(raw[4:], " EI (")
		}
		c.WriteString("q 100 0 0 20 100 ")
		c.WriteString(fl(float64(700 - 20*i)))
		c.WriteString(" cm BI /W 8 /H 2 /CS /G /BPC 8 /F /Fl ID\n")
		c.Write(deflated(t, raw, level))
		c.WriteString("\nEI Q\n")
	}
	c.WriteString("BT /F1 10 Tf 1 0 0 1 100 600 Tm (Nach den Streifen steht Text.) Tj ET\n")
	return rawPageDoc(t, c.String())
}

// The text after pictures written into a page's instructions is read:
// the pictures' packed data was lexed as instructions, and a bracket
// among its bytes opened a string that took the rest of the page.
func TestTextAfterPackedInlinePicturesIsRead(t *testing.T) {
	for name, level := range map[string]int{"stored": zlib.NoCompression, "deflated": zlib.BestCompression} {
		data := stripsPage(t, level)
		if got := extractAll(t, data); !strings.Contains(got, "Nach den Streifen steht Text.") {
			t.Errorf("%s: the page's text reads %q", name, got)
		}
		r, err := NewReader(data)
		if err != nil {
			t.Fatal(err)
		}
		frags, err := r.PageTextFragments(0)
		if err != nil {
			t.Fatal(err)
		}
		if len(frags) != 1 || frags[0].Text != "Nach den Streifen steht Text." {
			t.Errorf("%s: the page's pieces of text: %+v", name, frags)
		}
		content, err := r.pageContent(r.pages[0].dict)
		if err != nil {
			t.Fatal(err)
		}
		ids, eis := 0, 0
		for _, tok := range tokenizeContent(content) {
			switch tok.val {
			case opKeyword("ID"):
				ids++
			case opKeyword("EI"):
				eis++
			}
		}
		if ids != 3 || eis != 3 {
			t.Errorf("%s: the lexer sees %d pictures begin and %d end, want 3 and 3", name, ids, eis)
		}
	}
}

// Every strip is drawn, each in its place.
func TestRenderDrawsEveryPackedInlineStrip(t *testing.T) {
	for name, level := range map[string]int{"stored": zlib.NoCompression, "deflated": zlib.BestCompression} {
		r, err := NewReader(stripsPage(t, level))
		if err != nil {
			t.Fatal(err)
		}
		img, err := r.RenderPage(0, RenderOpts{DPI: 72, IncludeRasterImages: true})
		if err != nil {
			t.Fatal(err)
		}
		h := img.Bounds().Dy()
		for i, want := range []uint32{0x00, 0x80, 0xFF} {
			// The right half of each strip: clear of the bytes the
			// stored variant writes into the left.
			y := h - (700 - 20*i) - 10
			rr, _, _, _ := img.At(190, y).RGBA()
			if got := rr >> 8; got+8 < want || got > want+8 {
				t.Errorf("%s: strip %d is drawn as grey %d, want %d", name, i+1, got, want)
			}
		}
	}
}

// Deflated data is as long as it says: an "EI" among its bytes does
// not end the picture.
func TestInlineImageData_DeflatedDataSaysWhereItEnds(t *testing.T) {
	raw := []byte("abcd EI efgh")
	packed := deflated(t, raw, zlib.NoCompression)
	if !bytes.Contains(packed, []byte(" EI ")) {
		t.Fatal("fixture: the stored stream does not hold the letters")
	}
	content := append(append([]byte("ID\n"), packed...), []byte("\nEI\nQ")...)
	d := Dict{"Width": int64(12), "Height": int64(1), "BitsPerComponent": int64(8), "ColorSpace": Name("DeviceGray"), "Filter": Name("FlateDecode")}
	to, after := inlineImageData(noPage, content, 3, d)
	if !bytes.Equal(content[3:to], packed) || string(content[after:]) != "\nQ" {
		t.Errorf("data of %d bytes (want %d), rest %q", to-3, len(packed), content[after:])
	}
	if n := deflatedLength([]byte("not deflated")); n != 0 {
		t.Errorf("a length of %d for data that is not deflated", n)
	}
}
