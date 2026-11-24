package gopdf

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"
)

// ImageRef describes an image drawn on a page.
type ImageRef struct {
	// Name is the image's resource name in the page or form that draws it.
	Name Name
	// Width and Height are the image's pixel dimensions.
	Width, Height int
	// BitsPerComponent is the precision of each colour component.
	BitsPerComponent int
	// ColorSpace names the image's colour space, as the file declares it.
	ColorSpace string
	// Page is the 0-based index of the page it appears on.
	Page int
	// X, Y, W and H give where the image is drawn, in points from the
	// top-left of the page. They are zero if the image is in the page's
	// resources but never painted.
	//
	// They bound the image. For anything but an upright placement the box
	// is larger than the picture — a rotated image fills its bounding box
	// no better than a tilted photograph fills the envelope it came in —
	// so use Matrix where the placement matters.
	X, Y, W, H float64
	// Filter is the codec the image is stored in — "DCTDecode" for a
	// JPEG, "JBIG2Decode" for a scan, "FlateDecode" for anything this
	// package wrote — or empty for an image stored raw. It is the last
	// filter in the chain, which is the one that decides what the bytes
	// are a picture of.
	Filter string
	// Matrix is the transform in force where the image was drawn, mapping
	// the unit square onto the page. It is the placement exactly:
	// rotation, skew and flip included, in PDF's bottom-up coordinates.
	// It is the zero matrix for an image the page never paints.
	Matrix [6]float64

	ref    Ref
	stream *rawStream
	r      *Reader
}

// ObjectNumber identifies the image XObject behind this reference, or 0
// when the image is not an indirect object. Two references with the same
// non-zero number share one underlying image — a picture drawn several
// times, or on several pages — so callers processing each image once can
// key on it.
func (im ImageRef) ObjectNumber() int { return im.ref.Num }

// PageImages lists the images a page draws, including those inside form
// XObjects it invokes.
func (r *Reader) PageImages(page int) ([]ImageRef, error) {
	if page < 0 || page >= len(r.pages) {
		return nil, fmt.Errorf("gopdf: page %d out of range", page)
	}
	pi := r.pages[page]
	content, err := r.pageContent(pi.dict)
	if err != nil {
		return nil, err
	}
	sc := &imageScanner{r: r, page: page, box: pi.mediaBox, seen: map[Ref]bool{}}
	sc.scan(content, pi.resources, identityMatrix, 0)
	return sc.out, nil
}

// imageScanner walks content streams recording where images are painted.
type imageScanner struct {
	r    *Reader
	page int
	box  [4]float64
	out  []ImageRef
	seen map[Ref]bool
}

func (sc *imageScanner) scan(content []byte, resources any, base matrix, depth int) {
	if depth > maxFormDepth {
		return
	}
	res, _ := sc.r.resolve(resources).(Dict)
	xobjects, _ := sc.r.resolve(res["XObject"]).(Dict)

	ctm := base
	var stack []matrix
	var operands []contentToken

	for _, tok := range tokenizeContent(content) {
		op, isOp := tok.val.(opKeyword)
		if !isOp {
			if len(operands) < 8 {
				operands = append(operands, tok)
			}
			continue
		}
		num := func(i int) float64 {
			if i < len(operands) {
				if f, ok := toFloat(operands[i].val); ok {
					return f
				}
			}
			return 0
		}
		switch string(op) {
		case "q":
			if len(stack) < 64 {
				stack = append(stack, ctm)
			}
		case "Q":
			if n := len(stack); n > 0 {
				ctm = stack[n-1]
				stack = stack[:n-1]
			}
		case "cm":
			if len(operands) >= 6 {
				var m matrix
				for i := 0; i < 6; i++ {
					m[i] = num(i)
				}
				ctm = m.mul(ctm)
			}
		case "Do":
			if len(operands) >= 1 {
				if name, ok := operands[0].val.(Name); ok {
					sc.visit(name, xobjects[name], resources, ctm, depth)
				}
			}
		}
		operands = operands[:0]
	}
}

