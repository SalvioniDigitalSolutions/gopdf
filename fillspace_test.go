package gopdf

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// TestRewrittenLineRestatesColourWithItsSpace: a line whose first words
// are painted in CMYK (k) and whose last in a named one-component space
// ("/CS1 cs 1 scn") is rewritten when a value in it is replaced. The
// rewritten line restated the second colour as "1 scn" alone — after the
// k, in CMYK, where scn wants four numbers — and viewers reported the
// page broken ("Incorrect number of arguments in 'scn' command").
func TestRewrittenLineRestatesColourWithItsSpace(t *testing.T) {
	content := "BT /F1 12 Tf 1 0 0 1 72 700 Tm " +
		"0.19 0.23 0 0 k (Heading for Marco Rossi) Tj " +
		"/CS1 cs 1 scn ( and the rest of the line) Tj ET\n"
	objs := map[int]string{
		1: `<< /Type /Catalog /Pages 2 0 R >>`,
		2: `<< /Type /Pages /Kids [3 0 R] /Count 1 >>`,
		3: `<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R ` +
			`/Resources << /Font << /F1 5 0 R >> /ColorSpace << /CS1 6 0 R >> >> >>`,
		4: fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content),
		5: `<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>`,
		6: `[/Separation /Spot /DeviceCMYK << /FunctionType 2 /Domain [0 1] /C0 [0 0 0 0] /C1 [0 0 0 1] /N 1 >>]`,
	}
	var out bytes.Buffer
	if _, err := Pseudonymize(NewReaderOrFail(t, rawPDFOf(objs, "")), &out, []Pseudonym{{From: "Marco Rossi", To: "[PERSON 1]"}}); err != nil {
		t.Fatal(err)
	}
	text := extractAll(t, out.Bytes())
	if strings.Contains(text, "Marco Rossi") || !strings.Contains(text, "[PERSON 1]") {
		t.Fatalf("not replaced: %q", text)
	}
	r := NewReaderOrFail(t, out.Bytes())
	content2, err := r.pageContent(r.pages[0].dict)
	if err != nil {
		t.Fatal(err)
	}
	components := map[string]int{"/DeviceGray": 1, "/DeviceRGB": 3, "/DeviceCMYK": 4, "/CS1": 1}
	space := 1 // DeviceGray, at the start of a page
	var operands []contentToken
	for _, tok := range tokenizeContent(content2) {
		op, isOp := tok.val.(opKeyword)
		if !isOp {
			operands = append(operands, tok)
			continue
		}
		switch string(op) {
		case "g":
			space = 1
		case "rg":
			space = 3
		case "k":
			space = 4
		case "cs":
			if n, ok := operands[0].val.(Name); ok {
				space = components["/"+string(n)]
			}
		case "sc", "scn":
			if len(operands) != space {
				t.Errorf("%s with %d operands where the colour space has %d components:\n%s", op, len(operands), space, content2)
			}
		}
		operands = operands[:0]
	}
}
