package gopdf

import (
	"bytes"
	"errors"
	"image/png"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestJBIG2Decodes puts every case through the decoder and requires the
// picture the case was built from.
func TestJBIG2Decodes(t *testing.T) {
	for _, c := range jbig2Cases() {
		t.Run(c.name, func(t *testing.T) {
			pix, err := decodeJBIG2(c.data, c.globals, c.w, c.h)
			if err != nil {
				t.Fatal(err)
			}
			comparePixels(t, "against the picture it was made from",
				&bitmap{w: c.w, h: c.h, pix: pix}, c.want)
		})
	}
}

// popplerDecodeJBIG2Globals is popplerDecodeJBIG2 for a stream that may
// keep its dictionaries in a globals stream.
func popplerDecodeJBIG2Globals(t *testing.T, data, globals []byte, w, h int) *bitmap {
	t.Helper()
	if globals == nil {
		return popplerDecodeJBIG2(t, data, w, h)
	}
	if _, err := exec.LookPath("pdfimages"); err != nil {
		t.Skip("pdfimages is not installed; the reference comparison needs Poppler")
	}
	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "scan.pdf")
	if err := os.WriteFile(pdfPath, jbig2PDFWithGlobals(t, data, globals, w, h), 0o600); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(dir, "img")
	if out, err := exec.Command("pdfimages", "-png", pdfPath, base).CombinedOutput(); err != nil {
		t.Fatalf("pdfimages refused the file: %v\n%s", err, out)
	}
	matches, _ := filepath.Glob(base + "-*.png")
	if len(matches) == 0 {
		t.Fatal("pdfimages extracted nothing")
	}
	f, err := os.Open(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	if b.Dx() != w || b.Dy() != h {
		t.Fatalf("Poppler extracted %dx%d, want %dx%d", b.Dx(), b.Dy(), w, h)
	}
	got := newBitmap(w, h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			if r>>8 < 128 && g>>8 < 128 && bl>>8 < 128 {
				got.set(x, y, 1)
			}
		}
	}
	return got
}

// TestJBIG2AgainstPopplerEverything is the check that matters: the same
// bytes through this decoder and through Poppler's, pixel for pixel.
//
// The cases are streams this package's test encoders wrote, so a round
// trip alone would prove only that the two halves share their reading
// of the specification. Poppler was written by other people from the
// same document.
//
// It is not a perfect referee. In a handful of places it departs from
// the specification, and a case that lands on one says which and is
// left to the round trip. Poppler is still asked: the day it gives the
// right answer, the test says the exemption can go.
func TestJBIG2AgainstPopplerEverything(t *testing.T) {
	for _, c := range jbig2Cases() {
		t.Run(c.name, func(t *testing.T) {
			pix, err := decodeJBIG2(c.data, c.globals, c.w, c.h)
			if err != nil {
				t.Fatal(err)
			}
			mine := &bitmap{w: c.w, h: c.h, pix: pix}
			if c.noPoppler != "" {
				if theirs := popplerTry(t, c.data, c.globals, c.w, c.h); theirs != nil &&
					bytes.Equal(theirs.pix, c.want.pix) {
					t.Log("Poppler decodes this as the specification has it; " +
						"the case no longer needs its exemption")
					return
				}
				t.Skip("not comparable: " + c.noPoppler)
			}
			theirs := popplerDecodeJBIG2Globals(t, c.data, c.globals, c.w, c.h)
			comparePixels(t, "against Poppler", mine, theirs)
			comparePixels(t, "Poppler against the picture it was made from", theirs, c.want)
		})
	}
}

