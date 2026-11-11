package ccitt

import (
	"bytes"
	"image"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The MMR decoder is checked three ways, none of which is a round trip
// through its own inverse alone: against a Group 4 file some other
// encoder wrote, against this package's fax reader (vendored from
// golang.org/x/image and independent of the code here) reading what the
// MMR encoder wrote, and only then against that encoder directly.

// gopher loads the fixture every fax test in the repository uses: the
// Group 4 data and the picture it encodes.
func gopher(t *testing.T) (fax []byte, pix []byte, w, h int) {
	t.Helper()
	dir := filepath.Join("..", "..", "testdata", "ccitt")
	fax, err := os.ReadFile(filepath.Join(dir, "bw-gopher.ccitt_group4"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(dir, "bw-gopher.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	b := m.Bounds()
	w, h = b.Dx(), b.Dy()
	pix = make([]byte, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if r, _, _, _ := m.At(b.Min.X+x, b.Min.Y+y).RGBA(); r>>8 < 128 {
				pix[y*w+x] = 1
			}
		}
	}
	return fax, pix, w, h
}

func firstDiff(got, want []byte, w int) (x, y int, ok bool) {
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			return i % w, i / w, true
		}
	}
	return 0, 0, false
}

// TestDecodeMMRReadsAForeignFile: data written by somebody else's
// encoder comes out as the picture it was made from.
func TestDecodeMMRReadsAForeignFile(t *testing.T) {
	fax, want, w, h := gopher(t)
	got, used, err := DecodeMMR(fax, w, h)
	if err != nil {
		t.Fatal(err)
	}
	if x, y, bad := firstDiff(got, want, w); bad {
		t.Fatalf("pixel (%d,%d) differs from the reference picture", x, y)
	}
	// The file ends with the end-of-block code, and all of it is used.
	if used != len(fax) {
		t.Errorf("consumed %d of %d bytes", used, len(fax))
	}
}

// mmrFixtures are bitmaps with the shapes that reach every mode: long
// runs for the make-up codes, fine detail for the vertical modes,
// features that end above the line for pass mode.
func mmrFixtures() map[string]struct {
	pix  []byte
	w, h int
} {
	out := map[string]struct {
		pix  []byte
		w, h int
	}{}
	add := func(name string, w, h int, f func(x, y int) bool) {
		pix := make([]byte, w*h)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				if f(x, y) {
					pix[y*w+x] = 1
				}
			}
		}
		out[name] = struct {
			pix  []byte
			w, h int
		}{pix, w, h}
	}
	rnd := rand.New(rand.NewSource(7))
	add("blank", 40, 9, func(x, y int) bool { return false })
	add("black", 40, 9, func(x, y int) bool { return true })
	add("one pixel wide", 1, 30, func(x, y int) bool { return y%3 == 0 })
	add("stripes", 67, 12, func(x, y int) bool { return x%2 == 0 })
	add("checker", 64, 16, func(x, y int) bool { return (x/3+y/2)%2 == 0 })
	add("diagonal", 90, 40, func(x, y int) bool { return (x+y)%11 < 3 })
	add("blobs", 120, 50, func(x, y int) bool {
		return (x-30)*(x-30)+(y-20)*(y-20) < 150 || (x > 70 && x < 110 && y > 30 && y < 44)
	})
	add("noise", 97, 31, func(x, y int) bool { return rnd.Intn(4) == 0 })
	add("sparse noise", 300, 20, func(x, y int) bool { return rnd.Intn(60) == 0 })
	// Wide enough for the make-up codes above 1728, and for two of the
	// longest in a row.
	add("long runs", 6000, 4, func(x, y int) bool {
		return (y == 1 && x > 100 && x < 5800) || (y == 2 && x%1900 < 1800) || y == 3
	})
	return out
}

// TestEncodeMMRIsReadByTheFaxReader: what the encoder writes is Group 4
// as the vendored reader understands it.
func TestEncodeMMRIsReadByTheFaxReader(t *testing.T) {
	for name, f := range mmrFixtures() {
		coded := EncodeMMR(f.pix, f.w, f.h, true)
		dst := image.NewGray(image.Rect(0, 0, f.w, f.h))
		if err := DecodeIntoGray(dst, bytes.NewReader(coded), MSB, Group4, nil); err != nil {
			t.Errorf("%s: the fax reader refused it: %v", name, err)
			continue
		}
		// The fax reader's default is white for a clear bit.
		for i, v := range f.pix {
			if black := dst.Pix[i] < 128; black != (v == 1) {
				t.Errorf("%s: pixel (%d,%d) read back wrong", name, i%f.w, i/f.w)
				break
			}
		}
	}
}

