package gopdf

import (
	"image"
	"image/color"
	"math/rand"
	"testing"
)

// refNext is bitReader.next as it read every sample before whole bytes
// were read at once: bit by bit.
func refNext(b *bitReader) uint32 {
	var v uint32
	for i := 0; i < b.bpc; i++ {
		byteIdx := b.rowStart + (b.pos >> 3)
		if byteIdx >= len(b.data) {
			return v << uint(b.bpc-i-1)
		}
		bit := (b.data[byteIdx] >> uint(7-(b.pos&7))) & 1
		v = v<<1 | uint32(bit)
		b.pos++
	}
	if b.bpc == 16 {
		v >>= 8
	}
	return v
}

// Eight-bit samples read whole give what they gave read bit by bit, for
// any width and component count, the data cut short included.
func TestBitReaderWholeBytesAsBitByBit(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 400; trial++ {
		w, h, comps := 1+rng.Intn(40), 1+rng.Intn(12), 1+rng.Intn(4)
		full := w * comps * h
		n := full
		switch trial % 4 {
		case 1:
			n = rng.Intn(full + 1) // cut short
		case 2:
			n = 0
		case 3:
			n = full + rng.Intn(9) // longer than the rows
		}
		data := make([]byte, n)
		rng.Read(data)
		fast, ref := newBitReader(data, w, comps, 8), newBitReader(data, w, comps, 8)
		for y := 0; y < h; y++ {
			fast.startRow(y)
			ref.startRow(y)
			for k := 0; k < w*comps; k++ {
				if a, b := fast.next(), refNext(ref); a != b {
					t.Fatalf("trial %d (w %d h %d comps %d, %d of %d bytes) row %d sample %d: %d, bit by bit %d", trial, w, h, comps, n, full, y, k, a, b)
				}
			}
		}
	}
}

// refMaskGray is maskGray as every mask was turned to grey before:
// through GrayModel, pixel by pixel.
func refMaskGray(m image.Image) *image.Gray {
	b := m.Bounds()
	out := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			out.SetGray(x, y, color.GrayModel.Convert(m.At(b.Min.X+x, b.Min.Y+y)).(color.Gray))
		}
	}
	return out
}

func randomNRGBA(rng *rand.Rand, r image.Rectangle, greyOpaque bool) *image.NRGBA {
	m := image.NewNRGBA(r)
	for i := 0; i < len(m.Pix); i += 4 {
		if greyOpaque && rng.Intn(10) > 0 {
			v := uint8(rng.Intn(256))
			m.Pix[i], m.Pix[i+1], m.Pix[i+2], m.Pix[i+3] = v, v, v, 255
			continue
		}
		rng.Read(m.Pix[i : i+4])
	}
	return m
}

// A mask decoded as grey or as NRGBA, at the origin or not, opaque grey
// or any colour, comes out as it did through GrayModel.
func TestMaskGrayAsThroughGrayModel(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	for trial := 0; trial < 200; trial++ {
		r := image.Rect(rng.Intn(5), rng.Intn(5), 0, 0)
		r.Max = r.Min.Add(image.Pt(1+rng.Intn(30), 1+rng.Intn(30)))
		var ms []image.Image
		ms = append(ms, randomNRGBA(rng, r, true), randomNRGBA(rng, r, false))
		g := image.NewGray(r)
		rng.Read(g.Pix)
		ms = append(ms, g)
		// a sub-image: its Pix starts before its bounds
		big := randomNRGBA(rng, image.Rect(0, 0, r.Max.X+7, r.Max.Y+7), true)
		ms = append(ms, big.SubImage(r).(*image.NRGBA))
		bg := image.NewGray(image.Rect(0, 0, r.Max.X+5, r.Max.Y+3))
		rng.Read(bg.Pix)
		ms = append(ms, bg.SubImage(r).(*image.Gray))
		for k, m := range ms {
			got, want := maskGray(m), refMaskGray(m)
			if got.Rect != want.Rect || string(got.Pix) != string(want.Pix) {
				t.Fatalf("trial %d mask %d (%T %v): differs from GrayModel's", trial, k, m, m.Bounds())
			}
		}
	}
}

// opaqueImage hides an image's type, so applySoftMask reads it through
// At and NRGBAModel, as it read every picture before.
type opaqueImage struct{ image.Image }

// An NRGBA picture read from its bytes combines with its mask as it did
// read through At and NRGBAModel, the mask finer, coarser or offset.
func TestApplySoftMaskNRGBAAsThroughAt(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for trial := 0; trial < 200; trial++ {
		br := image.Rect(0, 0, 1+rng.Intn(25), 1+rng.Intn(25))
		base := randomNRGBA(rng, br, false)
		var bases []*image.NRGBA
		bases = append(bases, base)
		big := randomNRGBA(rng, image.Rect(0, 0, br.Max.X+6, br.Max.Y+4), false)
		sub := image.Rect(3, 2, 3+br.Dx(), 2+br.Dy())
		bases = append(bases, big.SubImage(sub).(*image.NRGBA))
		mr := image.Rect(0, 0, 1+rng.Intn(60), 1+rng.Intn(60))
		mask := image.NewGray(mr)
		rng.Read(mask.Pix)
		for k, b := range bases {
			got := applySoftMask(b, mask).(*image.NRGBA)
			want := applySoftMask(opaqueImage{b}, mask).(*image.NRGBA)
			if got.Rect != want.Rect || string(got.Pix) != string(want.Pix) {
				t.Fatalf("trial %d base %d (%v, mask %v): differs from reading through At", trial, k, b.Bounds(), mr)
			}
		}
	}
}
