package gopdf

import (
	"bytes"
	"io"
	"os"
	"testing"
)

// FuzzParseTTF throws arbitrary bytes at the TrueType parser and, when
// something parses, exercises measurement, subsetting and re-parsing.
// The parser must reject garbage with an error, never a panic.
func FuzzParseTTF(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("\x00\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"))
	f.Add([]byte("ttcf\x00\x01\x00\x00\x00\x00\x00\x01\x00\x00\x00\x0c"))
	f.Add([]byte("OTTO\x00\x00\x00\x00\x00\x00\x00\x00"))
	// A CFF-based OpenType font exercises the charstring interpreter that
	// decides which subroutines a subset still needs.
	if data, err := os.ReadFile("/System/Library/Fonts/Supplemental/STIXGeneral.otf"); err == nil {
		if full, err := parseTTF(data); err == nil {
			used := map[uint16]bool{}
			for _, r := range "Hand" {
				used[full.cmap[r]] = true
			}
			if small, err := full.subset(used); err == nil {
				f.Add(small)
			}
		}
	}

	// Seed with a small valid font — a subset of a system font — so the
	// mutator reaches the success path without megabyte-sized inputs.
	if data, err := os.ReadFile("/System/Library/Fonts/Supplemental/Arial.ttf"); err == nil {
		f.Add(data[:2048])
		if full, err := parseTTF(data); err == nil {
			used := make(map[uint16]bool)
			for _, r := range "ABCabc012" {
				used[full.cmap[r]] = true
			}
			if small, err := full.subset(used); err == nil {
				f.Add(small)
			}
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return // keep per-exec cost bounded
		}
		ttf, err := parseTTF(data)
		if err != nil {
			return
		}
		font := &Font{name: "Fuzz", ttf: ttf}
		font.TextWidth("Hello, wörld! �", 12)
		used := map[uint16]bool{0: true}
		for _, r := range "ABC" {
			used[ttf.cmap[r]] = true
		}
		sub, err := ttf.subset(used)
		if err != nil {
			return
		}
		if _, err := parseTTF(sub); err != nil {
			t.Errorf("subset of valid font does not reparse: %v", err)
		}
	})
}

// FuzzReader throws arbitrary bytes at the PDF parser. Whatever parses is
// also pushed through text extraction, metadata, and a page import — none
// of which may panic or hang.
func FuzzReader(f *testing.F) {
	small := New()
	sp := small.AddPage()
	sp.SetFont(Helvetica, 10)
	sp.Text(50, 50, "seed (text) \\ with — specials")
	sp.LinkURL(10, 10, 40, 12, "https://example.com")
	small.Compress = false
	var buf bytes.Buffer
	if _, err := small.WriteTo(&buf); err == nil {
		f.Add(buf.Bytes())
	}
	f.Add(buildXrefStreamPDF())
	f.Add(formFixture())
	f.Add([]byte("%PDF-1.4\nstartxref\n0\n%%EOF"))

	// An encrypted document (empty user password) so the mutator reaches
	// the decryption paths.
	enc := New()
	enc.Encrypt("", "owner", AllowAll, AES128)
	ep := enc.AddPage()
	ep.SetFont(Helvetica, 10)
	ep.Text(50, 50, "encrypted seed")
	var encBuf bytes.Buffer
	if _, err := enc.WriteTo(&encBuf); err == nil {
		f.Add(encBuf.Bytes())
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return
		}
		r, err := NewReader(data)
		if err != nil {
			return
		}
		r.Info()
		for i := 0; i < r.NumPages() && i < 8; i++ {
			r.PageSize(i)
			r.PageText(i)
		}
		// Form discovery walks an attacker-controlled field tree.
		fields := r.FormFields()
		values := make(map[string]string, len(fields))
		for i, fl := range fields {
			if i >= 4 {
				break
			}
			values[fl.Name] = "x"
		}
		filler := New()
		if _, err := filler.FillForm(r, values); err == nil {
			filler.WriteTo(io.Discard)
		}
		keeper := New()
		if _, err := keeper.FillFormInteractive(r, values); err == nil {
			keeper.WriteTo(io.Discard)
		}
		// An incremental update rewrites objects of the original file.
		u := Update(r)
		if page, err := u.Page(0); err == nil {
			page.ReplaceText("e", "a")
			page.ReplaceTextReflow("1", "2")
		}
		u.SetFormValues(values)
		u.SetPageRotation(0, 90)
		u.SetInfo(Info{Title: "fuzz"})
		r.Annotations(0)
		if imgs, err := r.PageImages(0); err == nil {
			for i, im := range imgs {
				if i >= 3 {
					break
				}
				im.Decode()
			}
		}
		u.RemoveAnnotations(0, func(a Annotation) bool { return a.Type == AnnotText })
		if r.NumPages() > 1 {
			u.MovePage(r.NumPages()-1, 0)
		}
		u.WriteTo(io.Discard)
		doc := New()
		if _, err := doc.ImportPage(r, 0); err == nil {
			doc.WriteTo(io.Discard)
		}
		// Editing walks the content stream token by token and rewrites
		// it; neither the scanner nor the splicer may panic.
		ed := New()
		if page, err := ed.EditPage(r, 0); err == nil {
			for _, run := range page.Runs() {
				_ = run.Text
			}
			for _, b := range page.Blocks() {
				_ = b.Text
			}
			page.SetFitMode(FitScale)
			page.ReplaceText("e", "a")
			page.SetMaxExtraLines(2)
			page.ReplaceTextReflow("1", "22")
			ed.WriteTo(io.Discard)
		}
	})
}

