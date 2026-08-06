package gopdf

import (
	"fmt"
	"io"
)

// The parsed-PDF object model, shared by the Reader (parsing existing
// files) and the writer (re-serializing imported objects).
//
// A parsed value is one of: nil, bool, int64, float64, String, Name, Ref,
// Dict, Array, or *rawStream (indirect objects only).

// Name is a PDF name object, without the leading slash.
type Name string

// Ref is an indirect object reference in a parsed file.
type Ref struct {
	Num, Gen int
}

// String is a PDF string object's raw bytes.
type String []byte

// Dict is a PDF dictionary.
type Dict map[Name]any

// Array is a PDF array.
type Array []any

// rawStream is a stream object with its data kept exactly as stored in the
// source file (still encoded); its dict retains /Filter and /DecodeParms.
type rawStream struct {
	dict Dict
	data []byte
}

// rawRef is a writer-side reference into Document.raw, resolved to a real
// object number during serialization.
type rawRef int

// docFontRef is a writer-side reference to one of the document's own
// fonts (an index into Document.fonts), letting copied dictionaries point
// at fonts this library creates.
type docFontRef int

// writeName writes a PDF name with #-escaping for irregular characters.
func writeName(w io.Writer, n Name) {
	buf := []byte{'/'}
	for i := 0; i < len(n); i++ {
		c := n[i]
		if c <= ' ' || c > '~' || c == '#' || isDelim(c) {
			buf = append(buf, fmt.Sprintf("#%02X", c)...)
		} else {
			buf = append(buf, c)
		}
	}
	w.Write(buf)
}

// writeCtx carries the state writeValue needs: mapping writer-side
// references to object numbers, and encrypting strings when the document
// is protected.
type writeCtx struct {
	num     func(rawRef) int
	fontNum func(docFontRef) int
	encrypt func([]byte) []byte // nil when not encrypting
	// refsAreLiteral writes a parsed Ref as an indirect reference rather
	// than null. It is set when writing into a file whose original object
	// numbering still applies, as an incremental update does.
	refsAreLiteral bool
}

func (c *writeCtx) str(b []byte) []byte {
	if c != nil && c.encrypt != nil {
		return c.encrypt(b)
	}
	return b
}

func (c *writeCtx) ref(rr rawRef) int {
	if c != nil && c.num != nil {
		return c.num(rr)
	}
	return 0
}

func (c *writeCtx) font(fr docFontRef) int {
	if c != nil && c.fontNum != nil {
		return c.fontNum(fr)
	}
	return 0
}

// writeValue serializes a parsed value. Indirect references must already be
// rewritten to rawRef.
func writeValue(w io.Writer, v any, ctx *writeCtx) {
	switch t := v.(type) {
	case nil:
		io.WriteString(w, "null")
	case bool:
		fmt.Fprintf(w, "%t", t)
	case int64:
		fmt.Fprintf(w, "%d", t)
	case float64:
		io.WriteString(w, fl(t))
	case String:
		// Hex form is binary-safe and needs no escaping.
		fmt.Fprintf(w, "<%X>", ctx.str([]byte(t)))
	case Name:
		writeName(w, t)
	case rawRef:
		fmt.Fprintf(w, "%d 0 R", ctx.ref(t))
	case docFontRef:
		fmt.Fprintf(w, "%d 0 R", ctx.font(t))
	case Ref:
		if ctx != nil && ctx.refsAreLiteral {
			fmt.Fprintf(w, "%d %d R", t.Num, t.Gen)
		} else {
			// A reference that was never rewritten would dangle.
			io.WriteString(w, "null")
		}
	case Array:
		io.WriteString(w, "[")
		for i, e := range t {
			if i > 0 {
				io.WriteString(w, " ")
			}
			writeValue(w, e, ctx)
		}
		io.WriteString(w, "]")
	case Dict:
		io.WriteString(w, "<<")
		for _, k := range sortedKeys(t) {
			io.WriteString(w, " ")
			writeName(w, k)
			io.WriteString(w, " ")
			writeValue(w, t[k], ctx)
		}
		io.WriteString(w, " >>")
	default:
		// Unexpected value (e.g. an unresolved Ref); null keeps the
		// file well-formed.
		io.WriteString(w, "null")
	}
}

// sortedKeys returns dictionary keys in a stable order so output is
// deterministic.
func sortedKeys(d Dict) []Name {
	keys := make([]Name, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
