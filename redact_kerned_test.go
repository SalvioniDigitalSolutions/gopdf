package gopdf

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

// xOfRune returns where on the page the first rune r of the page's text
// is drawn, from the fragments' Advances.
func xOfRune(t *testing.T, pdf []byte, r rune) float64 {
	t.Helper()
	frags, err := NewReaderOrFail(t, pdf).PageTextFragments(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range frags {
		for i, c := range []rune(f.Text) {
			if c == r && len(f.Advances) > i {
				return f.X + f.Advances[i]
			}
		}
	}
	t.Fatalf("%q is not on the page", r)
	return 0
}

// Characters taken out of a run that only kerns its letters leave every
// kept character where it was drawn. Written back one after another with
// one move for what went, the kerns of the run were lost, and the rest of
// the line stood a little to the left of where it was.
func TestRemovingFromAKernedRunMovesNothingElse(t *testing.T) {
	content := "BT /F1 10 Tf 1 0 0 1 100 700 Tm [(Wa) 80 (ter Bian) 30 (chi R) 60 (oad)] TJ ET\n"
	src := rawPageDoc(t, content)
	before := xOfRune(t, src, 'o')
	rd := Redact(NewReaderOrFail(t, src))
	rd.Text("Bianchi")
	var out bytes.Buffer
	if _, err := rd.WriteTo(&out); err != nil {
		t.Fatal(err)
	}
	text, err := NewReaderOrFail(t, out.Bytes()).PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "Bianchi") || !strings.Contains(text, "Road") {
		t.Fatalf("read %q", text)
	}
	if after := xOfRune(t, out.Bytes(), 'o'); math.Abs(after-before) > 0.01 {
		t.Errorf("the o of Road moved from %.2f to %.2f", before, after)
	}
}

// The same with character and word spacing in force: what the spacing
// adds is counted where the kept characters are written.
func TestRemovingFromAKernedRunWithSpacingMovesNothingElse(t *testing.T) {
	content := "BT /F1 10 Tf 0.6 Tc 1.5 Tw 1 0 0 1 100 700 Tm [(Wa) 80 (ter Bian) 30 (chi R) 60 (oad)] TJ ET\n"
	src := rawPageDoc(t, content)
	before := xOfRune(t, src, 'o')
	rd := Redact(NewReaderOrFail(t, src))
	rd.Text("Bianchi")
	var out bytes.Buffer
	if _, err := rd.WriteTo(&out); err != nil {
		t.Fatal(err)
	}
	if after := xOfRune(t, out.Bytes(), 'o'); math.Abs(after-before) > 0.01 {
		t.Errorf("the o of Road moved from %.2f to %.2f", before, after)
	}
}
