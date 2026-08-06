package gopdf

import (
	"bytes"
	"fmt"
	"math"
	"strings"
)

// Color is an opaque RGB color.
type Color struct {
	R, G, B uint8
}

// RGB builds a Color from 8-bit components.
func RGB(r, g, b uint8) Color {
	return Color{r, g, b}
}

// Gray builds a neutral gray Color; 0 is black, 255 is white.
func Gray(v uint8) Color {
	return Color{v, v, v}
}

var (
	Black = Color{0, 0, 0}
	White = Color{255, 255, 255}
)

func (c Color) components() string {
	return fl(float64(c.R)/255) + " " + fl(float64(c.G)/255) + " " + fl(float64(c.B)/255)
}

// DrawMode selects how a shape or path is painted.
type DrawMode int

const (
	// Stroke outlines the shape with the stroke color.
	Stroke DrawMode = iota
	// Fill fills the shape with the fill color.
	Fill
	// FillStroke fills the shape, then outlines it.
	FillStroke
	// ClipPath restricts subsequent drawing to the shape instead of
	// painting it. Use it between Push and Pop so the clip is undone.
	ClipPath
)

func (m DrawMode) op() string {
	switch m {
	case Fill:
		return "f"
	case FillStroke:
		return "B"
	case ClipPath:
		// The clip operator takes the path, then a no-op paint ends it.
		return "W n"
	default:
		return "S"
	}
}

// Align selects horizontal text alignment for TextAligned.
type Align int

const (
	AlignLeft Align = iota
	AlignCenter
	AlignRight
)

// Page is a single page of a Document. All coordinates are in points with
// the origin at the top-left corner; y grows downward.
type Page struct {
	doc       *Document
	w, h      float64
	buf       bytes.Buffer
	font      *Font
	fontIdx   int
	fontSize  float64
	links     []link
	rotate    int
	rawAnnots []any // imported annotations, written verbatim

	// Set for pages imported with EditPage: the original content stream
	// (written before anything drawn through this API), the page's own
	// resource dictionary, its exact media box, and a prefix that keeps
	// this library's resource names from colliding with the source's.
	prelude      []byte
	ownResources Dict
	mediaBox     *[4]float64
	resPrefix    string

	// Known graphics state, used to skip operators that would not change
	// anything. Snapshotted by Push and restored by Pop, mirroring the
	// PDF q/Q semantics.
	state gstate
	stack []gstate
}

// gstate tracks the graphics-state parameters this API can set. Unset
// fields (nil / negative sentinels) mean the value is not known yet and
// the next setter must emit its operator.
type gstate struct {
	fill, stroke      *Color
	lineWidth         float64
	lineCap, lineJoin int
	dash              string
}

func newGstate() gstate {
	return gstate{lineWidth: -1, lineCap: -1, lineJoin: -1}
}

// link is a pending link annotation; exactly one of url and target is set.
type link struct {
	x, y, w, h float64
	url        string
	target     *Page
	targetY    float64
}

// SetRotate sets the page's display rotation in degrees clockwise; only
// multiples of 90 are meaningful.
func (p *Page) SetRotate(deg int) {
	p.rotate = ((deg % 360) + 360) % 360 / 90 * 90
}

// Width returns the page width in points.
func (p *Page) Width() float64 { return p.w }

// Height returns the page height in points.
func (p *Page) Height() float64 { return p.h }

func (p *Page) op(format string, args ...any) {
	fmt.Fprintf(&p.buf, format+"\n", args...)
}

// flip converts a top-left y coordinate to PDF's bottom-left space.
func (p *Page) flip(y float64) float64 {
	return p.h - y
}

// resName builds a resource name for this page, prefixed on imported
// pages so it cannot collide with a name the source document uses.
func (p *Page) resName(kind string, index int) string {
	return fmt.Sprintf("%s%s%d", p.resPrefix, kind, index)
}

// content returns the page's full content stream: the original operators
// of an imported page, followed by anything drawn through this API. The
// new content is wrapped so it cannot inherit graphics state left behind
// by the imported operators.
func (p *Page) content() []byte {
	if len(p.prelude) == 0 {
		return p.buf.Bytes()
	}
	out := make([]byte, 0, len(p.prelude)+p.buf.Len()+8)
	out = append(out, p.prelude...)
	if p.buf.Len() > 0 {
		out = append(out, "\nq\n"...)
		out = append(out, p.buf.Bytes()...)
		out = append(out, "\nQ\n"...)
	}
	return out
}

// --- Text ---

