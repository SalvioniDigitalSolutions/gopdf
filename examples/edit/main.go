// Command edit rewrites text inside an existing PDF without disturbing
// its layout.
//
//	edit -in report.pdf -list
//	edit -in report.pdf -out revised.pdf -replace "DRAFT=FINAL" -replace "2024=2025"
//
// Each -replace is an old=new pair. The replacement is drawn with the very
// font the original text used, so it renders identically; if that font has
// no glyph for a character, the edit is refused rather than mangled.
package main

import (
	"flag"
	"fmt"
	"log"
	"strings"

	"github.com/SalvioniDigitalSolutions/gopdf"
)

type replacements []string

func (r *replacements) String() string { return strings.Join(*r, ",") }
func (r *replacements) Set(v string) error {
	if !strings.Contains(v, "=") {
		return fmt.Errorf("expected old=new, got %q", v)
	}
	*r = append(*r, v)
	return nil
}

func main() {
	var (
		in     = flag.String("in", "", "input PDF (required)")
		out    = flag.String("out", "", "output PDF")
		list   = flag.Bool("list", false, "list the text runs and exit")
		blocks = flag.Bool("blocks", false, "list the paragraphs and exit")
		fit    = flag.String("fit", "advance", "width handling: advance, scale or none")
		reflow = flag.Bool("reflow", false, "re-wrap the whole paragraph instead of the single line")
		grow   = flag.Int("grow", 0, "with -reflow, allow the paragraph to gain up to N lines")
		pass   = flag.String("password", "", "password for an encrypted input")
		pairs  replacements
	)
	flag.Var(&pairs, "replace", "old=new text replacement (repeatable)")
	flag.Parse()
	inspect := *list || *blocks
	if *in == "" || (!inspect && (*out == "" || len(pairs) == 0)) {
		flag.Usage()
		log.Fatal("need -in with either -list/-blocks, or -out and at least one -replace")
	}

	src, err := gopdf.OpenPassword(*in, *pass)
	if err != nil {
		log.Fatal(err)
	}
	doc := gopdf.New()

	mode := map[string]gopdf.FitMode{
		"advance": gopdf.FitAdvance,
		"scale":   gopdf.FitScale,
		"none":    gopdf.FitNone,
	}[*fit]

	total := 0
	for i := 0; i < src.NumPages(); i++ {
		page, err := doc.EditPage(src, i)
		if err != nil {
			log.Fatal(err)
		}
		page.SetFitMode(mode)
		page.SetMaxExtraLines(*grow)

		if *list {
			for _, run := range page.Runs() {
				fmt.Printf("p%-3d x=%-7.1f y=%-7.1f %-5.1fpt %-12s %q\n",
					i+1, run.X, run.Y, run.FontSize, run.FontName, run.Text)
			}
			continue
		}
		if *blocks {
			for _, b := range page.Blocks() {
				fmt.Printf("p%-3d x=%-7.1f y=%-7.1f w=%-6.1f %d line(s) %q\n",
					i+1, b.X, b.Y, b.Width, len(b.Lines()), b.Text)
			}
			continue
		}
		for _, pair := range pairs {
			old, new, _ := strings.Cut(pair, "=")
			var n int
			if *reflow {
				n, err = page.ReplaceTextReflow(old, new)
			} else {
				n, err = page.ReplaceText(old, new)
			}
			if err != nil {
				log.Fatalf("page %d: %v", i+1, err)
			}
			total += n
		}
	}
	if inspect {
		return
	}
	if err := doc.Save(*out); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("replaced %d run(s) across %d page(s) -> %s\n", total, src.NumPages(), *out)
}
