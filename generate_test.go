package gopdf

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
)

// Generated documents, instead of a shelf of real ones.
//
// A corpus of real files is worth having and this package is swept
// against one, but it is a fixed set: it covers what those files happen
// to do, it cannot be checked in, and the interesting combinations in it
// are the ones somebody already thought of. A generator covers the
// combinations nobody thought of, produces as many as there is patience
// for, and a failure comes back with a seed that reproduces it exactly.
//
// What is asserted here are the properties that must hold for any
// document at all: it parses, its text comes back, writing it again
// changes nothing, it draws, and what redaction says it removed is gone.

// genPlan records what a generated document was told to contain, so the
// properties have something to check the result against.
type genPlan struct {
	seed     int64
	pages    int
	words    []string // every word written as page text, in order
	compress bool
	objStm   bool
	rotate   bool
}

func (p genPlan) String() string {
	return fmt.Sprintf("seed %d: %d pages, compress=%v objstm=%v rotate=%v",
		p.seed, p.pages, p.compress, p.objStm, p.rotate)
}

// genWords are ordinary words with nothing special about them, so a
// failure is about the document and not about the text.
var genWords = strings.Fields(`alpha bravo charlie delta echo foxtrot golf
hotel india juliet kilo lima mike november oscar papa quebec romeo sierra
tango uniform victor whiskey xray yankee zulu`)

// generate builds a document from a seed, exercising a different mixture
// of the writer's features each time, and reports what it wrote.
func generate(seed int64) (*Document, genPlan) {
	rng := rand.New(rand.NewSource(seed))
	plan := genPlan{seed: seed}

	doc := New()
	plan.compress = rng.Intn(2) == 0
	doc.Compress = plan.compress
	plan.objStm = rng.Intn(3) == 0
	doc.CompressObjects = plan.objStm
	if rng.Intn(4) == 0 {
		doc.SetInfo(Info{
			Title: "Generated " + strconv.FormatInt(seed, 10), Author: "seed",
		})
	}

	plan.pages = 1 + rng.Intn(3)
	for i := 0; i < plan.pages; i++ {
		size := []PageSize{A4, Letter, A5, Legal}[rng.Intn(4)]
		page := doc.AddPageSize(size)
		if rng.Intn(6) == 0 {
			page.SetRotate([]int{90, 180, 270}[rng.Intn(3)])
			plan.rotate = true
		}
		genContent(rng, page, &plan)
	}
	if rng.Intn(5) == 0 {
		doc.AddOutline(nil, "Contents", nil, 0)
	}
	return doc, plan
}

// genContent draws a page: some text, and a mixture of everything else
// the writer can put down.
func genContent(rng *rand.Rand, page *Page, plan *genPlan) {
	// Vector work first, so text is never hidden underneath it.
	for n := rng.Intn(4); n > 0; n-- {
		x, y := 40+rng.Float64()*300, 300+rng.Float64()*300
		page.SetFillColor(RGB(uint8(rng.Intn(256)), uint8(rng.Intn(256)),
			uint8(rng.Intn(256))))
		page.SetStrokeColor(Gray(uint8(rng.Intn(256))))
		page.SetLineWidth(0.5 + rng.Float64()*3)
		switch rng.Intn(6) {
		case 0:
			page.Rect(x, y, 40+rng.Float64()*80, 20+rng.Float64()*60, Fill)
		case 1:
			page.RoundedRect(x, y, 60, 40, 6, FillStroke)
		case 2:
			page.Circle(x, y, 10+rng.Float64()*30, Stroke)
		case 3:
			page.Ellipse(x, y, 40, 20, Fill)
		case 4:
			page.SetDash(3, 2)
			page.Line(x, y, x+80, y+40)
			page.SetDash()
		case 5:
			page.Polygon(Fill, x, y, x+50, y+10, x+30, y+60)
		}
	}
	if rng.Intn(3) == 0 {
		page.SetAlpha(0.4+rng.Float64()*0.5, 1)
	}
	if rng.Intn(4) == 0 {
		page.Push()
		page.Translate(rng.Float64()*20, rng.Float64()*20)
		page.RotateAt(float64(rng.Intn(20)-10), 300, 400)
		page.Rect(200, 500, 60, 30, Stroke)
		page.Pop()
	}

	// Text, in black and at full opacity, so extraction has something
	// unambiguous to find.
	page.SetAlpha(1, 1)
	page.SetFillColor(Black)
	font := []*Font{Helvetica, TimesRoman, Courier, HelveticaBold}[rng.Intn(4)]
	page.SetFont(font, 9+float64(rng.Intn(6)))
	y := 60.0
	for lines := 1 + rng.Intn(6); lines > 0; lines-- {
		var line []string
		for w := 1 + rng.Intn(5); w > 0; w-- {
			line = append(line, genWords[rng.Intn(len(genWords))])
		}
		// Words are made unique per line so a check can tell one
		// occurrence from another.
		line = append(line, fmt.Sprintf("tag%d", len(plan.words)))
		text := strings.Join(line, " ")
		page.Text(50, y, text)
		plan.words = append(plan.words, line...)
		y += 18
	}
}

