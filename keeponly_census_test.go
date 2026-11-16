package gopdf

// keeponly_census_test.go — every dictionary key real files carry, and
// what KeepOnly does with it (SPEC-PDF-OUTPUT-KEEPLIST §8 V6).
//
// KEEPONLY_CENSUS names a file listing PDFs, one path a line;
// KEEPONLY_CENSUS_OUT the table to write. A key is named by where it
// stands: the dictionary's /Type (and /Subtype), or, for a dictionary
// without one, the place it hangs from. Each key is "kept" when the
// copy still has it there, "dropped" when no copy has.
//
// The table is made from local test files and is kept with the private
// test suite, never in this repository. Every key KeepOnly does not
// list is dropped by construction; the census shows which, so a key
// that matters for how a page looks — and is dropped — is read by a
// person, and the render comparison of the corpus says whether it did
// matter.

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// censusTypes are the dictionaries named by their /Type wherever they
// hang; every other is named by the place it hangs from, and a
// resource by its category (its own name is the producer's choice).
var censusTypes = map[Name]bool{"Catalog": true, "Pages": true, "Page": true, "Font": true, "FontDescriptor": true,
	"XObject": true, "Annot": true, "ExtGState": true, "Pattern": true, "StructElem": true, "StructTreeRoot": true,
	"Outlines": true, "OCG": true, "OCMD": true, "Filespec": true, "EmbeddedFile": true, "Metadata": true,
	"Action": true, "OutputIntent": true, "Sig": true, "ObjStm": true, "XRef": true}

func censusLabel(r *Reader, d Dict, parent string, key Name) string {
	typ, _ := r.resolve(d["Type"]).(Name)
	sub, _ := r.resolve(d["Subtype"]).(Name)
	if censusTypes[typ] {
		if sub != "" {
			return string(typ) + "/" + string(sub)
		}
		return string(typ)
	}
	for _, cat := range []string{".Font", ".XObject", ".ColorSpace", ".Pattern", ".Shading", ".ExtGState", ".Properties"} {
		if strings.HasSuffix(parent, ".Resources"+cat) {
			key = "*"
		}
	}
	if sub != "" {
		return parent + "." + string(key) + "/" + string(sub)
	}
	return parent + "." + string(key)
}

// censusKeys walks r from its trailer and returns every label.key seen.
func censusKeys(r *Reader) map[string]bool {
	seen := map[string]bool{}
	visited := map[int]bool{}
	var walk func(v any, parent string, key Name, depth int)
	walk = func(v any, parent string, key Name, depth int) {
		if depth > 40 {
			return
		}
		if ref, ok := v.(Ref); ok {
			if visited[ref.Num] {
				return
			}
			visited[ref.Num] = true
		}
		v = r.resolve(v)
		var d Dict
		switch t := v.(type) {
		case Dict:
			d = t
		case *rawStream:
			d = t.dict
		case Array:
			for _, e := range t {
				walk(e, parent, key, depth+1)
			}
			return
		default:
			return
		}
		label := censusLabel(r, d, parent, key)
		if len(label) > 80 {
			label = label[:80]
		}
		resourceNames := false
		for _, cat := range []string{".Font", ".XObject", ".ColorSpace", ".Pattern", ".Shading", ".ExtGState", ".Properties"} {
			if strings.HasSuffix(label, ".Resources"+cat) {
				resourceNames = true
			}
		}
		for k, e := range d {
			if resourceNames {
				seen[label+" /*"] = true
			} else {
				seen[label+" /"+string(k)] = true
			}
			if k == "Parent" || k == "P" || k == "Prev" {
				continue
			}
			walk(e, label, k, depth+1)
		}
	}
	walk(r.trailer, "trailer", "", 0)
	return seen
}

func TestZZKeepOnlyCensus(t *testing.T) {
	list, out := os.Getenv("KEEPONLY_CENSUS"), os.Getenv("KEEPONLY_CENSUS_OUT")
	if list == "" || out == "" {
		t.Skip()
	}
	f, err := os.Open(list)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	files := map[string]int{}
	kept := map[string]int{}
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		src, err := os.ReadFile(sc.Text())
		if err != nil {
			continue
		}
		r, err := NewReader(src)
		if err != nil {
			continue
		}
		var cp bytes.Buffer
		if _, err := KeepOnly(r, &cp, KeepPolicy{}); err != nil {
			continue
		}
		cr, err := NewReader(cp.Bytes())
		if err != nil {
			continue
		}
		n++
		before, after := censusKeys(r), censusKeys(cr)
		for k := range before {
			files[k]++
			if after[k] {
				kept[k]++
			}
		}
	}
	var rows []string
	for k, c := range files {
		decision := "dropped"
		switch {
		case kept[k] == c:
			decision = "kept"
		case kept[k] > 0:
			decision = fmt.Sprintf("kept in %d", kept[k])
		}
		rows = append(rows, fmt.Sprintf("%-70s %6d  %s", k, c, decision))
	}
	sort.Strings(rows)
	head := fmt.Sprintf("# KeepOnly census: %d files, %d keys by place (key, files carrying it, what the copy does)\n", n, len(rows))
	if err := os.WriteFile(out, []byte(head+strings.Join(rows, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d files, %d keys", n, len(rows))
}
