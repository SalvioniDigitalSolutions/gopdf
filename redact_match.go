package gopdf

import (
	"strings"
	"unicode/utf8"
)

// Matching text that a content stream broke up.
//
// A word is not one operation in a content stream. Kerning, a change of
// colour, a ligature or nothing more than a producer's whim can split
// "Administration" into "Administra" and "tion", and searching each run
// on its own would then find neither. For redaction that failure mode is
// the wrong way round: a name left in the file is worse than a little
// text removed with it. So matching runs over a whole line at a time.
//
// Runs are only joined when they continue each other: same baseline, and
// the next one starting about where the last one ended. A gap wide enough
// to be a space becomes one, which stops a match running across columns,
// and a change of line breaks the chain entirely.

// runChain is a sequence of runs that read as continuous text, with the
// text they form and where each run sits inside it.
type runChain struct {
	text  string
	runs  []*TextRun
	start []int // byte offset of each run's text within text
	end   []int
	// elided[i] is how many trailing bytes of run i were left out of
	// text: the hyphen of a word broken across a line break. A match
	// reaching the end of that run takes the hyphen with it, so the
	// replacement does not leave one dangling.
	elided []int
}

// buildChains groups a page's runs into chains of continuous text.
func buildChains(runs []*TextRun) []runChain {
	var out []runChain
	var cur runChain
	var b strings.Builder

	flush := func() {
		if len(cur.runs) > 0 {
			cur.text = b.String()
			out = append(out, cur)
		}
		cur = runChain{}
		b.Reset()
	}

	for _, run := range runs {
		if run.Text == "" {
			continue
		}
		dropped := 0
		if len(cur.runs) > 0 {
			prev := cur.runs[len(cur.runs)-1]
			kind := joinKind(prev, run)
			// A line break where the last word ends in a hyphen is a
			// word split in two, not two words. The hyphen comes out of
			// the reading and the chain carries on, so "Bian-" and "chi"
			// are matched as "Bianchi".
			if kind == joinBreak && continuesHyphenated(b.String(), prev, run) {
				dropped = trimChainHyphen(&b, &cur)
				kind = joinTight
			}
			switch kind {
			case joinBreak:
				flush()
			case joinSpace:
				b.WriteByte(' ')
			}
		}
		_ = dropped
		cur.start = append(cur.start, b.Len())
		b.WriteString(run.Text)
		cur.end = append(cur.end, b.Len())
		cur.elided = append(cur.elided, 0)
		cur.runs = append(cur.runs, run)
	}
	flush()
	return out
}

type joinMode int

const (
	joinTight joinMode = iota // the runs continue each other directly
	joinSpace                 // a gap wide enough to read as a space
	joinBreak                 // a different line, or somewhere else entirely
)

// joinKind decides how two consecutive runs relate.
//
// Whether a gap reads as a space is decided by the same rule text
// extraction uses, so that a word the extractor reports as whole is one
// this can match. Two heuristics that disagree leave text that PageText
// finds and redaction does not.
func joinKind(prev, next *TextRun) joinMode {
	size := prev.FontSize
	if size <= 0 {
		size = next.FontSize
	}
	if size <= 0 {
		return joinBreak
	}
	// A different baseline is a different line.
	if d := next.Y - prev.Y; d > size*0.3 || d < -size*0.3 {
		return joinBreak
	}
	gap := next.X - (prev.X + prev.Width)
	switch {
	case gap < -size*0.5:
		return joinBreak // the pen went backwards: a new column or overprint
	case gap > size*2.5:
		return joinBreak // far enough to be somewhere else entirely
	case needsSpace(next.Text, gap, prev.spaceWidthPts()):
		return joinSpace
	default:
		return joinTight
	}
}

// spaceWidthPts returns how wide a space is in a run's own font and size.
func (run *TextRun) spaceWidthPts() float64 {
	if run.font != nil && run.fontSizeRaw != 0 {
		if w := run.font.codeWidth(32); w > 0 {
			return w / 1000 * run.FontSize * run.horizScale
		}
	}
	return run.FontSize * 0.25
}

// chainRanges maps a byte range of a chain's text onto the runs it covers,
// returning a range per run in the run's own text.
func (c runChain) chainRanges(lo, hi int) map[*TextRun][][2]int {
	out := make(map[*TextRun][][2]int)
	for i, run := range c.runs {
		s, e := c.start[i], c.end[i]
		if hi <= s || lo >= e {
			continue
		}
		from, to := maxI(lo, s)-s, minI(hi, e)-s
		// A match reaching the end of a run whose hyphen was left out of
		// the reading takes that hyphen too.
		if i < len(c.elided) && c.elided[i] > 0 && minI(hi, e) == e {
			to += c.elided[i]
		}
		if from < to {
			out[run] = append(out[run], [2]int{from, to})
		}
	}
	return out
}

// matchesInChains finds every literal and pattern match across a page's
// runs and returns the character ranges to remove, per run.
func (rd *Redactor) matchesInChains(runs []*TextRun) map[*TextRun][][2]int {
	found := make(map[*TextRun][][2]int)
	if len(rd.literals) == 0 && len(rd.patterns) == 0 {
		return found
	}
	add := func(m map[*TextRun][][2]int) {
		for run, rs := range m {
			found[run] = append(found[run], rs...)
		}
	}
	for _, chain := range buildChains(runs) {
		for _, lit := range rd.literals {
			for _, rg := range literalRanges(chain.text, lit, rd.mode()) {
				add(chain.chainRanges(rg[0], rg[1]))
			}
		}
		for _, re := range rd.patterns {
			for _, m := range re.FindAllStringIndex(chain.text, -1) {
				add(chain.chainRanges(m[0], m[1]))
			}
		}
	}
	return found
}

// continuesHyphenated reports whether the chain so far ends in a hyphen
// that a word on the next line continues.
//
// The test is on the chain's text rather than on the previous run's,
// because justified documents often set the hyphen as an operation of its
// own: the runs read "Akteu", "-", "ren", and asking the run before the
// break whether it ends in a hyphen would find only the hyphen.
func continuesHyphenated(chain string, prev, next *TextRun) bool {
	if chain == "" || next.Text == "" {
		return false
	}
	r, size := utf8.DecodeLastRuneInString(chain)
	if !isHyphen(r) {
		return false
	}
	before, _ := utf8.DecodeLastRuneInString(chain[:len(chain)-size])
	after, _ := utf8.DecodeRuneInString(next.Text)
	if !isWordRune(before) || !isWordRune(after) {
		return false
	}
	// Only downwards, and only back to about the same left edge: a run
	// further right is another column, not the next line.
	size2 := prev.FontSize
	if size2 <= 0 {
		size2 = next.FontSize
	}
	return next.Y > prev.Y && next.Y-prev.Y <= size2*2.5
}

// trimChainHyphen removes the hyphen just written from the chain's text
// and records how many bytes went, returning that count.
func trimChainHyphen(b *strings.Builder, cur *runChain) int {
	text := b.String()
	r, size := utf8.DecodeLastRuneInString(text)
	if !isHyphen(r) {
		return 0
	}
	b.Reset()
	b.WriteString(text[:len(text)-size])
	if n := len(cur.end); n > 0 {
		cur.end[n-1] -= size
		cur.elided[n-1] = size
	}
	return size
}