// popplerTry is Poppler's answer where it may not have one: nil if it
// is not installed, refuses the stream or extracts nothing usable.
func popplerTry(t *testing.T, data, globals []byte, w, h int) *bitmap {
	t.Helper()
	if _, err := exec.LookPath("pdfimages"); err != nil {
		return nil
	}
	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "scan.pdf")
	doc := jbig2PDF(t, data, w, h)
	if globals != nil {
		doc = jbig2PDFWithGlobals(t, data, globals, w, h)
	}
	if err := os.WriteFile(pdfPath, doc, 0o600); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(dir, "img")
	if err := exec.Command("pdfimages", "-png", pdfPath, base).Run(); err != nil {
		return nil
	}
	matches, _ := filepath.Glob(base + "-*.png")
	if len(matches) == 0 {
		return nil
	}
	f, err := os.Open(matches[0])
	if err != nil {
		return nil
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil
	}
	b := img.Bounds()
	if b.Dx() != w || b.Dy() != h {
		return nil
	}
	got := newBitmap(w, h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			if r>>8 < 128 && g>>8 < 128 && bl>>8 < 128 {
				got.set(x, y, 1)
			}
		}
	}
	return got
}

// TestJBIG2StandardTableCodes checks the prefix codes the standard
// tables come out with against the codes the specification prints.
//
// The tables are stored as lengths and the codes are derived, so this
// is a check of both at once: a wrong length anywhere in a table shifts
// every code after it.
func TestJBIG2StandardTableCodes(t *testing.T) {
	want := map[int][]uint32{
		1:  {0x0, 0x2, 0x6, 0x7},
		2:  {0x0, 0x2, 0x6, 0xe, 0x1e, 0x3e, 0x3f},
		3:  {0xfe, 0x0, 0x2, 0x6, 0xe, 0x1e, 0xff, 0x7e, 0x3e},
		4:  {0x0, 0x2, 0x6, 0xe, 0x1e, 0x1f},
		5:  {0x7e, 0x0, 0x2, 0x6, 0xe, 0x1e, 0x7f, 0x3e},
		6:  {0x1c, 0x8, 0x9, 0xa, 0x1d, 0x1e, 0xb, 0x0, 0x2, 0x3, 0xc, 0xd, 0x3e, 0x3f},
		7:  {0x8, 0x0, 0x9, 0x1a, 0x1b, 0xa, 0xb, 0x1c, 0x1d, 0xc, 0x1, 0x2, 0x3, 0x1e, 0x1f},
		8:  {0xfc, 0x1fc, 0xfd, 0x1fd, 0x7c, 0xa, 0x0, 0x1a, 0x3a, 0x4, 0x3b, 0xb, 0xc, 0x1b, 0x1c, 0x3c, 0x7d, 0x3d, 0x1fe, 0x1ff, 0x1},
		9:  {0xfc, 0x1fc, 0xfd, 0x1fd, 0x7c, 0xa, 0x2, 0x3, 0x1a, 0x3a, 0x4, 0x3b, 0xb, 0xc, 0x1b, 0x1c, 0x3c, 0x7d, 0x3d, 0x1fe, 0x1ff, 0x0},
		10: {0x7a, 0xfc, 0x7b, 0x18, 0x0, 0x19, 0x36, 0x7c, 0xfd, 0x1, 0x1a, 0x37, 0x38, 0x39, 0x3a, 0x3b, 0x3c, 0x7d, 0xfe, 0xff, 0x2},
		11: {0x0, 0x2, 0xc, 0xd, 0x1c, 0x1d, 0x3c, 0x7a, 0x7b, 0x7c, 0x7d, 0x7e, 0x7f},
		12: {0x0, 0x2, 0x6, 0x1c, 0x1d, 0x3c, 0x7a, 0x7b, 0x7c, 0x7d, 0x7e, 0xfe, 0xff},
		13: {0x0, 0x4, 0xc, 0x1c, 0xd, 0x5, 0x3a, 0x3b, 0x3c, 0x3d, 0x3e, 0x7e, 0x7f},
		14: {0x4, 0x5, 0x0, 0x6, 0x7},
		15: {0x7c, 0x3c, 0x1c, 0xc, 0x4, 0x0, 0x5, 0xd, 0x1d, 0x3d, 0x7d, 0x7e, 0x7f},
	}
	for n := 1; n <= 15; n++ {
		tab := jbig2StdTable(n)
		lines := jbig2StdLines[n]
		if len(lines) != len(want[n]) {
			t.Errorf("table %d has %d lines, want %d", n, len(lines), len(want[n]))
			continue
		}
		// The codes of a complete prefix code fill the code space
		// exactly.
		space := 0.0
		for i, l := range lines {
			pos := 0
			for _, m := range lines[:i] {
				if m.prefLen == l.prefLen {
					pos++
				}
			}
			if got := uint32(tab.first[l.prefLen]) + uint32(pos); got != want[n][i] {
				t.Errorf("table %d line %d (from %d): code %#x, want %#x",
					n, i, l.low, got, want[n][i])
			}
			space += 1 / float64(uint64(1)<<uint(l.prefLen))
		}
		if space != 1 {
			t.Errorf("table %d fills %v of the code space, want all of it", n, space)
		}
		// And its ordinary lines cover a run of values with no gap,
		// the lower and upper ranges starting where they end.
		var normal []huffLine
		lower, upper := 0, 0
		hasLower, hasUpper := false, false
		for _, l := range lines {
			switch l.kind {
			case huffNormal:
				normal = append(normal, l)
			case huffLower:
				lower, hasLower = l.low, true
			case huffUpper:
				upper, hasUpper = l.low, true
			}
		}
		for i := range normal {
			for j := i + 1; j < len(normal); j++ {
				if normal[j].low < normal[i].low {
					normal[i], normal[j] = normal[j], normal[i]
				}
			}
		}
		for i := 1; i < len(normal); i++ {
			if end := normal[i-1].low + 1<<uint(normal[i-1].rangeLen); end != normal[i].low {
				t.Errorf("table %d: the range from %d ends at %d and the next starts at %d",
					n, normal[i-1].low, end, normal[i].low)
			}
		}
		last := normal[len(normal)-1]
		if hasLower && lower != normal[0].low-1 {
			t.Errorf("table %d: lower range from %d, want %d", n, lower, normal[0].low-1)
		}
		if hasUpper && upper != last.low+1<<uint(last.rangeLen) {
			t.Errorf("table %d: upper range from %d, want %d", n, upper, last.low+1<<uint(last.rangeLen))
		}
	}
}