// visit records an image, or descends into a form XObject.
func (sc *imageScanner) visit(name Name, entry any, resources any, ctm matrix, depth int) {
	stm, ok := sc.r.resolve(entry).(*rawStream)
	if !ok {
		return
	}
	switch sc.r.resolve(stm.dict["Subtype"]) {
	case Name("Form"):
		ref, isRef := entry.(Ref)
		if isRef {
			if sc.seen[ref] {
				return
			}
			sc.seen[ref] = true
			defer delete(sc.seen, ref)
		}
		content, err := sc.r.decodeStream(stm.dict, stm.data)
		if err != nil {
			return
		}
		inner := ctm
		if mArr, ok := sc.r.resolve(stm.dict["Matrix"]).(Array); ok && len(mArr) == 6 {
			var m matrix
			for i, e := range mArr {
				f, _ := toFloat(sc.r.resolve(e))
				m[i] = f
			}
			inner = m.mul(ctm)
		}
		formRes := stm.dict["Resources"]
		if formRes == nil {
			formRes = resources
		}
		sc.scan(content, formRes, inner, depth+1)

	case Name("Image"):
		img := sc.r.describeImage(stm, entry)
		img.Name, img.Page = name, sc.page
		// An image is drawn into the unit square, so the transform is the
		// placement. All four corners are taken, not two: under a
		// rotation the diagonal alone describes a box the picture does
		// not fill.
		img.Matrix = ctm
		lo, hi := unitSquareBounds(ctm)
		img.X = lo[0] - sc.box[0]
		img.Y = sc.box[3] - hi[1]
		img.W = hi[0] - lo[0]
		img.H = hi[1] - lo[1]
		sc.out = append(sc.out, img)
	}
}

// describeImage reads what an image XObject says about its own
// samples: their size, how many bits each takes, the colour space,
// and whether the image is a stencil mask. Everything that decodes an
// image goes through here — the page scanner and the renderer alike —
// because a decoder that is handed only a width and a height falls
// back to eight bits a sample, and a one-bit scan read that way is
// noise.
func (r *Reader) describeImage(stm *rawStream, entry any) ImageRef {
	img := ImageRef{r: r, stream: stm}
	if ref, ok := entry.(Ref); ok {
		img.ref = ref
	}
	img.Width, _ = toInt(r.resolve(stm.dict["Width"]))
	img.Height, _ = toInt(r.resolve(stm.dict["Height"]))
	img.BitsPerComponent, _ = toInt(r.resolve(stm.dict["BitsPerComponent"]))
	img.ColorSpace = colorSpaceName(r, stm.dict["ColorSpace"])
	img.Filter = lastFilterName(r, stm.dict)
	if b, ok := r.resolve(stm.dict["ImageMask"]).(bool); ok && b {
		img.ColorSpace = "ImageMask"
		img.BitsPerComponent = 1
	}
	return img
}

// IsStencilMask reports whether the image is a stencil (/ImageMask):
// one bit a sample, no colour of its own. Decode returns such an
// image with its painted samples black and the rest white; what it is
// painted with on the page is the fill colour in force where it is
// drawn.
func (im ImageRef) IsStencilMask() bool { return im.ColorSpace == "ImageMask" }

// colorSpaceName describes a colour space for reporting.
func colorSpaceName(r *Reader, v any) string {
	switch t := r.resolve(v).(type) {
	case Name:
		return string(t)
	case Array:
		if len(t) > 0 {
			if n, ok := r.resolve(t[0]).(Name); ok {
				return string(n)
			}
		}
	}
	return ""
}

// --- decoding ---

// Decode returns the image's pixels.
//
// JPEG data is handed to the standard decoder, CCITT Group 3/4 fax data
// to the package's own; other images are decoded from their samples.
// Where the image has a soft mask, it is applied as the alpha channel.
// JBIG2 and JPEG 2000 images report an error: their codecs are not
// implemented.
func (im ImageRef) Decode() (image.Image, error) {
	if im.stream == nil || im.r == nil {
		return nil, fmt.Errorf("gopdf: image reference is not bound to a document")
	}
	base, err := im.decodeBase()
	if err != nil {
		return nil, err
	}
	base = fitDeclared(base, im.Width, im.Height)
	mask, err := im.softMask()
	if err != nil || mask == nil {
		return base, err
	}
	return applySoftMask(base, mask), nil
}

