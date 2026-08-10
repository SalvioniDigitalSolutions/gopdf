// Command redact removes text from a PDF and writes a fresh file with the
// content gone, rather than covered.
//
//	go run ./examples/redact -in case.pdf -list -text "Ada Lovelace"
//	go run ./examples/redact -in case.pdf -out clean.pdf \
//	    -text "Ada Lovelace" -pattern '\d{3}-\d{2}-\d{4}'
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"

	"github.com/SalvioniDigitalSolutions/gopdf"
)

type list []string

func (l *list) String() string     { return strings.Join(*l, ", ") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

func main() {
	var texts, patterns list
	in := flag.String("in", "", "input PDF")
	out := flag.String("out", "", "output PDF")
	dryRun := flag.Bool("list", false, "list what would be removed and stop")
	keepMeta := flag.Bool("keep-metadata", false, "keep the document metadata")
	noBox := flag.Bool("no-box", false, "do not paint a black box over each redaction")
	flag.Var(&texts, "text", "literal text to remove (repeatable)")
	flag.Var(&patterns, "pattern", "regular expression to remove (repeatable)")
	flag.Parse()

	if *in == "" || (len(texts) == 0 && len(patterns) == 0) {
		flag.Usage()
		os.Exit(2)
	}
	if *out == "" && !*dryRun {
		log.Fatal("-out is required unless -list is given")
	}

	r, err := gopdf.Open(*in)
	if err != nil {
		log.Fatal(err)
	}
	if r.Repaired() {
		fmt.Fprintln(os.Stderr, "note: this file was damaged and had to be repaired")
	}

	rd := gopdf.Redact(r)
	for _, t := range texts {
		rd.Text(t)
	}
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			log.Fatalf("bad pattern %q: %v", p, err)
		}
		rd.Pattern(re)
	}
	rd.StripMetadata(!*keepMeta)
	rd.SetOverlay(!*noBox)

	marks, err := rd.Marks()
	if err != nil {
		log.Fatal(err)
	}
	for _, m := range marks {
		fmt.Printf("%-10s page %d  %6.1f,%-6.1f %5.1f×%-5.1f  %q\n",
			m.Kind, m.Page+1, m.X, m.Y, m.W, m.H, m.Text)
	}
	if n, _ := rd.PartialArtwork(); n > 0 {
		fmt.Fprintf(os.Stderr,
			"note: %d piece(s) of artwork straddle a redacted area and were covered, not removed\n", n)
	}
	if len(marks) == 0 {
		fmt.Fprintln(os.Stderr, "nothing matched")
	}
	if *dryRun {
		return
	}
	if err := rd.Save(*out); err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(os.Stderr, "removed %d item(s), wrote %s\n", len(marks), *out)
}
