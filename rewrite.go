package gopdf

import (
	"errors"
	"fmt"
	"io"
	"sort"
)

// Rewriting a document from scratch.
//
// An incremental update appends: the original bytes stay in the file, and
// anything they contained can still be recovered from them. That is the
// right behaviour for editing a signed document and exactly the wrong one
// for removing content. A rewrite instead emits a fresh file holding only
// the objects the document still reaches, so whatever is not written is
// gone.
//
// Object numbers are carried over unchanged, which keeps every reference
// in the graph valid without a renumbering pass.

// rewriter emits a complete file from a reader's object graph, with
// substitutions applied.
type rewriter struct {
	r *Reader
	// replace maps an object number to the object to write in its place.
	replace map[int]any
	// drop lists object numbers to omit even when something points at
	// them. A reference to a dropped object reads as null.
	drop map[int]bool
	// info replaces the document information dictionary; a nil info keeps
	// whatever the source had, unless stripInfo is set.
	info      *Info
	stripInfo bool
}

func newRewriter(r *Reader) *rewriter {
	return &rewriter{
		r:       r,
		replace: make(map[int]any),
		drop:    make(map[int]bool),
	}
}

// object returns what should be written for an object number.
func (rw *rewriter) object(num int) (any, error) {
	if v, ok := rw.replace[num]; ok {
		return v, nil
	}
	return rw.r.object(num)
}

