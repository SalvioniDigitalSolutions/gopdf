package gopdf

import (
	"sort"
	"strings"
)

// Finding a literal that spans several boxes.
//
// An engine reports one word at a time, so "Ada Lovelace" is never in a
// single box. Matching each of its words separately finds the name, and
// finds too much besides: every word of every literal becomes a rule of
// its own. Narrowing that with SetOCRWordFilter, by itself, would then
// find too little — the words that may not match alone would stay
// readable in the very place the literal is written out.
//
// So the words are first read the way a person reads them, in order, and
// a literal spelled out by neighbouring words takes all of them: the
// article in the middle, the two-letter suffix at the end. Only a word
// met away from the rest of its literal is left to the one-word rule.
//
// Reading order is worked out from the boxes, and a page can be read
// more than one way: straight across, or down one column and then the
// next. Both are tried, since a letterhead is set in columns and a
// signature line runs across them. Neither is certain. Where the layout
// defeats both, a literal is not recognised as written out, and what
// happens to its words is again for the one-word rule to say.

// ocrPhrases looks for literals spelled out by neighbouring words. It
// returns, for each word, a literal that covers it, or "" where none
// does. Where several cover a word, one that carries a token is
// preferred, so the bar drawn over the word can say what stood there.
func (rd *Redactor) ocrPhrases(words []OCRWord) []string {
	covered := make([]string, len(words))
	if len(rd.literals) == 0 || len(words) == 0 {
		return covered
	}
	hasToken := make(map[string]bool, len(rd.tokens))
	for _, s := range rd.tokens {
		hasToken[s.From] = true
	}
	type span struct{ lo, hi, word int }
	for _, reading := range ocrReadings(words) {
		// The words are joined with single spaces, remembering where
		// each one landed in the joined text.
		var text strings.Builder
		spans := make([]span, 0, len(reading))
		for _, i := range reading {
			w := singleSpaced(words[i].Text)
			if w == "" {
				continue
			}
			if text.Len() > 0 {
				text.WriteByte(' ')
			}
			lo := text.Len()
			text.WriteString(w)
			spans = append(spans, span{lo, text.Len(), i})
		}
		joined := text.String()
		for _, lit := range rd.literals {
			// However the rule spaced its words, the joined text has
			// exactly one space between two of them.
			for _, hit := range literalRanges(joined, singleSpaced(lit), rd.mode()) {
				first := sort.Search(len(spans), func(k int) bool { return spans[k].hi > hit[0] })
				for k := first; k < len(spans) && spans[k].lo < hit[1]; k++ {
					w := spans[k].word
					if covered[w] == "" || (!hasToken[covered[w]] && hasToken[lit]) {
						covered[w] = lit
					}
				}
			}
		}
	}
	return covered
}