// TestJBIG2HuffmanValuesRoundTrip writes values with every standard
// table and reads them back, at the edges of every line.
func TestJBIG2HuffmanValuesRoundTrip(t *testing.T) {
	for n := 1; n <= 15; n++ {
		tab := jbig2StdTable(n)
		var values []int
		oob := false
		for _, l := range tab.lines {
			switch l.kind {
			case huffNormal:
				values = append(values, l.low, l.low+1<<uint(l.rangeLen)-1)
			case huffLower:
				values = append(values, l.low, l.low-1, l.low-100000)
			case huffUpper:
				values = append(values, l.low, l.low+1, l.low+100000)
			case huffOOB:
				oob = true
			}
		}
		w := &jbBits{}
		for _, v := range values {
			huffPut(w, tab, v, false)
		}
		if oob {
			huffPut(w, tab, 0, true)
		}
		r := &jbig2Bits{data: w.out}
		for _, want := range values {
			if got, ok := tab.decode(r); !ok || got != want {
				t.Fatalf("table %d: read %d (%v), want %d", n, got, ok, want)
			}
		}
		if oob {
			if _, ok := tab.decode(r); ok || r.bad {
				t.Errorf("table %d: the out-of-band value did not read as one", n)
			}
		}
		// Past the end is an error, not a value.
		if _, ok := tab.decode(&jbig2Bits{}); ok {
			t.Errorf("table %d: decoded a value from no data", n)
		}
	}
}

