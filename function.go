package gopdf

import "math"

// Evaluating a PDF function.
//
// A shading does not carry colours; it carries a function from a position
// along the gradient to a colour, and the colour has to be asked for. The
// three kinds that appear in real files are here: a sampled table, an
// exponential ramp between two colours, and a stitching of several
// others end to end. The fourth kind is a small PostScript program, which
// is not evaluated; a shading built on one falls back to a solid.

// pdfFunction maps one input to a colour's components.
type pdfFunction interface {
	eval(t float64) []float64
}

// loadFunction reads whatever function an object describes, or an array
// of them, one per output component.
func loadFunction(r *Reader, v any) pdfFunction {
	switch t := r.resolve(v).(type) {
	case Array:
		// An array of functions, each giving one component.
		var parts []pdfFunction
		for _, e := range t {
			f := loadFunction(r, e)
			if f == nil {
				return nil
			}
			parts = append(parts, f)
		}
		if len(parts) == 0 {
			return nil
		}
		return &sideBySide{parts: parts}
	case Dict:
		return loadFunctionDict(r, t, nil)
	case *rawStream:
		return loadFunctionDict(r, t.dict, t)
	}
	return nil
}

func loadFunctionDict(r *Reader, d Dict, stm *rawStream) pdfFunction {
	kind, _ := toInt(r.resolve(d["FunctionType"]))
	domain := floatArray(r, d["Domain"])
	switch kind {
	case 0:
		return loadSampled(r, d, stm, domain)
	case 2:
		return loadExponential(r, d, domain)
	case 3:
		return loadStitching(r, d, domain)
	}
	return nil // a PostScript calculator is left to the caller
}

// sideBySide runs several one-output functions and joins their results.
type sideBySide struct{ parts []pdfFunction }

func (f *sideBySide) eval(t float64) []float64 {
	out := make([]float64, 0, len(f.parts))
	for _, p := range f.parts {
		out = append(out, p.eval(t)...)
	}
	return out
}

// exponential ramps from C0 to C1, usually linearly.
type exponential struct {
	c0, c1 []float64
	n      float64
	d0, d1 float64
}

func loadExponential(r *Reader, d Dict, domain []float64) pdfFunction {
	f := &exponential{
		c0: floatArray(r, d["C0"]),
		c1: floatArray(r, d["C1"]),
		n:  1,
	}
	if v, ok := toFloat(r.resolve(d["N"])); ok && v != 0 {
		f.n = v
	}
	if len(f.c0) == 0 {
		f.c0 = []float64{0}
	}
	if len(f.c1) == 0 {
		f.c1 = []float64{1}
	}
	f.d0, f.d1 = domainOr(domain)
	return f
}

func (f *exponential) eval(t float64) []float64 {
	t = normalize(t, f.d0, f.d1)
	x := t
	if f.n != 1 {
		x = math.Pow(t, f.n)
	}
	n := len(f.c0)
	if len(f.c1) < n {
		n = len(f.c1)
	}
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		out[i] = f.c0[i] + x*(f.c1[i]-f.c0[i])
	}
	return out
}

// stitching lays several functions end to end over the domain.
type stitching struct {
	funcs    []pdfFunction
	bounds   []float64
	encode   []float64
	d0, d1   float64
	fallback []float64
}

func loadStitching(r *Reader, d Dict, domain []float64) pdfFunction {
	arr, ok := r.resolve(d["Functions"]).(Array)
	if !ok || len(arr) == 0 {
		return nil
	}
	f := &stitching{
		bounds: floatArray(r, d["Bounds"]),
		encode: floatArray(r, d["Encode"]),
	}
	for _, e := range arr {
		sub := loadFunction(r, e)
		if sub == nil {
			return nil
		}
		f.funcs = append(f.funcs, sub)
	}
	f.d0, f.d1 = domainOr(domain)
	return f
}