// genSeeds is how many documents the property tests build. It is modest
// by default so the suite stays quick, and GOPDF_GEN raises it for a
// deliberate soak.
func genSeeds(t *testing.T) int {
	if s := os.Getenv("GOPDF_GEN"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			t.Fatalf("GOPDF_GEN=%q is not a count", s)
		}
		return n
	}
	if testing.Short() {
		return 40
	}
	return 300
}

// TestGeneratedDocumentsParse: whatever the writer puts out, the reader
// takes back, with a cross-reference table that leads to every object.
func TestGeneratedDocumentsParse(t *testing.T) {
	for i := 0; i < genSeeds(t); i++ {
		seed := int64(i)
		doc, plan := generate(seed)
		var buf bytes.Buffer
		if _, err := doc.WriteTo(&buf); err != nil {
			t.Fatalf("%v: writing: %v", plan, err)
		}
		r, err := NewReader(buf.Bytes())
		if err != nil {
			t.Fatalf("%v: reading back: %v", plan, err)
		}
		if r.NumPages() != plan.pages {
			t.Fatalf("%v: came back with %d pages", plan, r.NumPages())
		}
		verifyXref(t, buf.Bytes())
	}
}

// TestGeneratedTextSurvives: every word written is a word extracted.
func TestGeneratedTextSurvives(t *testing.T) {
	for i := 0; i < genSeeds(t); i++ {
		seed := int64(i)
		doc, plan := generate(seed)
		var buf bytes.Buffer
		if _, err := doc.WriteTo(&buf); err != nil {
			t.Fatalf("%v: %v", plan, err)
		}
		r, err := NewReader(buf.Bytes())
		if err != nil {
			t.Fatalf("%v: %v", plan, err)
		}
		var all strings.Builder
		for p := 0; p < r.NumPages(); p++ {
			text, err := r.PageText(p)
			if err != nil {
				t.Fatalf("%v: page %d: %v", plan, p, err)
			}
			all.WriteString(text)
			all.WriteString(" ")
		}
		got := strings.Join(strings.Fields(all.String()), " ")
		for _, w := range plan.words {
			if !strings.Contains(got, w) {
				t.Fatalf("%v: %q was written and does not extract\n  got %q",
					plan, w, got)
			}
		}
	}
}

