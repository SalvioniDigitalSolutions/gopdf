package gopdf

import (
	"bytes"
	"encoding/binary"
	"regexp"
	"strconv"
)

// The encodings embedded font programs carry.
//
// A simple font whose dictionary names no base encoding has the one its
// font program carries (ISO 32000-1, 9.6.6.1). For the standard fonts,
// and for fonts set in their image, that is StandardEncoding, and the
// package reads them so. A program embedded with the document may carry
// an encoding of its own, and then that is what every viewer reads: a
// report made on a Macintosh embedded Times New Roman as a Type 1 font
// whose encoding put the en dash at 0xD0. Read with a table guessed from
// the font's flags, the dash was "Ð", a letter, and "1979–1996" one word
// of nine letters, in which no year was found by the redactor or by the
// check after it.
//
// Only an encoding the program spells out code by code is read here: a
// Type 1 program's "dup 208 /endash put", a CFF program's own encoding
// table. A program that says it uses StandardEncoding says nothing the
// package does not already assume.

// programEncodingNames returns the glyph name the embedded program of a
// simple font gives each code it encodes itself, or nil when it encodes
// nothing of its own or carries no program this package reads.
func programEncodingNames(r *Reader, font Dict) map[int]string {
	fd, _ := r.resolve(font["FontDescriptor"]).(Dict)
	if fd == nil {
		return nil
	}
	if stm, ok := r.resolve(fd["FontFile"]).(*rawStream); ok {
		data, err := r.decodeStream(stm.dict, stm.data)
		if err != nil {
			return nil
		}
		clear, _ := toInt(r.resolve(stm.dict["Length1"]))
		return type1EncodingNames(data, clear)
	}
	if stm, ok := r.resolve(fd["FontFile3"]).(*rawStream); ok {
		if st, _ := r.resolve(stm.dict["Subtype"]).(Name); st != "Type1C" {
			return nil
		}
		data, err := r.decodeStream(stm.dict, stm.data)
		if err != nil {
			return nil
		}
		return cffEncodingNames(data)
	}
	return nil
}

var type1EncodingEntry = regexp.MustCompile(`dup\s+(\d+)\s*/([^\s/\[\]{}()<>%]+)\s+put`)

// type1EncodingNames reads the /Encoding array of a Type 1 program's
// clear-text part — the first clear bytes (Length1), or what comes
// before eexec where the length is not stated.
func type1EncodingNames(data []byte, clear int) map[int]string {
	if clear <= 0 || clear > len(data) {
		clear = len(data)
		if i := bytes.Index(data, []byte("eexec")); i >= 0 {
			clear = i
		}
	}
	text := data[:clear]
	at := bytes.Index(text, []byte("/Encoding"))
	if at < 0 {
		return nil
	}
	text = text[at:]
	// The array ends where it is defined; anything after is not it.
	if end := bytes.Index(text, []byte(" def")); end >= 0 && bytes.Contains(text[:end], []byte("StandardEncoding")) {
		return nil
	}
	names := map[int]string{}
	for _, m := range type1EncodingEntry.FindAllSubmatch(text, -1) {
		code, err := strconv.Atoi(string(m[1]))
		if err != nil || code < 0 || code > 255 {
			continue
		}
		names[code] = string(m[2])
	}
	if len(names) == 0 {
		return nil
	}
	return names
}

