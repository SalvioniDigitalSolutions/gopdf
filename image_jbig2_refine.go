package gopdf

// JBIG2 refinement.
//
// A refinement codes a bitmap as a correction to one the decoder already
// has. The context of each pixel is drawn from two places at once: the
// neighbours already decoded in the new bitmap, and the neighbourhood of
// the same spot in the reference. Where the two bitmaps agree, which is
// nearly everywhere, the coder has almost nothing to say.
//
// It is used three ways: a symbol in a dictionary coded as a variation
// of another symbol, a symbol on a page coded as a variation of its
// dictionary shape, and a whole region coded as a correction to what is
// already on the page — which is how a lossy first pass is made
// lossless.

// refinementContexts is how many contexts a refinement template uses:
// thirteen pixels for the first, ten for the second.
func refinementContexts(template int) int {
	if template == 0 {
		return 1 << 13
	}
	return 1 << 10
}

// decodeRefinement is the generic refinement region decoding procedure.
// dst is filled; ref is the bitmap being refined, offset by (dx, dy)
// with respect to dst.
func decodeRefinement(dst *bitmap, dec *mqDecoder, cx []mqContext, template int,
	ref *bitmap, dx, dy int, tpgron bool, at [][2]int) {

	// The context of the typical-prediction bit: the centre pixel of the
	// reference set and every other pixel clear.
	ltpCtx := 0x0008
	if template == 0 {
		ltpCtx = 0x0020
	}
	ltp := false
	for y := 0; y < dst.h; y++ {
		if tpgron && dec.decode(&cx[ltpCtx]) == 1 {
			ltp = !ltp
		}
		for x := 0; x < dst.w; x++ {
			if ltp {
				// A pixel whose whole neighbourhood in the reference is
				// one colour takes that colour without being coded.
				if v, ok := refinementTypical(ref, x-dx, y-dy); ok {
					dst.pix[y*dst.w+x] = v
					continue
				}
			}
			ctx := refinementContext(dst, ref, x, y, dx, dy, template, at)
			dst.pix[y*dst.w+x] = byte(dec.decode(&cx[ctx]))
		}
	}
}

// refinementTypical reports the colour of a reference pixel when the
// eight around it share it.
func refinementTypical(ref *bitmap, x, y int) (byte, bool) {
	m := ref.at(x, y)
	for j := -1; j <= 1; j++ {
		for i := -1; i <= 1; i++ {
			if ref.at(x+i, y+j) != m {
				return 0, false
			}
		}
	}
	return m, true
}

// refinementContext builds a pixel's context from the new bitmap and the
// reference.
func refinementContext(dst, ref *bitmap, x, y, dx, dy, template int, at [][2]int) int {
	c := func(i, j int) int { return int(dst.at(x+i, y+j)) }
	r := func(i, j int) int { return int(ref.at(x-dx+i, y-dy+j)) }
	if template == 0 {
		return c(0, -1)<<12 | c(1, -1)<<11 | c(-1, 0)<<10 |
			c(at[0][0], at[0][1])<<9 |
			r(0, -1)<<8 | r(1, -1)<<7 |
			r(-1, 0)<<6 | r(0, 0)<<5 | r(1, 0)<<4 |
			r(-1, 1)<<3 | r(0, 1)<<2 | r(1, 1)<<1 |
			r(at[1][0], at[1][1])
	}
	return c(-1, -1)<<9 | c(0, -1)<<8 | c(1, -1)<<7 | c(-1, 0)<<6 |
		r(0, -1)<<5 |
		r(-1, 0)<<4 | r(0, 0)<<3 | r(1, 0)<<2 |
		r(0, 1)<<1 | r(1, 1)
}

// refinementAT reads the two adaptive pixels a refinement of the first
// template carries, and supplies the nominal ones otherwise so a caller
// need not care which template it has.
func refinementAT(data []byte, p *int, template int) ([][2]int, bool) {
	if template != 0 {
		return [][2]int{{-1, -1}, {-1, -1}}, true
	}
	if *p+4 > len(data) {
		return nil, false
	}
	at := [][2]int{
		{int(int8(data[*p])), int(int8(data[*p+1]))},
		{int(int8(data[*p+2])), int(int8(data[*p+3]))},
	}
	*p += 4
	return at, true
}
