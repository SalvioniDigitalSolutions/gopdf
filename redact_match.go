package gopdf

import (
	"math"
	"sort"
	"strings"
	"unicode"
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
	// inserted[i] holds the offsets within run i's slice of text where a
	// space was put in that the run's own text does not have — a gap the
	// operation left between two words. They come out again when a match
	// is mapped back onto the run, since the run never had them.
	inserted [][]int
	// kept holds, in a chain built with hyphensKept, the offset of each
	// hyphen a line break was joined across.
	kept []int
	// soft holds the offsets where two runs were joined with no space
	// across a gap wider than a kern (softGap): a word space by some
	// readers' rule and none by this one's (matchRanges).
	soft []int
}

// softGap is the share of the font size a gap between two runs has to
// span to be read as a word space by some reader: pdftotext breaks
// words at about a tenth of the size, this package's rule at seven
// tenths of the font's space.
const softGap = 0.1

// matchRanges finds a literal in a chain as the readers after a
// replacement find it: as a word of the chain's text, and as a word of
// the text of one of its runs. A run is one string the page draws, and
// the check that reads each drawn string apart, and the one that reads
// the file's bytes, take a value standing whole in one as a word, even
// where a reader joins that string to the next with no space between:
// "on Egypt" drawn against "ian Qurans", "KUHN" set against "AND" with
// a gap narrower than the space rule. The writers did not find the
// value there, the checks did, and the copy was refused. A value spread
// over several strings — a word drawn a letter at a time — is found
// only as a word of the chain, as before: "Ann" stays in "Annual".
func (c runChain) matchRanges(lit string, mode matchMode) [][2]int {
	out := literalRanges(c.text, lit, mode)
	if mode != matchWords || len(c.runs) < 2 {
		return out
	}
	added := false
	// And the chain read with a space at each soft join: two words
	// drawn apart by a gap a reader takes for a space ("Corporal" and
	// "Brunson", 0.4 of the size apart in Courier, whose space this
	// package's rule wants 0.42 of it) are read as two. A value with a
	// space there is found; one spelled tight is found in the chain as
	// it is, above.
	if len(c.soft) > 0 && strings.Contains(lit, " ") {
		var b strings.Builder
		back := make([]int, 0, len(c.text)+len(c.soft)+1)
		k := 0
		for i := 0; i <= len(c.text); i++ {
			for k < len(c.soft) && c.soft[k] == i {
				b.WriteByte(' ')
				back = append(back, i)
				k++
			}
			if i < len(c.text) {
				b.WriteByte(c.text[i])
				back = append(back, i)
			}
		}
		back = append(back, len(c.text))
		for _, rg := range literalRanges(b.String(), lit, mode) {
			lo, hi := back[rg[0]], back[rg[1]]
			taken := lo >= hi
			for _, o := range out {
				if lo < o[1] && hi > o[0] {
					taken = true
					break
				}
			}
			if !taken {
				out = append(out, [2]int{lo, hi})
				added = true
			}
		}
	}
	for i := range c.runs {
		s, e := c.start[i], c.end[i]
		if s < 0 || e > len(c.text) || s >= e {
			continue
		}
		for _, rg := range literalRanges(c.text[s:e], lit, mode) {
			lo, hi := rg[0]+s, rg[1]+s
			taken := false
			for _, o := range out {
				if lo < o[1] && hi > o[0] {
					taken = true
					break
				}
			}
			if !taken {
				out = append(out, [2]int{lo, hi})
				added = true
			}
		}
	}
	if added {
		sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	}
	return out
}

// buildChains groups a page's runs into chains of continuous text.
func buildChains(runs []*TextRun) []runChain {
	return buildChainsWith(runs, true)
}

// hyphenSplitChains are the lines a hyphenated line break runs between,
// read as they stand: "NIST-" at the end of one, "calibrated" at the
// start of the next. buildChains joins such a word ("NISTcalibrated")
// as a reader does; the check after a replacement reads the page both
// ways, and "NIST" standing before its hyphen was found there and
// nowhere by the replacer. Matching these as well, a value is replaced
// in either reading.
func hyphenSplitChains(runs []*TextRun) []runChain {
	var out []runChain
	raw := buildChainsWith(runs, false)
	for i, c := range raw {
		ends := strings.HasSuffix(strings.TrimRightFunc(c.text, unicode.IsSpace), "-")
		follows := i > 0 && strings.HasSuffix(strings.TrimRightFunc(raw[i-1].text, unicode.IsSpace), "-")
		if ends || follows {
			out = append(out, c)
		}
	}
	return out
}