// cffEncodingNames reads a name-keyed CFF program's own encoding: the
// glyph each code selects (its encoding table, supplements included),
// named through its charset. A program that uses the predefined
// Standard or Expert encoding, and a CID-keyed one, give nil.
func cffEncodingNames(data []byte) map[int]string {
	if len(data) < 4 {
		return nil
	}
	names, err := parseCFFIndex(data, int(data[2]))
	if err != nil {
		return nil
	}
	topIdx, err := parseCFFIndex(data, names.end)
	if err != nil || len(topIdx.items) == 0 {
		return nil
	}
	strs, err := parseCFFIndex(data, topIdx.end)
	if err != nil {
		return nil
	}
	top, err := parseCFFDict(topIdx.items[0])
	if err != nil || dictEntry(top, 1230) != nil { // ROS: CID-keyed
		return nil
	}
	enc := dictEntry(top, cffOpEncoding)
	if enc == nil || len(enc.values) != 1 || enc.values[0] <= 1 {
		return nil // StandardEncoding, ExpertEncoding
	}
	cs := dictEntry(top, cffOpCharStrings)
	if cs == nil || len(cs.values) != 1 {
		return nil
	}
	csIdx, err := parseCFFIndex(data, cs.values[0])
	if err != nil {
		return nil
	}
	nGlyphs := len(csIdx.items)
	sids := make([]uint16, nGlyphs)
	charset := dictEntry(top, cffOpCharset)
	switch {
	case charset == nil || len(charset.values) != 1 || charset.values[0] == 0:
		// ISOAdobe: glyph i is string i.
		for gid := range sids {
			sids[gid] = uint16(gid)
		}
	case charset.values[0] <= 2:
		return nil // the expert charsets, which no text font uses
	default:
		if sids, err = parseCharsetSIDs(data, charset.values[0], nGlyphs); err != nil {
			return nil
		}
	}
	name := func(sid uint16) string {
		if int(sid) < cffStandardStrings {
			return cffStandardNames[sid]
		}
		if i := int(sid) - cffStandardStrings; i < len(strs.items) {
			return string(strs.items[i])
		}
		return ""
	}
	out := map[int]string{}
	p := enc.values[0]
	if p < 0 || p+2 > len(data) {
		return nil
	}
	format := data[p]
	switch format & 0x7f {
	case 0:
		n := int(data[p+1])
		if p+2+n > len(data) {
			return nil
		}
		for i := 0; i < n; i++ {
			if gid := i + 1; gid < nGlyphs {
				out[int(data[p+2+i])] = name(sids[gid])
			}
		}
		p += 2 + n
	case 1:
		n := int(data[p+1])
		if p+2+2*n > len(data) {
			return nil
		}
		gid := 1
		for i := 0; i < n; i++ {
			first, left := int(data[p+2+2*i]), int(data[p+3+2*i])
			for k := 0; k <= left && gid < nGlyphs; k++ {
				if code := first + k; code < 256 {
					out[code] = name(sids[gid])
				}
				gid++
			}
		}
		p += 2 + 2*n
	default:
		return nil
	}
	if format&0x80 != 0 && p < len(data) {
		n := int(data[p])
		for i := 0; i < n && p+1+3*i+3 <= len(data); i++ {
			q := p + 1 + 3*i
			out[int(data[q])] = name(binary.BigEndian.Uint16(data[q+1:]))
		}
	}
	for code, n := range out {
		if n == "" || n == ".notdef" {
			delete(out, code)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Converters that re-encode a font give its glyphs names of their own
// making, and some make them of a short prefix and the character's code
// in the Windows encoding: "G84" for T, "MT32" for the space, "G151"
// for the em dash. No glyph list knows such a name. Read as nothing, the
// code kept a base encoding's guess — a control character for the low
// codes such fonts are re-encoded to — and a report's whole body read
// as "!\t!\n\"#$$%", or its line "Full text free online at
// www.cdc.gov/eid" as no text at all: the values in it were neither
// removed nor seen by the check after the removal. Where a font's
// unknown names are all of one such kind, each is read as the character
// its number is in the Windows encoding. A chart program names a Type 3
// font's glyphs by the number alone: a figure's caption, "Figure 2.
// Isopleth …", read as nothing but unmapped codes, and so did the place
// names on its map.
var numberedGlyphName = regexp.MustCompile(`^([A-Za-z]{0,3})(\d{2,3})$`)

// numberedNameRunes returns the characters a font's unknown glyph names
// stand for when they are numbered names of one prefix — at least three
// of them, and nearly all of the unknown ones — or nil.
func numberedNameRunes(unknown map[int]string) map[int]rune {
	if len(unknown) < 3 {
		return nil
	}
	prefixes := map[string]int{}
	for _, n := range unknown {
		if m := numberedGlyphName.FindStringSubmatch(n); m != nil {
			prefixes[m[1]]++
		}
	}
	prefix, most := "", 0
	for p, c := range prefixes {
		if c > most || (c == most && p < prefix) {
			prefix, most = p, c
		}
	}
	if most*10 < len(unknown)*9 {
		return nil
	}
	win := winAnsiRunes()
	out := map[int]rune{}
	for code, n := range unknown {
		m := numberedGlyphName.FindStringSubmatch(n)
		if m == nil || m[1] != prefix {
			continue
		}
		v, err := strconv.Atoi(m[2])
		if err != nil || v < 32 || v > 255 || win[v] == 0 {
			continue
		}
		out[code] = win[v]
	}
	return out
}
