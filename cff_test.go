package gopdf

import (
	"testing"
)

// cffParts pulls apart a CFF table for inspection.
type cffParts struct {
	top         []cffDictEntry
	strings     [][]byte
	gsubrs      [][]byte
	localSubrs  [][]byte
	charStrings [][]byte
	sids        []uint16
}

func inspectCFF(t *testing.T, cff []byte, nGlyphs int) cffParts {
	t.Helper()
	var p cffParts
	name, err := parseCFFIndex(cff, int(cff[2]))
	if err != nil {
		t.Fatal(err)
	}
	top, err := parseCFFIndex(cff, name.end)
	if err != nil {
		t.Fatal(err)
	}
	str, err := parseCFFIndex(cff, top.end)
	if err != nil {
		t.Fatal(err)
	}
	gs, err := parseCFFIndex(cff, str.end)
	if err != nil {
		t.Fatal(err)
	}
	p.strings, p.gsubrs = str.items, gs.items
	if p.top, err = parseCFFDict(top.items[0]); err != nil {
		t.Fatal(err)
	}
	cs := dictEntry(p.top, cffOpCharStrings)
	if cs == nil {
		t.Fatal("no CharStrings entry")
	}
	idx, err := parseCFFIndex(cff, cs.values[0])
	if err != nil {
		t.Fatal(err)
	}
	p.charStrings = idx.items

	if e := dictEntry(p.top, cffOpCharset); e != nil && len(e.values) == 1 && e.values[0] > 2 {
		if p.sids, err = parseCharsetSIDs(cff, e.values[0], nGlyphs); err != nil {
			t.Fatal(err)
		}
	}
	if e := dictEntry(p.top, cffOpPrivate); e != nil && len(e.values) == 2 {
		priv, err := parseCFFDict(cff[e.values[1] : e.values[1]+e.values[0]])
		if err != nil {
			t.Fatal(err)
		}
		if sub := dictEntry(priv, cffOpSubrs); sub != nil && len(sub.values) == 1 {
			li, err := parseCFFIndex(cff, e.values[1]+sub.values[0])
			if err != nil {
				t.Fatal(err)
			}
			p.localSubrs = li.items
		}
	}
	return p
}

func TestCFFSubsetPrunesSubroutines(t *testing.T) {
	font, err := LoadFont(testOTFPath(t))
	if err != nil {
		t.Fatal(err)
	}
	used := map[uint16]bool{}
	for _, r := range "Handgloves" {
		used[font.ttf.cmap[r]] = true
	}
	sub, err := font.ttf.subset(used)
	if err != nil {
		t.Fatal(err)
	}
	again, err := parseTTF(sub)
	if err != nil {
		t.Fatal(err)
	}
	before := inspectCFF(t, font.ttf.tables["CFF "], font.ttf.numGlyphs)
	after := inspectCFF(t, again.tables["CFF "], again.numGlyphs)

	// The indexes keep their length so the numbers charstrings call by
	// stay valid.
	if len(after.localSubrs) != len(before.localSubrs) {
		t.Errorf("local subroutine count changed: %d -> %d",
			len(before.localSubrs), len(after.localSubrs))
	}
	if len(after.gsubrs) != len(before.gsubrs) {
		t.Errorf("global subroutine count changed: %d -> %d",
			len(before.gsubrs), len(after.gsubrs))
	}
	if len(before.localSubrs) == 0 {
		t.Skip("this font has no local subroutines to prune")
	}

	// Whatever the kept glyphs reach must survive byte for byte, and the
	// rest must have shrunk to a bare return.
	reachLocal, _ := reachableSubrs(before.charStrings, before.localSubrs, before.gsubrs, used)
	if reachLocal == nil {
		t.Skip("the charstrings could not be followed; nothing is pruned")
	}
	pruned := 0
	for i := range before.localSubrs {
		if reachLocal[i] {
			if string(after.localSubrs[i]) != string(before.localSubrs[i]) {
				t.Errorf("local subroutine %d was needed but changed", i)
			}
			continue
		}
		if len(after.localSubrs[i]) != 1 || after.localSubrs[i][0] != 11 {
			t.Errorf("unused local subroutine %d is %d bytes, want a bare return",
				i, len(after.localSubrs[i]))
		}
		pruned++
	}
	if pruned == 0 {
		t.Error("no subroutines were pruned")
	}
	t.Logf("pruned %d of %d local subroutines", pruned, len(before.localSubrs))
}

