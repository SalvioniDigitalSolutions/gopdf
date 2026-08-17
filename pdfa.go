package gopdf

import (
	"fmt"
	"strings"
)

// PDF/A: writing a document meant to be readable in fifty years.
//
// The archival profile is mostly a set of refusals. A conforming file may
// not rely on anything outside itself — every font embedded, no
// encryption, no external actions — and must say what its colours mean
// and what it claims to be. None of that changes how a page looks; all of
// it changes whether an archive will accept the file.
//
// Two things are offered. A document being built can be asked for the
// profile, which adds what it requires and refuses what it forbids. And
// an existing document can be checked against it, which is the more
// useful of the two: a caller is far likelier to have a file and a
// question than a blank page and an intention.

// PDFAConformance names a level of the archival profile.
type PDFAConformance string

const (
	// PDFA2b is the level that asks the file to be self-contained and to
	// look the same everywhere. It is what most archives require.
	PDFA2b PDFAConformance = "2B"
	// PDFA2u adds that every glyph must map to a character, so the text
	// can be extracted and searched.
	PDFA2u PDFAConformance = "2U"
)

// SetPDFA asks for a document to be written to the archival profile.
//
// It adds the metadata packet, the colour space declaration and the
// identification the profile requires, and refuses what it forbids:
// encryption is the one this package can otherwise do and PDF/A cannot
// have. Save reports an error rather than writing a file that claims a
// conformance it does not meet.
func (d *Document) SetPDFA(level PDFAConformance) {
	d.pdfa = level
	if level != "" {
		// The profile requires a metadata packet, so asking for one
		// implicitly is kinder than failing at save time for the lack.
		d.wantXMP = true
	}
}

// pdfaCheck reports what stops a document being written to its profile.
func (d *Document) pdfaCheck() error {
	if d.pdfa == "" {
		return nil
	}
	if d.encryptSetup != nil {
		return fmt.Errorf("gopdf: a PDF/A-%s document cannot be encrypted",
			d.pdfa)
	}
	// Every font must travel with the file. The standard fourteen do not,
	// which is the usual reason a document fails the profile.
	for _, f := range d.fonts {
		if f == nil || f.ttf == nil {
			name := "a standard font"
			if f != nil {
				name = f.Name()
			}
			return fmt.Errorf("gopdf: a PDF/A-%s document must embed every font, "+
				"and %s is not embedded — load a font file with LoadFont instead "+
				"of using one of the standard fourteen", d.pdfa, name)
		}
	}
	return nil
}

// buildPDFAExtras returns the output intent the profile requires, or nil
// when no profile was asked for.
//
// The intent says what the document's colours mean. A conforming file is
// supposed to carry an ICC profile for it, and shipping one would mean
// shipping a colour profile inside this package; instead the intent names
// sRGB without embedding it, which is what a great many real PDF/A files
// do and what every checker accepts short of the strictest.
func (d *Document) buildPDFAExtras() any {
	if d.pdfa == "" {
		return nil
	}
	ref := rawRef(len(d.raw))
	d.raw = append(d.raw, Dict{
		"Type":                      Name("OutputIntent"),
		"S":                         Name("GTS_PDFA1"),
		"OutputConditionIdentifier": String(textStringBytes("sRGB IEC61966-2.1")),
		"RegistryName":              String(textStringBytes("http://www.color.org")),
		"Info":                      String(textStringBytes("sRGB IEC61966-2.1")),
	})
	return ref
}

// pdfaXMP is the part of the metadata packet that identifies the
// conformance level, which is where a reader looks to learn what the file
// claims to be.
func pdfaXMP(level PDFAConformance) string {
	if level == "" {
		return ""
	}
	part, conf := "2", "B"
	if len(level) >= 2 {
		part, conf = string(level[0]), strings.ToUpper(string(level[1]))
	}
	return fmt.Sprintf(
		"   <pdfaid:part xmlns:pdfaid=\"http://www.aiim.org/pdfa/ns/id/\">%s"+
			"</pdfaid:part>\n"+
			"   <pdfaid:conformance xmlns:pdfaid=\"http://www.aiim.org/pdfa/ns/id/\">%s"+
			"</pdfaid:conformance>\n", part, conf)
}

// --- checking an existing document ---

// PDFAIssue is one reason a document does not meet the profile.
type PDFAIssue struct {
	// Rule names the requirement, briefly.
	Rule string
	// Detail says what in this document breaks it.
	Detail string
	// Page is where the trouble is, or -1 for the document as a whole.
	Page int
}

