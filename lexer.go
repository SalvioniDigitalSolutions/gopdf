package gopdf

import (
	"errors"
	"fmt"
	"io"
	"strconv"
)

// parser is a recursive-descent parser for PDF syntax, used on file bodies,
// content streams, and CMaps.
type parser struct {
	data  []byte
	pos   int
	depth int
	r     *Reader // for resolving indirect /Length values; may be nil
}

// opKeyword is a bare keyword token (an operator in content streams, or
// structural keywords like obj/endobj in the file body).
type opKeyword string

const maxParseDepth = 200

var errSyntax = errors.New("gopdf: malformed PDF syntax")

func isWS(c byte) bool {
	return c == 0 || c == '\t' || c == '\n' || c == '\f' || c == '\r' || c == ' '
}

func isDelim(c byte) bool {
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

func isRegular(c byte) bool {
	return !isWS(c) && !isDelim(c)
}

func (p *parser) skipWS() {
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		if isWS(c) {
			p.pos++
		} else if c == '%' { // comment to end of line
			for p.pos < len(p.data) && p.data[p.pos] != '\n' && p.data[p.pos] != '\r' {
				p.pos++
			}
		} else {
			return
		}
	}
}

// next returns the next value or opKeyword, or io.EOF.
func (p *parser) next() (any, error) {
	if p.depth > maxParseDepth {
		return nil, errSyntax
	}
	p.skipWS()
	if p.pos >= len(p.data) {
		return nil, io.EOF
	}
	switch c := p.data[p.pos]; {
	case c == '(':
		return p.literalString()
	case c == '<':
		if p.pos+1 < len(p.data) && p.data[p.pos+1] == '<' {
			return p.dict()
		}
		return p.hexString()
	case c == '/':
		return p.name()
	case c == '[':
		return p.array()
	case c == ']', c == '>', c == ')', c == '{', c == '}':
		return nil, errSyntax
	case c >= '0' && c <= '9', c == '+', c == '-', c == '.':
		return p.number()
	default:
		return p.keyword()
	}
}

func (p *parser) array() (any, error) {
	p.pos++ // [
	p.depth++
	defer func() { p.depth-- }()
	var arr Array
	for {
		p.skipWS()
		if p.pos >= len(p.data) {
			return nil, errSyntax
		}
		if p.data[p.pos] == ']' {
			p.pos++
			return arr, nil
		}
		v, err := p.next()
		if err != nil {
			return nil, errSyntax
		}
		if _, ok := v.(opKeyword); ok {
			return nil, errSyntax
		}
		arr = append(arr, v)
	}
}

func (p *parser) dict() (any, error) {
	p.pos += 2 // <<
	p.depth++
	defer func() { p.depth-- }()
	d := make(Dict)
	for {
		p.skipWS()
		if p.pos+1 < len(p.data) && p.data[p.pos] == '>' && p.data[p.pos+1] == '>' {
			p.pos += 2
			return d, nil
		}
		if p.pos >= len(p.data) || p.data[p.pos] != '/' {
			return nil, errSyntax
		}
		key, err := p.name()
		if err != nil {
			return nil, err
		}
		v, err := p.next()
		if err != nil {
			return nil, errSyntax
		}
		if _, ok := v.(opKeyword); ok {
			return nil, errSyntax
		}
		d[key.(Name)] = v
	}
}

func (p *parser) name() (any, error) {
	p.pos++ // /
	var out []byte
	for p.pos < len(p.data) && isRegular(p.data[p.pos]) {
		c := p.data[p.pos]
		if c == '#' && p.pos+2 < len(p.data) {
			if hi, err1 := hexVal(p.data[p.pos+1]); err1 == nil {
				if lo, err2 := hexVal(p.data[p.pos+2]); err2 == nil {
					out = append(out, hi<<4|lo)
					p.pos += 3
					continue
				}
			}
		}
		out = append(out, c)
		p.pos++
	}
	return Name(out), nil
}

func hexVal(c byte) (byte, error) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', nil
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, nil
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, nil
	}
	return 0, errSyntax
}

