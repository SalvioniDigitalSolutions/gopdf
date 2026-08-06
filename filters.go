package gopdf

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"encoding/ascii85"
	"errors"
	"fmt"
	"io"
)

// maxDecodedStream bounds decoded stream size so a crafted file cannot
// expand into unbounded memory (decompression bomb).
const maxDecodedStream = 64 << 20

var errStreamTooLarge = errors.New("gopdf: decoded stream exceeds size limit")

// decodeStream applies the stream's /Filter chain to its raw data.
// Image-only filters (DCTDecode, JPXDecode, CCITTFaxDecode, JBIG2Decode)
// are not decoded and return an error; callers that copy streams verbatim
// never need to decode them.
func (r *Reader) decodeStream(d Dict, raw []byte) ([]byte, error) {
	filters, parms, err := r.filterChain(d)
	if err != nil {
		return nil, err
	}
	data := raw
	for i, f := range filters {
		var parm Dict
		if i < len(parms) {
			parm, _ = r.resolve(parms[i]).(Dict)
		}
		switch f {
		case "FlateDecode", "Fl":
			data, err = flateDecode(data)
		case "LZWDecode", "LZW":
			early := 1
			if v, ok := toInt(r.resolve(parm["EarlyChange"])); ok {
				early = v
			}
			data, err = lzwDecode(data, early)
		case "ASCIIHexDecode", "AHx":
			data, err = asciiHexDecode(data)
		case "ASCII85Decode", "A85":
			data, err = ascii85Decode(data)
		case "RunLengthDecode", "RL":
			data, err = runLengthDecode(data)
		case "Crypt":
			return nil, errors.New("gopdf: encrypted streams are not supported")
		default:
			return nil, fmt.Errorf("gopdf: unsupported stream filter /%s", f)
		}
		if err != nil {
			return nil, err
		}
		if f == "FlateDecode" || f == "Fl" || f == "LZWDecode" || f == "LZW" {
			if data, err = applyPredictor(data, parm, r); err != nil {
				return nil, err
			}
		}
	}
	return data, nil
}

// filterChain normalizes /Filter and /DecodeParms to parallel slices.
func (r *Reader) filterChain(d Dict) ([]Name, []any, error) {
	var filters []Name
	switch f := r.resolve(d["Filter"]).(type) {
	case nil:
	case Name:
		filters = []Name{f}
	case Array:
		for _, e := range f {
			n, ok := r.resolve(e).(Name)
			if !ok {
				return nil, nil, errSyntax
			}
			filters = append(filters, n)
		}
	default:
		return nil, nil, errSyntax
	}
	var parms []any
	switch pm := r.resolve(d["DecodeParms"]).(type) {
	case nil:
	case Dict:
		parms = []any{pm}
	case Array:
		parms = pm
	}
	return filters, parms, nil
}

// limitedCopy copies src into a buffer, failing if it grows past the bomb
// limit.
func limitedCopy(src io.Reader) ([]byte, error) {
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(src, maxDecodedStream+1))
	if err != nil && n == 0 {
		return nil, err
	}
	if n > maxDecodedStream {
		return nil, errStreamTooLarge
	}
	return buf.Bytes(), nil
}

func flateDecode(data []byte) ([]byte, error) {
	if zr, err := zlib.NewReader(bytes.NewReader(data)); err == nil {
		out, err := limitedCopy(zr)
		if err == nil || len(out) > 0 {
			return out, nil
		}
	}
	// Some generators write raw deflate without the zlib wrapper.
	return limitedCopy(flate.NewReader(bytes.NewReader(data)))
}

// lzwDecode implements the PDF LZW variant: MSB-first codes with an
// optional "early change" of the code width (the default in PDF).
func lzwDecode(data []byte, early int) ([]byte, error) {
	const (
		clearCode = 256
		eodCode   = 257
	)
	table := make([][]byte, 258, 4096)
	for i := 0; i < 256; i++ {
		table[i] = []byte{byte(i)}
	}
	reset := func() { table = table[:258] }

	var out []byte
	var prev []byte
	width := 9
	bitBuf, bitCnt := uint32(0), 0
	pos := 0
	for {
		for bitCnt < width {
			if pos >= len(data) {
				return out, nil // tolerate missing EOD
			}
			bitBuf = bitBuf<<8 | uint32(data[pos])
			pos++
			bitCnt += 8
		}
		code := int(bitBuf >> uint(bitCnt-width) & (1<<uint(width) - 1))
		bitCnt -= width

		switch {
		case code == clearCode:
			reset()
			width = 9
			prev = nil
			continue
		case code == eodCode:
			return out, nil
		}
		var entry []byte
		switch {
		case code < len(table):
			entry = table[code]
		case code == len(table) && prev != nil:
			entry = append(append([]byte{}, prev...), prev[0])
		default:
			return nil, errors.New("gopdf: corrupt LZW stream")
		}
		out = append(out, entry...)
		if len(out) > maxDecodedStream {
			return nil, errStreamTooLarge
		}
		if prev != nil && len(table) < 4096 {
			table = append(table, append(append([]byte{}, prev...), entry[0]))
		}
		prev = entry
		if len(table)+early >= 1<<uint(width) && width < 12 {
			width++
		}
	}
}