// TestJBIG2CustomTableMatchesStandard writes two standard tables out as
// code table segments and requires the tables read back to code exactly
// as the built-in ones do.
func TestJBIG2CustomTableMatchesStandard(t *testing.T) {
	for _, n := range []int{1, 8, 15} {
		var normal []huffLine
		lower, upper, oob := 0, 0, -1
		for _, l := range jbig2StdLines[n] {
			switch l.kind {
			case huffNormal:
				normal = append(normal, l)
			case huffLower:
				lower = l.prefLen
			case huffUpper:
				upper = l.prefLen
			case huffOOB:
				oob = l.prefLen
			}
		}
		segs, err := parseJBIG2Segments(encodeTableSeg(9, normal, lower, upper, oob))
		if err != nil {
			t.Fatal(err)
		}
		custom, err := parseHuffTable(segs[0].data)
		if err != nil {
			t.Fatalf("table %d written as a segment does not read back: %v", n, err)
		}
		std := jbig2StdTable(n)
		values := []int{-3000, -30, -16, -15, -3, -1, 0, 1, 2, 5, 21, 300, 5000, 70000}
		if n == 1 {
			values = []int{0, 1, 15, 16, 271, 272, 65807, 65808, 99999}
		}
		for _, v := range values {
			a, b := &jbBits{}, &jbBits{}
			huffPut(a, std, v, false)
			huffPut(b, custom, v, false)
			if !bytes.Equal(a.out, b.out) {
				t.Errorf("table %d codes %d as %x built in and %x from a segment", n, v, a.out, b.out)
			}
		}
	}
	// A table too short to hold its own header, one whose range runs
	// backwards, and one with no codes at all are refused.
	for _, bad := range [][]byte{
		nil, {0, 0, 0, 0, 0}, {0, 0, 0, 0, 9, 0, 0, 0, 1, 0xFF}, make([]byte, 12),
	} {
		if _, err := parseHuffTable(bad); err == nil {
			t.Errorf("a malformed code table (%x) was accepted", bad)
		}
	}
}

// TestJBIG2UnknownLengthParsing reads the one segment header that is
// allowed to leave its length out.
func TestJBIG2UnknownLengthParsing(t *testing.T) {
	pic := jbig2Fixture(jbW, jbH)
	body := append(genericBody(jbig2Region{w: jbW, h: jbH}, pic, 0, false), 0, 0, 0, jbH)
	stream := append(segmentWrapUnknown(1, body), jbig2Stream(t, pic, 0)...)
	segs, err := parseJBIG2Segments(stream)
	if err != nil {
		t.Fatal(err)
	}
	// The segment ends where its coding does, and the one after it is
	// found.
	if len(segs) != 2 || !segs[0].unknownLength || len(segs[0].data) != len(body) {
		t.Fatalf("%d segments; the first is %d bytes (unknown length %v), want %d",
			len(segs), len(segs[0].data), segs[0].unknownLength, len(body))
	}
	// Only a generic region may do this.
	other := segmentWrapUnknown(1, body)
	other[4] = 6 // a text region
	if _, err := parseJBIG2Segments(other); err == nil {
		t.Error("a text region of unknown length was accepted")
	}
	// And with no end to find, the stream is malformed.
	if _, err := parseJBIG2Segments(segmentWrapUnknown(1, body[:30])); err == nil {
		t.Error("a segment of unknown length with no end was accepted")
	}
}

