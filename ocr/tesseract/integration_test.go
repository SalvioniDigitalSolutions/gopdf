package tesseract_test

import (
	"bytes"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SalvioniDigitalSolutions/gopdf"
	"github.com/SalvioniDigitalSolutions/gopdf/ocr/tesseract"
)

// TestScannedDocumentEndToEnd builds a real scan — a page rendered to
// pixels, with no text objects left — then redacts a word out of it by
// recognition alone and checks the word cannot be read again.
//
// It needs tesseract to recognise and pdftoppm to rasterise, and skips
// without them.
func TestScannedDocumentEndToEnd(t *testing.T) {
	if !tesseract.Available() {
		t.Skip("tesseract is not installed")
	}
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm is not installed")
	}
	dir := t.TempDir()

	// A page with text on it, written normally.
	doc := gopdf.New()
	page := doc.AddPage()
	page.SetFont(gopdf.HelveticaBold, 20)
	page.Text(60, 80, "WITNESS STATEMENT")
	page.SetFont(gopdf.Helvetica, 15)
	page.Text(60, 130, "Name: Ada Lovelace")
	page.Text(60, 160, "This paragraph stays as it is.")
	src := filepath.Join(dir, "original.pdf")
	if err := doc.Save(src); err != nil {
		t.Fatal(err)
	}

	// Rasterise it, so the text becomes pixels.
	out := exec.Command("pdftoppm", "-png", "-r", "150", src, filepath.Join(dir, "scan"))
	if err := out.Run(); err != nil {
		t.Skipf("could not rasterise: %v", err)
	}
	f, err := os.Open(filepath.Join(dir, "scan-1.png"))
	if err != nil {
		t.Skipf("no rasterised page: %v", err)
	}
	pixels, err := png.Decode(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}

	// Rebuild it as a scan: one image, no text.
	scanDoc := gopdf.New()
	scanPage := scanDoc.AddPage()
	img, err := scanDoc.AddImage(pixels)
	if err != nil {
		t.Fatal(err)
	}
	scanPage.DrawImage(img, 0, 0, scanPage.Width(), scanPage.Height())
	var scan bytes.Buffer
	if _, err := scanDoc.WriteTo(&scan); err != nil {
		t.Fatal(err)
	}

	r, err := gopdf.NewReader(scan.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if text, _ := r.PageText(0); strings.TrimSpace(text) != "" {
		t.Fatalf("the scan should have no extractable text, got %q", text)
	}

	engine, err := tesseract.New(tesseract.Options{Languages: []string{"eng"}})
	if err != nil {
		t.Fatal(err)
	}
	rd := gopdf.Redact(r)
	rd.SetOCR(engine)
	rd.Substitute("Lovelace", "[[PII_NAME_1]]")

	marks, err := rd.Marks()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, m := range marks {
		if m.Kind == gopdf.RedactImageText && strings.Contains(m.Text, "Lovelace") {
			found = true
			if m.W <= 0 || m.H <= 0 {
				t.Errorf("the mark has no extent: %+v", m)
			}
		}
	}
	if !found {
		t.Fatalf("recognition did not find the word; marks are %+v", marks)
	}

	dst := filepath.Join(dir, "redacted.pdf")
	if err := rd.Save(dst); err != nil {
		// A failure here is the library refusing to hand over a document
		// whose word it could still read — which is the point.
		t.Fatalf("saving the redacted scan: %v", err)
	}

	// Prove it independently: rasterise the result and read it again.
	if err := exec.Command("pdftoppm", "-png", "-r", "150", dst,
		filepath.Join(dir, "check")).Run(); err != nil {
		t.Skipf("could not rasterise the result: %v", err)
	}
	got, err := exec.Command("tesseract", filepath.Join(dir, "check-1.png"), "-").Output()
	if err != nil {
		t.Skipf("could not read the result back: %v", err)
	}
	if strings.Contains(string(got), "Lovelace") {
		t.Errorf("the word can still be read in the redacted scan:\n%s", got)
	}
	// The rest of the page must survive.
	if !strings.Contains(string(got), "WITNESS") {
		t.Errorf("the rest of the page was lost:\n%s", got)
	}
}