// hyphenKeptChains are the lines a hyphenated line break runs between,
// joined with the hyphen kept: "ITC-" at the end of one, "18" at the
// start of the next, read "ITC-18". buildChains reads them "ITC18", as
// a reader joins a word divided at a line end; but where the hyphen is
// the value's own — a model number, "In-House", a reference — the value
// was found there by nobody but the check that reads the file's
// strings, and the copy was refused. Only the occurrences that run
// through such a join are taken from these: every other one is found
// in the chains already.
func hyphenKeptChains(runs []*TextRun) []runChain {
	var out []runChain
	for _, c := range buildChainsMode(runs, hyphensKept) {
		if len(c.kept) > 0 {
			out = append(out, c)
		}
	}
	return out
}

// throughKeptHyphen reports whether [lo,hi) of a hyphen-kept chain runs
// through one of its kept hyphens.
func (c runChain) throughKeptHyphen(lo, hi int) bool {
	for _, k := range c.kept {
		if lo <= k && hi > k {
			return true
		}
	}
	return false
}

// How buildChainsMode treats a hyphenated line break.
const (
	hyphensBreak   = iota // the line break stays a break
	hyphensDropped        // the lines join, the hyphen left out
	hyphensKept           // the lines join, the hyphen kept
)

// buildChainsWith is buildChains; joinHyphens false leaves a hyphenated
// line break a break.
func buildChainsWith(runs []*TextRun, joinHyphens bool) []runChain {
	if joinHyphens {
		return buildChainsMode(runs, hyphensDropped)
	}
	return buildChainsMode(runs, hyphensBreak)
}

// buildChainsMode is buildChains with a hyphenated line break treated
// as mode says.
func buildChainsMode(runs []*TextRun, mode int) []runChain {
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
			if mode != hyphensBreak && kind == joinBreak && continuesHyphenated(b.String(), prev, run) {
				if mode == hyphensDropped {
					dropped = trimChainHyphen(&b, &cur)
				} else {
					_, size := utf8.DecodeLastRuneInString(b.String())
					cur.kept = append(cur.kept, b.Len()-size)
				}
				kind = joinTight
			}
			switch kind {
			case joinBreak:
				flush()
			case joinSpace:
				b.WriteByte(' ')
			case joinTight:
				// A line joined across a hyphen moves down a line: not
				// a gap on one line, and no soft join.
				if size := prev.FontSize; size > 0 && math.Abs(run.Y-prev.Y) <= size*0.3 {
					if gap := run.X - (prev.X + prev.Width); gap > size*softGap {
						cur.soft = append(cur.soft, b.Len())
					}
				}
			}
		}
		_ = dropped
		cur.start = append(cur.start, b.Len())
		spaced, at := readRun(run)
		b.WriteString(spaced)
		cur.end = append(cur.end, b.Len())
		cur.elided = append(cur.elided, 0)
		cur.inserted = append(cur.inserted, at)
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
	case needsSpace(prev.Text, next.Text, gap, prev.spaceWidthPts()):
		return joinSpace
	default:
		return joinTight
	}
}