// SetFont selects the font and size (in points) for subsequent text calls.
func (p *Page) SetFont(f *Font, size float64) {
	p.font = f
	p.fontIdx = p.doc.addFont(f)
	p.fontSize = size
}

func (p *Page) ensureFont() {
	if p.font == nil {
		p.SetFont(Helvetica, 12)
	}
}

// Text draws s with its baseline starting at (x, y) using the current font.
// Text is filled with the current fill color.
//
// Embedded TrueType fonts render any character the font provides, with
// pair kerning applied when the font has a kern table. The standard 14
// fonts are limited to WinAnsi (CP-1252); characters outside that
// repertoire are replaced with '?'.
func (p *Page) Text(x, y float64, s string) {
	p.ensureFont()
	p.op("BT /%s %s Tf %s %s Td %s ET",
		p.resName("F", p.fontIdx+1), fl(p.fontSize), fl(x), fl(p.flip(y)), p.showText(s))
}

// showText renders s as a text-showing operation in the current font's
// encoding: a WinAnsi literal string for standard fonts, or an Identity-H
// TJ array of glyph IDs with kerning adjustments for embedded fonts
// (recording glyph usage for subsetting).
func (p *Page) showText(s string) string {
	if p.font.ttf == nil {
		return "(" + string(escapeString(winAnsiEncode(s))) + ") Tj"
	}
	t := p.font.ttf
	usage := p.doc.glyphUsage(p.fontIdx)
	var b strings.Builder
	b.WriteString("[<")
	prev, first := uint16(0), true
	for _, r := range s {
		gid := t.cmap[r]
		if !first {
			if k := t.toEm(t.kerning(prev, gid)); k != 0 {
				// TJ adjustments are subtracted from the advance.
				fmt.Fprintf(&b, "> %d <", -k)
			}
		}
		fmt.Fprintf(&b, "%04X", gid)
		if _, ok := usage[gid]; !ok {
			usage[gid] = r
		}
		prev, first = gid, false
	}
	b.WriteString(">] TJ")
	return b.String()
}

// TextWidth returns the rendered width of s in points for the current font
// and size.
func (p *Page) TextWidth(s string) float64 {
	p.ensureFont()
	return p.font.TextWidth(s, p.fontSize)
}

// TextAligned draws s aligned within the horizontal span from x to x+width,
// with the baseline at y.
func (p *Page) TextAligned(x, y, width float64, align Align, s string) {
	switch align {
	case AlignCenter:
		x += (width - p.TextWidth(s)) / 2
	case AlignRight:
		x += width - p.TextWidth(s)
	}
	p.Text(x, y, s)
}

// TextWrapped draws s word-wrapped to the given width, starting with the
// first baseline at (x, y) and advancing by lineHeight per line. Newlines
// in s force line breaks. It returns the y coordinate of the baseline that
// would follow the last drawn line.
func (p *Page) TextWrapped(x, y, width, lineHeight float64, s string) float64 {
	p.ensureFont()
	for _, paragraph := range strings.Split(s, "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			y += lineHeight
			continue
		}
		line := words[0]
		for _, word := range words[1:] {
			if p.TextWidth(line+" "+word) <= width {
				line += " " + word
			} else {
				p.Text(x, y, line)
				y += lineHeight
				line = word
			}
		}
		p.Text(x, y, line)
		y += lineHeight
	}
	return y
}

// --- Graphics state ---

// LineCap selects how stroke ends are drawn.
type LineCap int

const (
	CapButt LineCap = iota
	CapRound
	CapSquare
)

// LineJoin selects how stroke corners are drawn.
type LineJoin int

const (
	JoinMiter LineJoin = iota
	JoinRound
	JoinBevel
)

// The setters below skip their operator when the value is already current,
// keeping content streams small when callers re-set state per element.

// SetStrokeColor sets the color used to outline shapes and draw lines.
func (p *Page) SetStrokeColor(c Color) {
	if p.state.stroke != nil && *p.state.stroke == c {
		return
	}
	p.state.stroke = &c
	p.op("%s RG", c.components())
}

// SetFillColor sets the color used to fill shapes and draw text.
func (p *Page) SetFillColor(c Color) {
	if p.state.fill != nil && *p.state.fill == c {
		return
	}
	p.state.fill = &c
	p.op("%s rg", c.components())
}

// SetLineWidth sets the stroke width in points.
func (p *Page) SetLineWidth(w float64) {
	if p.state.lineWidth == w {
		return
	}
	p.state.lineWidth = w
	p.op("%s w", fl(w))
}

