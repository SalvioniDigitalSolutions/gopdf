package gopdf

// keeponly_programs.go — the small programs a PDF carries, kept by
// KeepOnly only as what they do.
//
// A character map and a PostScript calculator function are text a
// producer writes; besides mappings and arithmetic they can hold
// comments, names, any string. The copy keeps neither as it came: a
// character map is written anew from its mappings, a calculator
// function anew from its numbers and operators. A font program is kept
// when it is one (its first bytes say so); anything else in its place
// is left out, and the font is drawn from the viewer's own.

import (
	"bytes"
	"fmt"
)

// --- character maps ---

type cmapRange struct {
	lo, hi String
	dst    any // a String, an int (a CID), or an Array of Strings
}

// rebuildCMap returns a character map with only the mappings of data,
// or nil when data holds none. toUnicode chooses the kind written.
func rebuildCMap(data []byte, toUnicode bool, registry, ordering String, supplement any) []byte {
	toks, cut := tokenizeContentLimited(data)
	if cut {
		return nil
	}
	var space []cmapRange
	var bfchar, bfrange, cidchar, cidrange, notdefchar, notdefrange []cmapRange
	var usecmap Name
	wmode := -1
	var ops []any
	section := ""
	flush := func() {
		switch section {
		case "codespacerange":
			for i := 0; i+1 < len(ops); i += 2 {
				lo, ok1 := ops[i].(String)
				hi, ok2 := ops[i+1].(String)
				if ok1 && ok2 {
					space = append(space, cmapRange{lo: lo, hi: hi})
				}
			}
		case "bfchar", "cidchar", "notdefchar":
			for i := 0; i+1 < len(ops); i += 2 {
				src, ok := ops[i].(String)
				if !ok {
					continue
				}
				r := cmapRange{lo: src, hi: src}
				switch d := ops[i+1].(type) {
				case String:
					if section != "bfchar" {
						continue
					}
					r.dst = d
				case Name:
					if section != "bfchar" {
						continue
					}
					// A glyph name as a destination: kept only when it is
					// one of the standard ones a viewer knows.
					if _, known := glyphNameRune(string(d)); !known {
						continue
					}
					r.dst = d
				default:
					n, ok := toInt(d)
					if !ok || section == "bfchar" {
						continue
					}
					r.dst = n
				}
				switch section {
				case "bfchar":
					bfchar = append(bfchar, r)
				case "cidchar":
					cidchar = append(cidchar, r)
				default:
					notdefchar = append(notdefchar, r)
				}
			}
		case "bfrange", "cidrange", "notdefrange":
			for i := 0; i+2 < len(ops); i += 3 {
				lo, ok1 := ops[i].(String)
				hi, ok2 := ops[i+1].(String)
				if !ok1 || !ok2 {
					continue
				}
				r := cmapRange{lo: lo, hi: hi}
				switch d := ops[i+2].(type) {
				case String:
					if section != "bfrange" {
						continue
					}
					r.dst = d
				case Array:
					if section != "bfrange" {
						continue
					}
					arr := Array{}
					for _, e := range d {
						if s, ok := e.(String); ok {
							arr = append(arr, s)
						}
					}
					r.dst = arr
				default:
					n, ok := toInt(d)
					if !ok || section == "bfrange" {
						continue
					}
					r.dst = n
				}
				switch section {
				case "bfrange":
					bfrange = append(bfrange, r)
				case "cidrange":
					cidrange = append(cidrange, r)
				default:
					notdefrange = append(notdefrange, r)
				}
			}
		}
	}
	for _, tok := range toks {
		op, isOp := tok.val.(opKeyword)
		if !isOp {
			ops = append(ops, tok.val)
			continue
		}
		switch s := string(op); {
		case len(s) > 5 && s[:5] == "begin" && s != "begincmap":
			section = s[5:]
			ops = nil
		case len(s) > 3 && s[:3] == "end" && section != "" && s[3:] == section:
			flush()
			section = ""
			ops = nil
		case s == "usecmap":
			if len(ops) > 0 {
				if n, ok := ops[len(ops)-1].(Name); ok {
					usecmap = n
				}
			}
			ops = nil
		case s == "def":
			if len(ops) >= 2 {
				if key, ok := ops[len(ops)-2].(Name); ok && key == "WMode" {
					if n, ok := toInt(ops[len(ops)-1]); ok && (n == 0 || n == 1) {
						wmode = n
					}
				}
			}
			if section == "" {
				ops = nil
			}
		default:
			if section == "" {
				ops = nil
			}
		}
	}
	if len(bfchar)+len(bfrange)+len(cidchar)+len(cidrange) == 0 && usecmap == "" {
		return nil
	}
	if usecmap != "" && !knownCMapName(usecmap) {
		usecmap = ""
	}
	var b bytes.Buffer
	str := func(s String) {
		writeValue(&b, s, nil)
	}
	b.WriteString("/CIDInit /ProcSet findresource begin\n12 dict begin\nbegincmap\n")
	b.WriteString("/CIDSystemInfo << /Registry ")
	str(registry)
	b.WriteString(" /Ordering ")
	str(ordering)
	fmt.Fprintf(&b, " /Supplement %v >> def\n", supplement)
	if toUnicode {
		b.WriteString("/CMapName /Celatum-UCS def\n/CMapType 2 def\n")
	} else {
		b.WriteString("/CMapName /Celatum-Encoding def\n/CMapType 1 def\n")
	}
	if wmode >= 0 {
		fmt.Fprintf(&b, "/WMode %d def\n", wmode)
	}
	if usecmap != "" {
		b.WriteString("/" + string(usecmap) + " usecmap\n")
	}
	block := func(name string, rs []cmapRange, single bool) {
		for i := 0; i < len(rs); i += 100 {
			end := min(i+100, len(rs))
			fmt.Fprintf(&b, "%d begin%s\n", end-i, name)
			for _, r := range rs[i:end] {
				str(r.lo)
				if !single {
					b.WriteByte(' ')
					str(r.hi)
				}
				b.WriteByte(' ')
				if r.dst != nil {
					writeValue(&b, r.dst, nil)
				}
				b.WriteByte('\n')
			}
			fmt.Fprintf(&b, "end%s\n", name)
		}
	}
	if len(space) > 0 {
		for i := 0; i < len(space); i += 100 {
			end := min(i+100, len(space))
			fmt.Fprintf(&b, "%d begincodespacerange\n", end-i)
			for _, r := range space[i:end] {
				str(r.lo)
				b.WriteByte(' ')
				str(r.hi)
				b.WriteByte('\n')
			}
			b.WriteString("endcodespacerange\n")
		}
	}
	block("bfchar", bfchar, true)
	block("bfrange", bfrange, false)
	block("cidchar", cidchar, true)
	block("cidrange", cidrange, false)
	block("notdefchar", notdefchar, true)
	block("notdefrange", notdefrange, false)
	b.WriteString("endcmap\nCMapName currentdict /CMap defineresource pop\nend\nend\n")
	return b.Bytes()
}

