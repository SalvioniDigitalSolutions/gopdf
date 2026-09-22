package gopdf

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"
)

// appendUpdate adds objects to a document through an incremental
// update, the way a producer that stores thumbnails or an XFA packet
// would, and re-points the catalog and first page at them.
func appendUpdate(t *testing.T, raw []byte, catAdd, pageAdd string, extra func(n int) map[int]string) []byte {
	t.Helper()
	find := func(typ string) (int, string) {
		for _, m := range regexp.MustCompile(`(?s)(\d+) 0 obj\s*(<<.*?>>)\s*endobj`).FindAllSubmatch(raw, -1) {
			if bytes.Contains(m[2], []byte(typ)) {
				var n int
				fmt.Sscanf(string(m[1]), "%d", &n)
				return n, string(m[2])
			}
		}
		return 0, ""
	}
	catN, cat := find("/Type /Catalog")
	pageN, page := find("/Type /Page ")
	if catN == 0 || pageN == 0 {
		t.Fatal("layout not recognised")
	}
	prev := regexp.MustCompile(`startxref\s+(\d+)`).FindSubmatch(raw)
	var size int
	fmt.Sscanf(string(regexp.MustCompile(`/Size (\d+)`).FindSubmatch(raw)[1]), "%d", &size)
	objs := extra(size)
	objs[catN] = strings.TrimSuffix(strings.TrimSpace(cat), ">>") + catAdd + " >>"
	objs[pageN] = strings.TrimSuffix(strings.TrimSpace(page), ">>") + pageAdd + " >>"
	var order []int
	for n := range objs {
		order = append(order, n)
	}
	for i := range order {
		for j := i + 1; j < len(order); j++ {
			if order[j] < order[i] {
				order[i], order[j] = order[j], order[i]
			}
		}
	}
	var out bytes.Buffer
	out.Write(raw)
	if !bytes.HasSuffix(raw, []byte("\n")) {
		out.WriteString("\n")
	}
	offs := map[int]int{}
	for _, n := range order {
		offs[n] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", n, objs[n])
	}
	xref := out.Len()
	out.WriteString("xref\n")
	for _, n := range order {
		fmt.Fprintf(&out, "%d 1\n%010d 00000 n \n", n, offs[n])
	}
	max := order[len(order)-1] + 1
	if size > max {
		max = size
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root %d 0 R /Prev %s >>\nstartxref\n%d\n%%%%EOF\n", max, catN, prev[1], xref)
	return out.Bytes()
}

func authored(t *testing.T, line string) []byte {
	t.Helper()
	doc := New()
	doc.SetInfo(Info{})
	pg := doc.AddPage()
	pg.SetFont(Helvetica, 11)
	pg.Text(56, 67, line)
	var buf bytes.Buffer
	if _, err := doc.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func pseudonymized(t *testing.T, raw []byte, name string) []byte {
	t.Helper()
	r, err := NewReader(raw)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := Pseudonymize(r, &out, []Pseudonym{{From: name, To: "[PERSON_1]", FitWidth: true}}); err != nil {
		t.Fatalf("pseudonymize: %v", err)
	}
	return out.Bytes()
}

// A page thumbnail is a rendering of the page before redaction; the
// pseudonymizer must drop it like the redactor does.
func TestPseudonymize_DropsPageThumbnail(t *testing.T) {
	const name = "Marialuisa Vanetti"
	raw := authored(t, "Contratto di "+name+".")
	img := "THUMBNAIL PIXELS OF " + name
	raw = appendUpdate(t, raw, "", "", func(n int) map[int]string { return map[int]string{} })
	raw = appendUpdate(t, raw, "", fmt.Sprintf(" /Thumb %d 0 R", 100), func(n int) map[int]string {
		return map[int]string{100: fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width 4 /Height 4 /ColorSpace /DeviceGray /BitsPerComponent 8 /Length %d >>\nstream\n%s\nendstream", len(img), img)}
	})
	out := pseudonymized(t, raw, name)
	if bytes.Contains(out, []byte("/Thumb")) || bytes.Contains(out, []byte("THUMBNAIL PIXELS")) {
		t.Fatal("thumbnail survived")
	}
}

// An XFA datasets packet holds every field value as XML; the names in
// it must be rewritten like any other string.
func TestPseudonymize_ScrubsXFA(t *testing.T) {
	const name = "Marialuisa Vanetti"
	raw := authored(t, "Modulo di "+name+".")
	xml := "<xfa:datasets xmlns:xfa=\"http://www.xfa.org/schema/xfa-data/1.0/\"><xfa:data><form><cliente>" + name + "</cliente></form></xfa:data></xfa:datasets>"
	raw = appendUpdate(t, raw, " /AcroForm 200 0 R", "", func(n int) map[int]string {
		return map[int]string{
			200: "<< /Fields [] /XFA [(datasets) 201 0 R] >>",
			201: fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(xml), xml),
		}
	})
	out := pseudonymized(t, raw, name)
	re := regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\nendstream`)
	found := false
	for _, m := range re.FindAllSubmatch(out, -1) {
		body := inflateIfZlib(m[1])
		if bytes.Contains(body, []byte("xfa:datasets")) {
			found = true
			if bytes.Contains(body, []byte(name)) || !bytes.Contains(body, []byte("[PERSON_1]")) {
				t.Fatalf("XFA not rewritten:\n%s", body)
			}
		}
	}
	if !found {
		t.Fatal("XFA packet lost")
	}
}

func inflateIfZlib(b []byte) []byte {
	zr, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return b
	}
	defer zr.Close()
	out, err := io.ReadAll(zr)
	if err != nil {
		return b
	}
	return out
}
