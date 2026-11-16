package gopdf

// keeponly_content.go — the content of a page, form, pattern or glyph
// as KeepOnly keeps it.
//
// Content in a layer that is hidden by default is taken out: a viewer
// does not show it, the copy does not hold it. Content in a layer shown
// by default stays, without its layer marking (the copy has no layers).
// A picture or form placed in a hidden layer is not placed. The texts
// of marked-content property lists (an actual text, an alternate
// description) are cleaned by the policy. And the resources the
// remaining content uses are noted, so the copy keeps only those.

import (
	"bytes"
	"fmt"
	"sort"
)

type keptContent struct {
	data []byte
	used resUse
	err  error
}

type contentEdit struct {
	start, end int
	repl       []byte
}

// content keeps data, drawn with res.
func (k *keeper) content(data []byte, res Dict) keptContent {
	toks, cut := tokenizeContentLimited(data)
	if cut {
		return keptContent{err: fmt.Errorf("a content stream too large to read whole")}
	}
	used := resUse{}
	var edits []contentEdit
	var operands []contentToken
	depth := 0
	hiddenFrom, hiddenDepth := -1, 0

	resource := func(cat, n Name) any {
		sub, _ := k.r.resolve(res[cat]).(Dict)
		if sub == nil {
			return nil
		}
		return sub[n]
	}
	inBI := false
	for _, tok := range toks {
		op, isOp := tok.val.(opKeyword)
		if !isOp {
			operands = append(operands, tok)
			continue
		}
		args := operands
		operands = nil
		if inBI && op != "ID" {
			continue
		}
		switch op {
		case "BDC", "BMC":
			if hiddenFrom >= 0 {
				depth++
				continue
			}
			depth++
			if op == "BMC" || len(args) < 2 {
				continue
			}
			tag, _ := args[0].val.(Name)
			switch props := args[1].val.(type) {
			case Name:
				target := resource("Properties", props)
				if tag == "OC" {
					if k.oc.hidden(target) {
						hiddenFrom, hiddenDepth = args[0].start, depth
						k.rep.HiddenLayerContent++
					} else {
						edits = append(edits, contentEdit{args[0].start, tok.end, []byte("/OC BMC")})
					}
					continue
				}
				used.add("Properties", props)
			case Dict:
				if tag == "OC" {
					// A layer is named through the resources; an inline
					// one is no layer anyone can switch. Shown.
					edits = append(edits, contentEdit{args[0].start, tok.end, []byte("/OC BMC")})
					continue
				}
				if cleaned, changed := k.inlineProperties(props); changed {
					var b bytes.Buffer
					writeValue(&b, cleaned, &writeCtx{exactReals: true})
					edits = append(edits, contentEdit{args[1].start, args[1].end, b.Bytes()})
				}
			}
		case "EMC":
			if hiddenFrom >= 0 && depth == hiddenDepth {
				edits = append(edits, contentEdit{hiddenFrom, tok.end, nil})
				hiddenFrom = -1
			}
			if depth > 0 {
				depth--
			}
		}
		if hiddenFrom >= 0 {
			continue
		}
		switch op {
		case "Tf":
			if len(args) > 0 {
				if n, ok := args[0].val.(Name); ok {
					used.add("Font", n)
				}
			}
		case "Do":
			if len(args) > 0 {
				if n, ok := args[0].val.(Name); ok {
					xo := resource("XObject", n)
					if stm, ok := k.r.resolve(xo).(*rawStream); ok && k.oc.hidden(stm.dict["OC"]) {
						edits = append(edits, contentEdit{args[0].start, tok.end, nil})
						k.rep.HiddenLayerContent++
						continue
					}
					used.add("XObject", n)
				}
			}
		case "cs", "CS":
			if len(args) > 0 {
				if n, ok := args[0].val.(Name); ok && !deviceSpace(n) {
					used.add("ColorSpace", n)
				}
			}
		case "scn", "SCN":
			if len(args) > 0 {
				if n, ok := args[len(args)-1].val.(Name); ok {
					used.add("Pattern", n)
				}
			}
		case "sh":
			if len(args) > 0 {
				if n, ok := args[0].val.(Name); ok {
					used.add("Shading", n)
				}
			}
		case "gs":
			if len(args) > 0 {
				if n, ok := args[0].val.(Name); ok {
					used.add("ExtGState", n)
				}
			}
		case "DP":
			if len(args) >= 2 {
				switch props := args[1].val.(type) {
				case Name:
					used.add("Properties", props)
				case Dict:
					if cleaned, changed := k.inlineProperties(props); changed {
						var b bytes.Buffer
						writeValue(&b, cleaned, &writeCtx{exactReals: true})
						edits = append(edits, contentEdit{args[1].start, args[1].end, b.Bytes()})
					}
				}
			}
		case "BI":
			inBI = true
		case "ID":
			inBI = false
			for i := 0; i+1 < len(args); i++ {
				if key, ok := args[i].val.(Name); ok && (key == "CS" || key == "ColorSpace") {
					if n, ok := args[i+1].val.(Name); ok && !deviceSpace(n) && !inlineSpaceAbbrev(n) {
						used.add("ColorSpace", n)
					}
				}
			}
		}
	}
	if hiddenFrom >= 0 {
		// A hidden layer left open runs to the end of the content.
		edits = append(edits, contentEdit{hiddenFrom, len(data), nil})
	}
	return keptContent{data: applyContentEdits(data, edits), used: used}
}

func deviceSpace(n Name) bool {
	switch n {
	case "DeviceGray", "DeviceRGB", "DeviceCMYK", "Pattern":
		return true
	}
	return false
}

