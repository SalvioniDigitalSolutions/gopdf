package gopdf

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// A substitute is chosen from the name a document gives a font and from
// what it says of the face. Two requests with one name and different
// faces are two choices, whichever comes first: remembered by the name
// alone, the second request was answered with the font chosen for the
// first, and the same page was drawn with one font or another by what
// had been drawn before it in the process.
func TestSubstituteIsChosenByTheWholeRequest(t *testing.T) {
	font, err := os.ReadFile(testFontPath(t))
	if err != nil {
		t.Fatal(err)
	}
	// Two faces of one invented family; a byte after the font's end
	// tells the files apart and is read by nothing.
	dir := t.TempDir()
	regular := append(append([]byte{}, font...), 'R')
	bold := append(append([]byte{}, font...), 'B')
	if err := os.WriteFile(filepath.Join(dir, "Quillon.ttf"), regular, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Quillon Bold.ttf"), bold, 0o644); err != nil {
		t.Fatal(err)
	}
	plain := FontRequest{BaseFont: "Quillon"}
	heavy := FontRequest{BaseFont: "Quillon", Bold: true}
	which := func(data []byte) string {
		switch {
		case bytes.Equal(data, regular):
			return "the regular face"
		case bytes.Equal(data, bold):
			return "the bold face"
		case data == nil:
			return "nothing"
		}
		return "another font"
	}
	for _, order := range [][]FontRequest{{plain, heavy}, {heavy, plain}} {
		sub := fontsIn([]string{dir})
		for _, req := range order {
			want := "the regular face"
			if req.Bold {
				want = "the bold face"
			}
			if got := which(sub(req)); got != want {
				t.Errorf("asked first for bold=%v: the request with bold=%v was given %s, want %s",
					order[0].Bold, req.Bold, got, want)
			}
		}
		// And the same request again is the same answer.
		for _, req := range order {
			if a, b := sub(req), sub(req); !bytes.Equal(a, b) {
				t.Errorf("the same request (bold=%v) was answered twice differently", req.Bold)
			}
		}
	}
}
