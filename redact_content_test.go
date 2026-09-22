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
