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