// decodeBase decodes the image's own samples, without its soft mask.
func (im ImageRef) decodeBase() (image.Image, error) {
	r := im.r
	filters, parms, err := r.filterChain(im.stream.dict)
	if err != nil {
		return nil, err
	}
	for _, f := range filters {
		if f == "JPXDecode" {
			return nil, fmt.Errorf("gopdf: JPEG 2000 images are not decoded")
		}
	}
	// A JBIG2 scan is decoded by the generic-region decoder; a file that
	// uses a symbol dictionary reports that rather than guessing.
	if n := len(filters); n > 0 && filters[n-1] == "JBIG2Decode" {
		return im.decodeJBIG2Image(filters, parms)
	}
	// A fax stream is decoded by the vendored CCITT codec; anything
	// before it in the chain is unwrapped inside decodeFax.
	if n := len(filters); n > 0 && (filters[n-1] == "CCITTFaxDecode" || filters[n-1] == "CCF") {
		return im.decodeFax(filters, parms)
	}
	// A JPEG is decoded by the standard library; anything before it in
	// the chain is unwrapped first.
	if len(filters) > 0 && (filters[len(filters)-1] == "DCTDecode" || filters[len(filters)-1] == "DCT") {
		data, err := im.unwrapOuter(filters)
		if err != nil {
			return nil, err
		}
		m, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		if c, ok := m.(*image.CMYK); ok {
			im.cmykAsStored(c, data)
		}
		return m, nil
	}

	samples, err := r.decodeStream(im.stream.dict, im.stream.data)
	if err != nil {
		return nil, err
	}
	return im.fromSamples(samples)
}

// unwrapOuter reverses every filter ahead of the last one, which the
// caller decodes itself (JPEG or fax data).
func (im ImageRef) unwrapOuter(filters []Name) ([]byte, error) {
	if len(filters) <= 1 {
		return im.stream.data, nil
	}
	shorter := cloneDict(im.stream.dict)
	shorter["Filter"] = Array{}
	for _, f := range filters[:len(filters)-1] {
		shorter["Filter"] = append(shorter["Filter"].(Array), f)
	}
	return im.r.decodeStream(shorter, im.stream.data)
}

// JPEG returns the image's own JPEG stream when it is stored as one
// (DCTDecode), unwrapping any outer compression, so callers that keep
// photographs in their original encoding can embed the bytes untouched.
// The bool reports whether such a stream exists; images stored any
// other way return false — decode those with Decode.
func (im ImageRef) JPEG() ([]byte, bool) {
	if im.stream == nil || im.r == nil {
		return nil, false
	}
	filters, _, err := im.r.filterChain(im.stream.dict)
	if err != nil || len(filters) == 0 {
		return nil, false
	}
	if last := filters[len(filters)-1]; last != "DCTDecode" && last != "DCT" {
		return nil, false
	}
	data, err := im.unwrapOuter(filters)
	if err != nil {
		return nil, false
	}
	// A JPEG that stores more than the image shows is not handed out
	// whole: the bytes would carry the hidden part with them.
	if cfg, err := jpeg.DecodeConfig(bytes.NewReader(data)); err != nil ||
		(im.Width > 0 && cfg.Width != im.Width) || (im.Height > 0 && cfg.Height != im.Height) {
		return nil, false
	}
	return data, true
}

