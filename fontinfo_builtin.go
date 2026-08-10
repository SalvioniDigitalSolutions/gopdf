package gopdf

// A font that is not in the document.
//
// Every fontInfo elsewhere is built from a font the file already carries:
// its widths come from /Widths, its encoding from /Encoding, and what it
// can set is bounded by the glyphs the file embeds. That is the right
// model for rewriting text a document already draws.
//
// It is the wrong model for text the document has never drawn. A token
// substituted into a page may need characters the original subset has no
// glyph for — "[[PII_NAME_1]]" into a font subset that carries no
// bracket — and there is no reader-side font to ask. What is needed then
// is a font of this library's own, described by the authoring metrics in
// font.go, wearing the same interface as one read out of a file.

// builtinFontInfo describes one of the standard fourteen as a fontInfo,
// so the flow engine can measure and encode with it exactly as it does
// with a font read from the document.
//
// name is the resource name the content stream will use, which the caller
// gets from registering the font with the page.
func builtinFontInfo(f *Font, name Name) *fontInfo {
	fi := &fontInfo{
		name:     name,
		encode:   make(map[rune][]byte, 256),
		widths:   make(map[uint32]float64, 256),
		observed: make(map[uint32]bool),
		// The tables below are complete, so nothing is to be inverted out
		// of a decoder later.
		built: true,
		// Not embedded: a viewer supplies the outlines, so every code the
		// encoding covers can be written back.
		embedded: false,
		builtin:  f,
	}
	// WinAnsi is what this library writes the standard fonts with, so the
	// mapping from rune to code is that table, read backwards.
	table := winAnsiRunes()
	for code := 0; code < 256; code++ {
		r := table[code]
		if r == 0 {
			continue
		}
		if _, taken := fi.encode[r]; !taken {
			fi.encode[r] = []byte{byte(code)}
		}
		fi.widths[uint32(code)] = float64(f.glyphWidth(byte(code)))
	}
	fi.defWidth = float64(f.defaultWidth)
	return fi
}

// standardFallback chooses the standard font that best matches a style,
// so a token replacing bold text still reads bold.
func standardFallback(st flowStyle) *Font {
	bold, italic := false, false
	if st.font != nil {
		bold, italic = st.font.bold, st.font.italic
	}
	switch {
	case bold && italic:
		return HelveticaBoldOblique
	case bold:
		return HelveticaBold
	case italic:
		return HelveticaOblique
	default:
		return Helvetica
	}
}
