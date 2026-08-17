package gopdf

import (
	"fmt"
	"sort"
	"strings"
)

// Page labels.
//
// The page a reader calls "iv" is the fourth page of the file, and the
// one they call "1" is often the ninth. A document says so with a page
// label tree: ranges of pages, each with a numbering style, an optional
// prefix, and the number the range starts at.
//
// Without it a viewer numbers the pages 1, 2, 3 and a citation to page
// 12 lands somewhere else. It is a small feature that nothing else can
// substitute for, since the labels are nowhere in the page content.

// PageLabelStyle is how a range of pages is numbered.
type PageLabelStyle string

const (
	// LabelDecimal numbers pages 1, 2, 3.
	LabelDecimal PageLabelStyle = "D"
	// LabelRomanUpper numbers them I, II, III.
	LabelRomanUpper PageLabelStyle = "R"
	// LabelRomanLower numbers them i, ii, iii.
	LabelRomanLower PageLabelStyle = "r"
	// LabelLettersUpper numbers them A, B, C, and after Z carries on
	// AA, BB, CC — which is what the specification asks for, however odd
	// it looks.
	LabelLettersUpper PageLabelStyle = "A"
	// LabelLettersLower numbers them a, b, c.
	LabelLettersLower PageLabelStyle = "a"
	// LabelNone gives the pages of a range no number at all, leaving
	// only the prefix.
	LabelNone PageLabelStyle = ""
)

// PageLabelRange is one run of pages sharing a numbering scheme.
type PageLabelRange struct {
	// From is the zero-based index of the first page in the range.
	From int
	// Style is how the pages are numbered.
	Style PageLabelStyle
	// Prefix is put before the number, where there is one: "A-" gives
	// A-1, A-2.
	Prefix string
	// Start is the number the range counts from, which is 1 unless the
	// document says otherwise.
	Start int
}

// PageLabels returns the label ranges the document defines, in page
// order, or nil if it defines none.
func (r *Reader) PageLabels() []PageLabelRange {
	tree := r.resolve(r.Catalog()["PageLabels"])
	entries := r.numberTree(tree, 0)
	if len(entries) == 0 {
		return nil
	}
	out := make([]PageLabelRange, 0, len(entries))
	for _, e := range entries {
		d, ok := r.resolve(e.value).(Dict)
		if !ok {
			continue
		}
		rg := PageLabelRange{From: e.key, Start: 1}
		if s, ok := r.resolve(d["S"]).(Name); ok {
			rg.Style = PageLabelStyle(s)
		}
		if p, ok := r.resolve(d["P"]).(String); ok {
			rg.Prefix = decodeTextString(p)
		}
		if st, ok := toInt(r.resolve(d["St"])); ok && st > 0 {
			rg.Start = st
		}
		out = append(out, rg)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].From < out[j].From })
	return out
}

// PageLabel returns the label a reader sees for a page: "iv", "A-3",
// "12". A document with no labels numbers its pages from one, which is
// what a viewer shows, so that is what comes back.
func (r *Reader) PageLabel(index int) string {
	if index < 0 || index >= len(r.pages) {
		return ""
	}
	ranges := r.PageLabels()
	if len(ranges) == 0 {
		return fmt.Sprint(index + 1)
	}
	// The range a page belongs to is the last one starting at or before
	// it. A document whose first range starts after page 0 leaves the
	// pages before it unlabelled, and a viewer numbers those plainly.
	at := -1
	for i, rg := range ranges {
		if rg.From <= index {
			at = i
		}
	}
	if at < 0 {
		return fmt.Sprint(index + 1)
	}
	rg := ranges[at]
	return rg.Prefix + formatLabel(rg.Style, rg.Start+index-rg.From)
}

// formatLabel renders one number in a style.
func formatLabel(style PageLabelStyle, n int) string {
	if n < 1 {
		return ""
	}
	switch style {
	case LabelDecimal:
		return fmt.Sprint(n)
	case LabelRomanUpper:
		return roman(n)
	case LabelRomanLower:
		return strings.ToLower(roman(n))
	case LabelLettersUpper:
		return letterLabel(n, 'A')
	case LabelLettersLower:
		return letterLabel(n, 'a')
	}
	return "" // /S absent: the prefix alone is the label
}