// HoldsUnshownPixels reports whether the image's stored data holds
// more pixels than the image declares — pixels no viewer shows and
// anyone who extracts the stream gets. A scan stored in tiles is the
// usual case: the tile at the right edge of the page is stored as a
// whole tile and declared as the few columns that are left, and what
// fills the rest of it is whatever the scanning software had in its
// buffer, often the text of the tile before. Checked for JPEG, JPEG
// 2000 and fax data, the codings whose stream carries its own size.
func (im ImageRef) HoldsUnshownPixels() bool {
	if im.stream == nil || im.r == nil || im.Width <= 0 || im.Height <= 0 {
		return false
	}
	filters, parms, err := im.r.filterChain(im.stream.dict)
	if err != nil || len(filters) == 0 {
		return false
	}
	last := filters[len(filters)-1]
	switch last {
	case "DCTDecode", "DCT":
		data, err := im.unwrapOuter(filters)
		if err != nil {
			return false
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
		return err == nil && (cfg.Width > im.Width || cfg.Height > im.Height)
	case "JPXDecode":
		data, err := im.unwrapOuter(filters)
		if err != nil {
			return false
		}
		w, h, ok := jpxSize(data)
		return ok && (w > im.Width || h > im.Height)
	case "CCITTFaxDecode", "CCF":
		var parm Dict
		if i := len(filters) - 1; i < len(parms) {
			parm, _ = im.r.resolve(parms[i]).(Dict)
		}
		cols := int64(1728)
		if v, ok := im.r.resolve(parm["Columns"]).(int64); ok && v > 0 {
			cols = v
		}
		rows, _ := im.r.resolve(parm["Rows"]).(int64)
		return cols > int64(im.Width) || rows > int64(im.Height)
	}
	return false
}

// FitDecoded cuts pixels decoded from this image's stream by another
// decoder (a JPEG 2000 codec the package does not carry) to the
// image's declared size, as Decode does with its own: what lies
// outside is on no page and must not be read, drawn or re-encoded.
func (im ImageRef) FitDecoded(m image.Image) image.Image {
	if m == nil {
		return nil
	}
	return fitDeclared(m, im.Width, im.Height)
}

// jpxSize reads the image size from a JPEG 2000 codestream's SIZ
// marker segment (the one that follows the start-of-codestream marker),
// whether or not the codestream is wrapped in JP2 boxes.
func jpxSize(data []byte) (w, h int, ok bool) {
	i := bytes.Index(data, []byte{0xFF, 0x4F, 0xFF, 0x51})
	if i < 0 || len(data) < i+4+2+2+16 {
		return 0, 0, false
	}
	p := data[i+4+2+2:]
	xs, ys := binary.BigEndian.Uint32(p[0:]), binary.BigEndian.Uint32(p[4:])
	xo, yo := binary.BigEndian.Uint32(p[8:]), binary.BigEndian.Uint32(p[12:])
	if xs <= xo || ys <= yo || xs-xo > 1<<20 || ys-yo > 1<<20 {
		return 0, 0, false
	}
	return int(xs - xo), int(ys - yo), true
}

// fitDeclared returns m cut to the declared w×h when its data is
// larger. The part outside is on no page in any viewer, and nothing
// that reads, draws or re-encodes the image may see it: drawn into the
// image's box it was squeezed into view, read it was text from nowhere,
// re-encoded it went on hiding in the output. The result is a copy, so
// no buffer behind it still holds the rest.
func fitDeclared(m image.Image, w, h int) image.Image {
	b := m.Bounds()
	if w <= 0 || h <= 0 || (b.Dx() <= w && b.Dy() <= h) {
		return m
	}
	cw, ch := min(w, b.Dx()), min(h, b.Dy())
	rows := func(dstPix []byte, dstStride int, src []byte, srcStride, srcOff, bpp int) {
		for y := 0; y < ch; y++ {
			copy(dstPix[y*dstStride:y*dstStride+cw*bpp], src[srcOff+y*srcStride:])
		}
	}
	switch src := m.(type) {
	case *image.Gray:
		dst := image.NewGray(image.Rect(0, 0, cw, ch))
		rows(dst.Pix, dst.Stride, src.Pix, src.Stride, src.PixOffset(b.Min.X, b.Min.Y), 1)
		return dst
	case *image.Gray16:
		dst := image.NewGray16(image.Rect(0, 0, cw, ch))
		rows(dst.Pix, dst.Stride, src.Pix, src.Stride, src.PixOffset(b.Min.X, b.Min.Y), 2)
		return dst
	case *image.CMYK:
		dst := image.NewCMYK(image.Rect(0, 0, cw, ch))
		rows(dst.Pix, dst.Stride, src.Pix, src.Stride, src.PixOffset(b.Min.X, b.Min.Y), 4)
		return dst
	case *image.NRGBA:
		dst := image.NewNRGBA(image.Rect(0, 0, cw, ch))
		rows(dst.Pix, dst.Stride, src.Pix, src.Stride, src.PixOffset(b.Min.X, b.Min.Y), 4)
		return dst
	case *image.RGBA:
		dst := image.NewRGBA(image.Rect(0, 0, cw, ch))
		rows(dst.Pix, dst.Stride, src.Pix, src.Stride, src.PixOffset(b.Min.X, b.Min.Y), 4)
		return dst
	}
	dst := image.NewRGBA(image.Rect(0, 0, cw, ch))
	draw.Draw(dst, dst.Bounds(), m, b.Min, draw.Src)
	return dst
}

// cmykAsStored turns the CMYK picture Go's JPEG decoder made of a
// four-colour JPEG into the samples a PDF takes from it: as stored, then
// through the image's /Decode. Go undoes the inversion Adobe's tools write
// CMYK in (the APP14 marker), as a JPEG viewer does; a PDF does not — its
// DCTDecode hands on the samples as the JPEG holds them (libjpeg, Poppler)
// and an inverted picture says so in its /Decode. Taken as Go gave it, a
// report's Sandia National Laboratories logo, an Adobe YCCK JPEG every
// viewer shows black on white, was all black: no recogniser read a word
// of it, and the marked name in it was neither barred nor seen by any
// check of the copy.
func (im ImageRef) cmykAsStored(c *image.CMYK, data []byte) {
	invert := jpegIsAdobe(data)
	if d := floatArray(im.r, im.r.resolve(im.stream.dict["Decode"])); len(d) >= 2 && d[0] == 1 && d[1] == 0 {
		invert = !invert
	}
	if !invert {
		return
	}
	for i := range c.Pix {
		c.Pix[i] = 255 - c.Pix[i]
	}
}

// fromSamples builds an image from raw component values.
func (im ImageRef) fromSamples(samples []byte) (image.Image, error) {
	w, h := im.Width, im.Height
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("gopdf: image has no dimensions")
	}
	bpc := im.BitsPerComponent
	if bpc == 0 {
		bpc = 8
	}
	r := im.r

	// An image mask is a one-bit stencil: set bits are painted.
	if im.ColorSpace == "ImageMask" {
		invert := false
		if d, ok := r.resolve(im.stream.dict["Decode"]).(Array); ok && len(d) >= 1 {
			if f, ok := toFloat(r.resolve(d[0])); ok && f == 1 {
				invert = true
			}
		}
		out := image.NewGray(image.Rect(0, 0, w, h))
		rd := newBitReader(samples, w, 1, 1)
		for y := 0; y < h; y++ {
			rd.startRow(y)
			for x := 0; x < w; x++ {
				on := rd.next() == 0 // a zero bit paints, by default
				if invert {
					on = !on
				}
				if on {
					out.SetGray(x, y, color.Gray{Y: 0})
				} else {
					out.SetGray(x, y, color.Gray{Y: 255})
				}
			}
		}
		return out, nil
	}

	space, comps, palette, err := im.colorSpaceInfo()
	if err != nil {
		return nil, err
	}
	maxVal := float64(int(1)<<uint(bpc) - 1)
	if bpc == 16 {
		// The reader reports 16-bit samples at 8-bit precision.
		maxVal = 255
	}
	rd := newBitReader(samples, w, comps, bpc)
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	px := make([]uint32, comps)

	// /Decode maps the sample range onto the colour range, a pair of
	// numbers for each component; [1 0] turns a component around. An
	// indexed image's pair maps to palette indices instead, where
	// anything but the identity is vanishingly rare, so it is left be.
	// unit returns component c's sample as a colour value in 0..1.
	unit := func(c int) float64 { return float64(px[c]) / maxVal }
	if dec := floatArray(r, im.stream.dict["Decode"]); space != "Indexed" && len(dec) >= 2*comps {
		unit = func(c int) float64 {
			lo, hi := dec[2*c], dec[2*c+1]
			return clamp01(lo + float64(px[c])/maxVal*(hi-lo))
		}
	}

	for y := 0; y < h; y++ {
		rd.startRow(y)
		row := out.Pix[y*out.Stride : y*out.Stride+4*w]
		for x := 0; x < w; x++ {
			for c := 0; c < comps; c++ {
				px[c] = rd.next()
			}
			var col color.NRGBA
			switch space {
			case "Indexed":
				idx := int(px[0])
				if idx*3+2 < len(palette) {
					col = color.NRGBA{palette[idx*3], palette[idx*3+1], palette[idx*3+2], 255}
				} else {
					col = color.NRGBA{0, 0, 0, 255}
				}
			case "DeviceGray", "CalGray", "G":
				v := uint8(unit(0) * 255)
				col = color.NRGBA{v, v, v, 255}
			case "DeviceCMYK", "CMYK":
				k := unit(3)
				col = color.NRGBA{
					uint8((1 - unit(0)) * (1 - k) * 255),
					uint8((1 - unit(1)) * (1 - k) * 255),
					uint8((1 - unit(2)) * (1 - k) * 255),
					255,
				}
			default: // DeviceRGB and anything three-component
				col = color.NRGBA{
					uint8(unit(0) * 255),
					uint8(unit(1) * 255),
					uint8(unit(2) * 255),
					255,
				}
			}
			// out.SetNRGBA(x, y, col), without its bounds check per pixel.
			row[4*x], row[4*x+1], row[4*x+2], row[4*x+3] = col.R, col.G, col.B, col.A
		}
	}
	return out, nil
}

