package gopdf

import (
	"errors"

	"github.com/SalvioniDigitalSolutions/gopdf/internal/ccitt"
)

// JBIG2 halftone regions.
//
// A photograph printed in black and white is a grid of dots of varying
// size, and scanning one produces a bitmap that defeats every other
// part of the format: no two neighbourhoods repeat. A halftone region
// codes what the dots mean instead. A pattern dictionary holds one small
// bitmap per grey level; the region is a grid of grey values, coded as
// bit planes, and each value is drawn as its pattern — on a grid that
// may be rotated, the way a printer's screen is.

// patternSegment decodes a pattern dictionary: the patterns side by
// side in one bitmap, coded as a generic region and cut apart.
func (d *jbig2Decoder) patternSegment(s jbig2Segment) error {
	data := s.data
	if len(data) < 7 {
		return errJBIG2
	}
	flags := data[0]
	mmr := flags&1 != 0
	template := int(flags>>1) & 3
	pw, ph := int(data[1]), int(data[2])
	grayMax := be32(data, 3)
	if pw == 0 || ph == 0 || grayMax >= 1<<16 {
		return errJBIG2
	}
	n := int(grayMax) + 1
	w := n * pw
	if !d.sideFits(w, ph) {
		return errJBIG2
	}
	if err := d.spendArea(w, ph); err != nil {
		return err
	}

	all := newBitmap(w, ph)
	if mmr {
		pix, _, err := ccitt.DecodeMMR(data[7:], w, ph)
		if err != nil {
			return jbig2MMRError(err)
		}
		all.pix = pix
	} else {
		// The first adaptive pixel reaches back exactly one pattern, so
		// each pattern is predicted from the one before it.
		at := [][2]int{{-pw, 0}, {-3, -1}, {2, -2}, {-2, -2}}
		if template != 0 {
			at = at[:1]
		}
		dec := newMQDecoder(data[7:])
		cx := make([]mqContext, 1<<16)
		if err := decodeGenericRegion(all, dec, cx, template, at, false); err != nil {
			return err
		}
	}
	patterns := make([]*bitmap, n)
	for i := range patterns {
		pat := newBitmap(pw, ph)
		for y := 0; y < ph; y++ {
			copy(pat.pix[y*pw:(y+1)*pw], all.pix[y*w+i*pw:y*w+(i+1)*pw])
		}
		patterns[i] = pat
	}
	d.patterns[s.num] = patterns
	return nil
}

// halftoneSegment decodes a halftone region segment.
func (d *jbig2Decoder) halftoneSegment(s jbig2Segment) error {
	data := s.data
	info, ok := parseRegionInfo(data)
	if !ok || len(data) < 38 {
		return errJBIG2
	}
	flags := data[17]
	mmr := flags&1 != 0
	template := int(flags>>1) & 3
	enableSkip := flags&8 != 0
	combOp := int(flags>>4) & 7
	defPixel := flags >> 7
	gw, gh := int(be32(data, 18)), int(be32(data, 22))
	gx, gy := int(int32(be32(data, 26))), int(int32(be32(data, 30)))
	rx, ry := int(be16(data, 34)), int(be16(data, 36))
	coded := data[38:]

	var patterns []*bitmap
	for _, num := range s.refers {
		if p, ok := d.patterns[num]; ok {
			patterns = p
			break
		}
	}
	if len(patterns) == 0 {
		return errors.New("gopdf: JBIG2 halftone region refers to no patterns")
	}
	pw, ph := patterns[0].w, patterns[0].h
	if !regionFits(info.w, info.h, d.page) || !d.sideFits(gw, gh) {
		return errJBIG2
	}
	// How many bits a grey value takes.
	bpp := 0
	for 1<<uint(bpp) < len(patterns) {
		bpp++
	}
	grid := int64(gw) * int64(gh)
	if err := d.spend(int64(info.w)*int64(info.h) +
		grid*int64(bpp+1) + grid*int64(pw*ph)); err != nil {
		return err
	}

	// cell reports where a grid cell's pattern goes in the region.
	cell := func(m, n int) (x, y int) {
		return (gx + m*ry + n*rx) >> 8, (gy + m*rx - n*ry) >> 8
	}

	// The cells whose patterns would fall wholly outside the region can
	// be left out of the coding altogether.
	var skip *bitmap
	if enableSkip && !mmr {
		skip = newBitmap(gw, gh)
		for m := 0; m < gh; m++ {
			for n := 0; n < gw; n++ {
				x, y := cell(m, n)
				if x+pw <= 0 || x >= info.w || y+ph <= 0 || y >= info.h {
					skip.pix[m*gw+n] = 1
				}
			}
		}
	}

	// The grey values: one bit plane per bit, the most significant
	// first, each plane coded as the difference from the one above it.
	vals := make([]uint32, gw*gh)
	at := [][2]int{{3, -1}, {-3, -1}, {2, -2}, {-2, -2}}
	if template > 1 {
		at[0][0] = 2
	}
	if template != 0 {
		at = at[:1]
	}
	var dec *mqDecoder
	var cx []mqContext
	if !mmr {
		dec = newMQDecoder(coded)
		cx = make([]mqContext, 1<<16)
	}
	var above *bitmap
	for j := bpp - 1; j >= 0; j-- {
		plane := newBitmap(gw, gh)
		if mmr {
			pix, used, err := ccitt.DecodeMMR(coded, gw, gh)
			if err != nil {
				return jbig2MMRError(err)
			}
			plane.pix = pix
			coded = coded[used:]
		} else if err := decodeGenericRegionSkip(plane, dec, cx, template, at,
			false, skip); err != nil {
			return err
		}
		if above != nil {
			for i := range plane.pix {
				plane.pix[i] ^= above.pix[i]
			}
		}
		for i, v := range plane.pix {
			vals[i] |= uint32(v) << uint(j)
		}
		above = plane
	}

	region := newBitmap(info.w, info.h)
	if defPixel != 0 {
		for i := range region.pix {
			region.pix[i] = 1
		}
	}
	for m := 0; m < gh; m++ {
		for n := 0; n < gw; n++ {
			v := int(vals[m*gw+n])
			if v >= len(patterns) {
				v = len(patterns) - 1
			}
			x, y := cell(m, n)
			composeBitmap(region, patterns[v], x, y, combOp)
		}
	}
	return d.place(s, info, region)
}
