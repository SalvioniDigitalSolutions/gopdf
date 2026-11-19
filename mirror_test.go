package gopdf

import (
	"fmt"
	"image"
	"strings"
	"testing"
)

// shownOnlyOCR reads a word where a picture holds mid-grey pixels — but,
// like an engine before mirror writing, only where they stand in the
// top half of the picture it is handed.
type shownOnlyOCR struct{}

func (shownOnlyOCR) Recognize(img image.Image) ([]OCRWord, error) {
	b := img.Bounds()
	x0, y0, x1, y1 := b.Max.X, b.Max.Y, -1, -1
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, _, _, _ := img.At(x, y).RGBA()
			if v := r >> 8; v > 80 && v < 120 {
				x0, y0, x1, y1 = min(x0, x), min(y0, y), max(x1, x), max(y1, y)
			}
		}
	}
	if x1 < 0 || y0 >= b.Min.Y+b.Dy()/2 {
		return nil, nil
	}
	return []OCRWord{{Text: "Nevada", X: x0 - b.Min.X, Y: y0 - b.Min.Y, W: x1 - x0 + 1, H: y1 - y0 + 1, Confidence: 0.95}}, nil
}

// mirroredPictureDoc is a page that draws a grey picture flipped: the
// picture stores its mid-grey "word" in its bottom rows, and the page
// shows it at the top.
func mirroredPictureDoc(t *testing.T, flip bool) []byte {
	t.Helper()
	const w, h = 200, 100
	pix := make([]byte, w*h)
	for i := range pix {
		pix[i] = 240
	}
	for y := 80; y < 92; y++ {
		for x := 40; x < 120; x++ {
			pix[y*w+x] = 100
		}
	}
	cm := "400 0 0 200 100 500 cm"
	if flip {
		cm = "400 0 0 -200 100 700 cm"
	}
	content := "q " + cm + " /Im0 Do Q\n"
	return rawPDFOf(map[int]string{
		1: `<< /Type /Catalog /Pages 2 0 R >>`,
		2: `<< /Type /Pages /Kids [3 0 R] /Count 1 >>`,
		3: `<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /XObject << /Im0 5 0 R >> >> >>`,
		4: streamObj("", []byte(content)),
		5: streamObj(fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceGray /BitsPerComponent 8", w, h), pix),
	}, "")
}

// A picture its page shows mirrored is read as it is shown: the word in
// it is found and barred where it stands in the picture as stored, and
// the check after the removal reads it the same way.
func TestAMirrorPlacedPictureIsReadAsShown(t *testing.T) {
	for _, flip := range []bool{false, true} {
		r, err := NewReader(mirroredPictureDoc(t, flip))
		if err != nil {
			t.Fatal(err)
		}
		imgs, err := r.PageImages(0)
		if err != nil || len(imgs) != 1 {
			t.Fatalf("images: %v %v", imgs, err)
		}
		if imgs[0].Mirrored() != flip {
			t.Fatalf("flip %v: Mirrored() = %v", flip, imgs[0].Mirrored())
		}
		rd := Redact(r)
		rd.SetOCR(shownOnlyOCR{})
		rd.Text("Nevada")
		marks, err := rd.Marks()
		if err != nil {
			t.Fatal(err)
		}
		if flip {
			// As shown the word is in the top half: found.
			if len(marks) != 1 || !strings.EqualFold(marks[0].Text, "Nevada") {
				t.Fatalf("the word in a mirror-placed picture was not found: %+v", marks)
			}
		} else if len(marks) != 0 {
			// Upright, this picture shows its grey block at the bottom,
			// where the stand-in engine reads nothing.
			t.Fatalf("upright: %+v", marks)
		}
		if !flip {
			continue
		}
		out := redactTo(t, rd)
		r2, _ := NewReader(out)
		got, err := mustImages(t, r2)[0].Decode()
		if err != nil {
			t.Fatal(err)
		}
		// The bar covers the stored rows of the word, not the rows the
		// page shows it in.
		if v, _, _, _ := got.At(80, 86).RGBA(); v>>8 > 20 {
			t.Errorf("the word's stored pixels were not barred (grey %d)", v>>8)
		}
		if v, _, _, _ := got.At(80, 14).RGBA(); v>>8 < 200 {
			t.Errorf("the rows the word is shown in were barred in the stored picture (grey %d)", v>>8)
		}
	}
}

func mustImages(t *testing.T, r *Reader) []ImageRef {
	t.Helper()
	imgs, err := r.PageImages(0)
	if err != nil || len(imgs) == 0 {
		t.Fatalf("images: %v", err)
	}
	return imgs
}
