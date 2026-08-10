// Package tesseract reads the text in an image by running the tesseract
// command, for use with gopdf's redaction.
//
// It is a separate package so the library itself stays free of anything
// outside the standard library: nothing here is imported unless you ask
// for it, and what it needs is a binary on the machine rather than a Go
// dependency.
//
//	engine, err := tesseract.New(tesseract.Options{Languages: []string{"eng"}})
//	if err != nil {
//	    log.Fatal(err) // tesseract is not installed
//	}
//	rd := gopdf.Redact(r)
//	rd.SetOCR(engine)
//	rd.Text("Ada Lovelace")
//
// Install it with your package manager: `brew install tesseract`,
// `apt install tesseract-ocr`. Extra languages come as separate packages.
package tesseract

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SalvioniDigitalSolutions/gopdf"
)

// Options configures the engine.
type Options struct {
	// Binary is the command to run. It defaults to "tesseract", found on
	// PATH.
	Binary string
	// Languages are the trained models to use, most likely first, for
	// example {"eng"} or {"deu", "fra"}. It defaults to {"eng"}.
	Languages []string
	// PageSegMode is tesseract's --psm. The default, 3, suits a page of
	// mixed text; 6 treats the image as one block, 11 as scattered text.
	PageSegMode int
	// Timeout bounds a single call. It defaults to two minutes.
	Timeout time.Duration
}

// Engine reads text by running tesseract.
type Engine struct {
	opts Options
}

// New checks that tesseract can be run and returns an engine.
//
// The check happens here rather than at the first image, so a missing
// binary is reported while the caller is still deciding what to do
// instead — not half way through redacting a document.
func New(opts Options) (*Engine, error) {
	if opts.Binary == "" {
		opts.Binary = "tesseract"
	}
	if len(opts.Languages) == 0 {
		opts.Languages = []string{"eng"}
	}
	if opts.PageSegMode == 0 {
		opts.PageSegMode = 3
	}
	if opts.Timeout == 0 {
		opts.Timeout = 2 * time.Minute
	}
	path, err := exec.LookPath(opts.Binary)
	if err != nil {
		return nil, fmt.Errorf("tesseract: %q is not on PATH; install it with "+
			"your package manager (brew install tesseract, apt install "+
			"tesseract-ocr)", opts.Binary)
	}
	opts.Binary = path
	return &Engine{opts: opts}, nil
}

// Available reports whether tesseract can be run, for a caller that would
// rather degrade than fail.
func Available() bool {
	_, err := exec.LookPath("tesseract")
	return err == nil
}

// Recognize reads the words in an image.
func (e *Engine) Recognize(img image.Image) ([]gopdf.OCRWord, error) {
	if img == nil {
		return nil, nil
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return nil, nil
	}

	dir, err := os.MkdirTemp("", "gopdf-ocr-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	in := filepath.Join(dir, "page.png")
	f, err := os.Create(in)
	if err != nil {
		return nil, err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), e.opts.Timeout)
	defer cancel()

	// "-" sends the TSV to standard output, which carries a box per word.
	args := []string{in, "-",
		"-l", strings.Join(e.opts.Languages, "+"),
		"--psm", strconv.Itoa(e.opts.PageSegMode),
		"tsv"}
	cmd := exec.CommandContext(ctx, e.opts.Binary, args...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("tesseract: timed out after %s", e.opts.Timeout)
		}
		return nil, fmt.Errorf("tesseract: %w: %s", err,
			strings.TrimSpace(errBuf.String()))
	}
	return parseTSV(&out)
}

// parseTSV reads tesseract's tab-separated word list.
//
// The columns are fixed by tesseract: level, page, block, paragraph,
// line, word, left, top, width, height, confidence, text. Only the last
// six matter here, and rows that are not words carry no text.
func parseTSV(r io.Reader) ([]gopdf.OCRWord, error) {
	cr := csv.NewReader(r)
	cr.Comma = '\t'
	cr.LazyQuotes = true
	cr.FieldsPerRecord = -1

	var out []gopdf.OCRWord
	first := true
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, nil // a malformed tail costs the rest, not the lot
		}
		if first {
			first = false
			if len(rec) > 0 && rec[0] == "level" {
				continue // the header
			}
		}
		if len(rec) < 12 {
			continue
		}
		text := strings.TrimSpace(rec[11])
		if text == "" {
			continue
		}
		left, e1 := strconv.Atoi(rec[6])
		top, e2 := strconv.Atoi(rec[7])
		width, e3 := strconv.Atoi(rec[8])
		height, e4 := strconv.Atoi(rec[9])
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
			continue
		}
		conf, err := strconv.ParseFloat(rec[10], 64)
		if err != nil || conf < 0 {
			conf = 0
		}
		out = append(out, gopdf.OCRWord{
			Text: text, X: left, Y: top, W: width, H: height,
			Confidence: conf / 100,
		})
	}
}
