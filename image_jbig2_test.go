package gopdf

import (
	"bytes"
	"image"
	"math/rand"
	"testing"
)

// The JBIG2 decoder has no reference data to check against: no file in
// the corpus uses the format, and no encoder for it is installed. So it
// is checked by encoding: an MQ encoder is written here, streams are
// built with it, and the decoder has to recover them.
//
// That is worth more than it sounds. The encoder and the decoder are not
// mirror images — the encoder propagates a carry through bytes it has
// already emitted and stuffs a bit after 0xFF, the decoder reads that
// stuffing back and runs off the end of its own data on 0xFF — so a
// round trip exercises the parts most likely to be wrong. What it cannot
// catch is a shared misreading of the probability table, which is why
// the table is also checked against its own invariants.

// mqEncoder is the arithmetic encoder, written for the tests.
type mqEncoder struct {
	a, c  uint32
	ct    int
	bp    int
	out   []byte
	first bool
}

func newMQEncoder() *mqEncoder {
	return &mqEncoder{a: 0x8000, ct: 12, bp: -1, first: true}
}

func (e *mqEncoder) byteOut() {
	// The carry test is on bit 27, and a byte that came out as 0xFF is
	// followed by a seven-bit one so that no 0xFF can be followed by a
	// byte above 0x8F — which is how the decoder knows where the coded
	// data ends.
	if e.bp >= 0 && e.out[e.bp] == 0xFF {
		e.stuff()
		return
	}
	if e.c > 0x7FFFFFF {
		if e.bp >= 0 {
			e.out[e.bp]++
			if e.out[e.bp] == 0xFF {
				e.c &= 0x7FFFFFF
				e.stuff()
				return
			}
		}
	}
	e.out = append(e.out, byte(e.c>>19))
	e.bp++
	e.c &= 0x7FFFF
	e.ct = 8
}

// stuff emits the seven-bit byte that follows an 0xFF.
func (e *mqEncoder) stuff() {
	e.out = append(e.out, byte(e.c>>20))
	e.bp++
	e.c &= 0xFFFFF
	e.ct = 7
}

func (e *mqEncoder) renorm() {
	for {
		e.a <<= 1
		e.c <<= 1
		e.ct--
		if e.ct == 0 {
			e.byteOut()
		}
		if e.a&0x8000 != 0 {
			break
		}
	}
}

func (e *mqEncoder) encode(cx *mqContext, bit int) {
	st := &mqStates[cx.index]
	qe := st.qe
	if bit == int(cx.mps) {
		e.a -= qe
		if e.a&0x8000 == 0 {
			if e.a < qe {
				e.a = qe
			} else {
				e.c += qe
			}
			cx.index = st.nmps
			e.renorm()
		} else {
			e.c += qe
		}
		return
	}
	e.a -= qe
	if e.a < qe {
		e.c += qe
	} else {
		e.a = qe
	}
	if st.sw == 1 {
		cx.mps = 1 - cx.mps
	}
	cx.index = st.nlps
	e.renorm()
}

// flush empties the encoder, which needs the remaining bits pushed out
// and the last byte settled.
func (e *mqEncoder) flush() []byte {
	// SETBITS.
	temp := e.c + e.a
	e.c |= 0xFFFF
	if e.c >= temp {
		e.c -= 0x8000
	}
	e.c <<= uint(e.ct)
	e.byteOut()
	e.c <<= uint(e.ct)
	e.byteOut()
	if e.bp >= 0 && e.out[e.bp] != 0xFF {
		e.out = append(e.out, 0xFF)
	}
	e.out = append(e.out, 0xAC)
	return e.out
}

