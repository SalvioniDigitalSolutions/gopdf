package gopdf

import (
	"strings"
	"testing"
)

// A word set against the one before it with a gap two thirds of the
// font's space — a word break to pdftotext, none to this package's
// reading — is a word to the redactor: it is found and removed. A kern
// is not: a word kerned into a longer one stays as it is.
func TestATightlySetWordIsMatchedAsAWord(t *testing.T) {
	// Helvetica's space is 278/1000 em: a gap of 180 is 0.65 of it, and
	// 0.18 of the size.
	content := strings.Join([]string{
		`BT /F1 10 Tf 1 0 0 1 72 700 Tm [(40) -180 (flavodoxin) -180 (protein)] TJ ET`,
		`BT /F1 10 Tf 1 0 0 1 72 680 Tm [(Inflavodoxinase) -20 (d)] TJ ET`,
		`BT /F1 10 Tf 1 0 0 1 72 660 Tm [(Ada) -400 (Lovelace)] TJ ET`,
	}, "\n") + "\n"
	src := onePage(content, []string{`<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>`}, nil)
	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	// The fragments read the word break the operation leaves, as the
	// page's text does.
	frags, err := r.PageTextFragments(0)
	if err != nil || len(frags) != 3 {
		t.Fatalf("fragments: %v %v", frags, err)
	}
	if frags[2].Text != "Ada Lovelace" {
		t.Errorf("a word break inside an operation is missing from its fragment: %q", frags[2].Text)
	}
	if frags[0].Text != "40flavodoxinprotein" {
		t.Errorf("a tight gap became a word break in the reading: %q", frags[0].Text)
	}
	rd := Redact(r)
	rd.Text("flavodoxin")
	marks, err := rd.Marks()
	if err != nil {
		t.Fatal(err)
	}
	if len(marks) != 1 {
		t.Fatalf("want the tightly set word only, got %d marks: %+v", len(marks), marks)
	}
	out := redactTo(t, rd)
	got := extractAll(t, out)
	if strings.Contains(got, "40flavodoxin") || strings.Contains(got, "flavodoxinprotein") {
		t.Errorf("the tightly set word was left: %q", got)
	}
	if !strings.Contains(got, "Inflavodoxinase") {
		t.Errorf("a word kerned into a longer one was cut: %q", got)
	}
}

// A subset font numbered from 32 draws a letter at the code word
// spacing moves the pen after: the word space the page shows after
// that letter is a word break to the reader and the redactor alike.
func TestWordSpacingAfterALetterAtCode32IsAWordBreak(t *testing.T) {
	content := "BT /F1 10 Tf 4 Tw 1 0 0 1 72 700 Tm (40 ) Tj [(\\040flavodoxin)] TJ ET\n"
	src := onePage(content, []string{`<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding << /Differences [32 /M] >> >>`}, nil)
	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	frags, err := r.PageTextFragments(0)
	if err != nil || len(frags) != 2 {
		t.Fatalf("fragments: %v %v", frags, err)
	}
	if frags[1].Text != "M flavodoxin" {
		t.Errorf("the word space after the letter at code 32 is missing: %q", frags[1].Text)
	}
	rd := Redact(r)
	rd.Text("flavodoxin")
	if marks, err := rd.Marks(); err != nil || len(marks) != 1 {
		t.Fatalf("marks: %+v %v", marks, err)
	}
	if got := extractAll(t, redactTo(t, rd)); strings.Contains(got, "flavodoxin") {
		t.Errorf("the word was left: %q", got)
	}
}