// TestJBIG2IntermediateRegionIsHeld: a region marked intermediate is
// material for a later refinement and is not itself drawn.
func TestJBIG2IntermediateRegionIsHeld(t *testing.T) {
	pic := jbig2Fixture(jbW, jbH)
	full := jbig2Region{w: jbW, h: jbH}
	dot := filled(2, 2, 1)
	stream := joinSegs(jbig2PageInfo(jbW, jbH),
		encodeGenericSeg(1, 36, full, pic, 0, false),
		encodeGenericSeg(2, 38, jbig2Region{w: 2, h: 2, x: 5, y: 5}, dot, 0, false))
	pix, err := decodeJBIG2(stream, nil, jbW, jbH)
	if err != nil {
		t.Fatal(err)
	}
	mine := &bitmap{w: jbW, h: jbH, pix: pix}
	comparePixels(t, "a held region beside a drawn one", mine, onPage(dot, 5, 5))
	comparePixels(t, "a held region beside a drawn one, against Poppler", mine,
		popplerDecodeJBIG2(t, stream, jbW, jbH))

	// With nothing but the held region there is nothing to show.
	only := joinSegs(jbig2PageInfo(jbW, jbH), encodeGenericSeg(1, 36, full, pic, 0, false))
	if _, err := decodeJBIG2(only, nil, jbW, jbH); err == nil {
		t.Error("a stream that draws nothing decoded without complaint")
	}
}

// TestJBIG2WhatIsStillRefused: the two things not decoded say so,
// rather than coming out as a wrong picture.
func TestJBIG2WhatIsStillRefused(t *testing.T) {
	pic := jbig2Fixture(jbW, jbH)
	body := genericBody(jbig2Region{w: jbW, h: jbH}, pic, 0, false)
	body[17] |= 0x10 // the extended template of the format's amendment
	_, err := decodeJBIG2(joinSegs(jbig2PageInfo(jbW, jbH), segmentWrap(1, 38, body)), nil, jbW, jbH)
	if err == nil || !strings.Contains(err.Error(), "extended template") {
		t.Errorf("an extended-template region: %v", err)
	}
	// A text region asking for a table the file did not bring.
	syms := jbGlyphs()
	dict, _ := encodeArithSymbolDict(1, syms, 0, 0, nil, false)
	seg := encodeTextSeg(2, 6, jbig2Region{w: jbW, h: jbH}, syms, nil, jbTextOpts{huff: true}, 1)
	segs, _ := parseJBIG2Segments(seg)
	raw := append([]byte(nil), segs[0].data...)
	raw[19+1] |= 3 // the first-symbol table: "custom", with none given
	_, err = decodeJBIG2(joinSegs(jbig2PageInfo(jbW, jbH), dict, segmentWrap(2, 6, raw, 1)), nil, jbW, jbH)
	if !errors.Is(err, errJBIG2) {
		t.Errorf("a text region naming a missing table: %v", err)
	}
}

// within runs f and fails if it has not come back in time, which is the
// property the damaged-input and bounded-work tests are about.
func within(t *testing.T, what string, limit time.Duration, f func()) {
	t.Helper()
	done := make(chan any, 1)
	go func() {
		defer func() { done <- recover() }()
		f()
	}()
	select {
	case e := <-done:
		if e != nil {
			t.Fatalf("%s panicked: %v", what, e)
		}
	case <-time.After(limit):
		t.Fatalf("%s has taken over %v; something in the stream is deciding "+
			"how much work to do", what, limit)
	}
}

// TestJBIG2DamagedStreamsAreSurvivable takes every valid case and breaks
// it: a byte changed, a tail cut off, garbage appended. A stream that
// starts out right gets deep into every decoding path before it goes
// wrong, which random bytes never do. The answer may be an error or a
// picture; it may not be a panic or a long wait.
func TestJBIG2DamagedStreamsAreSurvivable(t *testing.T) {
	rnd := rand.New(rand.NewSource(20261111))
	for _, c := range jbig2Cases() {
		for i := 0; i < 60; i++ {
			data := append([]byte(nil), c.data...)
			globals := append([]byte(nil), c.globals...)
			target := &data
			if len(globals) > 0 && i%3 == 0 {
				target = &globals
			}
			what := ""
			switch i % 4 {
			case 0, 1: // a byte or three changed
				for n := 1 + rnd.Intn(3); n > 0; n-- {
					(*target)[rnd.Intn(len(*target))] ^= byte(1 + rnd.Intn(255))
				}
				what = "with bytes changed"
			case 2: // cut short
				*target = (*target)[:rnd.Intn(len(*target))]
				what = "cut short"
			default: // a count or size made enormous
				at := rnd.Intn(len(*target))
				for j := 0; j < 4 && at+j < len(*target); j++ {
					(*target)[at+j] = 0xFF
				}
				what = "with a run of 0xFF"
			}
			within(t, c.name+" "+what, 10*time.Second, func() {
				decodeJBIG2(data, globals, c.w, c.h)
			})
		}
	}
}

