package gopdf

import (
	"bytes"
	"testing"
)

// The symbol-coded half of JBIG2 is checked the same way as the generic
// half: the encoding side of each procedure is written here, a page is
// coded with it, and the decoder has to give the page back.
//
// The integer decoder is the part most worth exercising. It is not a
// plain binary read — a sign, then a run of prefix bits choosing a
// magnitude range, then the value inside it — and every field of the
// format goes through it, so a mistake there corrupts everything
// downstream rather than one number.

// encodeInt is the encoding side of the integer procedure.
func (a *arithIntCtx) encodeInt(enc *mqEncoder, v int, oob bool) {
	prev := 1
	bit := func(b int) {
		enc.encode(&a.cx[prev], b)
		if prev < 256 {
			prev = prev<<1 | b
		} else {
			prev = (((prev<<1 | b) & 511) | 256)
		}
	}
	if oob {
		// The out-of-band value is a negative zero: sign set, magnitude
		// nothing.
		bit(1)
		bit(0)
		for i := 0; i < 2; i++ {
			bit(0)
		}
		return
	}
	sign := 0
	mag := v
	if v < 0 {
		sign, mag = 1, -v
	}
	bit(sign)

	// The ranges the procedure defines, smallest first.
	type band struct {
		n, base int
	}
	bands := []band{{2, 0}, {4, 4}, {6, 20}, {8, 84}, {12, 340}, {32, 4436}}
	chosen := len(bands) - 1
	for i, b := range bands {
		if mag < b.base+(1<<uint(b.n)) {
			chosen = i
			break
		}
	}
	for i := 0; i < chosen; i++ {
		bit(1)
	}
	if chosen < len(bands)-1 {
		bit(0)
	}
	val := mag - bands[chosen].base
	for i := bands[chosen].n - 1; i >= 0; i-- {
		bit((val >> uint(i)) & 1)
	}
}

// encodeID is the encoding side of the symbol-number procedure.
func (a *arithIDCtx) encodeID(enc *mqEncoder, id int) {
	prev := 1
	for i := a.codeLen - 1; i >= 0; i-- {
		b := (id >> uint(i)) & 1
		enc.encode(&a.cx[prev], b)
		prev = prev<<1 | b
	}
}

func TestArithIntRoundTrip(t *testing.T) {
	// One value from each magnitude range, and the boundaries between
	// them, since that is where an off-by-one shows.
	values := []int{0, 1, 3, 4, 5, 19, 20, 21, 83, 84, 85,
		339, 340, 341, 4435, 4436, 4437, 70000, -1, -4, -20, -84, -4436}

	ctx := newArithIntCtx()
	enc := newMQEncoder()
	for _, v := range values {
		ctx.encodeInt(enc, v, false)
	}
	ctx.encodeInt(enc, 0, true) // and the out-of-band value
	data := enc.flush()

	dctx := newArithIntCtx()
	dec := newMQDecoder(data)
	for _, want := range values {
		got, ok := dctx.decodeInt(dec)
		if !ok {
			t.Fatalf("%d decoded as out-of-band", want)
		}
		if got != want {
			t.Fatalf("decoded %d, want %d", got, want)
		}
	}
	if _, ok := dctx.decodeInt(dec); ok {
		t.Error("the out-of-band value decoded as a number")
	}
}

func TestArithIDRoundTrip(t *testing.T) {
	for _, n := range []int{1, 2, 3, 16, 17, 300} {
		codeLen := symbolCodeLen(n)
		enc := newMQEncoder()
		ectx := newArithIDCtx(codeLen)
		var want []int
		for id := 0; id < n; id++ {
			want = append(want, id)
			ectx.encodeID(enc, id)
		}
		data := enc.flush()

		dec := newMQDecoder(data)
		dctx := newArithIDCtx(codeLen)
		for i, w := range want {
			if got := dctx.decodeID(dec); got != w {
				t.Fatalf("%d symbols: id %d decoded as %d", n, i, got)
			}
		}
	}
}