// TestMQCoderRoundTrip pushes bit sequences through the encoder and
// requires the decoder to give them back.
func TestMQCoderRoundTrip(t *testing.T) {
	rnd := rand.New(rand.NewSource(20260817))
	for _, c := range []struct {
		name string
		bits func(i int) int
		n    int
	}{
		{"all zero", func(int) int { return 0 }, 5000},
		{"all one", func(int) int { return 1 }, 5000},
		{"alternating", func(i int) int { return i & 1 }, 5000},
		{"mostly zero", func(int) int {
			if rnd.Intn(50) == 0 {
				return 1
			}
			return 0
		}, 5000},
		{"random", func(int) int { return rnd.Intn(2) }, 5000},
	} {
		want := make([]int, c.n)
		enc := newMQEncoder()
		ecx := make([]mqContext, 8)
		for i := range want {
			want[i] = c.bits(i)
			// Several contexts in rotation, as a real decoder uses.
			enc.encode(&ecx[i%len(ecx)], want[i])
		}
		data := enc.flush()

		dec := newMQDecoder(data)
		dcx := make([]mqContext, 8)
		for i, w := range want {
			if got := dec.decode(&dcx[i%len(dcx)]); got != w {
				t.Fatalf("%s: bit %d of %d decoded as %d, want %d (%d bytes coded)",
					c.name, i, c.n, got, w, len(data))
			}
		}
	}
}

// TestMQStateTable checks the probability table against the invariants
// the coder's definition gives it, which is the part a round trip
// cannot see: an encoder and a decoder sharing a wrong table agree with
// each other perfectly.
func TestMQStateTable(t *testing.T) {
	if len(mqStates) != 47 {
		t.Fatalf("%d states, want 47", len(mqStates))
	}
	for i, st := range mqStates {
		if st.qe == 0 || st.qe > 0x5601 {
			t.Errorf("state %d has Qe %#x, outside the defined range", i, st.qe)
		}
		if int(st.nmps) >= len(mqStates) || int(st.nlps) >= len(mqStates) {
			t.Errorf("state %d transitions outside the table: %d, %d",
				i, st.nmps, st.nlps)
		}
		if st.sw > 1 {
			t.Errorf("state %d has switch %d", i, st.sw)
		}
	}
	// The estimate never rises on the more-probable path, which is what
	// makes the coder converge.
	for i, st := range mqStates {
		if i == 0 || i == 6 || i == 14 || i == 46 {
			continue // the cycling states, which are allowed to hold
		}
		if mqStates[st.nmps].qe > st.qe {
			t.Errorf("state %d moves to a larger estimate on the probable path", i)
		}
	}
	// Only the first state of each run switches the sense.
	if mqStates[0].sw != 1 || mqStates[6].sw != 1 || mqStates[14].sw != 1 {
		t.Error("the switching states are not where they should be")
	}
}

// encodeGenericRegion is the encoder's side of the generic region
// procedure, so a bitmap can be made into a stream the decoder reads.
func encodeGenericRegion(b *bitmap, template int, at [][2]int) []byte {
	enc := newMQEncoder()
	cx := make([]mqContext, 1<<16)
	for y := 0; y < b.h; y++ {
		for x := 0; x < b.w; x++ {
			ctx := genericContext(b, x, y, template, at)
			enc.encode(&cx[ctx], int(b.at(x, y)))
		}
	}
	return enc.flush()
}

// TestJBIG2GenericRegionRoundTrip encodes a bitmap and requires the
// decoder to recover it, for every template.
func TestJBIG2GenericRegionRoundTrip(t *testing.T) {
	rnd := rand.New(rand.NewSource(4242))
	const w, h = 61, 43

	// A picture with structure: a border, a diagonal, a filled block and
	// some noise — the shapes a scan is made of.
	src := newBitmap(w, h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var v byte
			switch {
			case x == 0 || y == 0 || x == w-1 || y == h-1:
				v = 1
			case x == y:
				v = 1
			case x > 30 && x < 45 && y > 10 && y < 20:
				v = 1
			case rnd.Intn(23) == 0:
				v = 1
			}
			src.set(x, y, v)
		}
	}

	for template := 0; template < 4; template++ {
		at := defaultAT(template)
		data := encodeGenericRegion(src, template, at)

		got := newBitmap(w, h)
		dec := newMQDecoder(data)
		cx := make([]mqContext, 1<<16)
		if err := decodeGenericRegion(got, dec, cx, template, at, false); err != nil {
			t.Fatalf("template %d: %v", template, err)
		}
		for i := range src.pix {
			if got.pix[i] != src.pix[i] {
				t.Fatalf("template %d: pixel %d (%d,%d) is %d, want %d",
					template, i, i%w, i/w, got.pix[i], src.pix[i])
			}
		}
	}
}

