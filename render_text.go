package gopdf

import "math"

// Drawing text.
//
// A glyph is a path like any other, so once its outline can be read the
// rest is bookkeeping: where the pen is, how far each glyph moves it, and
// which of the eight rendering modes decides whether the shape is filled,
// stroked, added to the clip, or merely stepped over.
//
// The bookkeeping is the same arithmetic the text extractor already does,
// but it cannot be shared: extraction wants a string and stops at the
// glyph, and drawing wants a matrix per glyph and does not care what the
// glyph means. The two agree on the formula, which is the thing worth
// keeping the same.

// glyphState is the part of the graphics state that only text uses.
type glyphState struct {
	tm, tlm    matrix // the text matrix and the line matrix
	font       *glyphFont
	fontDict   Dict
	size       float64
	charSp     float64
	wordSp     float64
	hScale     float64 // /Tz, as a fraction
	leading    float64
	rise       float64
	mode       int
	singleByte bool // a simple font takes one byte per code
	widths     *fontInfo
}

func newGlyphState() glyphState {
	return glyphState{hScale: 1, tm: identity(), tlm: identity()}
}

// textRenderer carries what drawing text needs across a content stream.
type textRenderer struct {
	// fonts and info are keyed by the font's own object identity, never
	// by the resource name. A form XObject carries its own /Font
	// dictionary and producers reuse the same short names inside it, so
	// /TT0 on the page and /TT0 inside a form are routinely two
	// different fonts. Keying by name handed the second one the first
	// one's widths and glyph program — widths that did not cover the
	// codes being drawn, an advance of zero for each, and a whole run of
	// glyphs painted one on top of another as a single blot.
	fonts map[any]*glyphFont
	// clip accumulates the glyphs of modes 4 to 7, which do not take
	// effect until the text object ends.
	clip    *rasterPath
	clipAny bool
	// missing counts glyphs that could not be drawn, so the caller can be
	// told rather than left to wonder.
	missing int
	drawn   int
	info    map[any]*fontInfo
}

// fontKey identifies a font by the object it is, so two resource
// dictionaries using the same name cannot be taken for one another. An
// indirect reference names the object outright; a font written directly
// into its resource dictionary is reachable only through that
// dictionary, so within it the name is identity enough.
func fontKey(fonts Dict, name Name) any {
	if ref, ok := fonts[name].(Ref); ok {
		return ref
	}
	return name
}

// showText draws one string and advances the text matrix.
func (rn *renderer) showText(s []byte, gs *renderState, ts *glyphState, res Dict) {
	if ts.size == 0 && ts.mode != 7 {
		// A zero size still advances nothing and draws nothing.
		return
	}
	// Mode 3 draws nothing and clips nothing, so it only advances. With
	// text switched off the same is true of every painting mode: only
	// the clipping modes still have work to do, because what a text clip
	// removes from the artwork is part of the artwork.
	quiet := ts.mode == 3 || (!rn.opts.IncludeText && ts.mode < 4)
	// A threshold turns off everything below it, clipping included. The
	// scale is the text matrix's and the CTM's, and neither changes as
	// the pen advances through the string, so it is asked once.
	if rn.opts.MinTextSize > 0 && ts.effectiveSize(gs) < rn.opts.MinTextSize {
		quiet = true
	}
	for _, code := range textCodes(s, ts) {
		w := ts.advance(code.code)
		if !quiet {
			if ts.font == nil {
				rn.text.missing++
				rn.noteClipMode(ts)
			} else {
				rn.drawGlyph(code.code, gs, ts, res)
			}
		}
		// The advance is in text space: the glyph's width scaled by the
		// size, plus the spacing that applies between characters, all
		// stretched horizontally by /Tz.
		adv := (w*ts.size + ts.charSp) * ts.hScale
		if code.isSpace {
			adv += ts.wordSp * ts.hScale
		}
		ts.tm = matrix{1, 0, 0, 1, adv, 0}.mul(ts.tm)
	}
}

// textCode is one character code taken from a shown string.
type textCode struct {
	code    uint32
	isSpace bool
}

// textCodes splits a string into character codes. A composite font takes
// two bytes at a time; a simple one takes a byte, and only a simple
// font's single byte 32 counts as the word space the /Tw applies to.
func textCodes(s []byte, ts *glyphState) []textCode {
	if ts.singleByte {
		out := make([]textCode, 0, len(s))
		for _, b := range s {
			out = append(out, textCode{code: uint32(b), isSpace: b == 32})
		}
		return out
	}
	out := make([]textCode, 0, len(s)/2+1)
	for i := 0; i+1 < len(s); i += 2 {
		out = append(out, textCode{code: uint32(s[i])<<8 | uint32(s[i+1])})
	}
	if len(s)%2 == 1 {
		out = append(out, textCode{code: uint32(s[len(s)-1])})
	}
	return out
}

// effectiveSize is the size a glyph is drawn at on the page: the size
// the text state carries, after the text matrix and the current
// transformation have scaled it. It is the number PageTextFragments
// reports, so a threshold taken from extracted text means the same thing
// here.
func (ts *glyphState) effectiveSize(gs *renderState) float64 {
	m := ts.tm.mul(gs.ctm)
	return ts.size * math.Hypot(m[2], m[3])
}

// advance is a glyph's width in text space, where an em is 1.
func (ts *glyphState) advance(code uint32) float64 {
	if ts.widths != nil {
		return ts.widths.codeWidth(code) / 1000
	}
	return 0.5
}