// TestJBIG2NewPathsBoundedWork: the denial-of-service guard, for the
// paths the fuller decoder added. Each of these is a few dozen bytes
// claiming an enormous amount of work.
func TestJBIG2NewPathsBoundedWork(t *testing.T) {
	page := jbig2PageInfo(jbW, jbH)
	full := jbig2Region{w: jbW, h: jbH}
	pic := jbig2Fixture(jbW, jbH)
	syms := jbGlyphs()
	dict, _ := encodeArithSymbolDict(1, syms, 0, 0, nil, false)
	u32 := func(v uint32) []byte { return bytes32(v) }

	// A halftone grid of four thousand million cells.
	var hugeGrid bytes.Buffer
	jbRegionInfo(&hugeGrid, full)
	hugeGrid.WriteByte(0)
	hugeGrid.Write(u32(0xFFFF))
	hugeGrid.Write(u32(0xFFFF))
	hugeGrid.Write(make([]byte, 12))
	dots := []*bitmap{filled(4, 4, 0), filled(4, 4, 1)}

	// A pattern dictionary of sixty-five thousand patterns, 255 square.
	patterns := []byte{0, 255, 255, 0, 0, 0xFF, 0xFF, 0, 0, 0, 0}

	// A Huffman dictionary whose one height class claims symbols without
	// end, and one whose strip is as wide as the format can say.
	endless := &jbBits{}
	huffPut(endless, jbig2StdTable(4), 1, false)
	for i := 0; i < 4000; i++ {
		huffPut(endless, jbig2StdTable(2), 70000, false)
	}
	var wide bytes.Buffer
	jbPut16(&wide, 1)
	jbPut32(&wide, 60000)
	jbPut32(&wide, 60000)
	wide.Write(endless.out)

	// A text region of four million instances with nothing behind it,
	// in both codings.
	text := func(huff bool) []byte {
		seg := encodeTextSeg(2, 6, full, syms, nil, jbTextOpts{huff: huff}, 1)
		segs, _ := parseJBIG2Segments(seg)
		raw := append([]byte(nil), segs[0].data...)
		at := 19
		if huff {
			at = 21
		}
		copy(raw[at:], u32(1<<22))
		return joinSegs(page, dict, segmentWrap(2, 6, append(raw, make([]byte, 64)...), 1))
	}

	// A thousand refinements of the whole page, and a thousand regions
	// drawn over each other: each fits the page, and together they are a
	// thousand pages of work.
	var pile [][]byte
	pile = append(pile, page, encodeGenericSeg(1, 38, full, pic, 0, false))
	refine := encodeRefinementSeg(2, 42, jbig2Region{w: jbW, h: jbH, op: 4}, pic, pic, 0, false)
	for i := 0; i < 1000; i++ {
		pile = append(pile, refine)
	}
	var stack [][]byte
	stack = append(stack, page)
	mmr := encodeGenericMMRSeg(1, 38, full, pic, false)
	for i := 0; i < 1000; i++ {
		stack = append(stack, mmr)
	}

	// A symbol dictionary whose export flags never get anywhere.
	exports := encodeAggSymbolDict(2, syms, nil, true, 0, true, 1)

	cases := []struct {
		name string
		data []byte
	}{
		{"a halftone grid of 65535 by 65535", joinSegs(page,
			encodePatternDict(1, dots, 0, false), segmentWrap(2, 22, hugeGrid.Bytes(), 1))},
		{"a pattern dictionary of 65536 large patterns", joinSegs(page, segmentWrap(1, 16, patterns))},
		{"a height class of endless wide symbols", joinSegs(page, segmentWrap(1, 0, wide.Bytes()))},
		{"an arithmetic text region of four million instances", text(false)},
		{"a Huffman text region of four million instances", text(true)},
		{"a thousand refinements of the page", joinSegs(pile...)},
		{"a thousand regions drawn over each other", joinSegs(stack...)},
		{"a dictionary that only exports", joinSegs(page, dict, exports)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			within(t, c.name, 3*time.Second, func() {
				decodeJBIG2(c.data, nil, jbW, jbH)
			})
		})
	}

	// A stream earns work by its own length as well as by the size of
	// its page, so the piles above — long streams on a small page — are
	// decoded. What is refused is the stream that is short and asks for
	// a great deal: a thousand blank regions the size of a large page
	// are fifty bytes each and four million pixels each.
	const big = 2000
	blank := encodeGenericSeg(1, 38, jbig2Region{w: big, h: big}, newBitmap(big, big), 0, true)
	if len(blank) > 100 {
		t.Fatalf("a blank region coded to %d bytes; the case needs it small", len(blank))
	}
	heap := [][]byte{jbig2PageInfo(big, big)}
	for i := 0; i < 1000; i++ {
		heap = append(heap, blank)
	}
	var err error
	within(t, "a thousand blank regions on a large page", 3*time.Second, func() {
		_, err = decodeJBIG2(joinSegs(heap...), nil, big, big)
	})
	if !errors.Is(err, errJBIG2Work) {
		t.Errorf("a thousand blank regions on a large page: %v, want the work limit", err)
	}
	// One of them is an ordinary page and decodes.
	if _, err := decodeJBIG2(joinSegs(heap[:2]...), nil, big, big); err != nil {
		t.Errorf("one blank region on a large page: %v", err)
	}

	// And the limit is not in the way of honest streams: every case in
	// the list decodes with a small fraction of its allowance spent.
	for _, c := range jbig2Cases() {
		d := newJBIG2Decoder(newBitmap(c.w, c.h))
		start := d.work
		for _, part := range [][]byte{c.globals, c.data} {
			segs, err := parseJBIG2Segments(part)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range segs {
				if err := d.segment(s); err != nil {
					t.Fatalf("%s: %v", c.name, err)
				}
			}
		}
		if spent := start - d.work; spent > start/4 {
			t.Errorf("%s spent %d of an allowance of %d", c.name, spent, start)
		}
	}
}