// SetLineCap sets the stroke cap style.
func (p *Page) SetLineCap(c LineCap) {
	if p.state.lineCap == int(c) {
		return
	}
	p.state.lineCap = int(c)
	p.op("%d J", c)
}

// SetLineJoin sets the stroke join style.
func (p *Page) SetLineJoin(j LineJoin) {
	if p.state.lineJoin == int(j) {
		return
	}
	p.state.lineJoin = int(j)
	p.op("%d j", j)
}

// SetDash sets the stroke dash pattern as alternating dash and gap lengths
// in points. Call with no arguments to return to solid lines.
func (p *Page) SetDash(pattern ...float64) {
	parts := make([]string, len(pattern))
	for i, v := range pattern {
		parts[i] = fl(v)
	}
	dash := "[" + strings.Join(parts, " ") + "] 0 d"
	if p.state.dash == dash {
		return
	}
	p.state.dash = dash
	p.op("%s", dash)
}

// SetAlpha sets the constant opacity for filling and stroking: 0 is fully
// transparent, 1 fully opaque. Use between Push and Pop to keep the effect
// scoped.
func (p *Page) SetAlpha(fill, stroke float64) {
	idx := p.doc.addAlpha(clamp01(fill), clamp01(stroke))
	p.op("/%s gs", p.resName("GS", idx+1))
}

func clamp01(v float64) float64 {
	return math.Max(0, math.Min(1, v))
}

// Push saves the graphics state (colors, line settings, transform).
// Every Push must be paired with a Pop.
func (p *Page) Push() {
	p.stack = append(p.stack, p.state)
	p.op("q")
}

// Pop restores the most recently pushed graphics state.
func (p *Page) Pop() {
	if n := len(p.stack); n > 0 {
		p.state = p.stack[n-1]
		p.stack = p.stack[:n-1]
	}
	p.op("Q")
}

// Translate shifts the coordinate system by (dx, dy). Use between Push and
// Pop to keep the effect scoped.
func (p *Page) Translate(dx, dy float64) {
	p.op("1 0 0 1 %s %s cm", fl(dx), fl(-dy))
}

// Scale scales the coordinate system about the point (x, y).
func (p *Page) Scale(sx, sy, x, y float64) {
	cy := p.flip(y)
	p.op("%s 0 0 %s %s %s cm", fl(sx), fl(sy), fl(x-sx*x), fl(cy-sy*cy))
}

// RotateAt rotates the coordinate system by deg degrees counterclockwise
// about the point (x, y). Coordinates passed to subsequent drawing calls
// are interpreted in the rotated system. Use between Push and Pop.
func (p *Page) RotateAt(deg, x, y float64) {
	a := deg * math.Pi / 180
	c, s := math.Cos(a), math.Sin(a)
	cy := p.flip(y)
	p.op("%s %s %s %s %s %s cm",
		fl(c), fl(s), fl(-s), fl(c), fl(x-c*x+s*cy), fl(cy-s*x-c*cy))
}

// --- Shapes ---

// Line draws a straight line from (x1, y1) to (x2, y2) with the current
// stroke color and width.
func (p *Page) Line(x1, y1, x2, y2 float64) {
	p.op("%s %s m %s %s l S", fl(x1), fl(p.flip(y1)), fl(x2), fl(p.flip(y2)))
}

// Rect draws a rectangle with its top-left corner at (x, y).
func (p *Page) Rect(x, y, w, h float64, mode DrawMode) {
	p.op("%s %s %s %s re %s", fl(x), fl(p.flip(y)-h), fl(w), fl(h), mode.op())
}

// RoundedRect draws a rectangle with corners rounded to radius r.
func (p *Page) RoundedRect(x, y, w, h, r float64, mode DrawMode) {
	r = math.Min(r, math.Min(w, h)/2)
	k := r * kappa
	x2, y2 := x+w, y+h
	p.MoveTo(x+r, y)
	p.LineTo(x2-r, y)
	p.CurveTo(x2-r+k, y, x2, y+r-k, x2, y+r)
	p.LineTo(x2, y2-r)
	p.CurveTo(x2, y2-r+k, x2-r+k, y2, x2-r, y2)
	p.LineTo(x+r, y2)
	p.CurveTo(x+r-k, y2, x, y2-r+k, x, y2-r)
	p.LineTo(x, y+r)
	p.CurveTo(x, y+r-k, x+r-k, y, x+r, y)
	p.ClosePath()
	p.DrawPath(mode)
}

