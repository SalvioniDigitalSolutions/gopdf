package gopdf

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// TestFragmentsLoseNothingOnCorpus reads a list of real documents, named
// by GOPDF_CORPUS, and checks that the fragments carry everything
// PageText reports, in order.
//
// The assertion runs one way deliberately. Fragments may hold more than
// PageText — a code the font does not map becomes U+FFFD here and is
// dropped there, and PageText is known to stop early on a small number of
// pages where fragments read on. What must never happen is the reverse:
// text PageText finds that the fragments do not, since anything matching
// on fragments would then be blind to it.
func TestFragmentsLoseNothingOnCorpus(t *testing.T) {
	list := os.Getenv("GOPDF_CORPUS")
	if list == "" {
		t.Skip("set GOPDF_CORPUS to a file listing PDFs")
	}
	f, err := os.Open(list)
	if err != nil {
		t.Skip(err)
	}
	defer f.Close()

	squash := func(s string) string {
		return strings.Join(strings.Fields(strings.ReplaceAll(s, "\uFFFD", "")), "")
	}
	var files, pages, bad int
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() && bad < 10 {
		path := strings.TrimSpace(sc.Text())
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		r, err := NewReader(data)
		if err != nil {
			continue
		}
		files++
		for i := 0; i < r.NumPages(); i++ {
			frags, err := r.PageTextFragments(i)
			if err != nil {
				continue // an error is the contract, not a silent prefix
			}
			page, err := r.PageText(i)
			if err != nil {
				continue
			}
			pages++
			var joined strings.Builder
			for _, fr := range frags {
				joined.WriteString(fr.Text)
			}
			if missing, ok := firstMissing(squash(page), squash(joined.String())); !ok {
				bad++
				t.Errorf("%s page %d: fragments lost text PageText found, from %q",
					path, i+1, missing)
			}
		}
	}
	t.Logf("%d files, %d pages, %d lost text", files, pages, bad)
}

// firstMissing reports whether want is a subsequence of got, and where it
// first fails if not.
func firstMissing(want, got string) (string, bool) {
	w := []rune(want)
	g := []rune(got)
	j := 0
	for i := 0; i < len(w); i++ {
		for j < len(g) && g[j] != w[i] {
			j++
		}
		if j == len(g) {
			lo := i - 20
			if lo < 0 {
				lo = 0
			}
			return string(w[lo:min(i+20, len(w))]), false
		}
		j++
	}
	return "", true
}
