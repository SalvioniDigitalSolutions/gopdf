package gopdf

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
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
	X, Y, W, H float64

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
		img := ImageRef{Name: name, Page: sc.page, r: sc.r, stream: stm}
		if ref, ok := entry.(Ref); ok {
			img.ref = ref
		}
		img.Width, _ = toInt(sc.r.resolve(stm.dict["Width"]))
		img.Height, _ = toInt(sc.r.resolve(stm.dict["Height"]))
		img.BitsPerComponent, _ = toInt(sc.r.resolve(stm.dict["BitsPerComponent"]))
		img.ColorSpace = colorSpaceName(sc.r, stm.dict["ColorSpace"])
		if b, ok := sc.r.resolve(stm.dict["ImageMask"]).(bool); ok && b {
			img.ColorSpace = "ImageMask"
			img.BitsPerComponent = 1
		}
		// An image is drawn into the unit square, so the transform's own
		// extent is the area it covers.
		x0, y0 := ctm.apply(0, 0)
		x1, y1 := ctm.apply(1, 1)
		img.X = minF(x0, x1) - sc.box[0]
		img.W = absF(x1 - x0)
		img.H = absF(y1 - y0)
		img.Y = sc.box[3] - maxF(y0, y1)
		sc.out = append(sc.out, img)
	}
}

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
		switch f {
		case "JPXDecode":
			return nil, fmt.Errorf("gopdf: JPEG 2000 images are not decoded")
		case "JBIG2Decode":
			return nil, fmt.Errorf("gopdf: JBIG2 images are not decoded")
		}
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
		return jpeg.Decode(bytes.NewReader(data))
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
	return data, true
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

	for y := 0; y < h; y++ {
		rd.startRow(y)
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
				v := uint8(float64(px[0]) / maxVal * 255)
				col = color.NRGBA{v, v, v, 255}
			case "DeviceCMYK", "CMYK":
				f := func(i int) float64 { return float64(px[i]) / maxVal }
				k := f(3)
				col = color.NRGBA{
					uint8((1 - f(0)) * (1 - k) * 255),
					uint8((1 - f(1)) * (1 - k) * 255),
					uint8((1 - f(2)) * (1 - k) * 255),
					255,
				}
			default: // DeviceRGB and anything three-component
				col = color.NRGBA{
					uint8(float64(px[0]) / maxVal * 255),
					uint8(float64(px[1]) / maxVal * 255),
					uint8(float64(px[2]) / maxVal * 255),
					255,
				}
			}
			out.SetNRGBA(x, y, col)
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
	sub := ImageRef{r: im.r, stream: stm}
	sub.Width, _ = toInt(im.r.resolve(stm.dict["Width"]))
	sub.Height, _ = toInt(im.r.resolve(stm.dict["Height"]))
	sub.BitsPerComponent, _ = toInt(im.r.resolve(stm.dict["BitsPerComponent"]))
	sub.ColorSpace = "DeviceGray"
	m, err := sub.decodeBase()
	if err != nil {
		return nil, nil // a mask we cannot read simply leaves the image opaque
	}
	b := m.Bounds()
	out := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			c := color.GrayModel.Convert(m.At(b.Min.X+x, b.Min.Y+y)).(color.Gray)
			out.SetGray(x, y, c)
		}
	}
	return out, nil
}

// applySoftMask combines an image with a separately sized alpha channel.
func applySoftMask(base image.Image, mask *image.Gray) image.Image {
	b := base.Bounds()
	mb := mask.Bounds()
	if mb.Dx() == 0 || mb.Dy() == 0 {
		return base
	}
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		my := y * mb.Dy() / b.Dy()
		for x := 0; x < b.Dx(); x++ {
			mx := x * mb.Dx() / b.Dx()
			c := color.NRGBAModel.Convert(base.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
			c.A = mask.GrayAt(mb.Min.X+mx, mb.Min.Y+my).Y
			out.SetNRGBA(x, y, c)
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