// spaceWidthPts returns how wide a space is in a run's own font and size.
func (run *TextRun) spaceWidthPts() float64 {
	if run.font != nil && run.fontSizeRaw != 0 {
		if w, ok := run.font.spaceWidth1000(); ok {
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
		// The reading may hold spaces the run does not, so the offsets
		// are shifted back onto the text the run actually carries.
		var ins []int
		if i < len(c.inserted) {
			ins = c.inserted[i]
		}
		from, to = withoutInserted(from, ins), withoutInserted(to, ins)
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

// readRun returns a run's text as a reader sees it, with the spaces its
// own gaps imply, and where each was put in.
func readRun(run *TextRun) (string, []int) {
	if len(run.spaceAt) == 0 {
		return run.Text, nil
	}
	var b strings.Builder
	at := make([]int, 0, len(run.spaceAt))
	last := 0
	for _, off := range run.spaceAt {
		if off <= last-1 || off > len(run.Text) {
			continue
		}
		b.WriteString(run.Text[last:off])
		at = append(at, b.Len())
		b.WriteByte(' ')
		last = off
	}
	b.WriteString(run.Text[last:])
	return b.String(), at
}

// withoutInserted turns an offset in a run's reading into the offset in
// the run's own text, by discounting the spaces the reading added.
func withoutInserted(off int, inserted []int) int {
	n := 0
	for _, at := range inserted {
		if at < off {
			n++
		}
	}
	return off - n
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
	chains := buildChains(runs)
	for _, chain := range hyphenKeptChains(runs) {
		for _, lit := range rd.literals {
			for _, rg := range literalRanges(chain.text, lit, rd.mode()) {
				if chain.throughKeptHyphen(rg[0], rg[1]) {
					add(chain.chainRanges(rg[0], rg[1]))
				}
			}
		}
	}
	for _, chain := range append(append([]runChain(nil), chains...), hyphenSplitChains(runs)...) {
		for _, lit := range rd.literals {
			for _, rg := range chain.matchRanges(lit, rd.mode()) {
				add(chain.chainRanges(rg[0], rg[1]))
			}
		}
		for _, re := range rd.patterns {
			for _, m := range re.FindAllStringIndex(chain.text, -1) {
				add(chain.chainRanges(m[0], m[1]))
			}
		}
	}
	// And the occurrences a line break runs through: each line gives
	// up the part of the name it holds.
	for _, lit := range rd.literals {
		for _, occ := range acrossChains(chains, lit, rd.mode()) {
			for _, part := range occ {
				add(chains[part.chain].chainRanges(part.start, part.end))
			}
		}
	}
	return found
}

// chainPart is the stretch of one chain that an occurrence running over
// several chains covers.
type chainPart struct {
	chain      int
	start, end int
}

// acrossChains finds the occurrences of a literal that begin in one
// chain and end in another.
//
// A chain is a line, and a name of two words is as often as not broken
// between them: "Marialuisa" ends a line and "Vanetti" begins the next.
// Matching line by line found neither half — neither IS the name — and
// the name stayed on the page, in two pieces any reader joins. So the
// chains are also read one after the other, a space apart, the way the
// page's text is read back, and what is found only in that reading is
// returned with the part each chain holds of it.
//
// Greedy on purpose. Two lines that follow each other in the page's
// instructions and are not one paragraph — the foot of one column, the
// head of the next — can spell a name between them, and then both
// halves go. That is the right way round: anything reading the page
// back joins them just the same.
func acrossChains(chains []runChain, lit string, mode matchMode) [][]chainPart {
	if len(chains) < 2 || !strings.ContainsAny(lit, " \t\u00a0") {
		return nil
	}
	var b strings.Builder
	base := make([]int, len(chains))
	for i, c := range chains {
		// Between two chains what the page's text reads there: on one
		// baseline, a space only where the gap is one — a heading set in
		// pieces whose pen seems to go back ("DEPARTM", "ENT OF CO")
		// reads "DEPARTMENT OF COMMERCE", not "DEPARTM ENT OF CO";
		// between lines, a space; and never a second one beside a space
		// a chain has (" UNITED " and "STATES": "UNITED STATES").
		if i > 0 && !endsInSpace(b.String()) && !startsWithSpace(c.text) && chainsSpaced(chains[i-1], c) {
			b.WriteByte(' ')
		}
		base[i] = b.Len()
		b.WriteString(c.text)
	}
	joined := b.String()
	var out [][]chainPart
	for _, rg := range literalRanges(joined, lit, mode) {
		var parts []chainPart
		for i, c := range chains {
			lo, hi := base[i], base[i]+len(c.text)
			if rg[1] <= lo || rg[0] >= hi {
				continue
			}
			s, e := rg[0], rg[1]
			if s < lo {
				s = lo
			}
			if e > hi {
				e = hi
			}
			if e > s {
				parts = append(parts, chainPart{chain: i, start: s - lo, end: e - lo})
			}
		}
		if len(parts) > 1 {
			out = append(out, parts)
		}
	}
	return out
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

func endsInSpace(s string) bool {
	r, _ := utf8.DecodeLastRuneInString(s)
	return unicode.IsSpace(r)
}

func startsWithSpace(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsSpace(r)
}

// chainsSpaced reports whether the page's text reads a space between
// the end of chain a and the start of chain b: always across lines, and
// on one baseline where the gap is wide enough to be one (needsSpace,
// the extractor's own rule).
func chainsSpaced(a, b runChain) bool {
	if len(a.runs) == 0 || len(b.runs) == 0 {
		return true
	}
	prev, next := a.runs[len(a.runs)-1], b.runs[0]
	size := prev.FontSize
	if size <= 0 {
		size = next.FontSize
	}
	if size <= 0 || next.Y-prev.Y > size*0.3 || prev.Y-next.Y > size*0.3 {
		return true
	}
	return needsSpace(a.text, b.text, next.X-(prev.X+prev.Width), prev.spaceWidthPts())
}