// letterLabel is A..Z, then AA..ZZ, then AAA..ZZZ, which is how the
// specification defines it — not the spreadsheet-column ordering.
func letterLabel(n int, base rune) string {
	letter := rune((n - 1) % 26)
	count := (n-1)/26 + 1
	return strings.Repeat(string(base+letter), count)
}

var romanNumerals = []struct {
	value int
	sym   string
}{
	{1000, "M"}, {900, "CM"}, {500, "D"}, {400, "CD"},
	{100, "C"}, {90, "XC"}, {50, "L"}, {40, "XL"},
	{10, "X"}, {9, "IX"}, {5, "V"}, {4, "IV"}, {1, "I"},
}

func roman(n int) string {
	// Beyond a few thousand the numeral stops being a numeral; a page
	// count that high is a decimal page count in practice.
	if n > 3999 {
		return fmt.Sprint(n)
	}
	var b strings.Builder
	for _, r := range romanNumerals {
		for n >= r.value {
			b.WriteString(r.sym)
			n -= r.value
		}
	}
	return b.String()
}

// numberTreeEntry is one integer key and the value it leads to.
type numberTreeEntry struct {
	key   int
	value any
}

// numberTree flattens a number tree, which is a name tree keyed by
// integers: the same /Kids and /Limits, with /Nums where a name tree has
// /Names.
func (r *Reader) numberTree(v any, depth int) []numberTreeEntry {
	if depth > 32 {
		return nil
	}
	node, ok := r.resolve(v).(Dict)
	if !ok {
		return nil
	}
	var out []numberTreeEntry
	if arr, ok := r.resolve(node["Nums"]).(Array); ok {
		for i := 0; i+1 < len(arr); i += 2 {
			key, ok := toInt(r.resolve(arr[i]))
			if !ok {
				continue
			}
			out = append(out, numberTreeEntry{key: key, value: arr[i+1]})
		}
	}
	for _, kid := range arrayOf(r, node["Kids"]) {
		out = append(out, r.numberTree(kid, depth+1)...)
	}
	return out
}

// --- writing ---

// SetPageLabels gives a document being built its page numbering.
//
// Ranges need not be sorted and need not cover page 0; pages before the
// first range are numbered plainly, as a viewer numbers a document with
// no labels at all.
func (d *Document) SetPageLabels(ranges []PageLabelRange) {
	d.pageLabels = append([]PageLabelRange(nil), ranges...)
}

// buildPageLabels returns the number tree for the pending labels, or nil
// if there are none. Like the attachments, it runs before object numbers
// are handed out.
func (d *Document) buildPageLabels() any {
	if len(d.pageLabels) == 0 {
		return nil
	}
	ranges := append([]PageLabelRange(nil), d.pageLabels...)
	sort.SliceStable(ranges, func(i, j int) bool { return ranges[i].From < ranges[j].From })

	nums := make(Array, 0, len(ranges)*2)
	for _, rg := range ranges {
		if rg.From < 0 {
			continue
		}
		nums = append(nums, int64(rg.From), pageLabelDict(rg))
	}
	if len(nums) == 0 {
		return nil
	}
	ref := rawRef(len(d.raw))
	d.raw = append(d.raw, Dict{"Nums": nums})
	return ref
}

// SetPageLabels gives an existing document its page numbering, appended
// incrementally.
func (u *Updater) SetPageLabels(ranges []PageLabelRange) error {
	if len(ranges) == 0 {
		return u.SetCatalogEntry("PageLabels", nil)
	}
	sorted := append([]PageLabelRange(nil), ranges...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].From < sorted[j].From })

	nums := make(Array, 0, len(sorted)*2)
	for _, rg := range sorted {
		if rg.From < 0 {
			continue
		}
		nums = append(nums, int64(rg.From), pageLabelDict(rg))
	}
	if len(nums) == 0 {
		return u.SetCatalogEntry("PageLabels", nil)
	}
	return u.SetCatalogEntry("PageLabels", u.AddObject(Dict{"Nums": nums}))
}

// pageLabelDict is one range as the file stores it.
func pageLabelDict(rg PageLabelRange) Dict {
	d := Dict{}
	if rg.Style != LabelNone {
		d["S"] = Name(rg.Style)
	}
	if rg.Prefix != "" {
		d["P"] = String(textStringBytes(rg.Prefix))
	}
	if rg.Start > 1 {
		d["St"] = int64(rg.Start)
	}
	return d
}
