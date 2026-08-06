package gopdf_test

import (
	"errors"
	"fmt"
	"log"

	"github.com/SalvioniDigitalSolutions/gopdf"
)

// A minimal one-page document.
func Example() {
	doc := gopdf.New()
	page := doc.AddPage()
	page.SetFont(gopdf.Helvetica, 14)
	page.Text(72, 72, "Hello, PDF!")
	if err := doc.Save("hello.pdf"); err != nil {
		log.Fatal(err)
	}
}

// Embedding a TrueType font for full Unicode text.
func ExampleLoadFont() {
	font, err := gopdf.LoadFont("NotoSans-Regular.ttf")
	if err != nil {
		log.Fatal(err)
	}
	doc := gopdf.New()
	page := doc.AddPage()
	page.SetFont(font, 12)
	page.Text(72, 72, "Καλημέρα κόσμε — Привет, мир")
	if err := doc.Save("unicode.pdf"); err != nil {
		log.Fatal(err)
	}
}

// Word-wrapping a paragraph inside a column.
func ExamplePage_textWrapped() {
	doc := gopdf.New()
	page := doc.AddPage()
	page.SetFont(gopdf.TimesRoman, 11)
	next := page.TextWrapped(72, 72, 300, 15,
		"TextWrapped lays out a paragraph inside a fixed width and "+
			"returns the baseline for whatever comes after it.")
	page.Text(72, next, "…like this line.")
	if err := doc.Save("paragraph.pdf"); err != nil {
		log.Fatal(err)
	}
}

// Vector graphics with scoped transparency.
func ExamplePage_setAlpha() {
	doc := gopdf.New()
	page := doc.AddPage()
	page.Push()
	page.SetAlpha(0.5, 1)
	page.SetFillColor(gopdf.RGB(200, 40, 40))
	page.Circle(150, 150, 60, gopdf.Fill)
	page.SetFillColor(gopdf.RGB(40, 40, 200))
	page.Circle(200, 150, 60, gopdf.Fill)
	page.Pop()
	if err := doc.Save("circles.pdf"); err != nil {
		log.Fatal(err)
	}
}

// Merging files and stamping a watermark onto every page.
func ExampleDocument_importPage() {
	src, err := gopdf.Open("report.pdf")
	if err != nil {
		log.Fatal(err)
	}
	doc := gopdf.New()
	for i := 0; i < src.NumPages(); i++ {
		page, err := doc.ImportPage(src, i)
		if err != nil {
			log.Fatal(err)
		}
		page.Push()
		page.SetAlpha(0.3, 0.3)
		page.RotateAt(45, page.Width()/2, page.Height()/2)
		page.SetFont(gopdf.HelveticaBold, 64)
		page.SetFillColor(gopdf.RGB(200, 30, 30))
		page.TextAligned(0, page.Height()/2, page.Width(), gopdf.AlignCenter, "DRAFT")
		page.Pop()
	}
	if err := doc.Save("watermarked.pdf"); err != nil {
		log.Fatal(err)
	}
}

// Editing text in an existing document without moving anything else.
func ExampleDocument_editPage() {
	src, err := gopdf.Open("invoice.pdf")
	if err != nil {
		log.Fatal(err)
	}
	doc := gopdf.New()
	for i := 0; i < src.NumPages(); i++ {
		page, err := doc.EditPage(src, i)
		if err != nil {
			log.Fatal(err)
		}
		// Replacements are drawn with the page's own font, and the width
		// difference is compensated so the rest of the line stays put.
		if _, err := page.ReplaceText("DRAFT", "FINAL"); err != nil {
			log.Fatal(err)
		}
	}
	if err := doc.Save("final.pdf"); err != nil {
		log.Fatal(err)
	}
}

// Re-wrapping a paragraph after changing its wording.
func ExampleEditablePage_ReplaceTextReflow() {
	src, err := gopdf.Open("terms.pdf")
	if err != nil {
		log.Fatal(err)
	}
	doc := gopdf.New()
	page, err := doc.EditPage(src, 0)
	if err != nil {
		log.Fatal(err)
	}
	// The paragraph re-wraps across the lines it already occupies; if the
	// new text needs more, the edit is refused rather than overrunning
	// what follows.
	if _, err := page.ReplaceTextReflow("internal use only", "any lawful purpose"); err != nil {
		log.Fatal(err)
	}
	if err := doc.Save("terms-revised.pdf"); err != nil {
		log.Fatal(err)
	}
}

// Inspecting a page's text runs before editing them.
func ExampleEditablePage_Runs() {
	src, err := gopdf.Open("statement.pdf")
	if err != nil {
		log.Fatal(err)
	}
	page, err := gopdf.New().EditPage(src, 0)
	if err != nil {
		log.Fatal(err)
	}
	for _, run := range page.Runs() {
		fmt.Printf("%.1f,%.1f %.1fpt %q\n", run.X, run.Y, run.FontSize, run.Text)
	}
}

// Filling an interactive form and flattening the result.
func ExampleDocument_FillForm() {
	src, err := gopdf.Open("application.pdf")
	if err != nil {
		log.Fatal(err)
	}
	for _, f := range src.FormFields() {
		fmt.Printf("%s (%s) = %q\n", f.Name, f.Type, f.Value)
	}
	doc := gopdf.New()
	if _, err := doc.FillForm(src, map[string]string{
		"applicant": "Ada Lovelace",
		"country":   "France",
		"subscribe": "Yes",
	}); err != nil {
		log.Fatal(err)
	}
	if err := doc.Save("application-filled.pdf"); err != nil {
		log.Fatal(err)
	}
}

// Filling a form while leaving it editable.
func ExampleDocument_FillFormInteractive() {
	src, err := gopdf.Open("application.pdf")
	if err != nil {
		log.Fatal(err)
	}
	doc := gopdf.New()
	// The output keeps its interactive fields, with fresh appearance
	// streams so the values show before a viewer regenerates them.
	if _, err := doc.FillFormInteractive(src, map[string]string{
		"applicant": "Grace Hopper",
	}); err != nil {
		log.Fatal(err)
	}
	if err := doc.Save("application-draft.pdf"); err != nil {
		log.Fatal(err)
	}
}

// Reading a password-protected file.
func ExampleOpenPassword() {
	r, err := gopdf.OpenPassword("protected.pdf", "secret")
	if errors.Is(err, gopdf.ErrPasswordRequired) {
		log.Fatal("wrong password")
	} else if err != nil {
		log.Fatal(err)
	}
	text, err := r.PageText(0)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(text)
}

// Writing an encrypted document.
func ExampleDocument_encrypt() {
	doc := gopdf.New()
	doc.Encrypt("userpw", "ownerpw", gopdf.AllowPrint|gopdf.AllowCopy, gopdf.AES256)
	page := doc.AddPage()
	page.SetFont(gopdf.Helvetica, 12)
	page.Text(72, 72, "Confidential")
	if err := doc.Save("protected.pdf"); err != nil {
		log.Fatal(err)
	}
}

// Bookmarks and an internal link.
func ExampleDocument_addOutline() {
	doc := gopdf.New()
	intro := doc.AddPage()
	details := doc.AddPage()

	intro.SetFont(gopdf.Helvetica, 12)
	intro.Text(72, 72, "See details")
	intro.LinkPage(72, 60, 80, 16, details, 0)

	chapter := doc.AddOutline(nil, "Introduction", intro, 0)
	doc.AddOutline(chapter, "Details", details, 0)
	if err := doc.Save("outline.pdf"); err != nil {
		log.Fatal(err)
	}
}
