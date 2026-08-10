package tesseract

import (
	"bytes"
	"image"
	"image/color"
	"strings"
	"testing"
)

func TestParseTSV(t *testing.T) {
	const tsv = "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n" +
		"5\t1\t1\t1\t1\t1\t100\t200\t60\t18\t96.5\tAda\n" +
		"5\t1\t1\t1\t1\t2\t170\t200\t95\t18\t93.25\tLovelace\n" +
		"4\t1\t1\t1\t1\t0\t0\t0\t0\t0\t-1\t\n" +
		"5\t1\t1\t1\t2\t1\t100\t240\t80\t18\tnotanumber\tAccount\n"
	got, err := parseTSV(strings.NewReader(tsv))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d words, want 3: %+v", len(got), got)
	}
	if got[0].Text != "Ada" || got[0].X != 100 || got[0].Y != 200 ||
		got[0].W != 60 || got[0].H != 18 {
		t.Errorf("first word = %+v", got[0])
	}
	if d := got[0].Confidence - 0.965; d < -1e-9 || d > 1e-9 {
		t.Errorf("confidence = %v, want 0.965", got[0].Confidence)
	}
	if got[1].Text != "Lovelace" {
		t.Errorf("second word = %+v", got[1])
	}
	// An unparseable confidence should not lose the word.
	if got[2].Text != "Account" || got[2].Confidence != 0 {
		t.Errorf("third word = %+v", got[2])
	}
}

func TestParseTSVEmpty(t *testing.T) {
	got, err := parseTSV(strings.NewReader(""))
	if err != nil || len(got) != 0 {
		t.Errorf("empty input gave %v, %v", got, err)
	}
	// A truncated row is skipped rather than failing the lot.
	got, err = parseTSV(bytes.NewReader([]byte("5\t1\t1\n")))
	if err != nil || len(got) != 0 {
		t.Errorf("short row gave %v, %v", got, err)
	}
}

func TestNewReportsMissingBinary(t *testing.T) {
	_, err := New(Options{Binary: "definitely-not-a-real-ocr-binary"})
	if err == nil {
		t.Fatal("a missing binary should be reported")
	}
	if !strings.Contains(err.Error(), "not on PATH") {
		t.Errorf("the error should say what is wrong: %v", err)
	}
}

func TestNewDefaults(t *testing.T) {
	if !Available() {
		t.Skip("tesseract is not installed")
	}
	e, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.opts.Languages) == 0 || e.opts.PageSegMode == 0 || e.opts.Timeout == 0 {
		t.Errorf("defaults were not filled in: %+v", e.opts)
	}
}

// TestRecognizeBlankImage checks the engine runs and returns nothing
// rather than failing on an image with no text.
func TestRecognizeBlankImage(t *testing.T) {
	if !Available() {
		t.Skip("tesseract is not installed")
	}
	e, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	m := image.NewRGBA(image.Rect(0, 0, 120, 60))
	for i := range m.Pix {
		m.Pix[i] = 0xFF
	}
	words, err := e.Recognize(m)
	if err != nil {
		t.Fatalf("a blank image should not fail: %v", err)
	}
	for _, w := range words {
		if strings.TrimSpace(w.Text) != "" {
			t.Errorf("read %q from a blank image", w.Text)
		}
	}
	// A nil or empty image is handled without running anything.
	if got, err := e.Recognize(nil); err != nil || got != nil {
		t.Errorf("nil image gave %v, %v", got, err)
	}
	empty := image.NewRGBA(image.Rect(0, 0, 0, 0))
	if got, err := e.Recognize(empty); err != nil || got != nil {
		t.Errorf("empty image gave %v, %v", got, err)
	}
	_ = color.Black
}