func TestCFFSubsetPrunesGlyphNames(t *testing.T) {
	font, err := LoadFont(testOTFPath(t))
	if err != nil {
		t.Fatal(err)
	}
	used := map[uint16]bool{}
	for _, r := range "Handgloves" {
		used[font.ttf.cmap[r]] = true
	}
	sub, err := font.ttf.subset(used)
	if err != nil {
		t.Fatal(err)
	}
	again, err := parseTTF(sub)
	if err != nil {
		t.Fatal(err)
	}
	before := inspectCFF(t, font.ttf.tables["CFF "], font.ttf.numGlyphs)
	after := inspectCFF(t, again.tables["CFF "], again.numGlyphs)

	if len(before.strings) == 0 {
		t.Skip("this font has no custom glyph names")
	}
	if len(after.strings) >= len(before.strings) {
		t.Errorf("string index still holds %d of %d names",
			len(after.strings), len(before.strings))
	}
	// The charset must still cover every glyph, with dropped ones at
	// .notdef and kept ones still named.
	if len(after.sids) != again.numGlyphs {
		t.Fatalf("charset covers %d glyphs, want %d", len(after.sids), again.numGlyphs)
	}
	for gid := range after.sids {
		if gid == 0 || used[uint16(gid)] {
			continue
		}
		if after.sids[gid] != 0 {
			t.Errorf("dropped glyph %d still names string %d", gid, after.sids[gid])
			break
		}
	}
	for gid := range used {
		if before.sids[gid] != 0 && after.sids[gid] == 0 {
			t.Errorf("kept glyph %d lost its name", gid)
			break
		}
		// A custom name must point inside the rebuilt string index.
		if sid := after.sids[gid]; sid >= cffStandardStrings {
			if int(sid)-cffStandardStrings >= len(after.strings) {
				t.Errorf("glyph %d names string %d, past the end of the index", gid, sid)
			}
		}
	}
	t.Logf("string index %d -> %d names", len(before.strings), len(after.strings))
}

// TestCharstringWalkerHintMask covers the part of the interpreter most
// likely to go wrong: a hint mask's length depends on the hints declared
// before it, and misreading it desynchronises everything after.
func TestCharstringWalkerHintMask(t *testing.T) {
	// Two global subroutines; only the second is called.
	gsubrs := [][]byte{{11}, {11}}
	bias := subrBias(len(gsubrs))

	// 10 20 30 40 hstemhm   -> 2 hints
	// 50 60 hintmask <1 mask byte>
	// <n> callgsubr
	cs := []byte{
		139 + 10, 139 + 20, 139 + 30, 139 + 40, 18, // hstemhm, two hints
		139 + 50, 139 + 60, 19, 0x00, // hintmask, one more hint, one mask byte
		byte(139 + (1 - bias)), 29, // callgsubr for index 1
	}
	w := newCharstringWalker(nil, gsubrs)
	hints := 0
	w.walk(cs, &hints, 0)
	if w.giveUp {
		t.Fatal("the walker gave up on a well-formed charstring")
	}
	if hints != 3 {
		t.Errorf("counted %d hints, want 3", hints)
	}
	if !w.usedGlobal[1] {
		t.Error("the called subroutine was not marked; the hint mask was misread")
	}
	if w.usedGlobal[0] {
		t.Error("an uncalled subroutine was marked")
	}
}

// TestCharstringWalkerGivesUpSafely checks the conservative fallback: a
// charstring it cannot follow means every subroutine is kept.
func TestCharstringWalkerGivesUpSafely(t *testing.T) {
	gsubrs := [][]byte{{11}, {11}}
	// callgsubr with nothing on the stack cannot be followed.
	broken := [][]byte{{29}}
	local, global := reachableSubrs(broken, nil, gsubrs, map[uint16]bool{0: true})
	if local != nil || global != nil {
		t.Error("expected the walker to give up on an unreadable charstring")
	}
	// Giving up must leave the subroutines untouched.
	kept := pruneSubrs(gsubrs, nil)
	for i := range gsubrs {
		if string(kept[i]) != string(gsubrs[i]) {
			t.Errorf("subroutine %d was pruned despite giving up", i)
		}
	}
}

func TestSubrBias(t *testing.T) {
	for _, tc := range []struct{ n, want int }{
		{0, 107}, {1239, 107}, {1240, 1131}, {33899, 1131}, {33900, 32768},
	} {
		if got := subrBias(tc.n); got != tc.want {
			t.Errorf("subrBias(%d) = %d, want %d", tc.n, got, tc.want)
		}
	}
}
