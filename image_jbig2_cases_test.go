package gopdf

import (
	"bytes"
	"fmt"

	"github.com/SalvioniDigitalSolutions/gopdf/internal/ccitt"
)

// The streams every test of the fuller decoder runs on.
//
// Each case is a complete embedded stream with the picture it must
// decode to — a picture arrived at here by drawing, not by decoding.
// The same list feeds the comparison with Poppler, the damaged-input
// test and the fuzz target's seeds, so a feature added to it is covered
// by all three.

type jbCase struct {
	name          string
	data, globals []byte
	w, h          int
	want          *bitmap
	// noPoppler says why Poppler is not a reference for this case, where
	// it is not.
	noPoppler string
}

// The page every case uses: small enough for the fuzz target, which
// decodes onto 64 by 64, and not a whole number of bytes wide — Poppler
// combines a region wrongly in the last byte of a row when the region
// ends exactly on a byte boundary (see popplerByteEdge), and a page 64
// wide would put every full-width region there.
const jbW, jbH = 61, 48

// Why Poppler cannot referee some cases. None of these is assumed; each
// was measured when the case was written.
//
//   - The prediction bit, the skipped cells and the inherited state:
//     a stream written the way Poppler is said here to read decodes in
//     Poppler to the intended picture, and one written as the
//     specification has it does not.
//   - The byte boundary: every pixel Poppler gets wrong is in the last
//     byte of a row, and the same picture one pixel narrower comes out
//     right.
//   - The left edge: a single symbol placed at a negative position is
//     drawn wrongly, and correctly once it is moved in.
//   - The row count: the rows that were coded come out right, and
//     Poppler goes on decoding below them.
//
// The PDF renderer in macOS gives Poppler's answer to every one of
// these, pixel for pixel — its JBIG2 code and Poppler's both descend
// from Xpdf — so it is no second opinion.
const (
	popplerByteEdge = "Poppler treats REPLACE as OR, and ignores AND, in the last " +
		"byte of a row when what it combines ends on a byte boundary"
	popplerPerPixel = "Poppler reads the typical-prediction bit of a refinement once " +
		"per pixel; the specification has it once per row"
	popplerLeftEdge = "Poppler misplaces a pattern that hangs off the left edge of its region"
	popplerSkipRule = "Poppler decides which halftone cells are skipped by adding the " +
		"pattern's size to a position still in 256ths of a pixel, so it skips cells " +
		"that reach in from the top or left"
	popplerRowCount = "Poppler decodes the height in the header and reads the row " +
		"count without using it"
	popplerNoInherit = "Poppler starts a dictionary afresh even when it is flagged as " +
		"carrying on from another's coder state"
)

// stamp combines src onto dst — the test's own statement of what the
// combination operators mean.
func stamp(dst, src *bitmap, x0, y0, op int) {
	for y := 0; y < src.h; y++ {
		for x := 0; x < src.w; x++ {
			dx, dy := x0+x, y0+y
			if dx < 0 || dy < 0 || dx >= dst.w || dy >= dst.h {
				continue
			}
			a, b := dst.pix[dy*dst.w+dx] != 0, src.pix[y*src.w+x] != 0
			var v bool
			switch op {
			case 0:
				v = a || b
			case 1:
				v = a && b
			case 2:
				v = a != b
			case 3:
				v = a == b
			default:
				v = b
			}
			dst.pix[dy*dst.w+dx] = 0
			if v {
				dst.pix[dy*dst.w+dx] = 1
			}
		}
	}
}

func filled(w, h int, v byte) *bitmap {
	b := newBitmap(w, h)
	for i := range b.pix {
		b.pix[i] = v
	}
	return b
}

// jbShape is a small bitmap with something in it, different for every
// seed.
func jbShape(w, h, seed int) *bitmap {
	b := newBitmap(w, h)
	state := uint32(seed*2654435761 + 12345)
	for i := range b.pix {
		state = state*1664525 + 1013904223
		if state>>24 < 140 {
			b.pix[i] = 1
		}
	}
	// A frame on two sides, so no shape is blank and its extent shows.
	for x := 0; x < w; x++ {
		b.pix[x] = 1
	}
	for y := 0; y < h; y++ {
		b.pix[y*w] = 1
	}
	return b
}

// jbGlyphs are the symbols the cases use, in the order a dictionary
// holds them: by height, and by width within a height.
func jbGlyphs() []*bitmap {
	sizes := [][2]int{{3, 5}, {6, 5}, {4, 7}, {5, 7}, {7, 9}, {9, 9}, {5, 12}}
	out := make([]*bitmap, len(sizes))
	for i, s := range sizes {
		out[i] = jbShape(s[0], s[1], i+1)
	}
	return out
}

