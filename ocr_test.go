package gopdf

import (
	"bytes"
	"image"
	"image/color"
	"regexp"
	"strings"
	"testing"
)

// stubOCR stands in for a real engine, so the plumbing can be tested
// without one installed.
type stubOCR struct {
	words []OCRWord
	calls int
	// gone, once set, is returned instead after the first call, standing
	// for words the scrubbing removed.
	gone bool
}

func (s *stubOCR) Recognize(image.Image) ([]OCRWord, error) {
	s.calls++
	if s.gone && s.calls > 1 {
		return nil, nil
	}
	return s.words, nil
}

// scanDoc embeds a plain image as a whole page, standing for a scan: a
// document with no text objects at all.
func scanDoc(t *testing.T, w, h int) []byte {
	t.Helper()
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.Set(x, y, color.RGBA{R: 240, G: 240, B: 240, A: 255})
		}
	}
	doc := New()
	page := doc.AddPage()
	img, err := doc.AddImage(m)
	if err != nil {
		t.Fatal(err)
	}
	page.DrawImage(img, 0, 0, page.Width(), page.Height())
	return docBytes(t, doc)
}

func TestOCRScrubsMatchedWords(t *testing.T) {
	src := scanDoc(t, 200, 100)
	if got := extractAll(t, src); strings.TrimSpace(got) != "" {
		t.Fatalf("the fixture should have no extractable text, got %q", got)
	}
	r, _ := NewReader(src)
	engine := &stubOCR{gone: true, words: []OCRWord{
		{Text: "Ada", X: 10, Y: 10, W: 30, H: 12, Confidence: 0.9},
		{Text: "Lovelace", X: 45, Y: 10, W: 60, H: 12, Confidence: 0.9},
		{Text: "unrelated", X: 10, Y: 60, W: 70, H: 12, Confidence: 0.9},
	}}
	rd := Redact(r)
	rd.SetOCR(engine)
	rd.Text("Lovelace")

	marks, err := rd.Marks()
	if err != nil {
		t.Fatal(err)
	}
	if len(marks) != 1 {
		t.Fatalf("got %d marks, want 1: %+v", len(marks), marks)
	}
	if marks[0].Kind != RedactImageText {
		t.Errorf("kind = %q, want %q", marks[0].Kind, RedactImageText)
	}
	if marks[0].Text != "Lovelace" {
		t.Errorf("mark text = %q", marks[0].Text)
	}
	// The box must land where the word is: 45/200 across a full-width
	// page image.
	page, _ := r.PageSize(0)
	if wantX := 45.0 / 200 * page.W; marks[0].X < wantX-2 || marks[0].X > wantX+2 {
		t.Errorf("mark x = %v, want about %v", marks[0].X, wantX)
	}

	out := redactTo(t, rd)
	r2, _ := NewReader(out)
	imgs, err := r2.PageImages(0)
	if err != nil || len(imgs) == 0 {
		t.Fatalf("the image is gone: %v", err)
	}
	got, err := imgs[0].Decode()
	if err != nil {
		t.Fatal(err)
	}
	b := got.Bounds()
	// Inside the word's box the pixels must be the fill colour.
	px := b.Min.X + int(float64(b.Dx())*60/200)
	py := b.Min.Y + int(float64(b.Dy())*14/100)
	if rr, _, _, _ := got.At(px, py).RGBA(); rr>>8 > 20 {
		t.Errorf("the matched word was not scrubbed (red=%d)", rr>>8)
	}
	// Outside it the page is untouched.
	ux := b.Min.X + int(float64(b.Dx())*20/200)
	uy := b.Min.Y + int(float64(b.Dy())*80/100)
	if rr, _, _, _ := got.At(ux, uy).RGBA(); rr>>8 < 200 {
		t.Errorf("an unmatched area was scrubbed (red=%d)", rr>>8)
	}
}

func TestOCRPatternMatch(t *testing.T) {
	r, _ := NewReader(scanDoc(t, 200, 100))
	rd := Redact(r)
	rd.SetOCR(&stubOCR{gone: true, words: []OCRWord{
		{Text: "4815162342", X: 10, Y: 10, W: 80, H: 12, Confidence: 1},
		{Text: "Account", X: 10, Y: 40, W: 60, H: 12, Confidence: 1},
	}})
	rd.Pattern(regexp.MustCompile(`^\d{10}$`))
	marks, err := rd.Marks()
	if err != nil {
		t.Fatal(err)
	}
	if len(marks) != 1 || marks[0].Text != "4815162342" {
		t.Errorf("marks = %+v", marks)
	}
}

