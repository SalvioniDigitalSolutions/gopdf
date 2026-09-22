package gopdf

import (
	"bytes"
	"compress/zlib"
	"encoding/hex"
	"io"
	"regexp"
	"strings"
	"testing"
)

// A tagged document repeats the page's words inside marked-content
// dictionaries (/ActualText). Those are content-stream operands, not
// page text and not objects, and must be rewritten with the rest.
func TestPseudonymize_MarkedContentActualText(t *testing.T) {
	const name = "Marialuisa Vanetti"
	doc := New()
	doc.SetInfo(Info{})
	pg := doc.AddPage()
	pg.SetFont(Helvetica, 11)
	pg.op("/Span << /ActualText (%s) /Alt (Foto di %s) >> BDC", name, name)
	pg.Text(56, 67, "La cliente M. V. firma.")
	pg.op("EMC")
	pg.Text(56, 90, "Contratto di "+name+".")
	var buf bytes.Buffer
	if _, err := doc.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	r, err := NewReader(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := Pseudonymize(r, &out, []Pseudonym{{From: name, To: "[PERSON_1]", FitWidth: true}}); err != nil {
		t.Fatalf("pseudonymize: %v", err)
	}
	re := regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\nendstream`)
	seenActual := false
	for _, m := range re.FindAllSubmatch(out.Bytes(), -1) {
		body := m[1]
		if zr, err := zlib.NewReader(bytes.NewReader(body)); err == nil {
			body, _ = io.ReadAll(zr)
		}
		if bytes.Contains(body, []byte(name)) {
			t.Fatalf("original survives in a stream:\n%s", body)
		}
		if bytes.Contains(body, []byte("ActualText")) {
			seenActual = true
			token := []byte("[PERSON_1]")
			asHex := []byte(strings.ToUpper(hex.EncodeToString(token)))
			if !bytes.Contains(body, token) && !bytes.Contains(body, asHex) {
				t.Fatalf("marked content not rewritten:\n%s", body)
			}
		}
	}
	if !seenActual {
		t.Fatal("fixture lost its marked content")
	}
	rr, _ := NewReader(out.Bytes())
	txt, _ := rr.PageText(0)
	if strings.Contains(txt, name) {
		t.Fatalf("page text: %q", txt)
	}
}
