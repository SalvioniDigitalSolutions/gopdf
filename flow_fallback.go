package gopdf

// Setting a token the document's own font cannot.
//
// A subset font carries the glyphs the document draws and no others. Ask
// it for "[[PII_NAME_1]]" and it very likely has no bracket, so the
// substitution is refused — correctly, since drawing a character a font
// has no glyph for produces a blank box, and a document quietly full of
// blank boxes is worse than one that said no.
//
// But the refusal is avoidable. The token is not the document's text; it
// is text the edit is adding, and nothing says it has to be set in the
// document's font. Adding one of the standard fourteen to the page and
// setting the token in that keeps the substitution honest: the glyphs are
// real, the viewer supplies them, and the text either side is untouched.
//
// The permission is deliberately narrow. Only spans the edit inserted may
// move to a fallback; text the document already drew keeps its font
// whatever happens, because restyling that would change a page the caller
// did not ask to change.

// fallbackFor returns a resolver that sets inserted text in one of the
// standard fourteen, matched to the face of the style it replaces.
//
// The font is registered with the page as it is asked for, so the
// resource name baked into the content now is the name the resource
// dictionary will carry at write time.
func fallbackFor(host styleHost) func(flowStyle) (flowStyle, bool) {
	if host == nil {
		return nil
	}
	// One fontInfo per face per page, so repeated substitutions share a
	// resource rather than registering it again for every token.
	cache := map[*Font]flowStyle{}
	return func(st flowStyle) (flowStyle, bool) {
		f := standardFallback(st)
		out := st
		if got, ok := cache[f]; ok {
			out.fontName, out.font = got.fontName, got.font
			return out, true
		}
		name := host.registerFont(f)
		info := builtinFontInfo(f, Name(name))
		cache[f] = flowStyle{fontName: Name(name), font: info}
		out.fontName, out.font = Name(name), info
		// Everything else about the style carries over untouched: the
		// size, the colour, the spacing. Only the typeface changes.
		return out, true
	}
}