// knownCMapName reports a predefined character map a CMap may build on.
func knownCMapName(n Name) bool {
	for _, k := range fontEncodingNames {
		if k == n {
			return true
		}
	}
	return false
}

// glyphNameRune reports a glyph name a viewer knows: one of the
// standard encodings' names, or uniXXXX.
func glyphNameRune(name string) (rune, bool) {
	if r := glyphNameToRune(name); r != 0 {
		return r, true
	}
	return 0, false
}

// --- calculator functions ---

var calculatorOps = map[string]bool{
	"abs": true, "add": true, "atan": true, "ceiling": true, "cos": true, "cvi": true, "cvr": true, "div": true,
	"exp": true, "floor": true, "idiv": true, "ln": true, "log": true, "mod": true, "mul": true, "neg": true,
	"round": true, "sin": true, "sqrt": true, "sub": true, "truncate": true, "and": true, "bitshift": true,
	"eq": true, "false": true, "ge": true, "gt": true, "le": true, "lt": true, "ne": true, "not": true,
	"or": true, "true": true, "xor": true, "if": true, "ifelse": true, "copy": true, "dup": true,
	"exch": true, "index": true, "pop": true, "roll": true,
}

// rebuildCalculator returns a PostScript calculator program with only
// its numbers, braces and operators, or nil when it holds anything else.
func rebuildCalculator(data []byte) []byte {
	var out bytes.Buffer
	depth := 0
	i := 0
	for i < len(data) {
		c := data[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' || c == 0:
			i++
			continue
		case c == '%':
			for i < len(data) && data[i] != '\n' && data[i] != '\r' {
				i++
			}
			continue
		case c == '{' || c == '}':
			if c == '{' {
				depth++
			} else {
				depth--
				if depth < 0 {
					return nil
				}
			}
			out.WriteByte(c)
			out.WriteByte(' ')
			i++
			continue
		}
		j := i
		for j < len(data) && !bytes.ContainsRune([]byte(" \t\r\n\f\x00{}%"), rune(data[j])) {
			j++
		}
		tok := string(data[i:j])
		i = j
		if calculatorOps[tok] {
			out.WriteString(tok + " ")
			continue
		}
		if isCalculatorNumber(tok) {
			out.WriteString(tok + " ")
			continue
		}
		return nil
	}
	if depth != 0 || out.Len() == 0 {
		return nil
	}
	return out.Bytes()
}

func isCalculatorNumber(s string) bool {
	if s == "" {
		return false
	}
	digits := 0
	for i, c := range s {
		switch {
		case c >= '0' && c <= '9':
			digits++
		case (c == '-' || c == '+') && i == 0:
		case c == '.':
		default:
			return false
		}
	}
	return digits > 0
}

// --- font programs ---

// fontProgramKind reports whether data is a font program of the kind
// key and subtype say.
func fontProgramKind(key Name, subtype Name, data []byte) bool {
	starts := func(p string) bool { return bytes.HasPrefix(data, []byte(p)) }
	trueType := starts("\x00\x01\x00\x00") || starts("true") || starts("ttcf")
	switch key {
	case "FontFile":
		return starts("%!") || starts("\x80\x01")
	case "FontFile2":
		return trueType
	case "FontFile3":
		switch subtype {
		case "Type1C", "CIDFontType0C":
			return len(data) >= 4 && data[0] == 1 && data[2] >= 4
		case "OpenType":
			return starts("OTTO") || trueType
		}
	}
	return false
}
