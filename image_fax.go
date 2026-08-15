package gopdf

import (
	"bytes"
	"fmt"
	"image"

	"github.com/SalvioniDigitalSolutions/gopdf/internal/ccitt"
)

// decodeFax decodes a CCITT Group 3 or Group 4 fax stream — the classic
// encoding of black-and-white scans — honouring the filter's decode
// parameters. Mixed two-dimensional Group 3 (K > 0) is not implemented
// by the decoder and reports an error.
func (im ImageRef) decodeFax(filters []Name, parms []any) (image.Image, error) {
	r := im.r
	// Unwrap any filters ahead of the fax codec, as for JPEG.
	data, err := im.unwrapOuter(filters)
	if err != nil {
		return nil, err
	}

	var parm Dict
	if i := len(filters) - 1; i < len(parms) {
		parm, _ = r.resolve(parms[i]).(Dict)
	}
	intParm := func(key Name, def int) int {
		if v, ok := toInt(r.resolve(parm[key])); ok {
			return v
		}
		return def
	}
	boolParm := func(key Name) bool {
		b, ok := r.resolve(parm[key]).(bool)
		return ok && b
	}

	sub := ccitt.Group3
	switch k := intParm("K", 0); {
	case k < 0:
		sub = ccitt.Group4
	case k > 0:
		return nil, fmt.Errorf("gopdf: CCITT Group 3 two-dimensional (K > 0) images are not decoded")
	}

	cols := intParm("Columns", 1728)
	if cols <= 0 {
		cols = 1728
	}
	rows := im.Height
	if rows <= 0 {
		rows = intParm("Rows", 0)
	}
	if cols > 1<<16 || rows <= 0 || rows > 1<<20 {
		return nil, fmt.Errorf("gopdf: CCITT image has unreasonable dimensions %dx%d", cols, rows)
	}

	// The fax codecs carry runs of black and white, not sample values;
	// the image pipeline's polarity is fixed here. PDF's default — 0
	// bits are black, rendered black in DeviceGray — is the decoder's
	// own. BlackIs1 flips it, as does an image mask whose /Decode is
	// [1 0]; both together cancel out.
	invert := boolParm("BlackIs1")
	if im.ColorSpace == "ImageMask" {
		if d, ok := r.resolve(im.stream.dict["Decode"]).(Array); ok && len(d) >= 1 {
			if f, ok := toFloat(r.resolve(d[0])); ok && f == 1 {
				invert = !invert
			}
		}
	}

	dst := image.NewGray(image.Rect(0, 0, cols, rows))
	opts := &ccitt.Options{Invert: invert, Align: boolParm("EncodedByteAlign")}
	if err := ccitt.DecodeIntoGray(dst, bytes.NewReader(data), ccitt.MSB, sub, opts); err != nil {
		return nil, fmt.Errorf("gopdf: decode CCITT image: %w", err)
	}

	// Rows are Columns wide (1728 for a standard fax); an image narrower
	// than its transmission width keeps only its declared columns.
	if im.Width > 0 && im.Width < cols {
		crop := image.NewGray(image.Rect(0, 0, im.Width, rows))
		for y := 0; y < rows; y++ {
			copy(crop.Pix[y*crop.Stride:y*crop.Stride+im.Width], dst.Pix[y*dst.Stride:])
		}
		return crop, nil
	}
	return dst, nil
}