// inlineSpaceAbbrev reports the abbreviations an inline image may use
// for a device space.
func inlineSpaceAbbrev(n Name) bool {
	switch n {
	case "G", "RGB", "CMYK", "I", "Indexed":
		return true
	}
	return false
}

// inlineProperties cleans a property list written into the content.
func (k *keeper) inlineProperties(d Dict) (Dict, bool) {
	out := Dict{}
	changed := false
	for key, val := range d {
		switch key {
		case "MCID":
			if n := k.number(val); n != nil {
				out[key] = n
			} else {
				changed = true
			}
		case "BBox":
			if arr := k.numbers(val); arr != nil {
				out[key] = arr
			} else {
				changed = true
			}
		case "Type", "Subtype", "Attached":
			// An artifact's kind and edges: names a producer chooses,
			// renamed by the policy.
			var kept any
			if arr, ok := val.(Array); ok {
				names := Array{}
				for _, e := range arr {
					if n := k.freeName(e); n != nil {
						names = append(names, n)
					}
				}
				kept = names
			} else {
				kept = k.freeName(val)
			}
			if kept == nil {
				changed = true
				continue
			}
			if n, ok := kept.(Name); !ok || n != val {
				changed = true
			}
			out[key] = kept
		case "ActualText", "Alt", "E":
			t := k.text(val)
			if t == nil {
				changed = true
				continue
			}
			if s, ok := val.(String); !ok || !bytes.Equal(s, t.(String)) {
				changed = true
			}
			out[key] = t
		case "Lang":
			if l := k.lang(val); l != nil {
				out[key] = l
			} else {
				changed = true
			}
		default:
			changed = true
		}
	}
	return out, changed
}

func applyContentEdits(data []byte, edits []contentEdit) []byte {
	if len(edits) == 0 {
		return append([]byte(nil), data...)
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	var b bytes.Buffer
	pos := 0
	for _, e := range edits {
		if e.start < pos {
			// Inside a stretch already taken out (a hidden layer
			// holds what was edited in it).
			if e.end > pos {
				pos = e.end
			}
			continue
		}
		b.Write(data[pos:e.start])
		if len(e.repl) > 0 {
			b.WriteByte(' ')
			b.Write(e.repl)
			b.WriteByte(' ')
		} else {
			b.WriteByte(' ')
		}
		pos = e.end
	}
	b.Write(data[pos:])
	return b.Bytes()
}

// --- layers ---

// ocVisibility answers whether optional content is hidden by default,
// by the document's default configuration.
type ocVisibility struct {
	r    *Reader
	any  bool
	base Name
	on   map[int]bool
	off  map[int]bool
}

func newOCVisibility(r *Reader, cat Dict) *ocVisibility {
	o := &ocVisibility{r: r, base: "ON", on: map[int]bool{}, off: map[int]bool{}}
	props, ok := r.resolve(cat["OCProperties"]).(Dict)
	if !ok {
		return o
	}
	o.any = true
	d, _ := r.resolve(props["D"]).(Dict)
	if b, ok := r.resolve(d["BaseState"]).(Name); ok {
		o.base = b
	}
	for key, set := range map[Name]map[int]bool{"ON": o.on, "OFF": o.off} {
		if arr, ok := r.resolve(d[key]).(Array); ok {
			for _, e := range arr {
				if ref, ok := e.(Ref); ok {
					set[ref.Num] = true
				}
			}
		}
	}
	return o
}

// hidden reports whether v — an OCG or an OCMD, by reference — is
// hidden by default.
func (o *ocVisibility) hidden(v any) bool {
	if o == nil || v == nil {
		return false
	}
	return !o.visible(v, 0)
}

func (o *ocVisibility) visible(v any, depth int) bool {
	if depth > 32 {
		return true
	}
	ref, isRef := v.(Ref)
	d, _ := o.r.resolve(v).(Dict)
	if d == nil {
		return true
	}
	switch typ, _ := d["Type"].(Name); typ {
	case "OCG":
		if !isRef {
			return true
		}
		if o.base == "OFF" {
			return o.on[ref.Num]
		}
		return !o.off[ref.Num]
	case "OCMD":
		if ve, ok := o.r.resolve(d["VE"]).(Array); ok && len(ve) > 0 {
			return o.expression(ve, depth+1)
		}
		var groups []any
		switch g := o.r.resolve(d["OCGs"]).(type) {
		case Array:
			groups = g
		case Dict:
			groups = []any{d["OCGs"]}
		}
		if len(groups) == 0 {
			return true
		}
		policy, _ := o.r.resolve(d["P"]).(Name)
		if policy == "" {
			policy = "AnyOn"
		}
		on, off := 0, 0
		for _, g := range groups {
			if o.visible(g, depth+1) {
				on++
			} else {
				off++
			}
		}
		switch policy {
		case "AllOn":
			return off == 0
		case "AnyOff":
			return off > 0
		case "AllOff":
			return on == 0
		default:
			return on > 0
		}
	}
	return true
}

func (o *ocVisibility) expression(ve Array, depth int) bool {
	op, _ := o.r.resolve(ve[0]).(Name)
	val := func(e any) bool {
		if arr, ok := o.r.resolve(e).(Array); ok && len(arr) > 0 {
			return o.expression(arr, depth+1)
		}
		return o.visible(e, depth+1)
	}
	switch op {
	case "Not":
		return len(ve) < 2 || !val(ve[1])
	case "And":
		for _, e := range ve[1:] {
			if !val(e) {
				return false
			}
		}
		return true
	case "Or":
		for _, e := range ve[1:] {
			if val(e) {
				return true
			}
		}
		return false
	}
	return true
}