// reachable collects every object number the document still refers to,
// starting from the catalog. Substitutions are followed rather than the
// originals, so a reference a redaction removed does not keep its target
// alive.
func (rw *rewriter) reachable(roots []any) (map[int]bool, error) {
	seen := make(map[int]bool)
	// References are followed through a work list rather than by
	// recursing, so the depth of the graph — a long outline tree, a deep
	// page tree — costs nothing. Only direct nesting is bounded, and the
	// parser bounds that already.
	var pending []int
	var walk func(v any, depth int) error
	walk = func(v any, depth int) error {
		if depth > maxCopyDepth {
			return errors.New("gopdf: object graph too deep to rewrite")
		}
		switch t := v.(type) {
		case Ref:
			if seen[t.Num] || rw.drop[t.Num] {
				return nil
			}
			seen[t.Num] = true
			pending = append(pending, t.Num)
			return nil
		case Dict:
			for _, k := range sortedKeys(t) {
				if err := walk(t[k], depth+1); err != nil {
					return err
				}
			}
		case *rawStream:
			return walk(t.dict, depth+1)
		case Array:
			for _, e := range t {
				if err := walk(e, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, root := range roots {
		if err := walk(root, 0); err != nil {
			return nil, err
		}
	}
	for len(pending) > 0 {
		num := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		obj, err := rw.object(num)
		if err != nil {
			// A broken object costs only itself; the rest of the
			// document is still worth writing.
			continue
		}
		if err := walk(obj, 0); err != nil {
			return nil, err
		}
	}
	delete(seen, 0)
	return seen, nil
}

// writeTo emits the whole file.
func (rw *rewriter) writeTo(w io.Writer) (int64, error) {
	rootRef, ok := rw.r.trailer["Root"].(Ref)
	if !ok {
		return 0, errors.New("gopdf: the catalog is not an indirect object")
	}
	roots := []any{rootRef}

	// The information dictionary is reachable only from the trailer.
	infoRef, hasInfo := rw.r.trailer["Info"].(Ref)
	if hasInfo && !rw.stripInfo && rw.info == nil {
		roots = append(roots, infoRef)
	}

	live, err := rw.reachable(roots)
	if err != nil {
		return 0, err
	}
	nums := make([]int, 0, len(live))
	for num := range live {
		nums = append(nums, num)
	}
	sort.Ints(nums)

	ow := &offsetWriter{w: w}
	ow.printf("%%PDF-%s\n", rw.version())
	// A comment of high bytes marks the file as binary for transfer tools.
	ow.Write([]byte{'%', 0xE2, 0xE3, 0xCF, 0xD3, '\n'})

	ctx := &writeCtx{refsAreLiteral: true}
	offsets := make(map[int]int64, len(nums)+1)
	for _, num := range nums {
		obj, err := rw.object(num)
		if err != nil || obj == nil {
			// Write a placeholder so the number stays defined rather
			// than pointing at nothing.
			obj = nil
		}
		offsets[num] = ow.n
		ow.obj = num
		ow.printf("%d 0 obj\n", num)
		writeObjectBody(ow, obj, ctx)
		ow.str("endobj\n")
	}

	// A replacement information dictionary is written last, with a number
	// of its own when the source had none.
	written := nums
	newInfoNum := 0
	if rw.info != nil {
		newInfoNum = infoRef.Num
		if !hasInfo || live[infoRef.Num] {
			newInfoNum = maxInt(rw.r.maxObjectNumber(), maxOf(nums)) + 1
		}
		offsets[newInfoNum] = ow.n
		ow.obj = newInfoNum
		ow.printf("%d 0 obj\n<<", newInfoNum)
		writeInfoEntry(ow, "Title", rw.info.Title)
		writeInfoEntry(ow, "Author", rw.info.Author)
		writeInfoEntry(ow, "Subject", rw.info.Subject)
		writeInfoEntry(ow, "Keywords", rw.info.Keywords)
		writeInfoEntry(ow, "Creator", rw.info.Creator)
		writeInfoEntry(ow, "Producer", rw.info.Producer)
		ow.str(" >>\nendobj\n")
		written = append(append([]int(nil), nums...), newInfoNum)
		sort.Ints(written)
	}

	xrefAt := ow.n
	rw.writeXref(ow, written, offsets)
	ow.printf("trailer\n<< /Size %d /Root %d 0 R", maxOf(written)+1, rootRef.Num)
	switch {
	case rw.info != nil:
		ow.printf(" /Info %d 0 R", newInfoNum)
	case hasInfo && !rw.stripInfo && live[infoRef.Num]:
		ow.printf(" /Info %d 0 R", infoRef.Num)
	}
	if id, ok := rw.r.trailer["ID"].(Array); ok && len(id) == 2 && !rw.stripInfo {
		ow.str(" /ID ")
		writeValue(ow, id, ctx)
	}
	ow.printf(" >>\nstartxref\n%d\n%%%%EOF\n", xrefAt)
	return ow.n, ow.err
}

// version returns the version to stamp on the rewritten file, never
// lower than the source claimed.
func (rw *rewriter) version() string {
	v := "1.7"
	if len(rw.r.data) >= 8 {
		if got := string(rw.r.data[5:8]); got >= "1.0" && got <= "2.9" {
			v = got
		}
	}
	return v
}

// writeXref emits a classic cross-reference table. Objects that are gone
// are simply left out of the subsections rather than listed as free,
// which avoids having to maintain a free-object chain.
func (rw *rewriter) writeXref(ow *offsetWriter, nums []int, offsets map[int]int64) {
	ow.str("xref\n")
	// The head of the free list is mandatory.
	ow.str("0 1\n0000000000 65535 f \n")
	for i := 0; i < len(nums); {
		j := i + 1
		for j < len(nums) && nums[j] == nums[j-1]+1 {
			j++
		}
		ow.printf("%d %d\n", nums[i], j-i)
		for _, num := range nums[i:j] {
			ow.printf("%010d 00000 n \n", offsets[num])
		}
		i = j
	}
}

// writeObjectBody writes one object's value, streams included.
func writeObjectBody(ow *offsetWriter, v any, ctx *writeCtx) {
	stm, ok := v.(*rawStream)
	if !ok {
		writeValue(ow, v, ctx)
		ow.str("\n")
		return
	}
	dict := cloneDict(stm.dict)
	delete(dict, "Length")
	ow.str("<<")
	for _, k := range sortedKeys(dict) {
		ow.str(" ")
		writeName(ow, k)
		ow.str(" ")
		writeValue(ow, dict[k], ctx)
	}
	ow.printf(" /Length %d >>\nstream\n", len(stm.data))
	ow.Write(stm.data)
	ow.str("\nendstream\n")
}

func maxOf(nums []int) int {
	m := 0
	for _, n := range nums {
		if n > m {
			m = n
		}
	}
	return m
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Rewrite writes a document as a fresh file containing only the objects
// it still reaches. Superseded objects left behind by earlier incremental
// updates are dropped, which both shrinks the file and removes content
// that was replaced but never deleted.
//
// An encrypted source is written unencrypted: the objects are decrypted
// to be read, and re-encrypting them is a separate decision the caller
// should make deliberately.
func Rewrite(r *Reader, w io.Writer) (int64, error) {
	if r == nil {
		return 0, fmt.Errorf("gopdf: no document to rewrite")
	}
	return newRewriter(r).writeTo(w)
}
