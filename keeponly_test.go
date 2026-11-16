package gopdf

// keeponly_test.go — one document per hiding place, each with "Bianchi"
// planted in it; the copy KeepOnly writes holds it nowhere, and what is
// meant to stay — the text, the bookmarks, the links, the tags, the
// labels — still works.

import (
	"bytes"
	"compress/zlib"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// kfix is a one-page document written from raw objects. Objects 1–5 are
// the catalog, the page tree, the page, a font and the page's content;
// a fixture adds to them and adds objects of its own from 10 up.
type kfix struct {
	cat, page, res, content string
	objs                    map[int]string
	trailer                 string
}

func kstream(dict, data string) string {
	return fmt.Sprintf("<< %s /Length %d >>\nstream\n%s\nendstream", dict, len(data), data)
}

func (f kfix) pdf() []byte {
	objs := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R " + f.cat + " >>",
		2: "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Resources << /Font << /F1 4 0 R >> " + f.res + " >> /Contents 5 0 R " + f.page + " >>",
		4: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
		5: kstream("", "BT /F1 12 Tf 20 150 Td (Hello world) Tj ET\n"+f.content),
	}
	for n, o := range f.objs {
		objs[n] = o
	}
	return rawPDFOf(objs, f.trailer)
}

func rawPDFOf(objs map[int]string, trailer string) []byte {
	var nums []int
	for n := range objs {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	offs := map[int]int{}
	for _, n := range nums {
		offs[n] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", n, objs[n])
	}
	size := nums[len(nums)-1] + 1
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", size)
	for n := 1; n < size; n++ {
		if off, ok := offs[n]; ok {
			fmt.Fprintf(&b, "%010d 00000 n \n", off)
		} else {
			b.WriteString("0000000000 65535 f \n")
		}
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R %s >>\nstartxref\n%d\n%%%%EOF\n", size, trailer, xref)
	return b.Bytes()
}

var keepTestPolicy = KeepPolicy{
	Text:    func(s string) string { return strings.ReplaceAll(s, "Bianchi", "XXXX") },
	WebLink: func(u string) bool { return !strings.Contains(strings.ToLower(u), "bianchi") },
}

// keptCopy runs KeepOnly and checks the copy opens, keeps the page's
// text and has a sound cross-reference table.
func keptCopy(t *testing.T, src []byte) ([]byte, KeepReport) {
	t.Helper()
	r := NewReaderOrFail(t, src)
	var out bytes.Buffer
	rep, err := KeepOnly(r, &out, keepTestPolicy)
	if err != nil {
		t.Fatalf("KeepOnly: %v", err)
	}
	verifyXref(t, out.Bytes())
	cp := NewReaderOrFail(t, out.Bytes())
	if cp.NumPages() != r.NumPages() {
		t.Fatalf("the copy has %d pages, the source %d", cp.NumPages(), r.NumPages())
	}
	text, err := cp.PageText(0)
	if err != nil || !strings.Contains(text, "Hello world") {
		t.Fatalf("the copy's text is %q (%v); want it to hold Hello world", text, err)
	}
	return out.Bytes(), rep
}

// whereHeld searches a file the simple way, without its reader: the raw
// bytes, every hexadecimal string, every Flate stream inflated and
// searched the same way, each also as UTF-16. It returns a few bytes
// around the first find, or "".
func whereHeld(b []byte, value string) string {
	return whereHeldDepth(b, strings.ToLower(value), 0)
}

var (
	hexStringRe = regexp.MustCompile(`<([0-9A-Fa-f\s]+)>`)
	streamRe    = regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\n?endstream`)
)

func whereHeldDepth(b []byte, low string, depth int) string {
	if depth > 3 {
		return ""
	}
	var u16 []byte
	for _, c := range []byte(low) {
		u16 = append(u16, 0, c)
	}
	check := func(buf []byte, where string) string {
		l := bytes.ToLower(buf)
		for _, pat := range [][]byte{[]byte(low), u16} {
			if i := bytes.Index(l, pat); i >= 0 {
				from, to := max(0, i-30), min(len(buf), i+len(pat)+30)
				return fmt.Sprintf("%s: …%q…", where, buf[from:to])
			}
		}
		return ""
	}
	if w := check(b, "raw"); w != "" {
		return w
	}
	for _, m := range hexStringRe.FindAllSubmatch(b, -1) {
		h := bytes.Join(bytes.Fields(m[1]), nil)
		if len(h)%2 == 1 {
			h = append(h, '0')
		}
		dec := make([]byte, len(h)/2)
		if _, err := hex.Decode(dec, h); err == nil {
			if w := check(dec, "hex string"); w != "" {
				return w
			}
		}
	}
	for _, m := range streamRe.FindAllSubmatch(b, -1) {
		zr, err := zlib.NewReader(bytes.NewReader(m[1]))
		if err != nil {
			continue
		}
		dec, _ := io.ReadAll(zr)
		if w := whereHeldDepth(dec, low, depth+1); w != "" {
			return "stream > " + w
		}
	}
	return ""
}

// keptCatalog returns the copy's catalog.
func keptCatalog(t *testing.T, out []byte) (*Reader, Dict) {
	t.Helper()
	r := NewReaderOrFail(t, out)
	return r, r.Catalog()
}

func TestKeepOnlyLeavesEveryHidingPlaceOut(t *testing.T) {
	grayPixel := kstream("/Type /XObject /Subtype /Image /Width 1 /Height 1 /BitsPerComponent 8 /ColorSpace /DeviceGray", "\x80")
	xmp := kstream("/Type /Metadata /Subtype /XML", "<x:xmpmeta><dc:creator>Marco Bianchi</dc:creator></x:xmpmeta>")
	samplesWithValue := kstream("/Type /XObject /Subtype /Image /Width 7 /Height 1 /BitsPerComponent 8 /ColorSpace /DeviceGray", "Bianchi")
	drawIm1 := "q 10 0 0 10 200 20 cm /Im1 Do Q\n"

	cases := []struct {
		name  string
		f     kfix
		check func(t *testing.T, out []byte, rep KeepReport)
	}{
		{name: "XMP on the catalog", f: kfix{cat: "/Metadata 10 0 R", objs: map[int]string{10: xmp}}},
		{name: "XMP on the page", f: kfix{page: "/Metadata 10 0 R", objs: map[int]string{10: xmp}}},
		{name: "XMP on a picture", f: kfix{
			res: "/XObject << /Im1 11 0 R >>", content: drawIm1,
			objs: map[int]string{10: xmp, 11: strings.Replace(grayPixel, "/Type /XObject", "/Type /XObject /Metadata 10 0 R", 1)},
		}},
		{name: "XMP on a font", f: kfix{
			res: "/Font << /F1 4 0 R /F2 11 0 R >>", content: "BT /F2 9 Tf 20 120 Td (Second line) Tj ET\n",
			objs: map[int]string{
				10: xmp,
				11: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold /FontDescriptor 12 0 R /Metadata 10 0 R >>",
				12: "<< /Type /FontDescriptor /FontName /Helvetica-Bold /Flags 32 /FontBBox [0 0 1000 1000] /ItalicAngle 0 /Ascent 718 /Descent -207 /CapHeight 718 /StemV 140 /Metadata 10 0 R >>",
			},
		}},
		{name: "the Info dictionary", f: kfix{trailer: "/Info 10 0 R", objs: map[int]string{10: "<< /Author (Marco Bianchi) /Title (Letter to Bianchi) >>"}},
			check: func(t *testing.T, out []byte, _ KeepReport) {
				if info := NewReaderOrFail(t, out).Info(); info.Author != "" || info.Title != "" {
					t.Errorf("the copy's Info is %+v; want it empty", info)
				}
			}},
		{name: "a page thumbnail", f: kfix{page: "/Thumb 10 0 R", objs: map[int]string{10: samplesWithValue}}},
		{name: "a picture's alternate copy", f: kfix{
			res: "/XObject << /Im1 11 0 R >>", content: drawIm1,
			objs: map[int]string{10: samplesWithValue, 11: strings.Replace(grayPixel, "/Type /XObject", "/Type /XObject /Alternates [<< /Image 10 0 R /DefaultForPrinting true >>]", 1)},
		}},
		{name: "PieceInfo private data", f: kfix{page: "/PieceInfo << /Acme << /Private (Bianchi) /LastModified (D:20200101) >> >> /LastModified (D:20200101)"}},
		{name: "a script and an opening action", f: kfix{
			cat:  "/OpenAction << /S /JavaScript /JS (app.alert('Bianchi')) >> /AA << /WC << /S /JavaScript /JS (Bianchi) >> >> /Names << /JavaScript 10 0 R >>",
			page: "/AA << /O << /S /JavaScript /JS (Bianchi) >> >>",
			objs: map[int]string{10: "<< /Names [(a) << /S /JavaScript /JS (Bianchi) >>] >>"},
		}, check: func(t *testing.T, _ []byte, rep KeepReport) {
			if rep.Scripts == 0 {
				t.Error("the report counts no script")
			}
		}},
		{name: "an attachment", f: kfix{
			cat:  "/Names << /EmbeddedFiles 10 0 R >>",
			page: "/Annots [13 0 R]",
			objs: map[int]string{
				10: "<< /Names [(Bianchi.txt) 11 0 R] >>",
				11: "<< /Type /Filespec /F (Bianchi.txt) /UF (Bianchi.txt) /EF << /F 12 0 R >> >>",
				12: kstream("/Type /EmbeddedFile", "Dear Mr Bianchi"),
				13: "<< /Type /Annot /Subtype /FileAttachment /Rect [0 0 10 10] /FS 11 0 R /Contents (Bianchi) >>",
			},
		}, check: func(t *testing.T, out []byte, rep KeepReport) {
			if rep.Attachments == 0 {
				t.Error("the report counts no attachment")
			}
			if n := len(NewReaderOrFail(t, out).Attachments()); n != 0 {
				t.Errorf("the copy has %d attachments", n)
			}
		}},
		{name: "text in a layer hidden by default", f: kfix{
			cat: "/OCProperties << /OCGs [10 0 R] /D << /Order [10 0 R] /OFF [10 0 R] >> >>",
			res: "/Properties << /L1 10 0 R >>",
			content: "/OC /L1 BDC BT /F1 12 Tf 20 100 Td (Bianchi) Tj ET EMC\n" +
				"/Span << /MCID 3 >> BDC /OC /L1 BDC q BT /F1 12 Tf 20 80 Td (Marco Bianchi) Tj ET Q EMC EMC\n",
			objs: map[int]string{10: "<< /Type /OCG /Name (Bianchi's notes) >>"},
		}, check: func(t *testing.T, out []byte, rep KeepReport) {
			if !rep.Layers || rep.HiddenLayerContent != 2 {
				t.Errorf("report %+v; want layers and two hidden pieces", rep)
			}
			if _, cat := keptCatalog(t, out); cat["OCProperties"] != nil {
				t.Error("the copy keeps OCProperties")
			}
		}},
		{name: "a picture placed in a hidden layer", f: kfix{
			cat: "/OCProperties << /OCGs [10 0 R] /D << /BaseState /OFF /ON [] >> >>",
			res: "/XObject << /Im1 11 0 R >>", content: drawIm1,
			objs: map[int]string{10: "<< /Type /OCG /Name (Scan) >>", 11: strings.Replace(samplesWithValue, "/Type /XObject", "/Type /XObject /OC 10 0 R", 1)},
		}, check: func(t *testing.T, out []byte, rep KeepReport) {
			if rep.HiddenLayerContent != 1 {
				t.Errorf("report %+v; want the picture counted", rep)
			}
		}},
		{name: "a layer membership hidden by its policy", f: kfix{
			cat:     "/OCProperties << /OCGs [10 0 R 11 0 R] /D << /OFF [11 0 R] >> >>",
			res:     "/Properties << /M1 12 0 R /M2 13 0 R >>",
			content: "/OC /M1 BDC BT /F1 12 Tf 20 100 Td (Bianchi) Tj ET EMC /OC /M2 BDC BT /F1 12 Tf 20 60 Td (Bianchi) Tj ET EMC\n",
			objs: map[int]string{
				10: "<< /Type /OCG /Name (A) >>", 11: "<< /Type /OCG /Name (B) >>",
				12: "<< /Type /OCMD /OCGs [10 0 R 11 0 R] /P /AllOn >>",
				13: "<< /Type /OCMD /VE [/And 10 0 R [/Not 10 0 R]] >>",
			},
		}},
		{name: "private keys", f: kfix{
			cat:  "/AcmeCustomer (Bianchi) /PieceInfo << /X (Bianchi) >> /URI << /Base (https://bianchi.ch/) >> /SpiderInfo << /V 1 >> /Threads [<< /I << /Title (Bianchi) >> >>] /Collection << /D (Bianchi) >>",
			page: "/AcmeCustomer (Bianchi) /B [<< /T (Bianchi) >>] /SeparationInfo << /DeviceColorant (Bianchi) >>",
			res:  "/ExtGState << /G1 10 0 R >>", content: "/G1 gs\n",
			objs: map[int]string{10: "<< /Type /ExtGState /CA 1 /AcmeNote (Bianchi) >>"},
		}},
		{name: "an object nothing points to", f: kfix{objs: map[int]string{10: "<< /Note (Bianchi) >>", 11: kstream("", "Bianchi")}}},
		{name: "alt text and actual text in the tags", f: kfix{
			cat:     "/MarkInfo << /Marked true >> /StructTreeRoot 10 0 R /Lang (de-CH)",
			page:    "/StructParents 0",
			content: "/Figure << /MCID 0 /ActualText (Bianchi) /Acme (Bianchi) >> BDC EMC /P << /MCID 1 /E (Bianchi) >> BDC EMC\n",
			objs: map[int]string{
				10: "<< /Type /StructTreeRoot /K [11 0 R] /ParentTree 12 0 R /ParentTreeNextKey 1 /RoleMap << /Bild /Figure >> /IDTree << /Names [(Bianchi) 11 0 R] >> >>",
				11: "<< /Type /StructElem /S /Bild /P 10 0 R /Pg 3 0 R /Alt (Photo of Bianchi) /ActualText (Bianchi) /T (Bianchi) /ID (Bianchi) /Acme (Bianchi) /K [0 1] /A << /O /Layout /Placement /Block /Note (Bianchi) >> >>",
				12: "<< /Nums [0 [11 0 R 11 0 R]] >>",
			},
		}, check: func(t *testing.T, out []byte, _ KeepReport) {
			cp := NewReaderOrFail(t, out)
			nodes := cp.Structure()
			if len(nodes) != 1 || nodes[0].Role != "Figure" || nodes[0].Alt != "Photo of XXXX" {
				t.Fatalf("the copy's tags are %+v; want one Figure with its alt text cleaned", nodes)
			}
			if !cp.Tagged() {
				t.Error("the copy is no longer tagged")
			}
		}},
		{name: "a bookmark title", f: kfix{
			cat: "/Outlines 10 0 R /PageMode /UseOutlines",
			objs: map[int]string{
				10: "<< /Type /Outlines /First 11 0 R /Last 12 0 R /Count 2 >>",
				11: "<< /Title (Letter to Bianchi) /Parent 10 0 R /Next 12 0 R /Dest [3 0 R /XYZ 0 200 0] /SE 11 0 R >>",
				12: "<< /Title (Contract) /Parent 10 0 R /Prev 11 0 R /A << /S /GoTo /D [3 0 R /Fit] >> >>",
			},
		}, check: func(t *testing.T, out []byte, _ KeepReport) {
			r, cat := keptCatalog(t, out)
			ol, _ := r.Resolve(cat["Outlines"]).(Dict)
			first, _ := r.Resolve(ol["First"]).(Dict)
			second, _ := r.Resolve(first["Next"]).(Dict)
			if decodeTextString(first["Title"].(String)) != "Letter to XXXX" || first["Dest"] == nil {
				t.Errorf("first bookmark %v; want its title cleaned and its jump kept", first)
			}
			if second == nil || second["A"] == nil {
				t.Errorf("second bookmark %v; want it kept with its jump", second)
			}
		}},
		{name: "a mail link", f: kfix{
			page: "/Annots [10 0 R 11 0 R]",
			objs: map[int]string{
				10: "<< /Type /Annot /Subtype /Link /Rect [0 0 10 10] /A << /S /URI /URI (mailto:marco.bianchi@example.ch) >> >>",
				11: "<< /Type /Annot /Subtype /Link /Rect [0 20 10 30] /A << /S /Launch /F (Bianchi.exe) >> >>",
			},
		}, check: func(t *testing.T, out []byte, rep KeepReport) {
			if rep.Links != 2 {
				t.Errorf("report %+v; want two links counted", rep)
			}
		}},
		{name: "a web link holding the value", f: kfix{
			page: "/Annots [10 0 R 11 0 R]",
			objs: map[int]string{
				10: "<< /Type /Annot /Subtype /Link /Rect [0 0 10 10] /A << /S /URI /URI (https://example.ch/?q=Bianchi) >> >>",
				11: "<< /Type /Annot /Subtype /Link /Rect [0 20 10 30] /Contents (Bianchi) /NM (Bianchi) /A << /S /URI /URI (https://example.ch/about) >> >>",
			},
		}, check: func(t *testing.T, out []byte, _ KeepReport) {
			annots, _ := NewReaderOrFail(t, out).Annotations(0)
			if len(annots) != 1 || annots[0].URL != "https://example.ch/about" {
				t.Errorf("the copy's links are %+v; want only the clean web link", annots)
			}
		}},
		{name: "a named jump target", f: kfix{
			cat:  "/Names << /Dests 10 0 R >> /Dests << /Bianchi [3 0 R /Fit] >>",
			page: "/Annots [11 0 R 12 0 R]",
			objs: map[int]string{
				10: "<< /Kids [13 0 R] >>",
				13: "<< /Limits [(Bianchi) (Bianchi)] /Names [(Bianchi) [3 0 R /Fit]] >>",
				11: "<< /Type /Annot /Subtype /Link /Rect [0 0 10 10] /Dest (Bianchi) >>",
				12: "<< /Type /Annot /Subtype /Link /Rect [0 20 10 30] /A << /S /GoTo /D /Bianchi >> >>",
			},
		}, check: func(t *testing.T, out []byte, _ KeepReport) {
			r, cat := keptCatalog(t, out)
			names, _ := r.Resolve(cat["Names"]).(Dict)
			tree, _ := r.Resolve(names["Dests"]).(Dict)
			arr, _ := r.Resolve(tree["Names"]).(Array)
			if len(arr) != 2 || string(arr[0].(String)) != "XXXX" {
				t.Fatalf("the copy's jump targets are %v", arr)
			}
			old, _ := r.Resolve(cat["Dests"]).(Dict)
			if old["XXXX"] == nil {
				t.Errorf("the old-form targets are %v", old)
			}
			pg := r.PageDict(0)
			annots, _ := r.Resolve(pg["Annots"]).(Array)
			if len(annots) != 2 {
				t.Fatalf("%d links kept, want 2", len(annots))
			}
			l0, _ := r.Resolve(annots[0]).(Dict)
			if s, _ := l0["Dest"].(String); string(s) != "XXXX" {
				t.Errorf("the link jumps to %v; want the renamed target", l0["Dest"])
			}
		}},
		{name: "a page label", f: kfix{cat: "/PageLabels << /Nums [0 << /S /D /P (Bianchi-) /St 3 >>] >>"},
			check: func(t *testing.T, out []byte, _ KeepReport) {
				if got := NewReaderOrFail(t, out).PageLabel(0); got != "XXXX-3" {
					t.Errorf("the page label is %q, want XXXX-3", got)
				}
			}},
		{name: "a form and its signature", f: kfix{
			cat:  "/AcroForm << /Fields [10 0 R] /SigFlags 3 /DA (/Helv 0 Tf) >> /Perms << /DocMDP 11 0 R >>",
			page: "/Annots [10 0 R]",
			objs: map[int]string{
				10: "<< /Type /Annot /Subtype /Widget /FT /Tx /T (name) /V (Bianchi) /Rect [0 0 100 20] /P 3 0 R >>",
				11: "<< /Type /Sig /Name (Marco Bianchi) /Contents <00> >>",
			},
		}, check: func(t *testing.T, out []byte, rep KeepReport) {
			if !rep.Forms || !rep.Signatures || rep.Annotations != 1 {
				t.Errorf("report %+v; want the form, the signature and the field counted", rep)
			}
		}},
		{name: "a comment on the page", f: kfix{
			page: "/Annots [10 0 R]",
			objs: map[int]string{10: "<< /Type /Annot /Subtype /Text /Rect [0 0 10 10] /Contents (Call Bianchi) /T (Bianchi) >>"},
		}},
		{name: "a form drawn on the page", f: kfix{
			res:     "/XObject << /Fm1 10 0 R >>",
			content: "/Fm1 Do\n",
			objs: map[int]string{
				10: kstream("/Type /XObject /Subtype /Form /BBox [0 0 300 200] /Resources << /Font << /F1 4 0 R >> /Properties << /Hide 11 0 R >> >> /PieceInfo << /A << /Private (Bianchi) >> >> /Metadata 12 0 R /Ref << /F (Bianchi.pdf) >> /AcmeNote (Bianchi) /OPI << /Bianchi 1 >>",
					"BT /F1 9 Tf 20 40 Td (Footer) Tj ET /OC /Hide BDC BT /F1 9 Tf 20 20 Td (Bianchi) Tj ET EMC"),
				11: "<< /Type /OCG /Name (Hide) >>",
				12: xmp,
			},
			cat: "/OCProperties << /OCGs [11 0 R] /D << /OFF [11 0 R] >> >>",
		}},
		{name: "unused resources", f: kfix{
			res:  "/XObject << /Unused 10 0 R >> /Font << /F1 4 0 R /F9 11 0 R >>",
			objs: map[int]string{10: samplesWithValue, 11: "<< /Type /Font /Subtype /Type1 /BaseFont /Bianchi >>"},
		}},
		{name: "viewer preferences with a string", f: kfix{
			cat: "/ViewerPreferences << /DisplayDocTitle true /Bianchi (x) /PrintPageRange [0 0] /Note (Bianchi) >> /PageLayout /SinglePage /Lang (Bianchi)",
		}},
		{name: "a property list named in the resources", f: kfix{
			res:     "/Properties << /P1 10 0 R >>",
			content: "/Span /P1 BDC BT /F1 9 Tf 20 20 Td (x) Tj ET EMC\n",
			objs:    map[int]string{10: "<< /MCID 4 /ActualText (Bianchi) /Acme (Bianchi) >>"},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := c.f.pdf()
			if whereHeld(src, "Bianchi") == "" {
				t.Fatal("test premise: the source holds the value")
			}
			out, rep := keptCopy(t, src)
			if w := whereHeld(out, "Bianchi"); w != "" {
				t.Errorf("the copy holds the value — %s", w)
			}
			if c.check != nil {
				c.check(t, out, rep)
			}
		})
	}
}

// An earlier revision of the file stays in its bytes after an update;
// the copy holds only the current one.
func TestKeepOnlyLeavesEarlierRevisionsOut(t *testing.T) {
	first := kfix{content: "BT /F1 12 Tf 20 100 Td (Marco Bianchi) Tj ET\n"}.pdf()
	updated := appendUpdate(t, first, "", "", func(n int) map[int]string {
		return map[int]string{5: kstream("", "BT /F1 12 Tf 20 150 Td (Hello world) Tj ET")}
	})
	if NewReaderOrFail(t, updated).NumPages() != 1 || whereHeld(updated, "Bianchi") == "" {
		t.Fatal("test premise: an updated file still holding its first revision")
	}
	if text, _ := NewReaderOrFail(t, updated).PageText(0); strings.Contains(text, "Bianchi") {
		t.Fatal("test premise: the current revision does not show the value")
	}
	out, _ := keptCopy(t, updated)
	if w := whereHeld(out, "Bianchi"); w != "" {
		t.Errorf("the copy holds the earlier revision — %s", w)
	}
}

// What is meant to stay stays: content in a shown layer, a picture, a
// link within the document and one to the web, the page's look.
func TestKeepOnlyKeepsWhatIsShown(t *testing.T) {
	f := kfix{
		cat: "/OCProperties << /OCGs [10 0 R] /D << /ON [10 0 R] >> >>",
		res: "/Properties << /L1 10 0 R >> /XObject << /Im1 11 0 R >> /ExtGState << /G1 << /CA 0.5 >> >>",
		content: "/OC /L1 BDC BT /F1 12 Tf 20 100 Td (Shown layer) Tj ET EMC\n" +
			"q 40 0 0 20 200 20 cm /Im1 Do Q /G1 gs 0 0 1 rg 20 20 50 30 re f\n",
		page: "/Annots [12 0 R 13 0 R] /Rotate 0 /CropBox [0 0 300 200]",
		objs: map[int]string{
			10: "<< /Type /OCG /Name (Visible) >>",
			11: kstream("/Type /XObject /Subtype /Image /Width 2 /Height 2 /BitsPerComponent 8 /ColorSpace /DeviceRGB", "\xff\x00\x00\x00\xff\x00\x00\x00\xff\xff\xff\x00"),
			12: "<< /Type /Annot /Subtype /Link /Rect [0 0 10 10] /Border [0 0 0] /Dest [3 0 R /Fit] >>",
			13: "<< /Type /Annot /Subtype /Link /Rect [0 20 10 30] /A << /S /URI /URI (https://celatum.ch/) >> >>",
		},
	}
	src := f.pdf()
	out, rep := keptCopy(t, src)
	cp := NewReaderOrFail(t, out)
	text, _ := cp.PageText(0)
	if !strings.Contains(text, "Shown layer") {
		t.Errorf("the shown layer's text is gone: %q", text)
	}
	if rep.HiddenLayerContent != 0 || !rep.Layers {
		t.Errorf("report %+v", rep)
	}
	annots, _ := cp.Annotations(0)
	if len(annots) != 2 {
		t.Errorf("%d links kept, want 2: %+v", len(annots), annots)
	}
	if len(cp.Layers()) != 0 {
		t.Errorf("the copy still has layers: %+v", cp.Layers())
	}
	render := func(b []byte) image.Image {
		img, err := NewReaderOrFail(t, b).RenderPage(0, RenderOpts{DPI: 72, IncludeVector: true})
		if err != nil {
			t.Fatal(err)
		}
		return img
	}
	a, b := render(src), render(out)
	if a.Bounds() != b.Bounds() {
		t.Fatalf("page sizes differ: %v and %v", a.Bounds(), b.Bounds())
	}
	if diff := pixelDiffOf(a, b); diff > 0 {
		t.Errorf("the copy draws %d pixels differently", diff)
	}
}

// pixelDiffOf counts the pixels two renders differ in.
func pixelDiffOf(a, b image.Image) int {
	if a.Bounds() != b.Bounds() {
		return a.Bounds().Dx() * a.Bounds().Dy()
	}
	diff := 0
	for y := a.Bounds().Min.Y; y < a.Bounds().Max.Y; y++ {
		for x := a.Bounds().Min.X; x < a.Bounds().Max.X; x++ {
			r1, g1, b1, _ := a.At(x, y).RGBA()
			r2, g2, b2, _ := b.At(x, y).RGBA()
			if r1 != r2 || g1 != g2 || b1 != b2 {
				diff++
			}
		}
	}
	return diff
}

// The plan is checked before anything is written: an object the walk
// did not rebuild stops the copy.
func TestKeepOnlyRefusesAnObjectItDidNotRebuild(t *testing.T) {
	src := kfix{objs: map[int]string{10: "<< /Note (Bianchi) >>"}}.pdf()
	r := NewReaderOrFail(t, src)
	k := &keeper{r: r, p: keepTestPolicy, rw: newRewriter(r), kind: map[int]string{}, pages: map[int]bool{}, nextNum: r.maxObjectNumber() + 1}
	if err := k.plan(); err != nil {
		t.Fatal(err)
	}
	page := k.rw.replace[3].(Dict)
	page["Smuggled"] = Ref{Num: 10}
	err := k.verify()
	if err == nil || !errors.Is(err, errKeepOnly) || !strings.Contains(err.Error(), "10") {
		t.Fatalf("verify = %v; want a refusal naming object 10", err)
	}
}

// A document KeepOnly cannot read whole is refused, not copied.
func TestKeepOnlyFailsClosed(t *testing.T) {
	src := kfix{content: "BT /F1 12 Tf (x) Tj ET"}.pdf()
	// A content stream whose filter cannot be decoded.
	src = bytes.Replace(src, []byte("<<  /Length"), []byte("<< /Filter /NoSuchFilter /Length"), 1)
	r := NewReaderOrFail(t, src)
	var out bytes.Buffer
	if _, err := KeepOnly(r, &out, keepTestPolicy); err == nil {
		t.Fatal("a page whose content cannot be decoded was copied")
	}
	if out.Len() != 0 {
		t.Error("a refused copy wrote bytes")
	}
}

// A JPEG's notes — a camera's EXIF, XMP, a comment, an agency's caption,
// a JFIF preview — are not drawn and are left out; the picture decodes
// to the same pixels.
func TestKeepOnlyLeavesPictureNotesOut(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 16, 16))
	for i := range img.Pix {
		img.Pix[i] = uint8(i * 7)
	}
	var j bytes.Buffer
	if err := jpeg.Encode(&j, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	seg := func(marker byte, payload string) []byte {
		n := len(payload) + 2
		return append([]byte{0xFF, marker, byte(n >> 8), byte(n)}, payload...)
	}
	jfif := "JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x01\x01" + "Bia" // a 1×1 preview holding bytes
	var withNotes []byte
	withNotes = append(withNotes, 0xFF, 0xD8)
	withNotes = append(withNotes, seg(0xE0, jfif)...)
	withNotes = append(withNotes, seg(0xE1, "Exif\x00\x00Artist=Marco Bianchi")...)
	withNotes = append(withNotes, seg(0xE1, "http://ns.adobe.com/xap/1.0/\x00<dc:creator>Bianchi</dc:creator>")...)
	withNotes = append(withNotes, seg(0xED, "Photoshop 3.0\x00Bianchi")...)
	withNotes = append(withNotes, seg(0xFE, "scanned for Bianchi")...)
	withNotes = append(withNotes, j.Bytes()[2:]...)
	if _, err := jpeg.Decode(bytes.NewReader(withNotes)); err != nil {
		t.Fatalf("test premise: the JPEG with notes decodes: %v", err)
	}

	f := kfix{
		res: "/XObject << /Im1 10 0 R >>", content: "q 16 0 0 16 200 20 cm /Im1 Do Q\n",
		objs: map[int]string{10: kstream("/Type /XObject /Subtype /Image /Width 16 /Height 16 /BitsPerComponent 8 /ColorSpace /DeviceGray /Filter /DCTDecode", string(withNotes))},
	}
	out, _ := keptCopy(t, f.pdf())
	if w := whereHeld(out, "Bianchi"); w != "" {
		t.Errorf("the copy holds the value — %s", w)
	}
	if bytes.Contains(out, []byte("Bia")) {
		t.Error("the JFIF preview is still in the copy")
	}
	cp := NewReaderOrFail(t, out)
	imgs, err := cp.PageImages(0)
	if err != nil || len(imgs) != 1 {
		t.Fatalf("the copy's pictures: %v, %v", imgs, err)
	}
	stripped, ok := jpegWithoutNotes(withNotes)
	if !ok {
		t.Fatal("the JPEG could not be followed")
	}
	a, _ := jpeg.Decode(bytes.NewReader(withNotes))
	b, err := jpeg.Decode(bytes.NewReader(stripped))
	if err != nil {
		t.Fatalf("the cleaned JPEG does not decode: %v", err)
	}
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			if color.GrayModel.Convert(a.At(x, y)) != color.GrayModel.Convert(b.At(x, y)) {
				t.Fatalf("pixel %d,%d differs", x, y)
			}
		}
	}
}

// A JPEG 2000 file's XML and private boxes, and its codestream's
// comments, are left out; the boxes that draw it stay.
func TestJPXWithoutNotes(t *testing.T) {
	box := func(typ, payload string) []byte {
		n := 8 + len(payload)
		return append([]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}, append([]byte(typ), payload...)...)
	}
	com := []byte{0xFF, 0x64, 0x00, 0x0B, 0x00, 0x01}
	com = append(com, "Bianchi"...)
	siz := []byte{0xFF, 0x51, 0x00, 0x04, 0x00, 0x00}
	cs := append([]byte{0xFF, 0x4F}, siz...)
	cs = append(cs, com...)
	cs = append(cs, 0xFF, 0x90, 0x00, 0x0A, 1, 2, 3, 4, 5, 6, 7, 8, 0xFF, 0xD9)
	var file []byte
	file = append(file, box("jP  ", "\r\n\x87\n")...)
	file = append(file, box("ftyp", "jp2 \x00\x00\x00\x00jp2 ")...)
	file = append(file, box("jp2h", "colour")...)
	file = append(file, box("xml ", "<creator>Bianchi</creator>")...)
	file = append(file, box("uuid", "0123456789abcdefBianchi")...)
	file = append(file, box("jp2c", string(cs))...)
	out, ok := jpxWithoutNotes(file)
	if !ok {
		t.Fatal("the file could not be followed")
	}
	if bytes.Contains(out, []byte("Bianchi")) {
		t.Errorf("the notes stayed: %q", out)
	}
	for _, keep := range []string{"jP  ", "ftyp", "jp2h", "colour", "jp2c"} {
		if !bytes.Contains(out, []byte(keep)) {
			t.Errorf("%q was left out", keep)
		}
	}
	if !bytes.Contains(out, []byte{0xFF, 0x90, 0x00, 0x0A, 1, 2, 3}) {
		t.Error("the tile was changed")
	}
	bare, ok := jpxWithoutNotes(cs)
	if !ok || bytes.Contains(bare, []byte("Bianchi")) {
		t.Errorf("a bare codestream kept its comment: %q", bare)
	}
}