func TestMMRRoundTrip(t *testing.T) {
	for name, f := range mmrFixtures() {
		for _, eofb := range []bool{false, true} {
			coded := EncodeMMR(f.pix, f.w, f.h, eofb)
			got, used, err := DecodeMMR(coded, f.w, f.h)
			if err != nil {
				t.Errorf("%s (end of block %v): %v", name, eofb, err)
				continue
			}
			if x, y, bad := firstDiff(got, f.pix, f.w); bad {
				t.Errorf("%s (end of block %v): pixel (%d,%d) differs", name, eofb, x, y)
			}
			if used != len(coded) {
				t.Errorf("%s (end of block %v): consumed %d of %d bytes",
					name, eofb, used, len(coded))
			}
			// Two bitmaps back to back, as a halftone region's bit
			// planes are: the first must end exactly where the second
			// begins.
			two := append(append([]byte(nil), coded...), coded...)
			if _, used, err := DecodeMMR(two, f.w, f.h); err != nil || used != len(coded) {
				t.Errorf("%s (end of block %v): with more data after it, consumed %d of %d (%v)",
					name, eofb, used, len(coded), err)
			}
		}
	}
}

// TestDecodeMMREndsEarly: an end-of-block code before the last row
// leaves the rest white rather than failing.
func TestDecodeMMREndsEarly(t *testing.T) {
	f := mmrFixtures()["blobs"]
	const rows = 25
	coded := EncodeMMR(f.pix[:rows*f.w], f.w, rows, true)
	got, _, err := DecodeMMR(coded, f.w, f.h)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got[:rows*f.w], f.pix[:rows*f.w]) {
		t.Error("the rows before the end of the block came out wrong")
	}
	for _, v := range got[rows*f.w:] {
		if v != 0 {
			t.Fatal("a row after the end of the block is not white")
		}
	}
}

// TestDecodeMMRGarbageIsSurvivable: nonsense is an error or a picture,
// promptly, and never a panic.
func TestDecodeMMRGarbageIsSurvivable(t *testing.T) {
	rnd := rand.New(rand.NewSource(11))
	try := func(data []byte, w, h int) {
		t.Helper()
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() {
				if e := recover(); e != nil {
					t.Errorf("%d bytes at %dx%d panicked: %v", len(data), w, h, e)
				}
			}()
			DecodeMMR(data, w, h)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%d bytes at %dx%d took over five seconds", len(data), w, h)
		}
	}
	for i := 0; i < 400; i++ {
		data := make([]byte, rnd.Intn(300))
		rnd.Read(data)
		try(data, 1+rnd.Intn(200), 1+rnd.Intn(60))
	}
	// Valid data with a byte changed, which gets much further in.
	f := mmrFixtures()["noise"]
	coded := EncodeMMR(f.pix, f.w, f.h, true)
	for i := 0; i < len(coded); i++ {
		bad := append([]byte(nil), coded...)
		bad[i] ^= byte(1 + rnd.Intn(255))
		try(bad, f.w, f.h)
	}
	// Truncated data is an error, not a short picture.
	if _, _, err := DecodeMMR(coded[:len(coded)/2], f.w, f.h); err == nil {
		t.Error("half the data decoded without complaint")
	}
	// Sizes nothing could hold are refused before anything is allocated.
	for _, d := range [][2]int{{0, 5}, {5, 0}, {-1, 5}, {1 << 21, 1 << 21}} {
		if _, _, err := DecodeMMR(nil, d[0], d[1]); err == nil {
			t.Errorf("a size of %dx%d was accepted", d[0], d[1])
		}
	}
	// A wide bitmap of codes that each claim almost nothing: the work
	// has to follow the width, not the square of it. Only the decoding
	// is timed — the encoder above is the simple kind, and is quadratic
	// on exactly this input.
	const wide, tall = 20000, 40
	row := make([]byte, wide)
	for x := range row {
		row[x] = byte(x & 1)
	}
	codedMany := EncodeMMR(bytes.Repeat(row, tall), wide, tall, false)
	start := time.Now()
	if _, _, err := DecodeMMR(codedMany, wide, tall); err != nil {
		t.Fatal(err)
	}
	// Linear, this is some milliseconds; quadratic, it would be eight
	// thousand million steps.
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("%d rows of single pixels, %d wide, took %v to decode", tall, wide, d)
	}
}

// FuzzDecodeMMR throws arbitrary bytes and sizes at the decoder.
func FuzzDecodeMMR(f *testing.F) {
	for _, fx := range mmrFixtures() {
		if fx.w*fx.h > 1<<14 {
			continue
		}
		f.Add(EncodeMMR(fx.pix, fx.w, fx.h, true), uint16(fx.w), uint16(fx.h))
	}
	f.Add([]byte{}, uint16(8), uint16(8))
	f.Fuzz(func(t *testing.T, data []byte, w, h uint16) {
		// The size is bounded so a case cannot ask for a gigabyte of
		// bitmap; what is under test is the decoding, not the limit.
		pix, used, err := DecodeMMR(data, int(w%512), int(h%512))
		if err != nil {
			return
		}
		if len(pix) != int(w%512)*int(h%512) || used < 0 || used > len(data) {
			t.Fatalf("decoded %d pixels and consumed %d of %d bytes for %dx%d",
				len(pix), used, len(data), w%512, h%512)
		}
	})
}
