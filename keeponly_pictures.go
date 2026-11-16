package gopdf

// keeponly_pictures.go — a picture file holds more than its samples.
//
// A JPEG carries segments a viewer never draws: a camera's notes (EXIF,
// with names, places and a preview of the picture), XMP, a press
// agency's captions (IPTC), comments. A JPEG 2000 file carries boxes of
// XML, of private data, of rights. KeepOnly keeps the picture and leaves
// these out. What is needed to draw it stays: the frame, the tables, the
// colour transform (APP14), the colour profile (APP2).
//
// A file this code cannot follow is kept as it came: a picture the
// reader cannot follow is one a viewer cannot draw either, and the
// rendered-page check reads what is drawn.

import (
	"bytes"
	"encoding/binary"
)

// pictureWithoutNotes returns data without its notes, for a picture
// stored with filter DCTDecode or JPXDecode alone.
func pictureWithoutNotes(dict Dict, data []byte) []byte {
	var filter Name
	switch f := dict["Filter"].(type) {
	case Name:
		filter = f
	case Array:
		if len(f) == 1 {
			filter, _ = f[0].(Name)
		}
	}
	switch filter {
	case "DCTDecode", "DCT":
		if out, ok := jpegWithoutNotes(data); ok {
			return out
		}
	case "JPXDecode":
		if out, ok := jpxWithoutNotes(data); ok {
			return out
		}
	}
	return data
}

// jpegWithoutNotes drops a JPEG's APP1 and APP3–APP13, APP15 and COM
// segments, and the preview a JFIF APP0 may hold, up to the first scan.
// Everything from the scan on is copied as it is.
func jpegWithoutNotes(data []byte) ([]byte, bool) {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil, false
	}
	out := make([]byte, 0, len(data))
	out = append(out, 0xFF, 0xD8)
	i := 2
	changed := false
	for {
		// Fill bytes before a marker.
		for i < len(data) && data[i] == 0xFF && i+1 < len(data) && data[i+1] == 0xFF {
			i++
		}
		if i+1 >= len(data) || data[i] != 0xFF {
			return nil, false
		}
		m := data[i+1]
		switch {
		case m == 0xD8 || m == 0x01 || (m >= 0xD0 && m <= 0xD7):
			out = append(out, data[i:i+2]...)
			i += 2
			continue
		case m == 0xD9:
			// An end before any scan: a picture of nothing.
			out = append(out, data[i:]...)
			return out, changed
		}
		if i+4 > len(data) {
			return nil, false
		}
		n := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
		if n < 2 || i+2+n > len(data) {
			return nil, false
		}
		seg := data[i : i+2+n]
		switch {
		case m == 0xDA:
			// The scan: from here on, samples and what decodes them.
			out = append(out, data[i:]...)
			if !changed {
				return data, true
			}
			return out, true
		case m == 0xE0:
			if kept, ok := jfifWithoutPreview(seg); ok {
				if !bytes.Equal(kept, seg) {
					changed = true
				}
				out = append(out, kept...)
			} else {
				changed = true // JFXX, or an APP0 of something else
			}
		case m == 0xE2 || m == 0xEE:
			out = append(out, seg...)
		case (m >= 0xE1 && m <= 0xEF) || m == 0xFE:
			changed = true
		default:
			out = append(out, seg...)
		}
		i += 2 + n
	}
}

// jfifWithoutPreview keeps a JFIF APP0 segment with its preview taken
// out; any other APP0 is not kept.
func jfifWithoutPreview(seg []byte) ([]byte, bool) {
	// FF E0, length, "JFIF\0", version (2), units (1), density (4),
	// preview width and height (1+1), preview.
	if len(seg) < 18 || !bytes.Equal(seg[4:9], []byte("JFIF\x00")) {
		return nil, false
	}
	if seg[16] == 0 && seg[17] == 0 && len(seg) == 18 {
		return seg, true
	}
	out := append([]byte(nil), seg[:18]...)
	out[2], out[3] = 0, 16
	out[16], out[17] = 0, 0
	return out, true
}

// jpxWithoutNotes drops a JPEG 2000 file's XML, UUID, UUID-info and
// rights boxes, and the comments of a codestream's main header. A bare
// codestream has its comments dropped too.
func jpxWithoutNotes(data []byte) ([]byte, bool) {
	if len(data) >= 2 && data[0] == 0xFF && data[1] == 0x4F {
		return codestreamWithoutComments(data)
	}
	if len(data) < 12 || !bytes.Equal(data[4:8], []byte("jP  ")) {
		return nil, false
	}
	out := make([]byte, 0, len(data))
	i := 0
	for i < len(data) {
		if i+8 > len(data) {
			return nil, false
		}
		size := uint64(binary.BigEndian.Uint32(data[i : i+4]))
		typ := string(data[i+4 : i+8])
		head := 8
		switch size {
		case 0:
			size = uint64(len(data) - i)
		case 1:
			if i+16 > len(data) {
				return nil, false
			}
			size = binary.BigEndian.Uint64(data[i+8 : i+16])
			head = 16
		}
		if size < uint64(head) || size > uint64(len(data)-i) {
			return nil, false
		}
		box := data[i : i+int(size)]
		switch typ {
		case "xml ", "uuid", "uinf", "jp2i", "ipr ":
		case "jp2c":
			cs, ok := codestreamWithoutComments(box[head:])
			if !ok {
				out = append(out, box...)
				break
			}
			if len(cs) == len(box)-head {
				out = append(out, box...)
				break
			}
			// Written with an eight-byte header, sized to what it holds.
			var h [8]byte
			binary.BigEndian.PutUint32(h[:4], uint32(8+len(cs)))
			copy(h[4:], "jp2c")
			out = append(out, h[:]...)
			out = append(out, cs...)
		default:
			out = append(out, box...)
		}
		i += int(size)
	}
	return out, true
}

// codestreamWithoutComments drops the COM segments of a JPEG 2000
// codestream's main header (up to the first tile).
func codestreamWithoutComments(data []byte) ([]byte, bool) {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0x4F {
		return nil, false
	}
	out := make([]byte, 0, len(data))
	out = append(out, 0xFF, 0x4F)
	i := 2
	for {
		if i+4 > len(data) || data[i] != 0xFF {
			return nil, false
		}
		m := data[i+1]
		if m == 0x90 { // the first tile: the main header ends
			out = append(out, data[i:]...)
			return out, true
		}
		n := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
		if n < 2 || i+2+n > len(data) {
			return nil, false
		}
		if m != 0x64 {
			out = append(out, data[i:i+2+n]...)
		}
		i += 2 + n
	}
}
