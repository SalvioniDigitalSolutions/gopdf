package gopdf

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"strings"
	"testing"
)

func docBytes(t *testing.T, d *Document) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := d.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestRoundTrip writes a document with this library and reads it back.
func TestRoundTrip(t *testing.T) {
	doc := New()
	doc.SetInfo(Info{Title: "Résumé — roundtrip", Author: "gopdf"})
	p1 := doc.AddPage()
	p1.SetFont(Helvetica, 12)
	p1.Text(72, 100, "Hello, world — café!")
	p1.Text(72, 130, "second line")
	p2 := doc.AddPageSize(Letter.Landscape())
	p2.SetFont(TimesRoman, 12)
	p2.Text(72, 72, "landscape page")

	r, err := NewReader(docBytes(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	if r.NumPages() != 2 {
		t.Fatalf("NumPages = %d, want 2", r.NumPages())
	}
	if got := r.Info(); got.Title != "Résumé — roundtrip" || got.Author != "gopdf" {
		t.Errorf("Info = %+v", got)
	}
	size, err := r.PageSize(1)
	if err != nil || size.W != 792 || size.H != 612 {
		t.Errorf("PageSize(1) = %v, %v", size, err)
	}
	text, err := r.PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Hello, world — café!") {
		t.Errorf("page 0 text = %q", text)
	}
	if !strings.Contains(text, "\nsecond line") {
		t.Errorf("line break not inferred: %q", text)
	}
}

// TestRoundTripEmbeddedFont verifies text extraction through a subset
// TrueType font's ToUnicode CMap.
func TestRoundTripEmbeddedFont(t *testing.T) {
	font, err := LoadFont(testFontPath(t))
	if err != nil {
		t.Fatal(err)
	}
	doc := New()
	page := doc.AddPage()
	page.SetFont(font, 14)
	page.Text(72, 72, "Ωмир čeština")

	r, err := NewReader(docBytes(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	text, err := r.PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Ωмир čeština") {
		t.Errorf("extracted %q", text)
	}
}

func TestMergeAndStamp(t *testing.T) {
	a := New()
	pa := a.AddPage()
	pa.SetFont(Helvetica, 12)
	pa.Text(72, 72, "document A")

	b := New()
	pb := b.AddPage()
	pb.SetFont(Helvetica, 12)
	pb.Text(72, 72, "document B")
	pb2 := b.AddPage()
	pb2.SetFont(Helvetica, 12)
	pb2.Text(72, 72, "document B page 2")

	ra, err := NewReader(docBytes(t, a))
	if err != nil {
		t.Fatal(err)
	}
	rb, err := NewReader(docBytes(t, b))
	if err != nil {
		t.Fatal(err)
	}

	merged := New()
	if err := merged.AppendPDF(ra); err != nil {
		t.Fatal(err)
	}
	if err := merged.AppendPDF(rb); err != nil {
		t.Fatal(err)
	}
	// Stamp an overlay onto the first imported page.
	stamp, err := merged.ImportPage(rb, 0)
	if err != nil {
		t.Fatal(err)
	}
	stamp.SetFont(HelveticaBold, 24)
	stamp.SetFillColor(RGB(200, 0, 0))
	stamp.Text(100, 300, "APPROVED")

	out := docBytes(t, merged)
	verifyXref(t, out)

	rm, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	if rm.NumPages() != 4 {
		t.Fatalf("merged NumPages = %d, want 4", rm.NumPages())
	}
	// Text extraction descends into the imported form XObjects, so the
	// merged file reads back page by page.
	for i, want := range []string{"document A", "document B", "document B page 2"} {
		got, err := rm.PageText(i)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, want) {
			t.Errorf("merged page %d text = %q, want %q", i, got, want)
		}
	}
	// The stamped page keeps both the original content and the overlay.
	overlay, err := rm.PageText(3)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(overlay, "APPROVED") {
		t.Errorf("stamped page missing overlay: %q", overlay)
	}
	if !strings.Contains(overlay, "document B") {
		t.Errorf("stamped page lost base content: %q", overlay)
	}
}

// containsInDeflate reports whether any Flate stream in the file inflates
// to content containing s.
func containsInDeflate(pdf []byte, s string) bool {
	for i := 0; i+8 < len(pdf); i++ {
		if !bytes.HasPrefix(pdf[i:], []byte("stream\n")) {
			continue
		}
		zr, err := zlib.NewReader(bytes.NewReader(pdf[i+7:]))
		if err != nil {
			continue
		}
		data, _ := limitedCopy(zr)
		if bytes.Contains(data, []byte(s)) {
			return true
		}
	}
	return false
}

func TestImportRotatedPage(t *testing.T) {
	src := New()
	p := src.AddPage() // A4 portrait
	p.SetFont(Helvetica, 12)
	p.Text(72, 72, "rotated content")
	p.SetRotate(90)

	r, err := NewReader(docBytes(t, src))
	if err != nil {
		t.Fatal(err)
	}
	size, _ := r.PageSize(0)
	if size.W != A4.H || size.H != A4.W {
		t.Errorf("rotated source size = %v", size)
	}

	dst := New()
	ip, err := dst.ImportPage(r, 0)
	if err != nil {
		t.Fatal(err)
	}
	// The import normalizes rotation: target page has display dimensions.
	if ip.Width() != A4.H || ip.Height() != A4.W {
		t.Errorf("imported page = %vx%v", ip.Width(), ip.Height())
	}
	if _, err := NewReader(docBytes(t, dst)); err != nil {
		t.Fatal(err)
	}
}

func TestImportPreservesURILinks(t *testing.T) {
	src := New()
	p := src.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(72, 72, "link here")
	p.LinkURL(72, 60, 100, 20, "https://example.org/kept")
	p.LinkPage(72, 100, 100, 20, p, 0) // internal: dropped on import

	r, err := NewReader(docBytes(t, src))
	if err != nil {
		t.Fatal(err)
	}
	dst := New()
	if _, err := dst.ImportPage(r, 0); err != nil {
		t.Fatal(err)
	}
	rm, err := NewReader(docBytes(t, dst))
	if err != nil {
		t.Fatal(err)
	}
	annots, _ := rm.resolve(rm.pages[0].dict["Annots"]).(Array)
	if len(annots) != 1 {
		t.Fatalf("imported page has %d annots, want 1 (URI only)", len(annots))
	}
	annot, _ := rm.resolve(annots[0]).(Dict)
	action, _ := rm.resolve(annot["A"]).(Dict)
	uri, _ := rm.resolve(action["URI"]).(String)
	if string(uri) != "https://example.org/kept" {
		t.Errorf("imported URI = %q", uri)
	}
}

// TestXrefStreamAndObjStm parses a hand-built PDF 1.5 file using a
// cross-reference stream and an object stream.
func TestXrefStreamAndObjStm(t *testing.T) {
	data := buildXrefStreamPDF()
	r, err := NewReader(data)
	if err != nil {
		t.Fatal(err)
	}
	if r.NumPages() != 1 {
		t.Fatalf("NumPages = %d, want 1", r.NumPages())
	}
	text, err := r.PageText(0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "modern xref") {
		t.Errorf("text = %q", text)
	}
	// And the page must survive an import round-trip.
	doc := New()
	if _, err := doc.ImportPage(r, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := NewReader(docBytes(t, doc)); err != nil {
		t.Fatal(err)
	}
}

// buildXrefStreamPDF assembles a minimal PDF with objects 1-3 (catalog,
// pages, page) inside an /ObjStm, a plain content stream, and an xref
// stream — the layout modern writers produce.
func buildXrefStreamPDF() []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.5\n")

	offsets := make(map[int]int)

	// Object 4: content stream.
	content := "BT /F1 12 Tf 72 720 Td (modern xref) Tj ET"
	offsets[4] = buf.Len()
	fmt.Fprintf(&buf, "4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(content), content)

	// Object 7: font (referenced from the page's resources).
	offsets[7] = buf.Len()
	buf.WriteString("7 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n")

	// Object 5: object stream holding objects 1 (catalog), 2 (pages), 3 (page).
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 7 0 R >> >> >>",
	}
	var header, body strings.Builder
	for i, o := range objs {
		fmt.Fprintf(&header, "%d %d ", i+1, body.Len())
		body.WriteString(o)
		body.WriteString(" ")
	}
	stmData := header.String() + body.String()
	first := len(header.String())
	offsets[5] = buf.Len()
	fmt.Fprintf(&buf, "5 0 obj\n<< /Type /ObjStm /N 3 /First %d /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		first, len(stmData), stmData)

	// Object 6: xref stream. W [1 4 2]: type, offset/stm, gen/idx.
	xrefOff := buf.Len()
	offsets[6] = xrefOff
	row := func(typ byte, f2 int, f3 int) []byte {
		return []byte{typ,
			byte(f2 >> 24), byte(f2 >> 16), byte(f2 >> 8), byte(f2),
			byte(f3 >> 8), byte(f3)}
	}
	var xref bytes.Buffer
	xref.Write(row(0, 0, 0xFFFF))     // 0: free
	xref.Write(row(2, 5, 0))          // 1: in objstm 5, index 0
	xref.Write(row(2, 5, 1))          // 2
	xref.Write(row(2, 5, 2))          // 3
	xref.Write(row(1, offsets[4], 0)) // 4
	xref.Write(row(1, offsets[5], 0)) // 5
	xref.Write(row(1, offsets[6], 0)) // 6
	xref.Write(row(1, offsets[7], 0)) // 7
	fmt.Fprintf(&buf, "6 0 obj\n<< /Type /XRef /Size 8 /W [1 4 2] /Root 1 0 R /Length %d >>\nstream\n", xref.Len())
	buf.Write(xref.Bytes())
	buf.WriteString("\nendstream\nendobj\n")

	fmt.Fprintf(&buf, "startxref\n%d\n%%%%EOF\n", xrefOff)
	return buf.Bytes()
}

func TestFilters(t *testing.T) {
	r := &Reader{}

	hex, err := r.decodeStream(Dict{"Filter": Name("ASCIIHexDecode")}, []byte("48 65 6C 6C 6F>"))
	if err != nil || string(hex) != "Hello" {
		t.Errorf("ASCIIHex = %q, %v", hex, err)
	}

	rl, err := r.decodeStream(Dict{"Filter": Name("RunLengthDecode")},
		[]byte{2, 'a', 'b', 'c', 254, 'x', 128})
	if err != nil || string(rl) != "abcxxx" {
		t.Errorf("RunLength = %q, %v", rl, err)
	}

	out, err := r.decodeStream(Dict{"Filter": Name("ASCII85Decode")}, []byte("87cURDZ~>"))
	if err != nil || string(out) != "Hello" {
		t.Errorf("ASCII85 = %q, %v", out, err)
	}

	// Filter chain: hex-wrapped zlib.
	payload := []byte("chained filters work")
	z, _ := flateCompress(payload)
	chained, err := r.decodeStream(Dict{
		"Filter": Array{Name("ASCIIHexDecode"), Name("FlateDecode")},
	}, []byte(fmt.Sprintf("%X>", z)))
	if err != nil || !bytes.Equal(chained, payload) {
		t.Errorf("chain = %q, %v", chained, err)
	}
}

func TestPNGPredictor(t *testing.T) {
	// Two rows of 4 bytes, filter type 2 (up) and 1 (sub).
	raw := []byte{
		2, 10, 20, 30, 40, // row 1: up with zero prior = literal
		1, 5, 5, 5, 5, // row 2: sub: cumulative 5,10,15,20
	}
	out, err := applyPredictor(raw, Dict{
		"Predictor": int64(12), "Columns": int64(4),
	}, &Reader{})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{10, 20, 30, 40, 15, 25, 35, 45}
	// row2 = row2 filtered + left... verify via manual: sub adds left:
	// 5,10,15,20 then predictor "up"? No: row 2 uses sub only.
	want = []byte{10, 20, 30, 40, 5, 10, 15, 20}
	if !bytes.Equal(out, want) {
		t.Errorf("predictor out = %v, want %v", out, want)
	}
}

func TestLZWRoundTrip(t *testing.T) {
	// PDF LZW with EarlyChange=1: decode a stream produced by a known
	// simple encoder path — here we hand-encode "aaaa" with 9-bit codes:
	// Clear(256) 'a'(97) 258 EOD(257).
	bits := []int{256, 97, 258, 257}
	var out []byte
	var acc, n uint32
	for _, code := range bits {
		acc = acc<<9 | uint32(code)
		n += 9
		for n >= 8 {
			out = append(out, byte(acc>>(n-8)))
			n -= 8
		}
	}
	if n > 0 {
		out = append(out, byte(acc<<(8-n)))
	}
	got, err := lzwDecode(out, 1)
	if err != nil || string(got) != "aaa" {
		t.Errorf("lzw = %q, %v", got, err)
	}
}

func TestReaderRejects(t *testing.T) {
	if _, err := NewReader([]byte("not a pdf at all")); err == nil {
		t.Error("accepted junk")
	}
	doc := New()
	doc.AddPage()
	data := docBytes(t, doc)
	// A trailer claiming encryption with no usable /Encrypt dictionary
	// must be rejected rather than silently mis-parsed.
	enc := bytes.Replace(data, []byte("/Root 1 0 R"), []byte("/Root 1 0 R /Encrypt 9 0 R"), 1)
	if _, err := NewReader(enc); err == nil {
		t.Error("accepted a file with a broken /Encrypt entry")
	}
}

func TestExtractPagesAndMergeHelpers(t *testing.T) {
	dir := t.TempDir()
	mk := func(path, text string, pages int) {
		doc := New()
		for i := 0; i < pages; i++ {
			p := doc.AddPage()
			p.SetFont(Helvetica, 12)
			p.Text(72, 72, fmt.Sprintf("%s page %d", text, i+1))
		}
		if err := doc.Save(path); err != nil {
			t.Fatal(err)
		}
	}
	a, b := dir+"/a.pdf", dir+"/b.pdf"
	mk(a, "alpha", 3)
	mk(b, "beta", 2)

	merged := dir + "/merged.pdf"
	if err := Merge(merged, a, b); err != nil {
		t.Fatal(err)
	}
	rm, err := Open(merged)
	if err != nil {
		t.Fatal(err)
	}
	if rm.NumPages() != 5 {
		t.Errorf("merged pages = %d, want 5", rm.NumPages())
	}

	split := dir + "/split.pdf"
	if err := ExtractPages(split, a, 2, 0); err != nil {
		t.Fatal(err)
	}
	rs, err := Open(split)
	if err != nil {
		t.Fatal(err)
	}
	if rs.NumPages() != 2 {
		t.Errorf("split pages = %d, want 2", rs.NumPages())
	}
}

func TestPageRotateWrite(t *testing.T) {
	doc := New()
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(72, 72, "x")
	p.SetRotate(-90)
	out := docBytes(t, doc)
	if !bytes.Contains(out, []byte("/Rotate 270")) {
		t.Error("negative rotation not normalized to 270")
	}
}