func TestOCRConfidenceFloor(t *testing.T) {
	words := []OCRWord{{Text: "Lovelace", X: 10, Y: 10, W: 60, H: 12, Confidence: 0.4}}
	for _, c := range []struct {
		min  float64
		want int
	}{{0, 1}, {0.3, 1}, {0.5, 0}, {0.9, 0}} {
		r, _ := NewReader(scanDoc(t, 200, 100))
		rd := Redact(r)
		rd.SetOCR(&stubOCR{gone: true, words: words})
		rd.SetOCRConfidence(c.min)
		rd.Text("Lovelace")
		marks, err := rd.Marks()
		if err != nil {
			t.Fatal(err)
		}
		if len(marks) != c.want {
			t.Errorf("confidence floor %v gave %d marks, want %d", c.min, len(marks), c.want)
		}
	}
}

// TestOCRVerifiesTheScrub is the safety net for scans: nothing extracts
// text from an image, so the ordinary read-back cannot see it. The engine
// is run again over the result instead.
func TestOCRVerifiesTheScrub(t *testing.T) {
	r, _ := NewReader(scanDoc(t, 200, 100))
	// gone is false, so the engine keeps reporting the word: the scrub
	// did not take, and that must be caught.
	stubborn := &stubOCR{words: []OCRWord{
		{Text: "Lovelace", X: 10, Y: 10, W: 60, H: 12, Confidence: 1},
	}}
	rd := Redact(r)
	rd.SetOCR(stubborn)
	rd.Text("Lovelace")
	var buf bytes.Buffer
	_, err := rd.WriteTo(&buf)
	if err == nil {
		t.Fatal("a word still readable in the image was not caught")
	}
	if !strings.Contains(err.Error(), "still be read in an image") {
		t.Errorf("unhelpful error: %v", err)
	}
	if buf.Len() != 0 {
		t.Error("a document that failed its check should not be written")
	}
}