// jbManyGlyphs is a dictionary much larger than what a page uses.
func jbManyGlyphs(n int) []*bitmap {
	out := make([]*bitmap, n)
	for i := range out {
		out[i] = jbShape(3+i%4, 5+2*(i*3/n), 100+i)
	}
	return out
}

// jbRows lays symbols out in lines, as text is.
func jbRows(symbols []*bitmap, use []int, w, h int) []jbPlace {
	var out []jbPlace
	k := 0
	for y := 2; y+13 < h; y += 14 {
		for x := 2; ; k++ {
			id := use[k%len(use)]
			if x+symbols[id].w >= w-1 {
				break
			}
			out = append(out, jbPlace{id: id, x: x, y: y + k%3})
			x += symbols[id].w + 1 + k%3
		}
	}
	return out
}

func allOf(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// jbDraw is what a text region with these placements looks like.
func jbDraw(w, h int, symbols []*bitmap, places []jbPlace, op, defPixel int) *bitmap {
	out := filled(w, h, byte(defPixel))
	for _, p := range places {
		bm := symbols[p.id]
		if p.refined != nil {
			bm = p.refined
		}
		stamp(out, bm, p.x, p.y, op)
	}
	return out
}

// jbVary is a bitmap resembling another: a different size, shifted, and
// with some pixels changed — what a refinement is for.
func jbVary(b *bitmap, dw, dh, shift, seed int) *bitmap {
	out := newBitmap(b.w+dw, b.h+dh)
	for y := 0; y < out.h; y++ {
		for x := 0; x < out.w; x++ {
			v := b.at(x-shift, y-shift)
			if (x*7+y*3+seed)%11 == 0 {
				v ^= 1
			}
			out.pix[y*out.w+x] = v
		}
	}
	return out
}

// onPage puts a region's picture where the region goes.
func onPage(region *bitmap, x, y int) *bitmap {
	out := newBitmap(jbW, jbH)
	stamp(out, region, x, y, 0)
	return out
}

func jbig2Cases() []jbCase {
	var out []jbCase
	page := jbig2PageInfo(jbW, jbH)
	full := jbig2Region{w: jbW, h: jbH}
	add := func(name string, want *bitmap, segs ...[]byte) *jbCase {
		out = append(out, jbCase{
			name: name, w: jbW, h: jbH, want: want,
			data: joinSegs(append([][]byte{page}, segs...)...),
		})
		return &out[len(out)-1]
	}
	pic := jbig2Fixture(jbW, jbH)
	part := jbig2Fixture(30, 20)
	partAt := jbig2Region{w: 30, h: 20, x: 9, y: 11}

	// --- Generic regions: MMR, and the typical-prediction shortcut for
	// the templates it had not been checked on.
	add("generic region, MMR", pic, encodeGenericMMRSeg(1, 38, full, pic, false))
	add("generic region, MMR with an end of block", pic,
		encodeGenericMMRSeg(1, 38, full, pic, true))
	add("generic region, MMR, part of the page", onPage(part, 9, 11),
		encodeGenericMMRSeg(1, 38, partAt, part, false))
	blank := newBitmap(jbW, jbH)
	stamp(blank, filled(20, 3, 1), 30, 40, 0)
	add("generic region, MMR, mostly blank", blank,
		encodeGenericMMRSeg(1, 39, full, blank, false))
	bars := newBitmap(jbW, jbH)
	for y := 6; y < 40; y++ {
		for x := 5; x < 60; x++ {
			if y%12 < 7 {
				bars.pix[y*jbW+x] = 1
			}
		}
	}
	for template := 0; template < 4; template++ {
		add(fmt.Sprintf("generic region, typical prediction, template %d", template),
			bars, encodeGenericSeg(1, 38, full, bars, template, true))
	}

	// --- Huffman symbol dictionaries, under an ordinary text region.
	syms := jbGlyphs()
	places := jbRows(syms, allOf(len(syms)), jbW, jbH)
	text := jbDraw(jbW, jbH, syms, places, 0, 0)
	for _, d := range []struct {
		name string
		o    jbHuffDict
	}{
		{"strips stored plain", jbHuffDict{}},
		{"strips as MMR", jbHuffDict{mmr: true}},
		{"signed tables", jbHuffDict{dhSel: 1, dwSel: 1}},
		{"signed tables, strips as MMR", jbHuffDict{dhSel: 1, dwSel: 1, mmr: true}},
	} {
		add("Huffman dictionary, "+d.name+", arithmetic text", text,
			encodeHuffSymbolDict(1, syms, d.o),
			encodeTextSeg(2, 6, full, syms, places, jbTextOpts{}, 1))
		add("Huffman dictionary, "+d.name+", Huffman text", text,
			encodeHuffSymbolDict(1, syms, d.o),
			encodeTextSeg(2, 6, full, syms, places, jbTextOpts{huff: true}, 1))
	}

	// --- Text regions in every layout, both codings, over an
	// arithmetic dictionary.
	dict, _ := encodeArithSymbolDict(1, syms, 0, 0, nil, false)
	// Refinement fields always name a table, used or not.
	const stdRefine = 1<<6 | 1<<8 | 1<<10 | 1<<12
	for _, v := range []struct {
		name string
		o    jbTextOpts
	}{
		{"plain", jbTextOpts{}},
		{"second tables", jbTextOpts{huffFlags: 1 | 1<<2 | 1<<4}},
		{"third tables", jbTextOpts{huffFlags: 2<<2 | 2<<4}},
		{"four rows a strip", jbTextOpts{logStrips: 2}},
		{"eight rows a strip, with an offset", jbTextOpts{logStrips: 3, dsOffset: 3}},
		{"a negative offset", jbTextOpts{dsOffset: -2}},
		{"from the bottom left", jbTextOpts{refCorner: 0}},
		{"from the top left", jbTextOpts{refCorner: 1}},
		{"from the bottom right", jbTextOpts{refCorner: 2, logStrips: 1}},
		{"from the top right", jbTextOpts{refCorner: 3}},
		{"transposed, bottom left", jbTextOpts{transposed: true, refCorner: 0}},
		{"transposed, top left", jbTextOpts{transposed: true, refCorner: 1, logStrips: 2}},
		{"transposed, bottom right", jbTextOpts{transposed: true, refCorner: 2}},
		{"transposed, top right", jbTextOpts{transposed: true, refCorner: 3, dsOffset: 1}},
		{"uneven codes", jbTextOpts{codeLens: []int{2, 2, 3, 3, 3, 4, 4}}},
	} {
		for _, huff := range []bool{false, true} {
			o := v.o
			o.huff = huff
			coding := "arithmetic"
			if huff {
				coding = "Huffman"
				o.huffFlags |= stdRefine
			} else if o.codeLens != nil || o.huffFlags != 0 {
				continue // a Huffman matter only
			}
			add(coding+" text, "+v.name, text, dict,
				encodeTextSeg(2, 6, full, syms, places, o, 1))
		}
	}
	// A region that is only part of the page, combined by its own
	// operator and starting out black.
	inner := jbig2Region{w: 50, h: 34, x: 8, y: 9}
	innerPlaces := jbRows(syms, allOf(len(syms)), inner.w, inner.h)
	for _, huff := range []bool{false, true} {
		for _, c := range []struct {
			name      string
			op, start int
		}{{"or", 0, 0}, {"and on black", 1, 1}, {"xor", 2, 0}, {"xnor on black", 3, 1}} {
			// Each symbol twice, the second overlapping the first, so
			// the operator has something to do.
			// (Kept clear of byte boundaries: see popplerByteEdge.)
			var twice []jbPlace
			for _, p := range innerPlaces {
				for e := p.x + syms[p.id].w; e%8 == 0 || (e+1)%8 == 0; e++ {
					p.x++
				}
				twice = append(twice, p, jbPlace{id: p.id, x: p.x + 1, y: p.y + 1})
			}
			coding := "arithmetic"
			if huff {
				coding = "Huffman"
			}
			add(coding+" text, part of the page, symbols combined by "+c.name,
				onPage(jbDraw(inner.w, inner.h, syms, twice, c.op, c.start), inner.x, inner.y),
				dict, encodeTextSeg(2, 7, inner, syms, twice, jbTextOpts{
					huff: huff, combOp: c.op, defPixel: c.start, huffFlags: stdRefine,
				}, 1))
		}
	}

	// A dictionary far larger than the page's use of it, so the table
	// of symbol codes has runs to code: unused symbols by the dozen and
	// by the handful, and a length repeated.
	many := jbManyGlyphs(40)
	lens := make([]int, 40)
	for _, i := range []int{12, 13, 14, 15, 16, 24} {
		lens[i] = 3
	}
	lens[21] = 2
	manyPlaces := jbRows(many, []int{12, 21, 13, 14, 24, 15, 16}, jbW, jbH)
	manyDict, _ := encodeArithSymbolDict(1, many, 0, 0, nil, false)
	add("Huffman text, a large dictionary sparsely used",
		jbDraw(jbW, jbH, many, manyPlaces, 0, 0), manyDict,
		encodeTextSeg(2, 6, full, many, manyPlaces,
			jbTextOpts{huff: true, codeLens: lens, huffFlags: stdRefine}, 1))

	// --- Tables a file brings with it.
	fsLines := []huffLine{hn(-64, 3, 6), hn(0, 1, 5), hn(32, 2, 5), hn(64, 3, 7)}
	dsLines := []huffLine{hn(-8, 3, 3), hn(0, 1, 3), hn(8, 4, 5)}
	dtLines := []huffLine{hn(1, 1, 0), hn(2, 2, 2), hn(6, 3, 4)}
	dhLines := []huffLine{hn(1, 1, 2), hn(5, 2, 3)}
	bmLines := []huffLine{hn(0, 1, 4), hn(16, 2, 8), hn(272, 3, 16)}
	mustTable := func(seg []byte) *huffTable {
		segs, err := parseJBIG2Segments(seg)
		if err != nil {
			panic(err)
		}
		t, err := parseHuffTable(segs[0].data)
		if err != nil {
			panic(err)
		}
		return t
	}
	fsSeg := encodeTableSeg(10, fsLines, 0, 0, -1)
	dsSeg := encodeTableSeg(11, dsLines, 0, 4, 2)
	dtSeg := encodeTableSeg(12, dtLines, 0, 3, -1)
	dhSeg := encodeTableSeg(20, dhLines, 0, 2, -1)
	bmSeg := encodeTableSeg(21, bmLines, 0, 3, -1)
	// Tight lines, so every step along a line fits the small table.
	var tight []jbPlace
	for y, k := 2, 0; y < 40; y += 13 {
		for x := 2; x+10 < jbW; k++ {
			id := k % len(syms)
			tight = append(tight, jbPlace{id: id, x: x, y: y})
			x += syms[id].w + 2
		}
	}
	add("Huffman text and dictionary with their own tables",
		jbDraw(jbW, jbH, syms, tight, 0, 0),
		dhSeg, bmSeg, fsSeg, dsSeg, dtSeg,
		encodeHuffSymbolDict(1, syms, jbHuffDict{
			dhSel: 3, bmSel: 1, mmr: true,
			custom: []*huffTable{mustTable(dhSeg), mustTable(bmSeg)},
		}, 20, 21),
		encodeTextSeg(2, 6, full, syms, tight, jbTextOpts{
			huff: true, refCorner: 1, huffFlags: 3 | 3<<2 | 3<<4 | stdRefine,
			custom: []*huffTable{mustTable(fsSeg), mustTable(dsSeg), mustTable(dtSeg)},
		}, 1, 10, 11, 12))

	// --- Refinement regions: a rough picture first, then the
	// correction that makes it the right one.
	rough := newBitmap(jbW, jbH)
	copy(rough.pix, pic.pix)
	for i := range rough.pix {
		if (i%jbW*31+i/jbW*17)%13 == 0 {
			rough.pix[i] ^= 1
		}
	}
	roughPart := newBitmap(30, 20)
	for y := 0; y < 20; y++ {
		for x := 0; x < 30; x++ {
			roughPart.pix[y*30+x] = rough.at(9+x, 11+y)
		}
	}
	mended := newBitmap(jbW, jbH)
	copy(mended.pix, rough.pix)
	stamp(mended, part, 9, 11, 4)
	// A picture with flat areas, which is what the typical-prediction
	// shortcut of a refinement feeds on.
	flat := newBitmap(jbW, jbH)
	stamp(flat, filled(30, 20, 1), 10, 8, 0)
	stamp(flat, filled(12, 12, 1), 46, 30, 0)
	flatRough := newBitmap(jbW, jbH)
	copy(flatRough.pix, flat.pix)
	for _, p := range [][2]int{{10, 8}, {39, 27}, {25, 8}, {46, 30}, {3, 3}, {50, 35}} {
		flatRough.pix[p[1]*jbW+p[0]] ^= 1
	}
	for template := 0; template < 2; template++ {
		for _, tpgron := range []bool{false, true} {
			how := fmt.Sprintf("template %d", template)
			if tpgron {
				how += ", typical prediction"
			}
			replace := jbig2Region{w: jbW, h: jbH, op: 4}
			from := len(out)
			add("refinement of the page, "+how, pic,
				encodeGenericSeg(1, 38, full, rough, 0, false),
				encodeRefinementSeg(2, 42, replace, pic, rough, template, tpgron))
			add("refinement of flat areas of the page, "+how, flat,
				encodeGenericSeg(1, 38, full, flatRough, 0, false),
				encodeRefinementSeg(2, 43, replace, flat, flatRough, template, tpgron))
			add("refinement of part of the page, "+how, mended,
				encodeGenericSeg(1, 38, full, rough, 0, false),
				encodeRefinementSeg(2, 42, jbig2Region{w: 30, h: 20, x: 9, y: 11, op: 4},
					part, roughPart, template, tpgron))
			add("refinement of a region held back, "+how, pic,
				encodeGenericSeg(1, 36, full, rough, 0, false),
				encodeRefinementSeg(2, 42, full, pic, rough, template, tpgron, 1))
			if tpgron {
				for i := from; i < len(out); i++ {
					out[i].noPoppler = popplerPerPixel
				}
			}
		}
	}
	// A correction to a correction: the first is itself held back.
	rougher := newBitmap(jbW, jbH)
	copy(rougher.pix, rough.pix)
	for i := range rougher.pix {
		if i%7 == 3 {
			rougher.pix[i] ^= 1
		}
	}
	add("refinement of a refinement", pic,
		encodeGenericSeg(1, 36, full, rougher, 1, false),
		encodeRefinementSeg(2, 40, full, rough, rougher, 1, false, 1),
		encodeRefinementSeg(3, 42, full, pic, rough, 0, false, 2))

	// --- Symbols corrected where they are placed.
	refinedPlaces := append([]jbPlace(nil), places...)
	for i := range refinedPlaces {
		p := &refinedPlaces[i]
		switch i % 5 {
		case 1: // the same size, some pixels changed
			p.refined = jbVary(syms[p.id], 0, 0, 0, i)
		case 2: // larger, shifted
			p.refined = jbVary(syms[p.id], 2, 1, 1, i)
			p.rdx = -1
		case 3: // smaller
			p.refined = jbVary(syms[p.id], -1, -2, 0, i)
			p.rdy = 1
		}
	}
	// Refining changes sizes, so the lines are laid out again.
	relaid := func(ps []jbPlace) []jbPlace {
		out := append([]jbPlace(nil), ps...)
		x, y := 2, 2
		for i := range out {
			bm := syms[out[i].id]
			if out[i].refined != nil {
				bm = out[i].refined
			}
			if x+bm.w >= jbW-1 {
				x, y = 2, y+15
			}
			out[i].x, out[i].y = x, y
			x += bm.w + 2
		}
		return out
	}
	refinedPlaces = relaid(refinedPlaces)
	refinedText := jbDraw(jbW, jbH, syms, refinedPlaces, 0, 0)
	for rTemplate := 0; rTemplate < 2; rTemplate++ {
		add(fmt.Sprintf("arithmetic text with refined symbols, template %d", rTemplate),
			refinedText, dict,
			encodeTextSeg(2, 6, full, syms, refinedPlaces,
				jbTextOpts{refine: true, rTemplate: rTemplate}, 1))
		add(fmt.Sprintf("arithmetic text with refined symbols, strips, template %d", rTemplate),
			refinedText, dict,
			encodeTextSeg(2, 6, full, syms, refinedPlaces,
				jbTextOpts{refine: true, rTemplate: rTemplate, logStrips: 2, refCorner: 2}, 1))
		add(fmt.Sprintf("Huffman text with refined symbols, template %d", rTemplate),
			refinedText, dict,
			encodeTextSeg(2, 6, full, syms, refinedPlaces, jbTextOpts{
				huff: true, refine: true, rTemplate: rTemplate, huffFlags: stdRefine,
			}, 1))
	}
	// One refinement, and nothing after it, with each of the tables its
	// four numbers can use.
	lastOnly := append([]jbPlace(nil), places...)
	for i := range lastOnly {
		lastOnly[i].refined = nil
	}
	// The last instance in coding order: the lowest bottom edge, and the
	// rightmost of those.
	last := 0
	for i, p := range lastOnly {
		q := lastOnly[last]
		pb, qb := p.y+syms[p.id].h, q.y+syms[q.id].h
		if pb > qb || (pb == qb && p.x > q.x) {
			last = i
		}
	}
	lastOnly[last].refined = jbVary(syms[lastOnly[last].id], 0, 0, 0, 5)
	for sel, table := range []string{"small", "large"} {
		hf := sel<<6 | sel<<8 | sel<<10 | sel<<12
		add("Huffman text ending in a refined symbol, "+table+" tables",
			jbDraw(jbW, jbH, syms, lastOnly, 0, 0), dict,
			encodeTextSeg(2, 6, full, syms, lastOnly,
				jbTextOpts{huff: true, refine: true, huffFlags: hf}, 1))
	}

	// --- Symbols built out of other symbols.
	a := jbVary(syms[0], 0, 0, 0, 1)
	b := jbVary(syms[1], 1, 0, 1, 2)
	pairPlaces := []jbPlace{{id: 2, x: 0, y: 0}, {id: 3, x: 5, y: 0}}
	pair := jbDraw(10, 7, syms, pairPlaces, 0, 0)
	withA := append(append([]*bitmap(nil), syms...), a, b, pair)
	mixedPlaces := []jbPlace{
		{id: 4, x: 0, y: 0, refined: jbVary(syms[4], 0, 0, 0, 3)},
		{id: 7, x: 8, y: 2},
		{id: 0, x: 8, y: 0, refined: jbVary(syms[0], 1, -1, 0, 4), rdx: 1},
	}
	mixed := jbDraw(12, 9, withA, mixedPlaces, 0, 0)
	agg := []jbAggSym{
		{bm: a, of: 0},
		{bm: b, of: 1, rdx: 1},
		{bm: pair, places: pairPlaces},
		{bm: mixed, places: mixedPlaces},
	}
	built := append(append([]*bitmap(nil), syms...), a, b, pair, mixed)
	builtPlaces := []jbPlace{
		{id: 7, x: 2, y: 2}, {id: 8, x: 8, y: 2}, {id: 9, x: 18, y: 2}, {id: 10, x: 31, y: 2},
		{id: 0, x: 46, y: 2}, {id: 10, x: 2, y: 16}, {id: 9, x: 17, y: 17}, {id: 6, x: 30, y: 16},
		{id: 7, x: 38, y: 16}, {id: 8, x: 44, y: 18}, {id: 10, x: 6, y: 32}, {id: 7, x: 22, y: 34},
	}
	newOnly := []jbPlace{
		{id: 0, x: 2, y: 2}, {id: 1, x: 8, y: 2}, {id: 2, x: 18, y: 2}, {id: 3, x: 31, y: 2},
		{id: 3, x: 2, y: 16}, {id: 2, x: 17, y: 17}, {id: 0, x: 30, y: 16},
	}
	for _, huff := range []bool{false, true} {
		for rTemplate := 0; rTemplate < 2; rTemplate++ {
			coding := "arithmetic"
			if huff {
				coding = "Huffman"
			}
			how := fmt.Sprintf("%s, template %d", coding, rTemplate)
			add("dictionary of symbols built from others, "+how,
				jbDraw(jbW, jbH, built, builtPlaces, 0, 0), dict,
				encodeAggSymbolDict(2, syms, agg, huff, rTemplate, true, 1),
				encodeTextSeg(3, 6, full, built, builtPlaces, jbTextOpts{}, 2))
			add("dictionary of symbols built from others, passing on only its own, "+how,
				jbDraw(jbW, jbH, built[len(syms):], newOnly, 0, 0), dict,
				encodeAggSymbolDict(2, syms, agg, huff, rTemplate, false, 1),
				encodeTextSeg(3, 6, full, built[len(syms):], newOnly, jbTextOpts{}, 2))
		}
	}

	// --- Halftones.
	bayer := [4][4]int{{0, 8, 2, 10}, {12, 4, 14, 6}, {3, 11, 1, 9}, {15, 7, 13, 5}}
	dots := make([]*bitmap, 16)
	for k := range dots {
		dots[k] = newBitmap(4, 4)
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				if bayer[y][x] < k {
					dots[k].pix[y*4+x] = 1
				}
			}
		}
	}
	five := make([]*bitmap, 5)
	for k := range five {
		five[k] = jbShape(3, 5, 40+k)
	}
	halftone := func(r jbig2Region, h jbHalftone, patterns []*bitmap) *bitmap {
		region := filled(r.w, r.h, byte(h.defPixel))
		for m := 0; m < h.gh; m++ {
			for n := 0; n < h.gw; n++ {
				x, y := h.cell(m, n)
				stamp(region, patterns[h.vals[m*h.gw+n]], x, y, h.combOp)
			}
		}
		out := newBitmap(jbW, jbH)
		stamp(out, region, r.x, r.y, r.op)
		return out
	}
	grid := func(gw, gh, levels int) []int {
		vals := make([]int, gw*gh)
		for i := range vals {
			vals[i] = (i%gw*3 + i/gw*5 + i*i/7) % levels
		}
		return vals
	}
	square := jbHalftone{gw: 16, gh: 12, rx: 4 << 8, vals: grid(16, 12, 16)}
	for template := 0; template < 4; template++ {
		h := square
		h.template = template
		add(fmt.Sprintf("halftone, square grid, template %d", template),
			halftone(full, h, dots),
			encodePatternDict(1, dots, template, false),
			encodeHalftoneSeg(2, 22, full, h, dots, 1))
	}
	squareMMR := square
	squareMMR.mmr = true
	add("halftone, square grid, MMR", halftone(full, squareMMR, dots),
		encodePatternDict(1, dots, 0, true),
		encodeHalftoneSeg(2, 23, full, squareMMR, dots, 1))
	// A screen at an angle, as a printer's is, running off the region.
	tilted := jbHalftone{
		gw: 22, gh: 18, gx: 2 << 8, gy: 14 << 8, rx: 868, ry: 316,
		vals: grid(22, 18, 16),
	}
	for _, v := range []struct {
		name      string
		mod       func(*jbHalftone)
		noPoppler string
	}{
		{"plain", func(h *jbHalftone) {}, ""},
		{"cells off the region skipped", func(h *jbHalftone) { h.skip = true }, popplerSkipRule},
		{"skipped, template 2", func(h *jbHalftone) { h.skip, h.template = true, 2 }, popplerSkipRule},
		{"MMR", func(h *jbHalftone) { h.mmr = true }, ""},
		{"patterns combined by xor", func(h *jbHalftone) { h.combOp = 2 }, ""},
		{"patterns combined by xnor, on black", func(h *jbHalftone) { h.combOp, h.defPixel = 3, 1 }, ""},
		{"patterns replacing", func(h *jbHalftone) { h.combOp = 4 }, ""},
		// Patterns four wide end on a byte boundary every other time.
		{"patterns combined by and, on black", func(h *jbHalftone) { h.combOp, h.defPixel = 1, 1 },
			popplerByteEdge},
		{"hanging off the left edge", func(h *jbHalftone) { h.gx = -(6 << 8) }, popplerLeftEdge},
		{"hanging off the left edge, skipped", func(h *jbHalftone) { h.gx, h.skip = -(6 << 8), true },
			popplerLeftEdge},
	} {
		h := tilted
		v.mod(&h)
		c := add("halftone, tilted grid, "+v.name, halftone(full, h, dots),
			encodePatternDict(1, dots, h.template, h.mmr),
			encodeHalftoneSeg(2, 22, full, h, dots, 1))
		c.noPoppler = v.noPoppler
	}
	// Skipping on a grid where every cell is either wholly off the
	// region or starts inside it, which is where Poppler's rule and the
	// specification's give the same answer.
	for template := 0; template < 4; template++ {
		h := jbHalftone{
			gw: 16, gh: 13, gx: -(9 << 8), gy: -(9 << 8), rx: 5 << 8,
			vals: grid(16, 13, 16), skip: true, template: template,
		}
		add(fmt.Sprintf("halftone, cells wholly off the region skipped, template %d", template),
			halftone(full, h, dots),
			encodePatternDict(1, dots, template, false),
			encodeHalftoneSeg(2, 22, full, h, dots, 1))
	}
	// The two operators Poppler gets wrong at a byte boundary, on a grid
	// that never puts a pattern's edge on one.
	apart := jbHalftone{gw: 7, gh: 5, gx: 1 << 8, gy: 1 << 8, rx: 8 << 8, vals: grid(7, 5, 5), defPixel: 1}
	for _, v := range []struct {
		name string
		op   int
	}{{"and", 1}, {"replace", 4}} {
		h := apart
		h.combOp = v.op
		add("halftone, patterns clear of byte boundaries, combined by "+v.name+" on black",
			halftone(full, h, five),
			encodePatternDict(1, five, 0, false),
			encodeHalftoneSeg(2, 22, full, h, five, 1))
	}
	// A number of patterns that is not a power of two, on part of the
	// page.
	odd := jbHalftone{gw: 12, gh: 5, rx: 3 << 8, gy: 0, vals: grid(12, 5, 5)}
	odd.ry = 0
	oddAt := jbig2Region{w: 36, h: 25, x: 14, y: 12}
	// The grid's rows step down by the pattern height through rx only
	// when the grid is square; these patterns are taller than wide, so
	// the rows overlap — which is allowed, and worth having.
	add("halftone, five patterns, part of the page", halftone(oddAt, odd, five),
		encodePatternDict(1, five, 1, false),
		encodeHalftoneSeg(2, 22, oddAt, odd, five, 1))
	one := jbHalftone{gw: 8, gh: 6, rx: 4 << 8, vals: make([]int, 48)}
	add("halftone, a single pattern", halftone(full, one, dots[9:10]),
		encodePatternDict(1, dots[9:10], 0, false),
		encodeHalftoneSeg(2, 22, full, one, dots[9:10], 1))

	// --- A generic region that does not say how long it is.
	rows := func(n int) []byte { return []byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)} }
	for _, template := range []int{0, 2} {
		add(fmt.Sprintf("generic region of unknown length, template %d", template), pic,
			segmentWrapUnknown(1, append(genericBody(full, pic, template, false), rows(jbH)...)))
	}
	var mmrBody bytes.Buffer
	jbRegionInfo(&mmrBody, full)
	mmrBody.WriteByte(1)
	mmrBody.Write(ccitt.EncodeMMR(pic.pix, pic.w, pic.h, true))
	mmrBody.Write([]byte{0, 0})
	mmrBody.Write(rows(jbH))
	add("generic region of unknown length, MMR", pic, segmentWrapUnknown(1, mmrBody.Bytes()))
	// The header's height is a guess; the count after the data is the
	// truth.
	top := newBitmap(jbW, 30)
	copy(top.pix, pic.pix)
	short := genericBody(jbig2Region{w: jbW, h: 30}, top, 0, false)
	short[7] = jbH // the height in the header, larger than what follows
	c := add("generic region of unknown length, fewer rows than the header says",
		onPage(top, 0, 0), segmentWrapUnknown(1, append(short, rows(30)...)))
	c.noPoppler = popplerRowCount

	// --- How regions combine with the page.
	over := jbig2Fixture(20, 15)
	for op, name := range []string{"or", "and", "xor", "xnor", "replace"} {
		want := newBitmap(jbW, jbH)
		copy(want.pix, pic.pix)
		stamp(want, over, 10, 5, op)
		add("region combined by "+name, want,
			encodeGenericSeg(1, 38, full, pic, 0, false),
			encodeGenericSeg(2, 38, jbig2Region{w: 20, h: 15, x: 10, y: 5, op: op}, over, 0, false))
	}
	black := jbPageInfoFlags(jbW, jbH, 1|4)
	for op, name := range []string{"or", "and", "xor", "xnor", "replace"} {
		want := filled(jbW, jbH, 1)
		stamp(want, pic, 0, 0, op)
		out = append(out, jbCase{
			name: "page starting black, region combined by " + name,
			w:    jbW, h: jbH, want: want,
			data: joinSegs(black, encodeGenericSeg(1, 38, jbig2Region{w: jbW, h: jbH, op: op}, pic, 0, false)),
		})
	}

	// The same on a page that is a whole number of bytes wide, where
	// Poppler's answer for two of the operators is not the right one.
	for _, op := range []int{1, 4} {
		const wide = 64
		widePic := jbig2Fixture(wide, jbH)
		want := filled(wide, jbH, 1)
		stamp(want, widePic, 0, 0, op)
		out = append(out, jbCase{
			name: fmt.Sprintf("page starting black and a whole number of bytes wide, operator %d", op),
			w:    wide, h: jbH, want: want, noPoppler: popplerByteEdge,
			data: joinSegs(jbPageInfoFlags(wide, jbH, 1|4),
				encodeGenericSeg(1, 38, jbig2Region{w: wide, h: jbH, op: op}, widePic, 0, false)),
		})
	}

	// --- Segments that carry no pixels.
	add("segments with nothing to draw around the region", pic,
		segmentWrap(1, 62, []byte{0x20, 0, 0, 0, 'n', 'o', 't', 'e'}),
		segmentWrap(2, 52, []byte{0, 0, 0, 1, 1, 1, 1, 1, 1}),
		encodeGenericSeg(3, 38, full, pic, 0, false),
		segmentWrap(4, 50, []byte{0, 0, 0, jbH - 1}),
		segmentWrap(5, 49, nil))

	// --- One symbol, whose number takes no bits at all.
	lone := syms[3:4]
	lonePlaces := []jbPlace{{x: 2, y: 2}, {x: 9, y: 2}, {x: 16, y: 3}, {x: 4, y: 13}, {x: 30, y: 30}}
	loneDict, _ := encodeArithSymbolDict(1, lone, 0, 0, nil, false)
	add("a dictionary of one symbol", jbDraw(jbW, jbH, lone, lonePlaces, 0, 0), loneDict,
		encodeTextSeg(2, 6, full, lone, lonePlaces, jbTextOpts{}, 1))
	add("a Huffman dictionary of one symbol", jbDraw(jbW, jbH, lone, lonePlaces, 0, 0),
		encodeHuffSymbolDict(1, lone, jbHuffDict{}),
		encodeTextSeg(2, 6, full, lone, lonePlaces, jbTextOpts{huff: true, huffFlags: stdRefine}, 1))

	// --- A dictionary that starts from the coder state of the one
	// before it.
	first, state := encodeArithSymbolDict(1, syms[:4], 0, 0, nil, true)
	second, _ := encodeArithSymbolDict(2, syms[4:], 0, 4, state, false, 1)
	c = add("a dictionary carrying on from another's coder state", text, first, second,
		encodeTextSeg(3, 6, full, syms, places, jbTextOpts{}, 1, 2))
	c.noPoppler = popplerNoInherit

	// --- The dictionary in the globals stream, as a scanned document
	// keeps it, for the Huffman form too.
	out = append(out, jbCase{
		name: "Huffman dictionary in the globals", w: jbW, h: jbH, want: text,
		globals: encodeHuffSymbolDict(1, syms, jbHuffDict{mmr: true}),
		data: joinSegs(page, encodeTextSeg(2, 6, full, syms, places,
			jbTextOpts{huff: true, huffFlags: stdRefine}, 1)),
	})
	return out
}