func (p *parser) hexString() (any, error) {
	p.pos++ // <
	var out []byte
	var hi byte
	half := false
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		p.pos++
		if c == '>' {
			if half {
				out = append(out, hi<<4)
			}
			return String(out), nil
		}
		if isWS(c) {
			continue
		}
		v, err := hexVal(c)
		if err != nil {
			return nil, err
		}
		if half {
			out = append(out, hi<<4|v)
			half = false
		} else {
			hi, half = v, true
		}
	}
	return nil, errSyntax
}

func (p *parser) literalString() (any, error) {
	p.pos++ // (
	var out []byte
	depth := 1
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		p.pos++
		switch c {
		case '(':
			depth++
			out = append(out, c)
		case ')':
			depth--
			if depth == 0 {
				return String(out), nil
			}
			out = append(out, c)
		case '\\':
			if p.pos >= len(p.data) {
				return nil, errSyntax
			}
			e := p.data[p.pos]
			p.pos++
			switch e {
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case '\n': // line continuation
			case '\r':
				if p.pos < len(p.data) && p.data[p.pos] == '\n' {
					p.pos++
				}
			default:
				if e >= '0' && e <= '7' {
					v := int(e - '0')
					for k := 0; k < 2 && p.pos < len(p.data); k++ {
						c2 := p.data[p.pos]
						if c2 < '0' || c2 > '7' {
							break
						}
						v = v*8 + int(c2-'0')
						p.pos++
					}
					out = append(out, byte(v))
				} else {
					out = append(out, e)
				}
			}
		case '\r': // EOL inside strings normalizes to \n
			out = append(out, '\n')
			if p.pos < len(p.data) && p.data[p.pos] == '\n' {
				p.pos++
			}
		default:
			out = append(out, c)
		}
	}
	return nil, errSyntax
}

func (p *parser) number() (any, error) {
	start := p.pos
	for p.pos < len(p.data) && (p.data[p.pos] == '+' || p.data[p.pos] == '-' ||
		p.data[p.pos] == '.' || (p.data[p.pos] >= '0' && p.data[p.pos] <= '9')) {
		p.pos++
	}
	tok := string(p.data[start:p.pos])
	if i, err := strconv.ParseInt(tok, 10, 64); err == nil {
		// Look ahead for "gen R", which makes this an indirect reference.
		save := p.pos
		p.skipWS()
		genStart := p.pos
		for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			p.pos++
		}
		if p.pos > genStart {
			gen, _ := strconv.ParseInt(string(p.data[genStart:p.pos]), 10, 32)
			p.skipWS()
			if p.pos < len(p.data) && p.data[p.pos] == 'R' &&
				(p.pos+1 >= len(p.data) || !isRegular(p.data[p.pos+1])) {
				p.pos++
				return Ref{Num: int(i), Gen: int(gen)}, nil
			}
		}
		p.pos = save
		return i, nil
	}
	f, err := strconv.ParseFloat(tok, 64)
	if err != nil {
		return nil, errSyntax
	}
	return f, nil
}

func (p *parser) keyword() (any, error) {
	start := p.pos
	for p.pos < len(p.data) && isRegular(p.data[p.pos]) {
		p.pos++
	}
	if p.pos == start {
		return nil, errSyntax
	}
	switch kw := string(p.data[start:p.pos]); kw {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null":
		return nil, nil
	default:
		return opKeyword(kw), nil
	}
}

// expectInt parses the next token as an integer.
func (p *parser) expectInt() (int64, error) {
	v, err := p.next()
	if err != nil {
		return 0, err
	}
	i, ok := v.(int64)
	if !ok {
		return 0, errSyntax
	}
	return i, nil
}

// expectKeyword consumes the next token, which must be the given keyword.
func (p *parser) expectKeyword(kw string) error {
	v, err := p.next()
	if err != nil {
		return err
	}
	if k, ok := v.(opKeyword); !ok || string(k) != kw {
		return fmt.Errorf("gopdf: expected %q", kw)
	}
	return nil
}

// toFloat converts a parsed numeric value.
func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case int64:
		return float64(t), true
	case float64:
		return t, true
	}
	return 0, false
}

// toInt converts a parsed numeric value.
func toInt(v any) (int, bool) {
	switch t := v.(type) {
	case int64:
		return int(t), true
	case float64:
		return int(t), true
	}
	return 0, false
}