// TestJBIG2SharedDictionaryOnAStrip: a document scanned in strips keeps
// one dictionary for images far smaller than it, holding symbols taller
// than a strip is. The dictionary has to decode for every strip.
func TestJBIG2SharedDictionaryOnAStrip(t *testing.T) {
	const w, h = 400, 8
	// Three hundred symbols of forty by thirty: ninety pages of this
	// strip, were they laid side by side.
	big := make([]*bitmap, 300)
	for i := range big {
		big[i] = jbShape(40, 30, i)
	}
	small := jbShape(5, 6, 999)
	symbols := append([]*bitmap{small}, big...)
	globals, _ := encodeArithSymbolDict(1, symbols, 0, 0, nil, false)
	places := []jbPlace{{id: 0, x: 3, y: 1}, {id: 0, x: 200, y: 2}}
	data := joinSegs(jbig2PageInfo(w, h),
		encodeTextSeg(2, 6, jbig2Region{w: w, h: h}, symbols, places, jbTextOpts{}, 1))

	pix, err := decodeJBIG2(data, globals, w, h)
	if err != nil {
		t.Fatalf("a strip with a large shared dictionary: %v", err)
	}
	mine := &bitmap{w: w, h: h, pix: pix}
	comparePixels(t, "the strip", mine, jbDraw(w, h, symbols, places, 0, 0))
	comparePixels(t, "the strip, against Poppler", mine,
		popplerDecodeJBIG2Globals(t, data, globals, w, h))
}