// singleSpaced returns s with every run of white space, the non-breaking
// kind included, reduced to one space and none at either end.
func singleSpaced(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// ocrReadings returns the orders in which the words of an image may be
// read, each as a list of indexes into words.
//
// The first reads straight across: every line from its left end to its
// right, the lines from the top down. That is how a signature line with
// two names on one baseline reads, and how a name wraps in a paragraph.
// The others exist only where a line has a wide gap in it, and read each
// run of words between gaps on into the run beneath it — down the
// address block, without the date beside it getting in between.
func ocrReadings(words []OCRWord) [][]int {
	lines := ocrLines(words)
	across := make([]int, 0, len(words))
	for _, line := range lines {
		across = append(across, line...)
	}
	readings := [][]int{across}
	if runs, gapped := ocrRuns(words, lines); gapped {
		readings = append(readings, ocrColumns(runs)...)
	}
	return readings
}

// ocrLines groups words into lines, each from left to right, the lines
// from the top down.
//
// An engine is not asked to report in any order, so the lines are worked
// out from the boxes, and locally: a word joins the line of the nearest
// word to its left that stands level with it. Two words a gap apart
// barely differ in height on a scan turned a degree or two, where the
// two ends of one line differ by a whole line — so asking each word only
// about its neighbour keeps a tilted line in one piece, and keeps it
// apart from the line beneath.
func ocrLines(words []OCRWord) [][]int {
	line := make([]int, len(words)) // union-find: the line a word belongs to
	for i := range line {
		line[i] = i
	}
	find := func(i int) int {
		for line[i] != i {
			line[i] = line[line[i]]
			i = line[i]
		}
		return i
	}
	for i, w := range words {
		nearest := -1
		for j, u := range words {
			if j == i || !leftOf(u, w, j, i) || !level(u, w) {
				continue
			}
			if nearest < 0 || u.X+u.W > words[nearest].X+words[nearest].W {
				nearest = j
			}
		}
		if nearest >= 0 {
			line[find(i)] = find(nearest)
		}
	}
	var lines [][]int
	at := make(map[int]int, len(words)) // a line's root to its place in lines
	for i := range words {
		root := find(i)
		k, ok := at[root]
		if !ok {
			k = len(lines)
			at[root] = k
			lines = append(lines, nil)
		}
		lines[k] = append(lines[k], i)
	}
	for _, l := range lines {
		sort.SliceStable(l, func(a, b int) bool {
			return leftOf(words[l[a]], words[l[b]], l[a], l[b])
		})
	}
	// A line is placed by where it begins. Lines that begin at different
	// margins on a tilted scan may come out of order, which costs a match
	// across that one line break.
	sort.SliceStable(lines, func(a, b int) bool {
		wa, wb := words[lines[a][0]], words[lines[b][0]]
		if ya, yb := 2*wa.Y+wa.H, 2*wb.Y+wb.H; ya != yb {
			return ya < yb
		}
		return wa.X < wb.X
	})
	return lines
}

// leftOf reports whether box a is met before box b reading left to
// right. Boxes centred on the same spot keep the order they came in.
func leftOf(a, b OCRWord, ia, ib int) bool {
	ca, cb := 2*a.X+a.W, 2*b.X+b.W
	if ca != cb {
		return ca < cb
	}
	return ia < ib
}

// level reports whether two boxes stand on the same line: they share at
// least half the height of the taller one. A word without ascenders
// still shares most of its neighbour's height; a speck of noise, or a
// rule that runs down two lines, does not, and so neither joins a line
// nor ties two together.
func level(a, b OCRWord) bool {
	top, bottom := a.Y, a.Y+a.H
	if b.Y > top {
		top = b.Y
	}
	if b.Y+b.H < bottom {
		bottom = b.Y + b.H
	}
	tall := a.H
	if b.H > tall {
		tall = b.H
	}
	return tall > 0 && 2*(bottom-top) >= tall
}

// ocrRun is a stretch of one line with no wide gap in it: a line of one
// column.
type ocrRun struct {
	words          []int
	x0, x1, y0, y1 int
}

// ocrRuns cuts lines into runs wherever two neighbouring words stand
// more than two and a half times their height apart, which is wider than
// justified type is ever spaced and narrower than a gutter. It reports
// whether any line was cut.
func ocrRuns(words []OCRWord, lines [][]int) (runs []ocrRun, gapped bool) {
	for _, line := range lines {
		start := 0
		for k := 1; k <= len(line); k++ {
			if k < len(line) {
				a, b := words[line[k-1]], words[line[k]]
				tall := a.H
				if b.H > tall {
					tall = b.H
				}
				if 2*(b.X-(a.X+a.W)) <= 5*tall {
					continue
				}
				gapped = true
			}
			run := ocrRun{words: line[start:k]}
			for n, i := range run.words {
				w := words[i]
				if n == 0 || w.X < run.x0 {
					run.x0 = w.X
				}
				if n == 0 || w.X+w.W > run.x1 {
					run.x1 = w.X + w.W
				}
				if n == 0 || w.Y < run.y0 {
					run.y0 = w.Y
				}
				if n == 0 || w.Y+w.H > run.y1 {
					run.y1 = w.Y + w.H
				}
			}
			runs = append(runs, run)
			start = k
		}
	}
	return runs, gapped
}

// ocrColumns strings runs together downwards: each run is followed by
// the one beneath it, where there is exactly one. A heading over two
// columns has two runs beneath it and is followed by neither, so each
// column starts afresh.
func ocrColumns(runs []ocrRun) [][]int {
	above := make([]int, len(runs))
	beneath := make([]int, len(runs)) // how many runs name this one as above
	for t := range runs {
		above[t] = -1
		for s := range runs {
			if s == t || !runs[s].over(runs[t]) {
				continue
			}
			if above[t] < 0 || runs[s].y0+runs[s].y1 > runs[above[t]].y0+runs[above[t]].y1 {
				above[t] = s
			}
		}
		if above[t] >= 0 {
			beneath[above[t]]++
		}
	}
	next := make([]int, len(runs))
	for s := range next {
		next[s] = -1
	}
	continues := func(t int) bool { return above[t] >= 0 && beneath[above[t]] == 1 }
	for t := range runs {
		if continues(t) {
			next[above[t]] = t
		}
	}
	var columns [][]int
	for s := range runs {
		if continues(s) {
			continue
		}
		var column []int
		for k := s; k >= 0; k = next[k] {
			column = append(column, runs[k].words...)
		}
		// One word is no run of words; reading across already has it.
		if len(column) > 1 {
			columns = append(columns, column)
		}
	}
	return columns
}

// over reports whether r stands above t and over it: higher up, with
// some part of it straight above some part of t.
func (r ocrRun) over(t ocrRun) bool {
	if r.y0+r.y1 >= t.y0+t.y1 {
		return false
	}
	return r.x0 < t.x1 && t.x0 < r.x1
}
