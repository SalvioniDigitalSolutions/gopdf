package gopdf

import (
	"fmt"
	"sort"
	"strings"
)

// GradientStop is one colour in a gradient ramp, at a position from 0 at
// the start of the gradient axis to 1 at its end.
type GradientStop struct {
	Offset float64
	Color  Color
}

// Stop is shorthand for building a GradientStop.
func Stop(offset float64, c Color) GradientStop {
	return GradientStop{Offset: offset, Color: c}
}

// GradientDirection selects the axis of a rectangular gradient.
type GradientDirection int

const (
	// GradientVertical runs from the top edge to the bottom edge.
	GradientVertical GradientDirection = iota
	// GradientHorizontal runs from the left edge to the right edge.
	GradientHorizontal
	// GradientDiagonal runs from the top-left corner to the bottom-right.
	GradientDiagonal
)

// shading is a gradient registered with the document.
type shading struct {
	radial bool
	coords []float64 // [x0 y0 x1 y1] or [x0 y0 r0 x1 y1 r1], PDF space
	stops  []GradientStop
}

// normalizeStops validates and orders a gradient's stops.
func normalizeStops(stops []GradientStop) ([]GradientStop, error) {
	if len(stops) < 2 {
		return nil, fmt.Errorf("gopdf: a gradient needs at least two colour stops")
	}
	out := make([]GradientStop, len(stops))
	copy(out, stops)
	for i := range out {
		out[i].Offset = clamp01(out[i].Offset)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Offset < out[j].Offset })
	// The ramp must span the whole domain for /Extend to behave.
	if out[0].Offset != 0 {
		out = append([]GradientStop{{0, out[0].Color}}, out...)
	}
	if out[len(out)-1].Offset != 1 {
		out = append(out, GradientStop{1, out[len(out)-1].Color})
	}
	return out, nil
}

// addShading registers a gradient and returns its resource index.
func (d *Document) addShading(s *shading) int {
	d.shadings = append(d.shadings, s)
	return len(d.shadings) - 1
}

// Clip restricts subsequent drawing to the path just built with MoveTo,
// LineTo and CurveTo. Use it between Push and Pop, and follow it with a
// gradient or other painting operator.
//
// evenOdd selects the even-odd rule instead of the default nonzero
// winding rule.
func (p *Page) Clip(evenOdd bool) {
	if evenOdd {
		p.op("W* n")
	} else {
		p.op("W n")
	}
}

// PaintLinearGradient fills the current clipping region with a gradient
// running from (x0, y0) to (x1, y1). Clip first — with ClipRect, or a path
// followed by Clip — or the gradient covers the whole page.
func (p *Page) PaintLinearGradient(x0, y0, x1, y1 float64, stops ...GradientStop) error {
	norm, err := normalizeStops(stops)
	if err != nil {
		return err
	}
	idx := p.doc.addShading(&shading{
		coords: []float64{x0, p.flip(y0), x1, p.flip(y1)},
		stops:  norm,
	})
	p.op("/%s sh", p.resName("Sh", idx+1))
	return nil
}

// PaintRadialGradient fills the current clipping region with a gradient
// spreading from a circle of radius rInner to one of radius rOuter, both
// centred at (cx, cy).
func (p *Page) PaintRadialGradient(cx, cy, rInner, rOuter float64, stops ...GradientStop) error {
	norm, err := normalizeStops(stops)
	if err != nil {
		return err
	}
	if rInner < 0 || rOuter <= 0 {
		return fmt.Errorf("gopdf: radial gradient needs a positive outer radius")
	}
	y := p.flip(cy)
	idx := p.doc.addShading(&shading{
		radial: true,
		coords: []float64{cx, y, rInner, cx, y, rOuter},
		stops:  norm,
	})
	p.op("/%s sh", p.resName("Sh", idx+1))
	return nil
}

// FillGradientRect fills a rectangle with a linear gradient.
func (p *Page) FillGradientRect(x, y, w, h float64, dir GradientDirection, stops ...GradientStop) error {
	var x0, y0, x1, y1 float64
	switch dir {
	case GradientHorizontal:
		x0, y0, x1, y1 = x, y, x+w, y
	case GradientDiagonal:
		x0, y0, x1, y1 = x, y, x+w, y+h
	default:
		x0, y0, x1, y1 = x, y, x, y+h
	}
	p.Push()
	p.ClipRect(x, y, w, h)
	err := p.PaintLinearGradient(x0, y0, x1, y1, stops...)
	p.Pop()
	return err
}

// FillGradientCircle fills a circle with a radial gradient spreading from
// its centre to its edge.
func (p *Page) FillGradientCircle(cx, cy, r float64, stops ...GradientStop) error {
	p.Push()
	p.Circle(cx, cy, r, ClipPath)
	err := p.PaintRadialGradient(cx, cy, 0, r, stops...)
	p.Pop()
	return err
}

// --- serialization ---

// writeShading emits one gradient's shading dictionary and the function
// that drives its colour ramp.
func (d *Document) writeShading(ow *offsetWriter, s *shading, funcNum int) {
	kind := 2
	if s.radial {
		kind = 3
	}
	ow.printf("<< /ShadingType %d /ColorSpace /DeviceRGB /Coords [", kind)
	for i, c := range s.coords {
		if i > 0 {
			ow.str(" ")
		}
		ow.str(fl(c))
	}
	ow.printf("] /Extend [true true] /Function %d 0 R >>\n", funcNum)
}

// shadingFunction renders the colour ramp. Two stops need a single
// exponential function; more are stitched together.
func shadingFunction(s *shading) string {
	comps := func(c Color) string {
		return fmt.Sprintf("[%s]", c.components())
	}
	if len(s.stops) == 2 {
		return fmt.Sprintf("<< /FunctionType 2 /Domain [0 1] /C0 %s /C1 %s /N 1 >>",
			comps(s.stops[0].Color), comps(s.stops[1].Color))
	}
	var subs, bounds, encode strings.Builder
	for i := 0; i < len(s.stops)-1; i++ {
		if i > 0 {
			subs.WriteString(" ")
			encode.WriteString(" ")
		}
		fmt.Fprintf(&subs, "<< /FunctionType 2 /Domain [0 1] /C0 %s /C1 %s /N 1 >>",
			comps(s.stops[i].Color), comps(s.stops[i+1].Color))
		encode.WriteString("0 1")
		if i > 0 {
			bounds.WriteString(" ")
		}
		if i < len(s.stops)-2 {
			bounds.WriteString(fl(s.stops[i+1].Offset))
		}
	}
	return fmt.Sprintf("<< /FunctionType 3 /Domain [0 1] /Functions [%s] /Bounds [%s] /Encode [%s] >>",
		subs.String(), strings.TrimSpace(bounds.String()), encode.String())
}