// defaultAT is the adaptive template the specification suggests, which
// is what an encoder uses unless it has reason not to.
func defaultAT(template int) [][2]int {
	switch template {
	case 0:
		return [][2]int{{3, -1}, {-3, -1}, {2, -2}, {-2, -2}}
	case 1:
		return [][2]int{{3, -1}}
	case 2:
		return [][2]int{{2, -1}}
	default:
		return [][2]int{{2, -1}}
	}
}

// TestJBIG2SegmentParsing reads the framing an embedded stream uses.
func TestJBIG2SegmentParsing(t *testing.T) {
	// One segment: number 1, type 38 (immediate generic region), no
	// references, one-byte page association, four bytes of payload.
	seg := []byte{
		0, 0, 0, 1, // segment number
		38,         // flags: type 38, one-byte page association
		0x00,       // referred-to: none
		1,          // page 1
		0, 0, 0, 4, // data length
		'a', 'b', 'c', 'd',
	}
	segs, err := parseJBIG2Segments(seg)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 1 {
		t.Fatalf("%d segments, want 1", len(segs))
	}
	if segs[0].kind != 38 || string(segs[0].data) != "abcd" {
		t.Errorf("segment = %d %q", segs[0].kind, segs[0].data)
	}

	// Two of them in a row.
	two := append(append([]byte(nil), seg...), seg...)
	if segs, err := parseJBIG2Segments(two); err != nil || len(segs) != 2 {
		t.Errorf("two segments read as %d (%v)", len(segs), err)
	}

	// A length running past the end is a malformed stream, not a panic
	// and not a silent truncation.
	bad := append([]byte(nil), seg...)
	bad[10] = 0xFF
	if _, err := parseJBIG2Segments(bad); err == nil {
		t.Error("a segment claiming more data than there is was accepted")
	}
	// And a stream of nothing has no segments.
	if segs, err := parseJBIG2Segments(nil); err != nil || len(segs) != 0 {
		t.Errorf("an empty stream gave %d segments (%v)", len(segs), err)
	}
}

// TestJBIG2GarbageIsSurvivable: the decoder is fed a stream from a
// hostile source and must return rather than run away or panic.
func TestJBIG2GarbageIsSurvivable(t *testing.T) {
	rnd := rand.New(rand.NewSource(99))
	for i := 0; i < 200; i++ {
		n := rnd.Intn(200)
		data := make([]byte, n)
		rnd.Read(data)
		func() {
			defer func() {
				if e := recover(); e != nil {
					t.Fatalf("input %d panicked: %v", i, e)
				}
			}()
			decodeJBIG2(data, nil, 40, 30)
		}()
	}
	// A size nothing could hold is refused before anything is allocated.
	if _, err := decodeJBIG2(nil, nil, 1<<20, 1<<20); err == nil {
		t.Error("an implausible size was accepted")
	}
	if _, err := decodeJBIG2(nil, nil, 0, 10); err == nil {
		t.Error("a zero width was accepted")
	}
}

// TestJBIG2InAPage decodes an image through the ordinary image path,
// which is what a caller actually touches.
func TestJBIG2InAPage(t *testing.T) {
	const w, h = 24, 16
	src := newBitmap(w, h)
	for y := 4; y < 12; y++ {
		for x := 6; x < 18; x++ {
			src.set(x, y, 1)
		}
	}
	stream := jbig2Stream(t, src, 0)

	doc := New()
	doc.Compress = false
	doc.AddPage()
	r := NewReaderOrFail(t, docBytes(t, doc))
	u := Update(r)
	img := u.AddObject(NewStream(Dict{
		"Type": Name("XObject"), "Subtype": Name("Image"),
		"Width": int64(w), "Height": int64(h),
		"ColorSpace": Name("DeviceGray"), "BitsPerComponent": int64(1),
		"Filter": Name("JBIG2Decode"),
	}, stream))
	res, _ := r.InheritedPageValue(0, "Resources").(Dict)
	merged := res.Clone()
	if merged == nil {
		merged = Dict{}
	}
	merged["XObject"] = Dict{"Im0": img}
	if err := u.SetPageEntry(0, "Resources", merged); err != nil {
		t.Fatal(err)
	}
	page, err := u.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	page.op("q 200 0 0 100 100 600 cm /Im0 Do Q")
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}

	out := NewReaderOrFail(t, buf.Bytes())
	imgs, err := out.PageImages(0)
	if err != nil || len(imgs) != 1 {
		t.Fatalf("%d images (%v)", len(imgs), err)
	}
	if imgs[0].Filter != "JBIG2Decode" {
		t.Errorf("filter = %q", imgs[0].Filter)
	}
	m, err := imgs[0].Decode()
	if err != nil {
		t.Fatalf("decoding the scan: %v", err)
	}
	if b := m.Bounds(); b.Dx() != w || b.Dy() != h {
		t.Fatalf("decoded %dx%d, want %dx%d", b.Dx(), b.Dy(), w, h)
	}
	// A set bit is black, and everything else is white.
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			gray, _, _, _ := m.At(x, y).RGBA()
			black := gray>>8 < 128
			want := src.at(x, y) != 0
			if black != want {
				t.Fatalf("pixel (%d,%d) is black=%v, want %v", x, y, black, want)
			}
		}
	}
	// And the page renders with it.
	if _, err := out.RenderPage(0, RenderOpts{
		DPI: 72, IncludeVector: true, IncludeRasterImages: true,
	}); err != nil {
		t.Errorf("rendering a page with a JBIG2 image: %v", err)
	}
}

