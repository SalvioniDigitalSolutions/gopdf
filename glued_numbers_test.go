package gopdf

// Numbers set side by side without a space ("-12645.6-2.8") are two
// numbers, as viewers read them; a token's kern is never written against
// the stream's own number; and KeepOnly keeps an array holding them
// whole, brackets and all.

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func TestGluedNumbersReadAsViewersDo(t *testing.T) {
	toks := tokenizeContent([]byte("[ <41> -12645.6-2.8( )]TJ --5 1.2.3 Tw"))
	arr, ok := toks[0].val.(Array)
	if !ok || len(arr) != 4 {
		t.Fatalf("the array is not read whole: %#v", toks)
	}
	if f, _ := toFloat(arr[1]); f != -12645.6 {
		t.Errorf("first number %v", arr[1])
	}
	if f, _ := toFloat(arr[2]); f != -2.8 {
		t.Errorf("second number %v", arr[2])
	}
	var nums []float64
	for _, tk := range toks[2:] {
		if f, ok := toFloat(tk.val); ok {
			nums = append(nums, f)
		}
	}
	if len(nums) != 3 || nums[0] != -5 || nums[1] != 1.2 || nums[2] != 0.3 {
		t.Errorf("--5 1.2.3 read as %v", nums)
	}
}

var gluedNumber = regexp.MustCompile(`[0-9.][+-][0-9.]`)

func TestFittedTokenIsNotGluedToTheNextKern(t *testing.T) {
	content := "BT /F1 10 Tf 1 0 0 1 60 700 Tm [(The Tax Exempt Bonds)-2.8( Office Has)-2.8( )]TJ ET\n"
	r, err := NewReader(rawPageDoc(t, content))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := Pseudonymize(r, &out, []Pseudonym{{From: "The Tax Exempt Bonds Office Has", To: "[REDACTED]", FitWidth: true}}); err != nil {
		t.Fatal(err)
	}
	got := pageContent(t, out.Bytes())
	if gluedNumber.Match(got) {
		t.Fatalf("two numbers run together: %s", got)
	}
	rr, _ := NewReader(out.Bytes())
	txt, _ := rr.PageText(0)
	if !strings.Contains(txt, "[REDACTED]") || strings.Contains(txt, "Tax Exempt") {
		t.Fatalf("page reads %q", txt)
	}
}

func TestKeepOnlyKeepsAnArrayWithGluedNumbers(t *testing.T) {
	content := "BT /F1 10 Tf 1 0 0 1 60 700 Tm [(Ab)-120-2.8(Cd)]TJ ET\n"
	r, err := NewReader(rawPageDoc(t, content))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := KeepOnly(r, &out, keepTestPolicy); err != nil {
		t.Fatal(err)
	}
	got := pageContent(t, out.Bytes())
	if !bytes.Contains(got, []byte("[")) || !bytes.Contains(got, []byte("]")) {
		t.Fatalf("the array lost its brackets: %s", got)
	}
	rr, _ := NewReader(out.Bytes())
	if txt, _ := rr.PageText(0); !strings.Contains(txt, "Ab") || !strings.Contains(txt, "Cd") {
		t.Fatalf("page reads %q", txt)
	}
}