func TestOCRMatchesAnyRule(t *testing.T) {
	rd := Redact(&Reader{})
	rd.Text("Ada Lovelace")
	rd.Pattern(regexp.MustCompile(`^\d{4}$`))
	cases := map[string]bool{
		"Lovelace":     true,  // a word of the literal
		"Ada":          true,  // likewise, deliberately over-broad
		"Lovelace,":    true,  // punctuation the engine attached
		"Ada Lovelace": true,  // the whole literal in one box
		"Adam":         false, // not a word of it
		"Al":           false, // too short to match on
		"1984":         true,  // the pattern
		"19845":        false,
	}
	for text, want := range cases {
		if got := rd.matchesAnyRule(text); got != want {
			t.Errorf("matchesAnyRule(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestOCRPageRectFor(t *testing.T) {
	img := ImageRef{X: 100, Y: 50, W: 200, H: 100}
	got := pageRectFor(img, rect{0.25, 0.5, 0.75, 1})
	want := rect{150, 100, 250, 150}
	if got != want {
		t.Errorf("pageRectFor = %+v, want %+v", got, want)
	}
}

func TestOCRWithoutEngineDoesNothing(t *testing.T) {
	r, _ := NewReader(scanDoc(t, 100, 50))
	rd := Redact(r)
	rd.Text("anything")
	marks, err := rd.Marks()
	if err != nil {
		t.Fatal(err)
	}
	if len(marks) != 0 {
		t.Errorf("no engine was set, so nothing should be found: %+v", marks)
	}
}

func TestOCRSubstituteDrawsToken(t *testing.T) {
	r, _ := NewReader(scanDoc(t, 200, 100))
	rd := Redact(r)
	rd.SetOCR(&stubOCR{gone: true, words: []OCRWord{
		{Text: "Lovelace", X: 40, Y: 20, W: 70, H: 14, Confidence: 1},
	}})
	rd.Substitute("Lovelace", "[[PII_NAME_1]]")
	out := redactTo(t, rd)

	r2, _ := NewReader(out)
	content, err := r2.pageContent(r2.pages[0].dict)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(content, []byte("[[PII_NAME_1]]")) {
		t.Errorf("the token was not drawn over the scrubbed pixels:\n%s", content)
	}
	if !bytes.Contains(content, []byte(labelFontName)) {
		t.Error("the token font was not referenced")
	}
	// And the font resource really is on the page.
	res, _ := r2.resolve(r2.pages[0].resources).(Dict)
	fonts, _ := r2.resolve(res["Font"]).(Dict)
	if fonts[labelFontName] == nil {
		t.Error("the token font is missing from the page resources")
	}
	// The pixels went all the same.
	imgs, _ := r2.PageImages(0)
	got, err := imgs[0].Decode()
	if err != nil {
		t.Fatal(err)
	}
	b := got.Bounds()
	px := b.Min.X + int(float64(b.Dx())*60/200)
	py := b.Min.Y + int(float64(b.Dy())*25/100)
	if rr, _, _, _ := got.At(px, py).RGBA(); rr>>8 > 20 {
		t.Errorf("the pixels were not scrubbed under the token (red=%d)", rr>>8)
	}
}

func TestRedactSetLabel(t *testing.T) {
	src := redactFixture(t)
	r, _ := NewReader(src)
	rd := Redact(r)
	rd.Text("Ada Lovelace")
	rd.SetLabel("[REDACTED]")
	rd.SetLabelColor(RGB(255, 255, 0))
	out := redactTo(t, rd)

	r2, _ := NewReader(out)
	content, _ := r2.pageContent(r2.pages[0].dict)
	if !bytes.Contains(content, []byte("[REDACTED]")) {
		t.Error("the label was not drawn")
	}
	if !bytes.Contains(content, []byte("1 1 0 rg")) {
		t.Error("the label colour was not applied")
	}
	assertGone(t, out, "Ada Lovelace")
}

// TestRedactDropsSecondCopies covers the routes by which a redacted page
// stays readable: a thumbnail of the page, an alternate of the image.
func TestRedactDropsSecondCopies(t *testing.T) {
	src := scanDoc(t, 120, 60)
	r, _ := NewReader(src)
	u := Update(r)
	// Plant a thumbnail and a private cache on the page.
	thumb := u.add(&rawStream{
		dict: Dict{"Type": Name("XObject"), "Subtype": Name("Image"),
			"Width": int64(4), "Height": int64(4),
			"ColorSpace": Name("DeviceGray"), "BitsPerComponent": int64(8)},
		data: bytes.Repeat([]byte{0x40}, 16),
	})
	pi := r.pages[0]
	pd := cloneDict(pi.dict)
	pd["Thumb"] = Ref{Num: thumb}
	pd["PieceInfo"] = Dict{"MyApp": Dict{"Private": String("Ada Lovelace")}}
	num, _ := r.pageObjectNumber(0)
	u.set(num, pd)
	var planted bytes.Buffer
	if _, err := u.WriteTo(&planted); err != nil {
		t.Fatal(err)
	}

	r2, err := NewReader(planted.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if _, has := r2.pages[0].dict["Thumb"]; !has {
		t.Fatal("the fixture has no thumbnail")
	}
	rd := Redact(r2)
	rd.Area(0, 0, 0, 50, 50)
	out := redactTo(t, rd)

	r3, _ := NewReader(out)
	if _, has := r3.pages[0].dict["Thumb"]; has {
		t.Error("the page thumbnail survived; it shows the page before redaction")
	}
	if _, has := r3.pages[0].dict["PieceInfo"]; has {
		t.Error("the producer's private cache survived")
	}
	if bytes.Contains(out, []byte("Ada Lovelace")) {
		t.Error("text in the private cache survived")
	}
	marks, _ := rd.Marks()
	var copies int
	for _, m := range marks {
		if m.Kind == RedactCopy {
			copies++
		}
	}
	if copies != 2 {
		t.Errorf("reported %d dropped copies, want 2: %+v", copies, marks)
	}
}

func TestReverseMappings(t *testing.T) {
	subs := []Pseudonym{
		{From: "Ada Lovelace", To: "[[P1]]"},
		{From: "Dorset Street", To: "[[P2]]"},
	}
	back := Reverse(subs)
	if len(back) != 2 || back[0].From != "[[P1]]" || back[0].To != "Ada Lovelace" {
		t.Fatalf("Reverse = %+v", back)
	}
	k := Key{Mappings: subs}
	if !k.Reversible() {
		t.Error("a key with no destroyed pixels should be reversible")
	}
	if (Key{Mappings: subs, PixelsDestroyed: 1}).Reversible() {
		t.Error("a key that overwrote pixels is not reversible")
	}
	if k.Reverse()[1].To != "Dorset Street" {
		t.Errorf("Key.Reverse = %+v", k.Reverse())
	}
}

// TestPseudonymizeRoundTrip is the reversion promise for text: with the
// key, the original comes back.
func TestPseudonymizeRoundTrip(t *testing.T) {
	doc := New()
	p := doc.AddPage()
	p.SetFont(Helvetica, 11)
	p.Text(60, 100, "The claimant Ada Lovelace attended on 3 May.")
	src := docBytes(t, doc)

	key := []Pseudonym{{From: "Ada Lovelace", To: "[[PII_NAME_1]]"}}
	var anon bytes.Buffer
	if _, err := Pseudonymize(NewReaderOrFail(t, src), &anon, key); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(extractAll(t, anon.Bytes()), "Ada Lovelace") {
		t.Fatal("the name survived the substitution")
	}

	var back bytes.Buffer
	if _, err := Pseudonymize(NewReaderOrFail(t, anon.Bytes()), &back, Reverse(key)); err != nil {
		t.Fatal(err)
	}
	got := collapse(extractAll(t, back.Bytes()))
	if !strings.Contains(got, "The claimant Ada Lovelace attended on 3 May.") {
		t.Errorf("the original did not come back: %q", got)
	}
	if strings.Contains(got, "[[PII_NAME_1]]") {
		t.Errorf("the token was left behind: %q", got)
	}
}
