package gopdf

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Standing in for a font the document does not carry.
//
// A PDF is allowed to name Arial and not embed it, on the understanding
// that whoever opens it has Arial. That is a reasonable bargain between a
// viewer and a reader, and a poor one for a library: this package has no
// fonts of its own and inventing outlines would be worse than drawing
// nothing.
//
// So the caller supplies them. A substitute is used for its shapes only —
// every advance still comes from the widths in the document, so the text
// lands exactly where the document says even when the substitute is a
// little wider or narrower than the font that was meant. The line breaks
// where the document breaks it; only the letterforms are approximate.

// FontRequest describes a font the document names but does not embed.
type FontRequest struct {
	// BaseFont is the name the document gives, subset prefix and all,
	// such as "Arial-BoldMT" or "BCDEEE+Cambria".
	BaseFont string
	// Bold, Italic, Serif and Fixed are what the font descriptor and the
	// name between them say about the face, so a substitute can be
	// chosen to match rather than merely to exist.
	Bold, Italic, Serif, Fixed bool
}

// SystemFonts returns a substitution function that looks for a matching
// face among the fonts installed on this machine.
//
// It reads font files from disk, which is why it is not the default:
// a library should not go rummaging through the filesystem unasked, and
// a render that depends on what happens to be installed is a render that
// differs from machine to machine. Passing it is a decision, and the
// decision is the caller's.
func SystemFonts() func(FontRequest) []byte {
	dirs := systemFontDirs()
	cache := map[string][]byte{}
	var index []string
	// The returned function is the obvious thing to share between
	// goroutines rendering different pages, so it holds a lock rather
	// than a warning in its documentation.
	var mu sync.Mutex
	return func(req FontRequest) []byte {
		mu.Lock()
		defer mu.Unlock()
		if index == nil {
			index = scanFontFiles(dirs)
			if index == nil {
				index = []string{} // scanned and found nothing
			}
		}
		key := req.BaseFont
		if data, ok := cache[key]; ok {
			return data
		}
		var chosen []byte
		// Candidates are tried best first, and a candidate is only taken
		// if it can actually set the text: a name that looks right is no
		// use if the file behind it is a font of symbols, which is what
		// happens when a machine has no Arial and the closest match by
		// name turns out to be a dingbat.
		for _, c := range rankFonts(index, req) {
			data, err := os.ReadFile(c.path)
			if err != nil || !usableFont(data, c.namedFamily) {
				continue
			}
			chosen = data
			break
		}
		cache[key] = chosen
		return chosen
	}
}

// usableFont reports whether a font file is worth setting text in.
//
// A candidate found only by weight and slope has to prove it can set
// ordinary words, because the alternative is a page of dingbats. A
// candidate whose family name is the one the document asked for has
// already proved what matters and is taken as it is — a Devanagari face
// has no Latin letters to show, and demanding them would reject exactly
// the font that was wanted.
func usableFont(data []byte, namedFamily bool) bool {
	f, err := parseTTF(data)
	if err != nil || len(f.cmap) == 0 {
		return false
	}
	if namedFamily {
		return true
	}
	for _, ru := range []rune{'e', 'a', 'A', '1'} {
		if gid, ok := f.cmap[ru]; !ok || gid == 0 {
			return false
		}
	}
	return true
}

func systemFontDirs() []string {
	home, _ := os.UserHomeDir()
	dirs := []string{
		"/System/Library/Fonts", "/System/Library/Fonts/Supplemental",
		"/Library/Fonts",                             // macOS
		"/usr/share/fonts", "/usr/local/share/fonts", // Unix
		`C:\Windows\Fonts`, // Windows
	}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, "Library", "Fonts"),
			filepath.Join(home, ".fonts"),
			filepath.Join(home, ".local", "share", "fonts"))
	}
	return dirs
}

// scanFontFiles lists the font files under a set of directories.
func scanFontFiles(dirs []string) []string {
	var out []string
	for _, dir := range dirs {
		// A font directory can be deep; the walk is bounded so a
		// pathological tree cannot hold a render up indefinitely.
		count := 0
		filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if count++; count > 20000 {
				return filepath.SkipAll
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".ttf", ".otf", ".ttc":
				out = append(out, path)
			}
			return nil
		})
	}
	return out
}

// rankFonts orders the installed faces by how close they are to what was
// asked for, closest first.
//
// The match is deliberately crude: the family name if it can be found,
// then the weight and slope. A substitute is an approximation by
// definition, and an elaborate matcher would only make the approximation
// harder to predict. What matters more than the ranking is that the
// caller of this list checks each candidate before settling on it.
type fontCandidate struct {
	path string
	// namedFamily marks a candidate whose file name carries the family
	// the document asked for, rather than one found by style alone.
	namedFamily bool
}

