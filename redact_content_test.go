package gopdf

import (
	"bytes"
	"fmt"
	"regexp"
	"testing"
)

// The bar redactor must reach the same operand strings the pseudonymizer
// does: marked-content ActualText and an XFA datasets packet.
func TestRedact_MarkedContentAndXFA(t *testing.T) {
	const name = "Marialuisa Vanetti"
	raw := authored(t, "Modulo di "+name+".")
	xml := "<xfa:datasets><xfa:data><form><cliente>" + name + "</cliente></form></xfa:data></xfa:datasets>"
	content := fmt.Sprintf("/Span << /ActualText (%s) >> BDC BT /F1 11 Tf 56 500 Td (M. V.) Tj ET EMC", name)
	raw = appendUpdate(t, raw, " /AcroForm 300 0 R", " /Contents [6 0 R 302 0 R]", func(n int) map[int]string {
		return map[int]string{
			300: "<< /Fields [] /XFA [(datasets) 301 0 R] >>",
			301: fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(xml), xml),
			302: fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		}
	})
	r, err := NewReader(raw)
	if err != nil {
		t.Fatal(err)
	}
	rd := Redact(r)
	rd.Text(name)
	var out bytes.Buffer
	if _, err := rd.WriteTo(&out); err != nil {
		t.Fatalf("redact: %v", err)
	}
	re := regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\nendstream`)
	seenXFA, seenSpan := false, false
	for _, m := range re.FindAllSubmatch(out.Bytes(), -1) {
		body := inflateIfZlib(m[1])
		if bytes.Contains(body, []byte(name)) {
			t.Fatalf("original survives in a stream:\n%s", body)
		}
		if bytes.Contains(body, []byte("xfa:datasets")) {
			seenXFA = true
		}
		if bytes.Contains(body, []byte("ActualText")) {
			seenSpan = true
		}
	}
	if !seenXFA || !seenSpan {
		t.Fatalf("fixture lost its channels: xfa=%v span=%v", seenXFA, seenSpan)
	}
}

// A FreeText note draws its text in an appearance stream the page never
// references; the redactor must drop that appearance and delete the
// text from the note, not refuse the document.
func TestRedact_DropsTaintedAppearance(t *testing.T) {
	const name = "Marialuisa Vanetti"
	raw := authored(t, "Contratto di "+name+".")
	ap := fmt.Sprintf("BT /F1 12 Tf 2 5 Td (Nota: %s) Tj ET", name)
	raw = appendUpdate(t, raw, "", " /Annots [400 0 R]", func(n int) map[int]string {
		return map[int]string{
			400: fmt.Sprintf("<< /Type /Annot /Subtype /FreeText /Rect [56 400 356 430] /DA (/F1 12 Tf 0 g) /Contents (Nota: %s) /AP << /N 401 0 R >> >>", name),
			401: fmt.Sprintf("<< /Type /XObject /Subtype /Form /BBox [0 0 300 30] /Resources << /Font << /F1 4 0 R >> >> /Length %d >>\nstream\n%s\nendstream", len(ap), ap),
		}
	})
	r, err := NewReader(raw)
	if err != nil {
		t.Fatal(err)
	}
	rd := Redact(r)
	rd.Text(name)
	var out bytes.Buffer
	if _, err := rd.WriteTo(&out); err != nil {
		t.Fatalf("redact: %v", err)
	}
	if bytes.Contains(out.Bytes(), []byte(name)) {
		t.Fatal("name survives in the output")
	}
	re := regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\nendstream`)
	for _, m := range re.FindAllSubmatch(out.Bytes(), -1) {
		if bytes.Contains(inflateIfZlib(m[1]), []byte(name)) {
			t.Fatal("name survives in a stream")
		}
	}
	if !bytes.Contains(out.Bytes(), []byte("/NeedAppearances true")) {
		t.Fatal("the viewer was not asked to redraw the annotation")
	}
}
