package gopdf

import (
	"os"
	"testing"
)

// A damaged file whose trailer is gone: the catalog is looked for among
// the objects the scan found. The look at an object's opening bytes ran
// on into the objects after it, and the pages before the catalog were
// each taken for it; which one won was the order of a map. The same
// file opened about four times in thirty and was refused otherwise.
func TestRecoverTrailer_FindsTheSameCatalogEveryTime(t *testing.T) {
	data, err := os.ReadFile("testdata/repair-catalog-guess.pdf")
	if err != nil {
		t.Fatal(err)
	}
	pages := -1
	for i := 0; i < 40; i++ {
		r, err := NewReader(data)
		if err != nil {
			t.Fatalf("opening %d: %v", i+1, err)
		}
		root, _ := r.trailer["Root"].(Ref)
		if root.Num != 5 {
			t.Fatalf("opening %d: the catalog was taken to be object %d, want 5", i+1, root.Num)
		}
		// One of its two pages is itself damaged beyond reading; what
		// is asked is the same document at every opening.
		n := r.NumPages()
		if n < 1 || (pages >= 0 && n != pages) {
			t.Fatalf("opening %d: %d pages, %d at the openings before", i+1, n, pages)
		}
		pages = n
	}
}