// ClipRect restricts subsequent drawing to the given rectangle. Use between
// Push and Pop to restore the previous clipping region.
func (p *Page) ClipRect(x, y, w, h float64) {
	p.op("%s %s %s %s re W n", fl(x), fl(p.flip(y)-h), fl(w), fl(h))
}

// kappa is the Bézier control-point factor approximating a quarter circle.
const kappa = 0.5522847498

// Circle draws a circle centered at (cx, cy).
func (p *Page) Circle(cx, cy, r float64, mode DrawMode) {
	p.Ellipse(cx, cy, r, r, mode)
}

// Ellipse draws an axis-aligned ellipse centered at (cx, cy) with the given
// horizontal and vertical radii.
func (p *Page) Ellipse(cx, cy, rx, ry float64, mode DrawMode) {
	y := p.flip(cy)
	kx, ky := rx*kappa, ry*kappa
	p.op("%s %s m", fl(cx+rx), fl(y))
	p.op("%s %s %s %s %s %s c", fl(cx+rx), fl(y+ky), fl(cx+kx), fl(y+ry), fl(cx), fl(y+ry))
	p.op("%s %s %s %s %s %s c", fl(cx-kx), fl(y+ry), fl(cx-rx), fl(y+ky), fl(cx-rx), fl(y))
	p.op("%s %s %s %s %s %s c", fl(cx-rx), fl(y-ky), fl(cx-kx), fl(y-ry), fl(cx), fl(y-ry))
	p.op("%s %s %s %s %s %s c %s", fl(cx+kx), fl(y-ry), fl(cx+rx), fl(y-ky), fl(cx+rx), fl(y), mode.op())
}

// Polygon draws a closed polygon through the given points, supplied as
// alternating x, y pairs. Polygon panics if given fewer than three points
// or an odd number of coordinates.
func (p *Page) Polygon(mode DrawMode, xy ...float64) {
	if len(xy) < 6 || len(xy)%2 != 0 {
		panic("gopdf: Polygon needs at least 3 x,y pairs")
	}
	p.op("%s %s m", fl(xy[0]), fl(p.flip(xy[1])))
	for i := 2; i < len(xy); i += 2 {
		p.op("%s %s l", fl(xy[i]), fl(p.flip(xy[i+1])))
	}
	p.op("h %s", mode.op())
}

// --- Paths ---

// MoveTo begins a new subpath at (x, y). Build paths with MoveTo, LineTo,
// CurveTo and ClosePath, then paint them with DrawPath.
func (p *Page) MoveTo(x, y float64) {
	p.op("%s %s m", fl(x), fl(p.flip(y)))
}

// LineTo appends a straight segment from the current point to (x, y).
func (p *Page) LineTo(x, y float64) {
	p.op("%s %s l", fl(x), fl(p.flip(y)))
}

// CurveTo appends a cubic Bézier segment from the current point to (x, y)
// using control points (cx1, cy1) and (cx2, cy2).
func (p *Page) CurveTo(cx1, cy1, cx2, cy2, x, y float64) {
	p.op("%s %s %s %s %s %s c",
		fl(cx1), fl(p.flip(cy1)), fl(cx2), fl(p.flip(cy2)), fl(x), fl(p.flip(y)))
}

// ClosePath closes the current subpath back to its starting point.
func (p *Page) ClosePath() {
	p.op("h")
}

// DrawPath paints the path built by preceding MoveTo/LineTo/CurveTo calls.
func (p *Page) DrawPath(mode DrawMode) {
	p.op("%s", mode.op())
}

// --- Images ---

// DrawImage places img with its top-left corner at (x, y), scaled to w by h
// points. Use img.Width and img.Height to preserve the aspect ratio.
func (p *Page) DrawImage(img *Image, x, y, w, h float64) {
	p.op("q %s 0 0 %s %s %s cm /%s Do Q",
		fl(w), fl(h), fl(x), fl(p.flip(y)-h), p.resName("I", img.index+1))
}

// --- Links ---

// LinkURL makes the rectangle with top-left corner (x, y) a clickable link
// to the given URL.
func (p *Page) LinkURL(x, y, w, h float64, url string) {
	p.links = append(p.links, link{x: x, y: y, w: w, h: h, url: url})
}

// LinkPage makes the rectangle with top-left corner (x, y) a link that
// jumps to targetY points from the top of the target page.
func (p *Page) LinkPage(x, y, w, h float64, target *Page, targetY float64) {
	p.links = append(p.links, link{x: x, y: y, w: w, h: h, target: target, targetY: targetY})
}