func asciiHexDecode(data []byte) ([]byte, error) {
	var out []byte
	var hi byte
	half := false
	for _, c := range data {
		if c == '>' {
			break
		}
		if isWS(c) {
			continue
		}
		v, err := hexVal(c)
		if err != nil {
			return nil, err
		}
		if half {
			out = append(out, hi<<4|v)
			half = false
		} else {
			hi, half = v, true
		}
	}
	if half {
		out = append(out, hi<<4)
	}
	return out, nil
}

func ascii85Decode(data []byte) ([]byte, error) {
	if i := bytes.Index(data, []byte("~>")); i >= 0 {
		data = data[:i]
	}
	return limitedCopy(ascii85.NewDecoder(bytes.NewReader(data)))
}

func runLengthDecode(data []byte) ([]byte, error) {
	var out []byte
	for i := 0; i < len(data); {
		n := int(data[i])
		i++
		switch {
		case n == 128:
			return out, nil
		case n < 128:
			if i+n+1 > len(data) {
				return nil, errSyntax
			}
			out = append(out, data[i:i+n+1]...)
			i += n + 1
		default:
			if i >= len(data) {
				return nil, errSyntax
			}
			for k := 0; k < 257-n; k++ {
				out = append(out, data[i])
			}
			i++
		}
		if len(out) > maxDecodedStream {
			return nil, errStreamTooLarge
		}
	}
	return out, nil
}

// applyPredictor reverses the PNG or TIFF predictor described by the
// filter's decode parameters.
func applyPredictor(data []byte, parm Dict, r *Reader) ([]byte, error) {
	pred := 1
	if v, ok := toInt(r.resolve(parm["Predictor"])); ok {
		pred = v
	}
	if pred <= 1 {
		return data, nil
	}
	colors := 1
	if v, ok := toInt(r.resolve(parm["Colors"])); ok && v > 0 {
		colors = v
	}
	bpc := 8
	if v, ok := toInt(r.resolve(parm["BitsPerComponent"])); ok && v > 0 {
		bpc = v
	}
	columns := 1
	if v, ok := toInt(r.resolve(parm["Columns"])); ok && v > 0 {
		columns = v
	}
	bpp := (colors*bpc + 7) / 8
	rowLen := (colors*bpc*columns + 7) / 8
	if rowLen <= 0 || rowLen > maxDecodedStream {
		return nil, errSyntax
	}

	if pred == 2 { // TIFF horizontal differencing (8-bit components)
		if bpc != 8 {
			return nil, fmt.Errorf("gopdf: TIFF predictor with %d bpc not supported", bpc)
		}
		for row := 0; row+rowLen <= len(data); row += rowLen {
			for i := bpp; i < rowLen; i++ {
				data[row+i] += data[row+i-bpp]
			}
		}
		return data, nil
	}

	// PNG predictors: each row is prefixed by a filter-type byte.
	nRows := len(data) / (rowLen + 1)
	out := make([]byte, 0, nRows*rowLen)
	var prior []byte
	for row := 0; row+rowLen+1 <= len(data); row += rowLen + 1 {
		ft := data[row]
		cur := append([]byte{}, data[row+1:row+1+rowLen]...)
		for i := range cur {
			var left, up, upLeft byte
			if i >= bpp {
				left = cur[i-bpp]
			}
			if prior != nil {
				up = prior[i]
				if i >= bpp {
					upLeft = prior[i-bpp]
				}
			}
			switch ft {
			case 0:
			case 1:
				cur[i] += left
			case 2:
				cur[i] += up
			case 3:
				cur[i] += byte((int(left) + int(up)) / 2)
			case 4:
				cur[i] += paeth(left, up, upLeft)
			default:
				return nil, errSyntax
			}
		}
		out = append(out, cur...)
		prior = cur
	}
	return out, nil
}

func paeth(a, b, c byte) byte {
	p := int(a) + int(b) - int(c)
	pa, pb, pc := abs(p-int(a)), abs(p-int(b)), abs(p-int(c))
	if pa <= pb && pa <= pc {
		return a
	}
	if pb <= pc {
		return b
	}
	return c
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