// drawGlyph paints one glyph at the current pen position.
func (rn *renderer) drawGlyph(code uint32, gs *renderState, ts *glyphState, res Dict) {
	gid, ok := ts.font.gid(code)
	if !ok {
		rn.text.missing++
		rn.noteClipMode(ts)
		return
	}
	outline := ts.font.outline(gid)
	if outline == nil {
		// An empty glyph — a space, most often — is not a failure.
		return
	}
	// Font units to device: the font's own scale, then the text state's
	// size, horizontal scale and rise, then the text matrix and the CTM.
	m := ts.font.unitScale.
		mul(matrix{ts.size * ts.hScale, 0, 0, ts.size, 0, ts.rise}).
		mul(ts.tm).
		mul(gs.ctm)

	var path rasterPath
	outline.appendTo(&path, m)
	if path.empty() {
		return
	}
	rn.text.drawn++

	if rn.opts.IncludeText {
		switch ts.mode {
		case 0, 4:
			rn.fill(&path, gs, false, res)
		case 1, 5:
			rn.strokeText(&path, gs)
		case 2, 6:
			rn.fill(&path, gs, false, res)
			rn.strokeText(&path, gs)
		}
	}
	if ts.mode >= 4 {
		if rn.text.clip == nil {
			rn.text.clip = &rasterPath{}
		}
		rn.text.clip.subs = append(rn.text.clip.subs, path.subs...)
		rn.text.clipAny = true
	}
}

// noteClipMode records that a clipping text object had a glyph it could
// not resolve.
//
// The clip then holds whatever glyphs were readable and no more, so what
// shows through is less than it should be. That is the right way round:
// a text clip that cannot be built is usually a watermark or a headline
// filled with a gradient, and losing it costs a decoration. Ignoring the
// clip instead would paint that gradient across the whole page and bury
// everything under it.
func (rn *renderer) noteClipMode(ts *glyphState) {
	if ts.mode >= 4 {
		rn.text.clipAny = true
	}
}

// strokeText outlines a glyph with the current stroke settings.
func (rn *renderer) strokeText(path *rasterPath, gs *renderState) {
	outline := strokeOutline(path, gs.line)
	rn.paint(outline, false, gs.stroke, gs.strokeAlpha, gs.clip, gs.mode)
}

// endTextObject applies whatever the text object added to the clip.
//
// A text clip is the union of every glyph shown in modes 4 to 7, and it
// takes effect when the object ends. A text object that used a clipping
// mode and showed nothing clips everything away, which is the rule that
// makes an empty ET meaningful rather than a no-op.
func (rn *renderer) endTextObject(gs *renderState) {
	if !rn.text.clipAny {
		return
	}
	path := rn.text.clip
	if path == nil {
		path = &rasterPath{}
	}
	gs.baseClip = rn.intersectClip(gs.baseClip, path, false)
	gs.clip = combineMasks(gs.baseClip, gs.softMask)
	rn.text.clip, rn.text.clipAny = nil, false
}

// setFont looks a font up in the resources and prepares to draw it.
func (rn *renderer) setFont(res Dict, name Name, size float64, ts *glyphState) {
	ts.size = size
	ts.font, ts.fontDict, ts.widths, ts.singleByte = nil, nil, nil, true

	fonts, ok := rn.r.resolve(res["Font"]).(Dict)
	if !ok {
		return
	}
	dict, ok := rn.r.resolve(fonts[name]).(Dict)
	if !ok {
		return
	}
	key := fontKey(fonts, name)
	ts.fontDict = dict
	ts.singleByte = rn.r.resolve(dict["Subtype"]) != Name("Type0")
	ts.widths = rn.fontInfoFor(key, name, dict)

	if rn.text.fonts == nil {
		rn.text.fonts = make(map[any]*glyphFont)
	}
	if f, seen := rn.text.fonts[key]; seen {
		ts.font = f
		return
	}
	f := loadGlyphFont(rn.r, dict, rn.opts.SubstituteFont)
	rn.text.fonts[key] = f
	ts.font = f
}

// identity is the transform that changes nothing.
func identity() matrix { return matrix{1, 0, 0, 1, 0, 0} }

// fontInfoFor builds the metric tables for a font, reusing the same
// machinery text extraction uses so a glyph is stepped over by exactly
// the width the extractor would have measured.
func (rn *renderer) fontInfoFor(key any, name Name, dict Dict) *fontInfo {
	if rn.text.info == nil {
		rn.text.info = make(map[any]*fontInfo)
	}
	if fi, seen := rn.text.info[key]; seen {
		return fi
	}
	d := &fontDecoder{cid: rn.r.resolve(dict["Subtype"]) == Name("Type0")}
	if !d.cid {
		d.encoding = simpleEncoding(rn.r, dict["Encoding"])
	}
	fi := newFontInfo(rn.r, name, dict, d)
	rn.text.info[key] = fi
	return fi
}

// nextLine moves the pen down one line, which is what T* does and what
// the quote operators do before showing their string.
func (ts *glyphState) nextLine() {
	ts.tlm = matrix{1, 0, 0, 1, 0, -ts.leading}.mul(ts.tlm)
	ts.tm = ts.tlm
}

// showAdjusted draws a TJ array: strings with kerning numbers between
// them, each number moving the pen back by that many thousandths of the
// size before the next string.
func (rn *renderer) showAdjusted(arr Array, gs *renderState, ts *glyphState, res Dict) {
	for _, e := range arr {
		switch v := e.(type) {
		case String:
			rn.showText([]byte(v), gs, ts, res)
		default:
			adj, ok := toFloat(v)
			if !ok {
				continue
			}
			d := -adj / 1000 * ts.size * ts.hScale
			ts.tm = matrix{1, 0, 0, 1, d, 0}.mul(ts.tm)
		}
	}
}