func (i PDFAIssue) String() string {
	if i.Page >= 0 {
		return fmt.Sprintf("page %d: %s (%s)", i.Page+1, i.Rule, i.Detail)
	}
	return fmt.Sprintf("%s (%s)", i.Rule, i.Detail)
}

// CheckPDFA reports what stops a document meeting the archival profile.
//
// An empty result means nothing this package can see is wrong, which is
// not the same as a certificate: the profile has requirements about
// colour management and about the internals of embedded font programs
// that a full validator checks and this does not. What it does catch is
// the things that actually go wrong — a font that is not embedded, an
// encrypted file, a reference to something outside the document.
func (r *Reader) CheckPDFA(level PDFAConformance) []PDFAIssue {
	var out []PDFAIssue
	add := func(page int, rule, detail string) {
		out = append(out, PDFAIssue{Rule: rule, Detail: detail, Page: page})
	}

	if r.IsEncrypted() {
		add(-1, "the file must not be encrypted", "it is")
	}
	// The identification lives in the metadata packet, and a file
	// claiming nothing is not claiming this.
	x := r.XMP()
	if len(x.Raw) == 0 {
		add(-1, "the file must carry an XMP metadata packet", "there is none")
	} else if !strings.Contains(string(x.Raw), "pdfaid") {
		add(-1, "the metadata must identify the conformance level",
			"the packet has no pdfaid part")
	}
	if _, ok := r.resolve(r.Catalog()["OutputIntents"]).(Array); !ok {
		add(-1, "the file must declare an output intent",
			"the catalog has none")
	}
	// An action that reaches outside the document is exactly what the
	// profile is for keeping out.
	if r.Catalog()["OpenAction"] != nil {
		if d, ok := r.resolve(r.Catalog()["OpenAction"]).(Dict); ok {
			if s := r.resolve(d["S"]); s == Name("Launch") || s == Name("JavaScript") ||
				s == Name("URI") {
				add(-1, "the file must not act on being opened", fmt.Sprint(s))
			}
		}
	}
	if names, ok := r.resolve(r.Catalog()["Names"]).(Dict); ok {
		if names["JavaScript"] != nil {
			add(-1, "the file must not carry scripts", "it has a JavaScript tree")
		}
	}

	// Every font on every page has to travel with the file.
	seen := map[string]bool{}
	for page := 0; page < len(r.pages); page++ {
		res, _ := r.resolve(r.InheritedPageValue(page, "Resources")).(Dict)
		fonts, _ := r.resolve(res["Font"]).(Dict)
		for name, v := range fonts {
			d, ok := r.resolve(v).(Dict)
			if !ok {
				continue
			}
			base := baseFontName(r, d)
			key := fmt.Sprint(page, name, base)
			if seen[key] {
				continue
			}
			seen[key] = true
			if !fontIsEmbedded(r, d) {
				add(page, "every font must be embedded",
					fmt.Sprintf("/%s is %s, which the file does not carry", name, base))
			}
			if level == PDFA2u && !fontHasToUnicode(r, d) {
				add(page, "every font must map its glyphs to characters",
					fmt.Sprintf("/%s has no ToUnicode map", name))
			}
		}
	}
	return out
}

// fontIsEmbedded reports whether a font dictionary carries its program.
func fontIsEmbedded(r *Reader, dict Dict) bool {
	target := dict
	if r.resolve(dict["Subtype"]) == Name("Type0") {
		desc, ok := r.resolve(dict["DescendantFonts"]).(Array)
		if !ok || len(desc) == 0 {
			return false
		}
		if d, ok := r.resolve(desc[0]).(Dict); ok {
			target = d
		}
	}
	if r.resolve(target["Subtype"]) == Name("Type3") {
		// A Type 3 font's glyphs are content streams in the file, so it
		// is embedded by construction.
		return true
	}
	fd, ok := r.resolve(target["FontDescriptor"]).(Dict)
	if !ok {
		return false
	}
	for _, key := range []Name{"FontFile", "FontFile2", "FontFile3"} {
		if _, ok := r.resolve(fd[key]).(*rawStream); ok {
			return true
		}
	}
	return false
}

// fontHasToUnicode reports whether a font says what its glyphs mean.
func fontHasToUnicode(r *Reader, dict Dict) bool {
	if _, ok := r.resolve(dict["ToUnicode"]).(*rawStream); ok {
		return true
	}
	// A simple font with a standard encoding says what its codes mean
	// through the encoding instead, which the profile accepts.
	switch r.resolve(dict["Encoding"]) {
	case Name("WinAnsiEncoding"), Name("MacRomanEncoding"), Name("StandardEncoding"):
		return true
	}
	return false
}
