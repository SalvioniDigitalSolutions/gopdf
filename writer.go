package gopdf

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// offsetWriter wraps an io.Writer, tracking the number of bytes written and
// holding the first error encountered so serialization code stays linear.
type offsetWriter struct {
	w   io.Writer
	n   int64
	err error

	// While capturing, output is diverted to a buffer and does not count
	// towards the file offset: the bytes are destined for an object
	// stream, not for this position in the file.
	capturing bool
	saved     io.Writer
	// wroteStream records whether the object being written is a stream,
	// which cannot live inside an object stream.
	wroteStream bool

	// Encryption state: when crypt is non-nil, strings and stream data
	// are encrypted with a key derived from the object being written.
	crypt *stdCrypt
	obj   int
}

// encryptBytes protects data belonging to the object currently being
// written. It is the identity when the document is not encrypted.
func (ow *offsetWriter) encryptBytes(data []byte, method cryptMethod) []byte {
	if ow.crypt == nil {
		return data
	}
	out, err := ow.crypt.encrypt(ow.obj, 0, data, method)
	if err != nil {
		if ow.err == nil {
			ow.err = err
		}
		return data
	}
	return out
}

// pdfString writes a Go string as a PDF string object, encrypting it when
// the document is protected. Plain ASCII is written as a literal string
// for readability; anything else becomes UTF-16BE, matching pdfTextString.
func (ow *offsetWriter) pdfString(s string) {
	if ow.crypt == nil {
		ow.str(pdfTextString(s))
		return
	}
	ow.pdfBytes(textStringBytes(s))
}

// pdfBytes writes raw bytes as a (possibly encrypted) hex string.
func (ow *offsetWriter) pdfBytes(b []byte) {
	ow.printf("<%X>", ow.encryptBytes(b, ow.strMethod()))
}

func (ow *offsetWriter) strMethod() cryptMethod {
	if ow.crypt == nil {
		return cryptNone
	}
	return ow.crypt.strF
}

func (ow *offsetWriter) stmMethod() cryptMethod {
	if ow.crypt == nil {
		return cryptNone
	}
	return ow.crypt.stmF
}

func (ow *offsetWriter) Write(p []byte) (int, error) {
	if ow.err != nil {
		return 0, ow.err
	}
	n, err := ow.w.Write(p)
	if !ow.capturing {
		ow.n += int64(n)
	}
	if err != nil {
		ow.err = err
	}
	return n, err
}

// beginCapture diverts output into buf until endCapture.
func (ow *offsetWriter) beginCapture(buf io.Writer) {
	ow.saved, ow.w, ow.capturing = ow.w, buf, true
	ow.wroteStream = false
}

// endCapture restores the file as the destination.
func (ow *offsetWriter) endCapture() {
	if ow.capturing {
		ow.w, ow.capturing = ow.saved, false
	}
}

func (ow *offsetWriter) str(s string) {
	ow.Write([]byte(s))
}

func (ow *offsetWriter) printf(format string, args ...any) {
	fmt.Fprintf(ow, format, args...)
}

// fl formats a float for a PDF content stream or object: fixed notation,
// at most 3 decimal places, trailing zeros trimmed.
func fl(v float64) string {
	s := strconv.FormatFloat(v, 'f', 3, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" || s == "-" || s == "-0" {
		return "0"
	}
	return s
}

// escapeString escapes bytes for use inside a PDF literal string ( ... ).
func escapeString(b []byte) []byte {
	var out bytes.Buffer
	for _, c := range b {
		switch c {
		case '(', ')', '\\':
			out.WriteByte('\\')
			out.WriteByte(c)
		case '\r':
			out.WriteString(`\r`)
		case '\n':
			out.WriteString(`\n`)
		default:
			out.WriteByte(c)
		}
	}
	return out.Bytes()
}

// pdfTextString renders a Go string as a PDF text string object.
// Pure-ASCII strings become literal strings; anything else is encoded as a
// UTF-16BE hex string with a byte order mark, which every viewer accepts
// for document metadata.
func pdfTextString(s string) string {
	if isASCII(s) {
		return "(" + string(escapeString([]byte(s))) + ")"
	}
	var b strings.Builder
	b.WriteString("<FEFF")
	for _, u := range utf16.Encode([]rune(s)) {
		fmt.Fprintf(&b, "%04X", u)
	}
	b.WriteString(">")
	return b.String()
}

// textStringBytes returns the raw bytes a PDF text string encodes: ASCII
// as-is, otherwise UTF-16BE with a byte order mark. Used when the string
// must be encrypted before being written.
func textStringBytes(s string) []byte {
	if isASCII(s) {
		return []byte(s)
	}
	out := []byte{0xFE, 0xFF}
	for _, u := range utf16.Encode([]rune(s)) {
		out = append(out, byte(u>>8), byte(u))
	}
	return out
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// pdfDate renders a time.Time as a PDF date string, e.g. (D:20260805093000+02'00').
func pdfDate(t time.Time) string {
	_, offset := t.Zone()
	sign := '+'
	if offset < 0 {
		sign = '-'
		offset = -offset
	}
	return fmt.Sprintf("(D:%s%c%02d'%02d')", t.Format("20060102150405"), sign, offset/3600, (offset%3600)/60)
}

// flateCompress compresses data in the zlib format expected by /FlateDecode.
func flateCompress(data []byte) ([]byte, error) {
	var b bytes.Buffer
	zw := zlib.NewWriter(&b)
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// writeStream emits a stream object body: a dictionary containing /Length
// (and /Filter /FlateDecode when compressed) merged with extra dictionary
// entries, followed by the stream data. Encryption, when enabled, is
// applied after compression, as the specification requires.
func (ow *offsetWriter) writeStream(extraDict string, data []byte, compress bool) error {
	filter := ""
	if compress {
		c, err := flateCompress(data)
		if err != nil {
			return err
		}
		data = c
		filter = "/Filter /FlateDecode "
	}
	data = ow.encryptBytes(data, ow.stmMethod())
	ow.wroteStream = true
	ow.printf("<< %s%s/Length %d >>\nstream\n", extraDict, filter, len(data))
	ow.Write(data)
	ow.str("\nendstream\n")
	return ow.err
}