func TestSymbolCodeLen(t *testing.T) {
	for _, c := range []struct{ n, want int }{
		{1, 1}, {2, 1}, {3, 2}, {4, 2}, {5, 3}, {8, 3}, {9, 4}, {256, 8}, {257, 9},
	} {
		if got := symbolCodeLen(c.n); got != c.want {
			t.Errorf("symbolCodeLen(%d) = %d, want %d", c.n, got, c.want)
		}
	}
}

// encodeSymbolDict builds a symbol dictionary segment for a set of
// symbols, sorted into height classes as the format requires.
func encodeSymbolDict(t *testing.T, symbols []*bitmap, template int) []byte {
	t.Helper()
	// Sorted by height, then width, which is what an encoder does so the
	// differences it codes are small.
	sorted := append([]*bitmap(nil), symbols...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0; j-- {
			a, b := sorted[j-1], sorted[j]
			if a.h > b.h || (a.h == b.h && a.w > b.w) {
				sorted[j-1], sorted[j] = b, a
				continue
			}
			break
		}
	}

	at := defaultAT(template)
	enc := newMQEncoder()
	cx := make([]mqContext, 1<<16)
	iadh, iadw := newArithIntCtx(), newArithIntCtx()
	iaex, iaai := newArithIntCtx(), newArithIntCtx()
	_ = iaai

	height := 0
	for i := 0; i < len(sorted); {
		enc2 := sorted[i].h
		iadh.encodeInt(enc, enc2-height, false)
		height = enc2
		width := 0
		for ; i < len(sorted) && sorted[i].h == height; i++ {
			iadw.encodeInt(enc, sorted[i].w-width, false)
			width = sorted[i].w
			encodeGenericInto(enc, cx, sorted[i], template, at)
		}
		iadw.encodeInt(enc, 0, true) // the height class ends
	}
	// Export every symbol: skip none, then take them all.
	iaex.encodeInt(enc, 0, false)
	iaex.encodeInt(enc, len(sorted), false)
	coded := enc.flush()

	var b bytes.Buffer
	flags := template << 10
	b.Write([]byte{byte(flags >> 8), byte(flags)})
	for _, a := range at {
		b.Write([]byte{byte(int8(a[0])), byte(int8(a[1]))})
	}
	put32 := func(v int) {
		b.Write([]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
	}
	put32(len(sorted)) // exported
	put32(len(sorted)) // new
	b.Write(coded)
	return segmentWrap(1, 0, b.Bytes())
}

// encodeGenericInto codes a bitmap into an encoder already in progress,
// which is what a symbol dictionary does: one coder for the whole
// dictionary, so it keeps learning across symbols.
func encodeGenericInto(enc *mqEncoder, cx []mqContext, b *bitmap,
	template int, at [][2]int) {
	for y := 0; y < b.h; y++ {
		for x := 0; x < b.w; x++ {
			enc.encode(&cx[genericContext(b, x, y, template, at)], int(b.at(x, y)))
		}
	}
}

// placement is one symbol put somewhere on a page.
type placement struct {
	id   int
	s, t int
}

// encodeTextRegion builds a text region segment placing symbols.
func encodeTextRegion(t *testing.T, w, h int, symbols []*bitmap,
	places []placement) []byte {
	t.Helper()

	enc := newMQEncoder()
	iadt, iafs := newArithIntCtx(), newArithIntCtx()
	iads := newArithIntCtx()
	iaid := newArithIDCtx(symbolCodeLen(len(symbols)))

	// One strip per distinct row, with the symbols of each in order.
	iadt.encodeInt(enc, 0, false) // the first strip is at zero
	stripT, firstS := 0, 0
	i := 0
	for i < len(places) {
		row := places[i].t
		iadt.encodeInt(enc, row-stripT, false)
		stripT = row

		iafs.encodeInt(enc, places[i].s-firstS, false)
		firstS = places[i].s
		curS := places[i].s

		for {
			iaid.encodeID(enc, places[i].id)
			curS += symbols[places[i].id].w - 1
			i++
			if i >= len(places) || places[i].t != row {
				break
			}
			iads.encodeInt(enc, places[i].s-curS, false)
			curS = places[i].s
		}
		iads.encodeInt(enc, 0, true) // the strip ends
	}
	coded := enc.flush()

	var b bytes.Buffer
	put32 := func(v int) {
		b.Write([]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
	}
	put32(w)
	put32(h)
	put32(0)       // x
	put32(0)       // y
	b.WriteByte(0) // combination operator
	// Flags: arithmetic, one row per strip, top-left corner, not
	// transposed, no offset.
	flags := 1 << 4 // refCorner = 1 (top left)
	b.Write([]byte{byte(flags >> 8), byte(flags)})
	put32(len(places))
	b.Write(coded)
	return segmentWrap(2, 6, b.Bytes(), 1)
}

// segmentWrap puts a segment header round a payload.
func segmentWrap(num, kind int, body []byte, refers ...int) []byte {
	var b bytes.Buffer
	b.Write([]byte{byte(num >> 24), byte(num >> 16), byte(num >> 8), byte(num)})
	b.WriteByte(byte(kind))
	// The referred-to segments, in the short form: a count in the top
	// three bits, then one byte per segment number. A text region that
	// names no dictionary has no symbols in scope, and a conforming
	// reader draws nothing at all — which is what Poppler did.
	if len(refers) > 4 {
		panic("the short form of the header holds four references")
	}
	b.WriteByte(byte(len(refers) << 5))
	for _, r := range refers {
		b.WriteByte(byte(r))
	}
	b.WriteByte(1) // page 1
	b.Write([]byte{byte(len(body) >> 24), byte(len(body) >> 16),
		byte(len(body) >> 8), byte(len(body))})
	b.Write(body)
	return b.Bytes()
}

// letterA and letterB are two small shapes to use as symbols.
func letterShapes() []*bitmap {
	a := newBitmap(5, 7)
	for _, p := range [][2]int{
		{2, 0}, {1, 1}, {3, 1}, {0, 2}, {4, 2}, {0, 3}, {1, 3}, {2, 3}, {3, 3},
		{4, 3}, {0, 4}, {4, 4}, {0, 5}, {4, 5}, {0, 6}, {4, 6},
	} {
		a.set(p[0], p[1], 1)
	}
	b := newBitmap(4, 7)
	for y := 0; y < 7; y++ {
		b.set(0, y, 1)
	}
	for _, p := range [][2]int{{1, 0}, {2, 0}, {3, 1}, {1, 3}, {2, 3}, {3, 4}, {1, 6}, {2, 6}} {
		b.set(p[0], p[1], 1)
	}
	return []*bitmap{a, b}
}

// TestJBIG2SymbolDictionaryRoundTrip codes two shapes into a dictionary
// and reads them back.
func TestJBIG2SymbolDictionaryRoundTrip(t *testing.T) {
	symbols := letterShapes()
	for template := 0; template < 4; template++ {
		seg := encodeSymbolDict(t, symbols, template)
		segs, err := parseJBIG2Segments(seg)
		if err != nil {
			t.Fatalf("template %d: %v", template, err)
		}
		if len(segs) != 1 || segs[0].kind != 0 {
			t.Fatalf("template %d: %d segments of kind %v", template,
				len(segs), segs[0].kind)
		}
		got, err := decodeSymbolDictionary(segs[0].data, nil)
		if err != nil {
			t.Fatalf("template %d: %v", template, err)
		}
		if len(got) != len(symbols) {
			t.Fatalf("template %d: %d symbols, want %d", template, len(got), len(symbols))
		}
		// Sorted by height then width on the way in, so the narrower of
		// two equal-height symbols comes first.
		want := []*bitmap{symbols[1], symbols[0]}
		for i, s := range got {
			if s.w != want[i].w || s.h != want[i].h {
				t.Fatalf("template %d: symbol %d is %dx%d, want %dx%d",
					template, i, s.w, s.h, want[i].w, want[i].h)
			}
			if !bytes.Equal(s.pix, want[i].pix) {
				t.Fatalf("template %d: symbol %d came back with different pixels",
					template, i)
			}
		}
	}
}

// TestJBIG2TextRegionRoundTrip codes a page of symbols and reads the page
// back — which is what a scanned page of prose actually is.
func TestJBIG2TextRegionRoundTrip(t *testing.T) {
	symbols := letterShapes()
	// Two lines of shapes, as a page of text is.
	places := []placement{
		{0, 2, 2}, {1, 9, 2}, {0, 15, 2},
		{1, 2, 12}, {0, 8, 12},
	}
	const w, h = 40, 24

	// What the page should look like: the symbols drawn where they go.
	want := newBitmap(w, h)
	for _, p := range places {
		drawSymbol(want, symbols[p.id], p.s, p.t, 1, false)
	}

	seg := encodeTextRegion(t, w, h, symbols, places)
	segs, err := parseJBIG2Segments(seg)
	if err != nil {
		t.Fatal(err)
	}
	page := newBitmap(w, h)
	if err := decodeTextRegion(segs[0].data, symbols, page); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(page.pix, want.pix) {
		for y := 0; y < h; y++ {
			if !bytes.Equal(page.pix[y*w:(y+1)*w], want.pix[y*w:(y+1)*w]) {
				t.Fatalf("row %d differs:\n got %v\nwant %v", y,
					page.pix[y*w:(y+1)*w], want.pix[y*w:(y+1)*w])
			}
		}
	}
}

// TestJBIG2SymbolCodedPage is the whole thing end to end: a dictionary
// and a text region in one stream, decoded through the image path.
func TestJBIG2SymbolCodedPage(t *testing.T) {
	symbols := letterShapes()
	places := []placement{{0, 2, 2}, {1, 9, 2}, {0, 16, 2}}
	const w, h = 32, 16

	want := newBitmap(w, h)
	// The dictionary sorts its symbols, so the ids a text region uses
	// refer to the sorted order.
	sorted := []*bitmap{symbols[1], symbols[0]}
	remap := map[int]int{0: 1, 1: 0}
	var sortedPlaces []placement
	for _, p := range places {
		sortedPlaces = append(sortedPlaces, placement{remap[p.id], p.s, p.t})
		drawSymbol(want, symbols[p.id], p.s, p.t, 1, false)
	}

	stream := append(encodeSymbolDict(t, symbols, 0),
		encodeTextRegion(t, w, h, sorted, sortedPlaces)...)

	pix, err := decodeJBIG2(stream, nil, w, h)
	if err != nil {
		t.Fatalf("decoding a symbol-coded page: %v", err)
	}
	if !bytes.Equal(pix, want.pix) {
		t.Errorf("the page came back different:\n got %v\nwant %v", pix, want.pix)
	}

	// And the globals stream is where a real scan keeps its dictionary,
	// so the same page must decode with the two split apart.
	pix2, err := decodeJBIG2(encodeTextRegion(t, w, h, sorted, sortedPlaces),
		encodeSymbolDict(t, symbols, 0), w, h)
	if err != nil {
		t.Fatalf("with the dictionary in the globals stream: %v", err)
	}
	if !bytes.Equal(pix2, want.pix) {
		t.Error("the page differs when the dictionary comes from the globals")
	}
}

// TestJBIG2SymbolGarbageIsSurvivable feeds the symbol path nonsense.
func TestJBIG2SymbolGarbageIsSurvivable(t *testing.T) {
	symbols := letterShapes()
	for i := 0; i < 60; i++ {
		data := make([]byte, i*3)
		for j := range data {
			data[j] = byte(i*7 + j*13)
		}
		func() {
			defer func() {
				if e := recover(); e != nil {
					t.Fatalf("symbol dictionary input %d panicked: %v", i, e)
				}
			}()
			decodeSymbolDictionary(data, nil)
		}()
		func() {
			defer func() {
				if e := recover(); e != nil {
					t.Fatalf("text region input %d panicked: %v", i, e)
				}
			}()
			decodeTextRegion(data, symbols, newBitmap(20, 20))
		}()
	}
	// A text region naming no symbols is an error, not a crash.
	if err := decodeTextRegion(make([]byte, 40), nil, newBitmap(10, 10)); err == nil {
		t.Error("a text region with no symbols was accepted")
	}
}