func rankFonts(index []string, req FontRequest) []fontCandidate {
	family := strings.ReplaceAll(strings.ToLower(familyOf(req.BaseFont)), " ", "")
	type scored struct {
		path   string
		score  int
		byName bool
	}
	ranked := make([]scored, 0, len(index))
	for _, path := range index {
		name := strings.ToLower(filepath.Base(path))
		name = strings.TrimSuffix(name, filepath.Ext(name))
		flat := strings.ReplaceAll(strings.ReplaceAll(name, " ", ""), "-", "")
		score := 0

		// A family match has to be exact once the style words are taken
		// out, or "Arial Black" scores the same as "Arial" for a request
		// that asked for neither — and comes first in the directory.
		byName := false
		if family != "" {
			stem := stripStyleWords(flat)
			switch {
			case stem == family:
				score += 20
				byName = true
			case strings.Contains(flat, family):
				score += 14
				byName = true
			case len(stem) >= 5 && strings.Contains(family, stem):
				// The other direction: a document asking for
				// "KohinoorDevanagari" should find "Kohinoor.ttc", which
				// carries the family under a shorter name.
				score += 12
				byName = true
			}
		}
		bold := strings.Contains(flat, "bold")
		italic := strings.Contains(flat, "italic") || strings.Contains(flat, "oblique")
		if bold == req.Bold {
			score += 4
		}
		if italic == req.Italic {
			score += 4
		}
		// A weight or width nobody asked for is a worse match than the
		// plain face, however well the family matches.
		for _, w := range []string{"black", "heavy", "light", "thin", "narrow",
			"condensed", "semibold", "extra", "ultra"} {
			if strings.Contains(flat, w) {
				score -= 6
			}
		}
		switch {
		case req.Fixed && (strings.Contains(flat, "mono") || strings.Contains(flat, "courier")):
			score += 6
		case req.Serif && (strings.Contains(flat, "times") || strings.Contains(flat, "serif") ||
			strings.Contains(flat, "georgia") || strings.Contains(flat, "garamond")):
			score += 6
		case !req.Serif && !req.Fixed:
			for _, want := range []string{"helvetica", "arial", "dejavusans", "liberationsans",
				"verdana", "tahoma", "geneva"} {
				if strings.Contains(flat, want) {
					score += 6
					break
				}
			}
		}
		ranked = append(ranked, scored{path, score, byName})
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	out := make([]fontCandidate, 0, len(ranked))
	for _, r := range ranked {
		out = append(out, fontCandidate{path: r.path, namedFamily: r.byName})
	}
	// Trying every font on the machine would be slow and pointless; the
	// best few are the only ones with a chance of being right.
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

// familyOf strips a subset prefix and a style suffix from a base font
// name, leaving the family: "BCDEEE+Arial-BoldMT" becomes "Arial".
func familyOf(base string) string {
	if i := strings.IndexByte(base, '+'); i == 6 {
		base = base[i+1:]
	}
	if i := strings.IndexByte(base, ','); i >= 0 {
		base = base[:i]
	}
	if i := strings.IndexByte(base, '-'); i > 0 {
		base = base[:i]
	}
	for _, suffix := range []string{"MT", "PS", "Bold", "Italic", "Oblique", "Regular"} {
		base = strings.TrimSuffix(base, suffix)
	}
	return strings.TrimSpace(base)
}

// fontRequestFor describes a font dictionary to a substitution function.
func fontRequestFor(r *Reader, dict, descriptor Dict) FontRequest {
	req := FontRequest{BaseFont: baseFontName(r, dict)}
	lower := strings.ToLower(req.BaseFont)
	req.Bold = strings.Contains(lower, "bold")
	req.Italic = strings.Contains(lower, "italic") || strings.Contains(lower, "oblique")
	if flags, ok := toInt(r.resolve(descriptor["Flags"])); ok {
		req.Fixed = flags&1 != 0
		req.Serif = flags&2 != 0
		req.Italic = req.Italic || flags&(1<<6) != 0
		// Bit 19 is the force-bold flag, which is not the same as a bold
		// face, so the weight is read instead where the font gives one.
	}
	if w, ok := toFloat(r.resolve(descriptor["StemV"])); ok && w >= 120 {
		req.Bold = true
	}
	if w, ok := toFloat(r.resolve(descriptor["FontWeight"])); ok && w >= 600 {
		req.Bold = true
	}
	return req
}

// stripStyleWords removes the weight and slope words from a font file's
// name, leaving what is meant to be the family.
func stripStyleWords(name string) string {
	for _, w := range []string{"bolditalic", "boldoblique", "bold", "italic", "oblique",
		"regular", "roman", "book", "black", "heavy", "light", "thin", "narrow",
		"condensed", "semibold", "medium", "extra", "ultra", "mt", "ps"} {
		name = strings.ReplaceAll(name, w, "")
	}
	return name
}