// colorSpaceInfo resolves the image's colour space to a family, a
// component count and, for indexed images, an RGB palette.
func (im ImageRef) colorSpaceInfo() (string, int, []byte, error) {
	r := im.r
	cs := r.resolve(im.stream.dict["ColorSpace"])
	switch t := cs.(type) {
	case Name:
		switch t {
		case "DeviceGray", "CalGray", "G":
			return "DeviceGray", 1, nil, nil
		case "DeviceCMYK", "CMYK":
			return "DeviceCMYK", 4, nil, nil
		case "DeviceRGB", "CalRGB", "RGB":
			return "DeviceRGB", 3, nil, nil
		}
		return "DeviceRGB", 3, nil, nil
	case Array:
		if len(t) == 0 {
			return "DeviceRGB", 3, nil, nil
		}
		family, _ := r.resolve(t[0]).(Name)
		switch family {
		case "Indexed", "I":
			if len(t) < 4 {
				return "", 0, nil, fmt.Errorf("gopdf: malformed indexed colour space")
			}
			base := colorSpaceName(r, t[1])
			lookup, err := im.paletteBytes(t[3])
			if err != nil {
				return "", 0, nil, err
			}
			return "Indexed", 1, expandPalette(base, lookup), nil
		case "ICCBased":
			n := 3
			if s, ok := r.resolve(t[len(t)-1]).(*rawStream); ok {
				if v, ok := toInt(r.resolve(s.dict["N"])); ok && v > 0 {
					n = v
				}
			}
			switch n {
			case 1:
				return "DeviceGray", 1, nil, nil
			case 4:
				return "DeviceCMYK", 4, nil, nil
			default:
				return "DeviceRGB", 3, nil, nil
			}
		case "DeviceN":
			if names, ok := r.resolve(t[1]).(Array); ok {
				return "DeviceGray", len(names), nil, nil
			}
		case "Separation":
			return "DeviceGray", 1, nil, nil
		case "CalGray":
			return "DeviceGray", 1, nil, nil
		}
		return "DeviceRGB", 3, nil, nil
	}
	return "DeviceRGB", 3, nil, nil
}

