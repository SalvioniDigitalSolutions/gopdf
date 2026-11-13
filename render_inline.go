package gopdf

import (
	"bytes"
	"compress/zlib"
	"io"
)

// A picture written into a page's own instructions.
//
// Most pictures are objects of the file that a page names and draws. A
// small one may instead be written where it is drawn: BI, a few entries
// describing it, ID, its data, EI. Label printers and older writers put
// logos, bar codes and QR codes on a page this way.
//
// The redactor already deletes such a picture when it lies in a marked
// area. The renderer did not draw it, so a page drawn to be looked at —
// or to be searched for what is on it — showed white where a reader
// shows the picture. These two functions read what the renderer needs:
// the description, in the full names the image decoder knows, and where
// the data ends.

// inlineKeys are the short names an inline picture's entries may go by.
var inlineKeys = map[Name]Name{
	"BPC": "BitsPerComponent", "CS": "ColorSpace", "D": "Decode", "DP": "DecodeParms",
	"F": "Filter", "H": "Height", "IM": "ImageMask", "I": "Interpolate", "W": "Width", "L": "Length",
}

// inlineFilters and inlineSpaces are the short names of filters and of
// colour spaces.
var (
	inlineFilters = map[Name]Name{
		"AHx": "ASCIIHexDecode", "A85": "ASCII85Decode", "LZW": "LZWDecode", "Fl": "FlateDecode",
		"RL": "RunLengthDecode", "CCF": "CCITTFaxDecode", "DCT": "DCTDecode",
	}
	inlineSpaces = map[Name]Name{"G": "DeviceGray", "RGB": "DeviceRGB", "CMYK": "DeviceCMYK", "I": "Indexed"}
)

// inlineImageDict turns the operands between BI and ID into the
// dictionary an image object would have.
func inlineImageDict(r *Reader, operands []contentToken, res Dict) Dict {
	d := Dict{"Type": Name("XObject"), "Subtype": Name("Image")}
	for i := 0; i+1 < len(operands); i += 2 {
		key, ok := operands[i].val.(Name)
		if !ok {
			continue
		}
		if long, short := inlineKeys[key]; short {
			key = long
		}
		val := operands[i+1].val
		switch key {
		case "Filter":
			val = inlineNames(val, inlineFilters)
		case "ColorSpace":
			val = inlineColorSpace(r, val, res)
		}
		d[key] = val
	}
	return d
}

// inlineNames spells out a short name, or each short name of an array.
func inlineNames(v any, long map[Name]Name) any {
	switch t := v.(type) {
	case Name:
		if l, ok := long[t]; ok {
			return l
		}
	case Array:
		out := make(Array, len(t))
		for i, e := range t {
			out[i] = inlineNames(e, long)
		}
		return out
	}
	return v
}

// inlineColorSpace spells out a colour space: a short name, a name the
// page's resources define, or an array whose first names are short.
func inlineColorSpace(r *Reader, v any, res Dict) any {
	switch t := v.(type) {
	case Name:
		if l, ok := inlineSpaces[t]; ok {
			return l
		}
		switch t {
		case "DeviceGray", "DeviceRGB", "DeviceCMYK":
			return t
		}
		if spaces, ok := r.resolve(res["ColorSpace"]).(Dict); ok {
			if named, found := spaces[t]; found {
				return named
			}
		}
	case Array:
		out := make(Array, len(t))
		copy(out, t)
		for i := 0; i < len(out) && i < 2; i++ {
			if n, ok := out[i].(Name); ok {
				if l, short := inlineSpaces[n]; short {
					out[i] = l
				}
			}
		}
		return out
	}
	return v
}