// jbig2Stream wraps a bitmap as an embedded JBIG2 stream of one
// immediate generic region.
func jbig2Stream(t *testing.T, b *bitmap, template int) []byte {
	t.Helper()
	at := defaultAT(template)
	coded := encodeGenericRegion(b, template, at)

	var region bytes.Buffer
	put32 := func(v int) {
		region.Write([]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
	}
	put32(b.w)
	put32(b.h)
	put32(0)                              // x
	put32(0)                              // y
	region.WriteByte(0)                   // combination operator: OR
	region.WriteByte(byte(template << 1)) // flags: arithmetic, no TPGDON
	for _, a := range at {
		region.Write([]byte{byte(int8(a[0])), byte(int8(a[1]))})
	}
	region.Write(coded)

	var seg bytes.Buffer
	seg.Write([]byte{0, 0, 0, 1}) // segment number
	seg.WriteByte(38)             // immediate generic region
	seg.WriteByte(0)              // no referred-to segments
	seg.WriteByte(1)              // page 1
	body := region.Bytes()
	seg.Write([]byte{byte(len(body) >> 24), byte(len(body) >> 16),
		byte(len(body) >> 8), byte(len(body))})
	seg.Write(body)
	return seg.Bytes()
}

// TestJBIG2TypicalPrediction covers the flag that makes the format
// small: a row the same as the one above is coded as a single bit.
func TestJBIG2TypicalPrediction(t *testing.T) {
	const w, h = 32, 24
	src := newBitmap(w, h)
	// Rows that repeat, which is what the prediction is for.
	for y := 8; y < 20; y++ {
		for x := 4; x < 28; x++ {
			src.set(x, y, 1)
		}
	}
	// Encoded with the prediction bit set, every row says whether it
	// repeats before its pixels are coded.
	enc := newMQEncoder()
	cx := make([]mqContext, 1<<16)
	ltp := false
	for y := 0; y < h; y++ {
		same := y > 0 && bytes.Equal(src.pix[y*w:(y+1)*w], src.pix[(y-1)*w:y*w])
		bit := 0
		if same != ltp {
			bit = 1
		}
		enc.encode(&cx[tpgdonContext(0)], bit)
		if bit == 1 {
			ltp = !ltp
		}
		if ltp {
			continue
		}
		for x := 0; x < w; x++ {
			enc.encode(&cx[genericContext(src, x, y, 0, defaultAT(0))], int(src.at(x, y)))
		}
	}
	data := enc.flush()

	got := newBitmap(w, h)
	dec := newMQDecoder(data)
	dcx := make([]mqContext, 1<<16)
	if err := decodeGenericRegion(got, dec, dcx, 0, defaultAT(0), true); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.pix, src.pix) {
		for y := 0; y < h; y++ {
			if !bytes.Equal(got.pix[y*w:(y+1)*w], src.pix[y*w:(y+1)*w]) {
				t.Fatalf("row %d differs:\n got %v\nwant %v", y,
					got.pix[y*w:(y+1)*w], src.pix[y*w:(y+1)*w])
			}
		}
	}
}

var _ = image.NewGray