// paletteBytes reads an indexed colour space's lookup table, which may be
// a string or a stream.
func (im ImageRef) paletteBytes(v any) ([]byte, error) {
	switch t := im.r.resolve(v).(type) {
	case String:
		return []byte(t), nil
	case *rawStream:
		return im.r.decodeStream(t.dict, t.data)
	}
	return nil, fmt.Errorf("gopdf: indexed colour space has no lookup table")
}

// expandPalette converts a lookup table to RGB triples.
func expandPalette(base string, lookup []byte) []byte {
	switch base {
	case "DeviceGray", "CalGray", "G":
		out := make([]byte, 0, len(lookup)*3)
		for _, v := range lookup {
			out = append(out, v, v, v)
		}
		return out
	case "DeviceCMYK", "CMYK":
		out := make([]byte, 0, len(lookup)/4*3)
		for i := 0; i+3 < len(lookup); i += 4 {
			k := float64(lookup[i+3]) / 255
			out = append(out,
				uint8((1-float64(lookup[i])/255)*(1-k)*255),
				uint8((1-float64(lookup[i+1])/255)*(1-k)*255),
				uint8((1-float64(lookup[i+2])/255)*(1-k)*255))
		}
		return out
	default:
		return lookup
	}
}

// softMask decodes the image's /SMask as an alpha channel.
func (im ImageRef) softMask() (*image.Gray, error) {
	stm, ok := im.r.resolve(im.stream.dict["SMask"]).(*rawStream)
	if !ok {
		return nil, nil
	}
	sub := im.r.describeImage(stm, nil)
	sub.ColorSpace = "DeviceGray"
	m, err := sub.decodeBase()
	if err != nil {
		return nil, nil // a mask we cannot read simply leaves the image opaque
	}
	return maskGray(m), nil
}