// inlineImageData says where an inline picture's data ends and where
// the instructions go on: the data is content[from:to], and after is
// just past EI.
//
// Data that is not packed has a length its description gives, and EI
// is looked for there. Otherwise the end is the first EI that stands as
// an operator, white space before it and white space or the end of the
// stream after it — which is what every reader does, and the reason
// writers do not put packed binary data in line without a text filter.
func inlineImageData(r *Reader, content []byte, from int, d Dict) (to, after int) {
	isEI := func(i int) bool {
		return i+1 < len(content) && content[i] == 'E' && content[i+1] == 'I' &&
			(i+2 == len(content) || isWS(content[i+2]) || isDelim(content[i+2]))
	}
	if _, packed := d["Filter"]; !packed {
		w, _ := toInt(d["Width"])
		h, _ := toInt(d["Height"])
		bpc, _ := toInt(d["BitsPerComponent"])
		comps := 0
		switch colorSpaceName(r, d["ColorSpace"]) {
		case "DeviceGray", "Indexed", "CalGray":
			comps = 1
		case "DeviceRGB", "CalRGB", "Lab":
			comps = 3
		case "DeviceCMYK":
			comps = 4
		}
		if b, ok := d["ImageMask"].(bool); ok && b {
			bpc, comps = 1, 1
		}
		if n := h * ((w*bpc*comps + 7) / 8); n > 0 && w > 0 && from+n <= len(content) {
			j := from + n
			for j < len(content) && isWS(content[j]) {
				j++
			}
			if isEI(j) {
				return from + n, j + 2
			}
		}
	}
	// Deflated data says itself where it ends. Taken at its word, an
	// "EI" among its bytes — two letters with white space around them
	// turn up in any long run of packed data — is not mistaken for the
	// end of the picture.
	if name, _ := d["Filter"].(Name); name == "FlateDecode" {
		if n := deflatedLength(content[from:]); n > 0 {
			j := from + n
			for j < len(content) && isWS(content[j]) {
				j++
			}
			if isEI(j) {
				return from + n, j + 2
			}
		}
	}
	for i := from; i+1 < len(content); i++ {
		if isEI(i) && (i == from || isWS(content[i-1])) {
			to = i
			if to > from {
				to-- // the white space before EI is not data
			}
			return to, i + 2
		}
	}
	return from, len(content)
}

// deflatedLength is how many bytes of data the deflated stream at its
// start takes up; 0 when the data is not one that inflates to its end.
func deflatedLength(data []byte) int {
	src := bytes.NewReader(data)
	zr, err := zlib.NewReader(src)
	if err != nil {
		return 0
	}
	defer zr.Close()
	// A picture is not larger than this inflated; one that claims to
	// be is left to the search for EI.
	if _, err := io.Copy(io.Discard, io.LimitReader(zr, 1<<28)); err != nil {
		return 0
	}
	var one [1]byte
	if n, _ := zr.Read(one[:]); n != 0 {
		return 0
	}
	return len(data) - src.Len()
}

// inlineValuesDict is inlineImageDict for operands read as plain
// values, by a reader that keeps no positions and has no page at hand:
// the names a page's resources define are left as they are, which is
// enough to tell how long the data is.
func inlineValuesDict(operands []any) Dict {
	toks := make([]contentToken, len(operands))
	for i, v := range operands {
		toks[i] = contentToken{val: v}
	}
	return inlineImageDict(noPage, toks, nil)
}

// noPage stands in for a document where only values written out in
// place are looked at.
var noPage = &Reader{}

// skipInlineData returns where lexing goes on after the ID operator
// that ends at idEnd: at the EI that closes the picture, so that EI is
// still read as the operator it is. operands are the tokens between BI
// and ID.
func skipInlineData(content []byte, idEnd int, operands []contentToken) int {
	from := idEnd
	if from < len(content) && isWS(content[from]) {
		from++
	}
	_, after := inlineImageData(noPage, content, from, inlineImageDict(noPage, operands, nil))
	if after >= 2 && after <= len(content) && content[after-2] == 'E' && content[after-1] == 'I' {
		return after - 2
	}
	return after
}
