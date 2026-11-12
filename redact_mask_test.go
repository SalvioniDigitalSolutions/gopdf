package gopdf

import (
	"bytes"
	"image"
	"image/color"
	"testing"
)

// A scrubbed picture that had a soft mask.
//
// It used to come back without one, its colour multiplied by its
// transparency: a logo on a clear ground came back on a black one,
// wherever the region lay. These tests hold it to what a region is:
// the pixels under it gone, colour and mask both, and the rest of the
// picture as it was.

// maskedLogoDoc writes a page that draws one 120×60 picture: a blue
// field, opaque in its middle band and a third transparent around it.
// The middle of the band carries a red block — the part to remove —
// drawn in the colour AND in the mask (its own transparency there).
func maskedLogoDoc(t *testing.T) []byte {
	t.Helper()
	const w, h = 120, 60
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBA{R: 30, G: 80, B: 190, A: 85}
			if y >= 20 && y < 40 {
				c.A = 200
			}
			if x >= 45 && x < 75 && y >= 22 && y < 38 {
				c = color.NRGBA{R: 220, G: 20, B: 20, A: 230}
			}
			m.SetNRGBA(x, y, c)
		}
	}
	doc := New()
	page := doc.AddPage()
	img, err := doc.AddImage(m)
	if err != nil {
		t.Fatal(err)
	}
	page.DrawImage(img, 100, 100, 240, 120)
	return docBytes(t, doc)
}

// onlyImage decodes the one picture page 1 paints, as stored (colour
// and mask apart).
func onlyImage(t *testing.T, pdf []byte) (*image.NRGBA, ImageRef) {
	t.Helper()
	r, err := NewReader(pdf)
	if err != nil {
		t.Fatal(err)
	}
	imgs, err := r.PageImages(0)
	if err != nil || len(imgs) != 1 {
		t.Fatalf("want one picture on the page, got %d (%v)", len(imgs), err)
	}
	m, err := imgs[0].Decode()
	if err != nil {
		t.Fatal(err)
	}
	n, ok := m.(*image.NRGBA)
	if !ok {
		t.Fatalf("the picture decodes as %T: it has no soft mask", m)
	}
	return n, imgs[0]
}

func TestRedactAreaOnAMaskedPictureKeepsTheRest(t *testing.T) {
	src := maskedLogoDoc(t)
	before, _ := onlyImage(t, src)
	if c := before.NRGBAAt(60, 30); c.R < 200 || c.A != 230 {
		t.Fatalf("fixture: the block to remove is %v", c)
	}

	r, _ := NewReader(src)
	rd := Redact(r)
	// The red block, on the page: the picture is drawn 240×120 at
	// (100,100), twice its pixel size.
	rd.Area(0, 100+2*45, 100+2*22, 2*30, 2*16)
	out := redactTo(t, rd)

	after, ref := onlyImage(t, out)
	if after.Bounds() != before.Bounds() {
		t.Fatalf("the picture is %v now, was %v", after.Bounds(), before.Bounds())
	}
	if _, has := ref.stream.dict["SMask"]; !has {
		t.Fatal("the picture lost its soft mask")
	}
	b := before.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			was, is := before.NRGBAAt(x, y), after.NRGBAAt(x, y)
			inside := x >= 45 && x < 75 && y >= 22 && y < 38
			edge := x >= 43 && x < 77 && y >= 20 && y < 40 // the area is rounded outwards
			switch {
			case inside:
				if is.R > 20 || is.G > 20 || is.B > 20 || is.A != 255 {
					t.Fatalf("under the area at (%d,%d) the picture holds %v, want the fill, opaque", x, y, is)
				}
			case edge:
			default:
				if is != was {
					t.Fatalf("outside the area at (%d,%d) the picture holds %v, was %v", x, y, is, was)
				}
			}
		}
	}
}

// The mask under the area is part of what goes: a picture whose shapes
// are in its mask shows them there and nowhere else.
func TestRedactAreaOnAMaskedPictureLeavesNoOldMask(t *testing.T) {
	src := maskedLogoDoc(t)
	r, _ := NewReader(src)
	_, ref := onlyImage(t, src)
	oldMask := r.resolve(ref.stream.dict["SMask"]).(*rawStream).data
	oldColour := ref.stream.data
	if len(oldMask) == 0 || len(oldColour) == 0 {
		t.Fatal("fixture: no stored samples to look for")
	}

	rd := Redact(r)
	rd.Area(0, 100+2*45, 100+2*22, 2*30, 2*16)
	out := redactTo(t, rd)
	if bytes.Contains(out, oldMask) {
		t.Error("the old mask's stored samples are still in the file")
	}
	if bytes.Contains(out, oldColour) {
		t.Error("the old colour samples are still in the file")
	}
	// One picture and one mask, no more: nothing of the old pair is
	// kept beside the new.
	r2, _ := NewReader(out)
	streams := 0
	r2.Walk(func(_ Ref, obj any) bool {
		if st, ok := obj.(*Stream); ok && st != nil {
			if sub, _ := st.Dict["Subtype"].(Name); sub == "Image" {
				streams++
			}
		}
		return true
	})
	if streams != 2 {
		t.Errorf("the output holds %d image objects, want the picture and its mask", streams)
	}
}

// A picture without a mask is written back as it always was.
func TestRedactAreaOnAnOpaquePictureStaysOpaque(t *testing.T) {
	r, _ := NewReader(scanDoc(t, 200, 100))
	page, _ := r.PageSize(0)
	rd := Redact(r)
	rd.Area(0, 0.4*page.W, 0.4*page.H, 0.2*page.W, 0.2*page.H)
	out := redactTo(t, rd)
	r2, _ := NewReader(out)
	imgs, err := r2.PageImages(0)
	if err != nil || len(imgs) != 1 {
		t.Fatalf("the image is gone: %v", err)
	}
	if _, has := imgs[0].stream.dict["SMask"]; has {
		t.Error("a picture that had no mask was given one")
	}
}