// maskGray is a decoded soft mask as grey, pixel by pixel through
// GrayModel, at the origin.
func maskGray(m image.Image) *image.Gray {
	b := m.Bounds()
	out := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	switch src := m.(type) {
	case *image.Gray:
		// GrayModel hands a grey on as it is: the rows are copied.
		for y := 0; y < b.Dy(); y++ {
			i := src.PixOffset(b.Min.X, b.Min.Y+y)
			copy(out.Pix[y*out.Stride:y*out.Stride+b.Dx()], src.Pix[i:i+b.Dx()])
		}
		return out
	case *image.NRGBA:
		// The mask's own samples, read as grey (fromSamples): an opaque
		// grey pixel's value through GrayModel is worked out once for
		// each of the 256, any other pixel's as before.
		var grey [256]uint8
		for v := range grey {
			grey[v] = color.GrayModel.Convert(color.NRGBA{uint8(v), uint8(v), uint8(v), 255}).(color.Gray).Y
		}
		for y := 0; y < b.Dy(); y++ {
			i := src.PixOffset(b.Min.X, b.Min.Y+y)
			srow := src.Pix[i : i+4*b.Dx()]
			orow := out.Pix[y*out.Stride : y*out.Stride+b.Dx()]
			for x := range orow {
				p := srow[4*x : 4*x+4]
				if p[0] == p[1] && p[1] == p[2] && p[3] == 255 {
					orow[x] = grey[p[0]]
				} else {
					orow[x] = color.GrayModel.Convert(color.NRGBA{p[0], p[1], p[2], p[3]}).(color.Gray).Y
				}
			}
		}
		return out
	}
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			c := color.GrayModel.Convert(m.At(b.Min.X+x, b.Min.Y+y)).(color.Gray)
			out.SetGray(x, y, c)
		}
	}
	return out
}

// applySoftMask combines an image with a separately sized alpha
// channel. The result has the finer of the two grids on each axis.
//
// A mask finer than its image is the usual form of a scanner's
// "compact" page: the ink is a few coarse pixels of colour — one, at
// the limit — and the letters are the mask. Sampled down to the
// image's own size the mask leaves nothing of them, and the page
// rendered blank.
func applySoftMask(base image.Image, mask *image.Gray) image.Image {
	b := base.Bounds()
	mb := mask.Bounds()
	if mb.Dx() == 0 || mb.Dy() == 0 || b.Dx() == 0 || b.Dy() == 0 {
		return base
	}
	w, h := b.Dx(), b.Dy()
	if mb.Dx() > w {
		w = mb.Dx()
	}
	if mb.Dy() > h {
		h = mb.Dy()
	}
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	// NRGBAModel hands an NRGBA colour on as it is: from an NRGBA
	// picture (fromSamples) the pixel is read straight from its bytes.
	nb, _ := base.(*image.NRGBA)
	for y := 0; y < h; y++ {
		by := y * b.Dy() / h
		my := y * mb.Dy() / h
		row := out.Pix[y*out.Stride : y*out.Stride+4*w]
		for x := 0; x < w; x++ {
			var c color.NRGBA
			if nb != nil {
				i := nb.PixOffset(b.Min.X+x*b.Dx()/w, b.Min.Y+by)
				c = color.NRGBA{nb.Pix[i], nb.Pix[i+1], nb.Pix[i+2], nb.Pix[i+3]}
			} else {
				c = color.NRGBAModel.Convert(base.At(b.Min.X+x*b.Dx()/w, b.Min.Y+by)).(color.NRGBA)
			}
			c.A = mask.GrayAt(mb.Min.X+x*mb.Dx()/w, mb.Min.Y+my).Y
			row[4*x], row[4*x+1], row[4*x+2], row[4*x+3] = c.R, c.G, c.B, c.A
		}
	}
	return out
}

// bitReader walks packed component samples, where each row restarts on a
// byte boundary.
type bitReader struct {
	data     []byte
	rowBytes int
	bpc      int
	pos      int // bit position within the current row
	rowStart int
}

func newBitReader(data []byte, width, comps, bpc int) *bitReader {
	return &bitReader{
		data:     data,
		rowBytes: (width*comps*bpc + 7) / 8,
		bpc:      bpc,
	}
}

func (b *bitReader) startRow(y int) {
	b.rowStart = y * b.rowBytes
	b.pos = 0
}

