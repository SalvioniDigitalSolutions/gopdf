// Command stamp demonstrates PDF manipulation: it reads an existing PDF,
// overlays a diagonal watermark on every page, and writes the result.
//
// Usage: stamp [input.pdf [output.pdf [text]]]
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/SalvioniDigitalSolutions/gopdf"
)

func main() {
	in, out, text := "../demo/demo.pdf", "stamped.pdf", "APPROVED"
	if len(os.Args) > 1 {
		in = os.Args[1]
	}
	if len(os.Args) > 2 {
		out = os.Args[2]
	}
	if len(os.Args) > 3 {
		text = os.Args[3]
	}

	r, err := gopdf.Open(in)
	if err != nil {
		log.Fatal(err)
	}
	doc := gopdf.New()
	for i := 0; i < r.NumPages(); i++ {
		page, err := doc.ImportPage(r, i)
		if err != nil {
			log.Fatal(err)
		}
		page.Push()
		page.SetAlpha(0.35, 0.35)
		page.RotateAt(45, page.Width()/2, page.Height()/2)
		page.SetFont(gopdf.HelveticaBold, 72)
		page.SetFillColor(gopdf.RGB(200, 30, 30))
		w := page.TextWidth(text)
		page.Text(page.Width()/2-w/2, page.Height()/2, text)
		page.Pop()
	}
	if err := doc.Save(out); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("stamped %d page(s): %s -> %s\n", r.NumPages(), in, out)
}
