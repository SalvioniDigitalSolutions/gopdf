package gopdf

import "testing"

// spacesFont is a simple font, as a scanner's invisible text layer
// carries it, whose /ToUnicode reads two codes as a space — <00> and
// <20> — and two other codes as one letter, which its encoding gives
// neither of.
func spacesFont() *fontInfo {
	enc := &[256]rune{}
	for c := 0x20; c < 0x7f; c++ {
		enc[c] = rune(c)
	}
	enc[0x05], enc[0x09] = 0, 0
	toU := map[uint32]string{0x00: " ", 0x20: " ", 0x41: "A", 0x09: "ß", 0x05: "ß"}
	return &fontInfo{
		name:     "T1",
		decoder:  &fontDecoder{toUnicode: toU, encoding: enc},
		encode:   map[rune][]byte{},
		widths:   map[uint32]float64{},
		observed: map[uint32]bool{},
	}
}

// Where several codes read as one character, the code the font's
// encoding gives that character too is written, else the lowest — the
// same one every time, not whichever the map happened to hand out
// first: two saves of one scanned letter put its bars in different
// places.
func TestEncoderChoosesTheSameCodeEveryTime(t *testing.T) {
	for i := 0; i < 200; i++ {
		fi := spacesFont()
		got, err := fi.encodeText("A A")
		if err != nil {
			t.Fatal(err)
		}
		if want := []byte{0x41, 0x20, 0x41}; string(got) != string(want) {
			t.Fatalf("try %d: a space written as % x, want % x (the code the encoding also gives it)", i, got, want)
		}
		got, err = fi.encodeText("ß")
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "\x05" {
			t.Fatalf("try %d: ß written as % x, want 05 (the lowest of its codes)", i, got)
		}
	}
}