func (b *bitReader) next() uint32 {
	if b.bpc == 8 {
		// A sample is a whole byte: the byte, read at once rather than
		// bit by bit as below; past the data, 0 (as below).
		i := b.rowStart + b.pos>>3
		if i >= len(b.data) {
			return 0
		}
		b.pos += 8
		return uint32(b.data[i])
	}
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
	// 16-bit samples are reported at 8-bit precision.
	if b.bpc == 16 {
		v >>= 8
	}
	return v
}

// --- replacing ---

// ReplaceImage swaps an image's pixels for those of m, keeping the
// placement the page already has: the new image is scaled into exactly
// the same area, whatever its pixel dimensions.
//
// The image object is shared, so every page drawing it shows the
// replacement. Alpha in m is preserved as a soft mask.
func (u *Updater) ReplaceImage(img ImageRef, m image.Image) error {
	if img.ref.Num == 0 {
		return fmt.Errorf("gopdf: image %q is not an indirect object and cannot be replaced", img.Name)
	}
	data, err := encodeImageForPDF(m)
	if err != nil {
		return err
	}
	dict := Dict{
		"Type": Name("XObject"), "Subtype": Name("Image"),
		"Width": int64(data.width), "Height": int64(data.height),
		"ColorSpace": Name(data.colorSpace), "BitsPerComponent": int64(8),
	}
	stream := &rawStream{dict: dict, data: data.data}
	if u.scratch().Compress {
		if compressed, err := flateCompress(data.data); err == nil {
			stream.data = compressed
			dict["Filter"] = Name("FlateDecode")
		}
	}
	if data.smask != nil {
		maskDict := Dict{
			"Type": Name("XObject"), "Subtype": Name("Image"),
			"Width": int64(data.width), "Height": int64(data.height),
			"ColorSpace": Name("DeviceGray"), "BitsPerComponent": int64(8),
		}
		maskStream := &rawStream{dict: maskDict, data: data.smask}
		if u.scratch().Compress {
			if compressed, err := flateCompress(data.smask); err == nil {
				maskStream.data = compressed
				maskDict["Filter"] = Name("FlateDecode")
			}
		}
		dict["SMask"] = Ref{Num: u.add(maskStream)}
	}
	u.set(img.ref.Num, stream)
	return nil
}

// encodeImageForPDF converts a Go image to the samples a PDF image
// object holds, reusing the writer's own conversion.
func encodeImageForPDF(m image.Image) (*imageData, error) {
	scratch := New()
	if _, err := scratch.AddImage(m); err != nil {
		return nil, err
	}
	return scratch.images[0], nil
}

// unitSquareBounds returns the corners of the box a transform maps the
// unit square into.
func unitSquareBounds(m matrix) (lo, hi [2]float64) {
	first := true
	for _, c := range [][2]float64{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
		x, y := m.apply(c[0], c[1])
		if first {
			lo, hi, first = [2]float64{x, y}, [2]float64{x, y}, false
			continue
		}
		lo[0], hi[0] = minF(lo[0], x), maxF(hi[0], x)
		lo[1], hi[1] = minF(lo[1], y), maxF(hi[1], y)
	}
	return lo, hi
}

// Rotation returns the angle the image is drawn at, in degrees clockwise
// from upright as seen on the page, in the range [0, 360).
//
// It is the angle of the placement's horizontal axis. A sheared placement
// has no single angle; this reports the one its baseline runs at.
func (im ImageRef) Rotation() float64 {
	if im.Matrix == ([6]float64{}) {
		return 0
	}
	// PDF space runs upwards and the page coordinates here run down, so
	// the sign of the vertical component flips.
	deg := math.Atan2(-im.Matrix[1], im.Matrix[0]) * 180 / math.Pi
	if deg < 0 {
		deg += 360
	}
	if deg >= 360 {
		deg -= 360
	}
	return deg
}

// Upright reports whether the image is drawn square to the page, which is
// when its bounding box is the picture and not merely around it.
func (im ImageRef) Upright() bool {
	return im.Matrix[1] == 0 && im.Matrix[2] == 0
}

// lastFilterName is the codec at the end of a stream's filter chain,
// which is the one that says what the data is.
func lastFilterName(r *Reader, d Dict) string {
	switch t := r.resolve(d["Filter"]).(type) {
	case Name:
		return string(t)
	case Array:
		if len(t) > 0 {
			if n, ok := r.resolve(t[len(t)-1]).(Name); ok {
				return string(n)
			}
		}
	}
	return ""
}