func (f *stitching) eval(t float64) []float64 {
	lo, hi := f.d0, f.d1
	if t < lo {
		t = lo
	}
	if t > hi {
		t = hi
	}
	i := 0
	for i < len(f.bounds) && t >= f.bounds[i] {
		i++
	}
	if i >= len(f.funcs) {
		i = len(f.funcs) - 1
	}
	// The piece's own slice of the domain, mapped onto its encoding.
	sublo := lo
	if i > 0 {
		sublo = f.bounds[i-1]
	}
	subhi := hi
	if i < len(f.bounds) {
		subhi = f.bounds[i]
	}
	e0, e1 := 0.0, 1.0
	if len(f.encode) >= 2*i+2 {
		e0, e1 = f.encode[2*i], f.encode[2*i+1]
	}
	x := e0
	if subhi != sublo {
		x = e0 + (t-sublo)/(subhi-sublo)*(e1-e0)
	}
	return f.funcs[i].eval(x)
}

// sampled reads colours out of a table.
type sampled struct {
	samples  []float64 // already scaled to 0..1
	nOut     int
	size     int
	d0, d1   float64
	decodeLo []float64
	decodeHi []float64
}

func loadSampled(r *Reader, d Dict, stm *rawStream, domain []float64) pdfFunction {
	if stm == nil {
		return nil
	}
	data, err := r.decodeStream(stm.dict, stm.data)
	if err != nil {
		return nil
	}
	sizes := floatArray(r, d["Size"])
	bps, _ := toInt(r.resolve(d["BitsPerSample"]))
	rng := floatArray(r, d["Range"])
	if len(sizes) < 1 || bps <= 0 || len(rng) < 2 {
		return nil
	}
	size := int(sizes[0])
	nOut := len(rng) / 2
	if size <= 0 || nOut <= 0 || size > 1<<20 {
		return nil
	}
	f := &sampled{nOut: nOut, size: size}
	f.d0, f.d1 = domainOr(domain)
	for i := 0; i < nOut; i++ {
		f.decodeLo = append(f.decodeLo, rng[2*i])
		f.decodeHi = append(f.decodeHi, rng[2*i+1])
	}
	max := float64(uint64(1)<<uint(bps) - 1)
	br := &sampleBits{data: data}
	total := size * nOut
	f.samples = make([]float64, 0, total)
	for i := 0; i < total; i++ {
		v, ok := br.read(bps)
		if !ok {
			break
		}
		f.samples = append(f.samples, float64(v)/max)
	}
	if len(f.samples) < total {
		return nil
	}
	return f
}

func (f *sampled) eval(t float64) []float64 {
	t = normalize(t, f.d0, f.d1)
	pos := t * float64(f.size-1)
	i0 := int(pos)
	if i0 < 0 {
		i0 = 0
	}
	if i0 > f.size-1 {
		i0 = f.size - 1
	}
	i1 := i0 + 1
	if i1 > f.size-1 {
		i1 = f.size - 1
	}
	frac := pos - float64(i0)
	out := make([]float64, f.nOut)
	for c := 0; c < f.nOut; c++ {
		a := f.samples[i0*f.nOut+c]
		b := f.samples[i1*f.nOut+c]
		v := a + frac*(b-a)
		out[c] = f.decodeLo[c] + v*(f.decodeHi[c]-f.decodeLo[c])
	}
	return out
}

// normalize maps a value in a domain onto 0..1, clamped.
func normalize(t, lo, hi float64) float64 {
	if hi == lo {
		return 0
	}
	v := (t - lo) / (hi - lo)
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func domainOr(domain []float64) (float64, float64) {
	if len(domain) >= 2 && domain[1] != domain[0] {
		return domain[0], domain[1]
	}
	return 0, 1
}

// floatArray reads an array of numbers, resolving references.
func floatArray(r *Reader, v any) []float64 {
	arr, ok := r.resolve(v).(Array)
	if !ok {
		return nil
	}
	out := make([]float64, 0, len(arr))
	for _, e := range arr {
		f, ok := toFloat(r.resolve(e))
		if !ok {
			return nil
		}
		out = append(out, f)
	}
	return out
}

// sampleBits reads a run of fixed-width samples, which a sampled
// function packs end to end without regard for byte boundaries.
type sampleBits struct {
	data []byte
	pos  int // in bits
}

func (b *sampleBits) read(bits int) (uint64, bool) {
	if bits <= 0 || bits > 32 {
		return 0, false
	}
	var v uint64
	for i := 0; i < bits; i++ {
		idx := b.pos >> 3
		if idx >= len(b.data) {
			return 0, false
		}
		bit := (b.data[idx] >> uint(7-(b.pos&7))) & 1
		v = v<<1 | uint64(bit)
		b.pos++
	}
	return v, true
}