// TestGeneratedRoundTripIsStable: reading a document and writing it
// again leaves its text alone. A rewrite that loses something loses it
// on the second pass as surely as the first, so comparing the two is a
// check on the whole pipeline.
func TestGeneratedRoundTripIsStable(t *testing.T) {
	n := genSeeds(t) / 2
	for i := 0; i < n; i++ {
		seed := int64(i)
		doc, plan := generate(seed)
		var first bytes.Buffer
		if _, err := doc.WriteTo(&first); err != nil {
			t.Fatalf("%v: %v", plan, err)
		}
		r, err := NewReader(first.Bytes())
		if err != nil {
			t.Fatalf("%v: %v", plan, err)
		}
		before := allText(t, r)

		var second bytes.Buffer
		if _, err := Update(r).WriteTo(&second); err != nil {
			t.Fatalf("%v: rewriting: %v", plan, err)
		}
		r2, err := NewReader(second.Bytes())
		if err != nil {
			t.Fatalf("%v: reading the rewrite: %v", plan, err)
		}
		if after := allText(t, r2); after != before {
			t.Fatalf("%v: the rewrite changed the text\n before %q\n after  %q",
				plan, before, after)
		}
	}
}

// TestGeneratedRedactionRemoves: a word redaction says it removed is
// gone from the text and from the bytes.
func TestGeneratedRedactionRemoves(t *testing.T) {
	n := genSeeds(t) / 3
	for i := 0; i < n; i++ {
		seed := int64(i)
		doc, plan := generate(seed)
		if len(plan.words) == 0 {
			continue
		}
		// A tag is unique to its line, so removing it is unambiguous.
		var target string
		for _, w := range plan.words {
			if strings.HasPrefix(w, "tag") {
				target = w
				break
			}
		}
		if target == "" {
			continue
		}
		var src bytes.Buffer
		if _, err := doc.WriteTo(&src); err != nil {
			t.Fatalf("%v: %v", plan, err)
		}
		r, err := NewReader(src.Bytes())
		if err != nil {
			t.Fatalf("%v: %v", plan, err)
		}
		rd := Redact(r)
		rd.Text(target)
		var out bytes.Buffer
		if _, err := rd.WriteTo(&out); err != nil {
			t.Fatalf("%v: redacting %q: %v", plan, target, err)
		}
		r2, err := NewReader(out.Bytes())
		if err != nil {
			t.Fatalf("%v: reading the redaction: %v", plan, err)
		}
		if got := allText(t, r2); strings.Contains(got, target) {
			t.Fatalf("%v: %q is still readable after redaction", plan, target)
		}
		if bytes.Contains(out.Bytes(), []byte(target)) {
			t.Fatalf("%v: %q is still in the bytes after redaction", plan, target)
		}
	}
}

// TestGeneratedPagesRender: every generated page draws, without a panic
// and without reporting a glyph it could not find — the fonts here are
// the standard fourteen, which a substitute covers exactly.
func TestGeneratedPagesRender(t *testing.T) {
	n := genSeeds(t) / 4
	sub := SystemFonts()
	for i := 0; i < n; i++ {
		seed := int64(i)
		doc, plan := generate(seed)
		var buf bytes.Buffer
		if _, err := doc.WriteTo(&buf); err != nil {
			t.Fatalf("%v: %v", plan, err)
		}
		r, err := NewReader(buf.Bytes())
		if err != nil {
			t.Fatalf("%v: %v", plan, err)
		}
		for p := 0; p < r.NumPages(); p++ {
			img, rep, err := r.RenderPageDetail(p, RenderOpts{
				DPI: 50, IncludeVector: true, IncludeText: true,
				SubstituteFont: sub,
			})
			if err != nil {
				t.Fatalf("%v: rendering page %d: %v", plan, p, err)
			}
			if img.Bounds().Empty() {
				t.Fatalf("%v: page %d rendered to nothing", plan, p)
			}
			if rep.Glyphs == 0 && len(plan.words) > 0 {
				t.Fatalf("%v: page %d drew no glyphs at all", plan, p)
			}
		}
	}
}

// allText joins every page of a document.
func allText(t *testing.T, r *Reader) string {
	t.Helper()
	var b strings.Builder
	for p := 0; p < r.NumPages(); p++ {
		text, err := r.PageText(p)
		if err != nil {
			t.Fatalf("page %d: %v", p, err)
		}
		b.WriteString(text)
		b.WriteString(" ")
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