// FuzzParseCFF throws arbitrary bytes at the CFF parser and, when
// something parses, walks the charstrings for outlines and widths.
//
// A CFF is a nest of indexes holding offsets into each other, and every
// one of those offsets comes from the file: an index whose count says a
// thousand entries in twenty bytes, a charstring whose subroutine calls
// itself, a private dictionary pointing past the end. None of that may
// be a panic, and none of it may run for ever.
func FuzzParseCFF(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{1, 0, 4, 1})             // a bare, plausible header
	f.Add([]byte{1, 0, 4, 4, 0, 0, 0, 0}) // and one with an empty index
	if data, err := os.ReadFile("/System/Library/Fonts/Supplemental/STIXGeneral.otf"); err == nil {
		if ttf, err := parseTTF(data); err == nil {
			if cff, ok := ttf.tables["CFF "]; ok {
				f.Add(cff)
			}
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		out, err := parseCFFOutlines(data)
		if err != nil || out == nil {
			return
		}
		n := out.numGlyphs()
		if n > 2000 {
			n = 2000
		}
		for gid := 0; gid < n; gid++ {
			out.outline(uint16(gid))
			out.gidForCID(uint16(gid))
		}
	})
}

// FuzzTokenizeContent throws arbitrary bytes at the content-stream
// lexer. Every editing path in this package starts by tokenizing a
// stream somebody else wrote, and the spans it reports are used to cut
// that stream up — so a span outside the input would splice from
// nowhere.
func FuzzTokenizeContent(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("BT /F1 12 Tf (hi) Tj ET"))
	f.Add([]byte("[ (a) -20 (b) ] TJ"))
	f.Add([]byte("<</A 1>> BDC q 1 0 0 1 0 0 cm Q EMC"))
	f.Add([]byte("(unterminated"))
	f.Add([]byte("<0102"))
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, tok := range tokenizeContent(data) {
			if tok.start < 0 || tok.end > len(data) || tok.start > tok.end {
				t.Fatalf("token span [%d,%d) is outside %d bytes",
					tok.start, tok.end, len(data))
			}
		}
	})
}

// FuzzToUnicodeCMap throws arbitrary bytes at the CMap parser, which
// decides what a character code means. It is reached from every
// document that carries a /ToUnicode, and it has found a
// denial-of-service before.
func FuzzToUnicodeCMap(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("1 beginbfchar <20> <0041> endbfchar"))
	f.Add([]byte("1 beginbfrange <20> <7e> <0041> endbfrange"))
	f.Add([]byte("2 begincidrange <0000> <ffff> 0 endcidrange"))
	f.Add([]byte("beginbfchar <20> [<0041> <0042>] endbfchar"))
	f.Fuzz(func(t *testing.T, data []byte) {
		for code, text := range parseToUnicodeCMap(data) {
			// A mapping to an enormous string from a two-byte code would
			// mean the parser had been talked into building one.
			if len(text) > 4096 {
				t.Fatalf("code %d maps to %d bytes", code, len(text))
			}
		}
	})
}

// FuzzFilters throws arbitrary bytes at the stream decoders. They are
// the first thing to touch a hostile file: the bytes arrive compressed
// and something has to expand them before anything else can look.
func FuzzFilters(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("~>"))
	f.Add([]byte("z~>"))
	f.Add([]byte{0x80, 0x01, 0x02})
	f.Add([]byte("!!!!!~>"))
	f.Fuzz(func(t *testing.T, data []byte) {
		ascii85Decode(data)
		runLengthDecode(data)
		// The predictors read their configuration from the document, so
		// a few plausible shapes are tried against the same bytes.
		for _, parm := range []Dict{
			{"Predictor": 12, "Columns": 4},
			{"Predictor": 15, "Columns": 1, "Colors": 3},
			{"Predictor": 2, "Columns": 8, "BitsPerComponent": 4},
		} {
			applyPredictor(data, parm, nil)
		}
	})
}

// FuzzJBIG2 throws arbitrary bytes at the JBIG2 decoder, which reads a
// segment header giving lengths and counts and then believes them.
func FuzzJBIG2(f *testing.F) {
	f.Add([]byte{}, []byte{})
	f.Add([]byte{0, 0, 0, 1, 0x30, 0, 1, 0}, []byte{})
	f.Fuzz(func(t *testing.T, data, globals []byte) {
		// The size is bounded so a fuzz case cannot ask for a gigabyte
		// of bitmap; what is under test is the parsing, not the limit.
		decodeJBIG2(data, globals, 64, 64)
	})
}
