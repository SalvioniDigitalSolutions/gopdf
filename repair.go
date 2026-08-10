package gopdf

import (
	"bytes"
	"errors"
)

// Recovering a damaged cross-reference table.
//
// Real files get their offsets wrong: a producer miscounts a header, a
// transfer rewrites line endings, bytes are prepended in front of the
// header. Every serious reader answers the same way — ignore the table and
// find the objects by scanning — and so does this one. Recovery only runs
// once the ordinary path has failed or been caught lying, so a healthy
// file never pays for it.

// patchXref checks every offset the table gives against the bytes it
// points at, and corrects the ones that are wrong from a scan of the
// file. Correcting entries individually keeps whatever the table got
// right, which matters: a scan can be fooled by bytes inside a stream
// that happen to read as an object header, so it is used only where the
// table has already been shown to be wrong.
//
// It reports whether anything had to be corrected.
func (r *Reader) patchXref() bool {
	var bad []int
	for num, e := range r.xref {
		if e.inObjStm || e.offset < 0 {
			continue
		}
		if !r.objectHeaderAt(e.offset, num) {
			bad = append(bad, num)
		}
	}
	if len(bad) == 0 {
		return false
	}
	found := r.scanObjects()
	for _, num := range bad {
		if off, ok := found[num]; ok {
			r.xref[num] = xrefEntry{offset: off}
		} else {
			// Nothing of that number is in the file; it reads as null.
			r.xref[num] = xrefEntry{offset: -1}
		}
	}
	// An object the table never mentioned is still worth having, so long
	// as it does not displace one the table located correctly.
	for num, off := range found {
		if _, ok := r.xref[num]; !ok {
			r.xref[num] = xrefEntry{offset: off}
		}
	}
	return true
}

// objectHeaderAt reports whether "num gen obj" starts at off.
func (r *Reader) objectHeaderAt(off int64, num int) bool {
	if off < 0 || off >= int64(len(r.data)) {
		return false
	}
	p := &parser{data: r.data, pos: int(off)}
	got, err := p.expectInt()
	if err != nil || int(got) != num {
		return false
	}
	if _, err := p.expectInt(); err != nil {
		return false
	}
	return p.expectKeyword("obj") == nil
}

// reconstructXref rebuilds the cross-reference table by scanning the file
// for object headers. Later definitions win, which is what an incremental
// update means. Objects held in object streams are recovered by expanding
// every /ObjStm the scan turns up.
func (r *Reader) reconstructXref() error {
	found := r.scanObjects()
	if len(found) == 0 {
		return errors.New("gopdf: no objects found while repairing the file")
	}

	r.xref = make(map[int]xrefEntry, len(found))
	for num, off := range found {
		r.xref[num] = xrefEntry{offset: off}
	}
	var objStmNums []int
	r.cache = make(map[int]any)
	r.objStms = make(map[int]*objStm)

	// Object streams hide their contents from the scan, so expand them.
	for num := range found {
		if r.objectIsType(num, "ObjStm") {
			objStmNums = append(objStmNums, num)
		}
	}
	for _, num := range objStmNums {
		stm, err := r.loadObjStm(num)
		if err != nil {
			continue // a broken container costs only the objects inside it
		}
		idx := 0
		for inner := range stm.offsets {
			// A directly-defined object outranks one in a stream, since
			// the scan only finds definitions the file really contains.
			if _, ok := found[inner]; !ok {
				r.xref[inner] = xrefEntry{inObjStm: true, stmNum: num, stmIdx: idx}
			}
			idx++
		}
	}

	r.recoverTrailer()
	if r.trailer["Root"] == nil {
		return errors.New("gopdf: no document catalog found while repairing the file")
	}
	return nil
}

// scanObjects finds every "N G obj" header in the file and maps the
// object number to where its header starts. A later definition wins,
// which is what an incremental update means.
func (r *Reader) scanObjects() map[int]int64 {
	found := make(map[int]int64)
	for pos := 0; pos < len(r.data); {
		i := bytes.Index(r.data[pos:], []byte("obj"))
		if i < 0 {
			break
		}
		at := pos + i
		pos = at + 3
		if num, off, ok := objectHeaderBefore(r.data, at); ok {
			found[num] = off
		}
	}
	return found
}

// objectHeaderBefore reads the "N G" preceding an "obj" keyword at at,
// returning the object number and the offset the header starts at.
func objectHeaderBefore(data []byte, at int) (num int, off int64, ok bool) {
	// Only whitespace may separate the generation number from "obj".
	i := at - 1
	if i < 0 || !isWS(data[i]) {
		return 0, 0, false
	}
	for i >= 0 && isWS(data[i]) {
		i--
	}
	genEnd := i + 1
	for i >= 0 && data[i] >= '0' && data[i] <= '9' {
		i--
	}
	genStart := i + 1
	if genStart == genEnd {
		return 0, 0, false
	}
	if i < 0 || !isWS(data[i]) {
		return 0, 0, false
	}
	for i >= 0 && isWS(data[i]) {
		i--
	}
	numEnd := i + 1
	for i >= 0 && data[i] >= '0' && data[i] <= '9' {
		i--
	}
	numStart := i + 1
	if numStart == numEnd || numEnd-numStart > 10 {
		return 0, 0, false
	}
	// The number must start a token, not end a longer one.
	if numStart > 0 && !isWS(data[numStart-1]) && !isDelim(data[numStart-1]) {
		return 0, 0, false
	}
	n := 0
	for _, c := range data[numStart:numEnd] {
		n = n*10 + int(c-'0')
	}
	if n <= 0 || n > maxObjects {
		return 0, 0, false
	}
	return n, int64(numStart), true
}

// objectIsType reports whether an object is a dictionary or stream with
// the given /Type, without disturbing the object cache.
func (r *Reader) objectIsType(num int, want Name) bool {
	e, ok := r.xref[num]
	if !ok || e.inObjStm || e.offset < 0 {
		return false
	}
	// Look only at the object's opening bytes: parsing every object in a
	// damaged file just to find the streams is needless work.
	end := e.offset + 512
	if end > int64(len(r.data)) {
		end = int64(len(r.data))
	}
	window := r.data[e.offset:end]
	if !bytes.Contains(window, []byte("/Type")) {
		return false
	}
	i := bytes.Index(window, []byte("/"+want))
	if i < 0 {
		return false
	}
	// Reject a longer name that merely starts the same way.
	after := i + 1 + len(want)
	return after >= len(window) || isWS(window[after]) || isDelim(window[after])
}

// recoverTrailer finds the document catalog and the encryption
// dictionary. It prefers what a trailer says, and falls back to looking
// for the catalog among the objects the scan found.
func (r *Reader) recoverTrailer() {
	// Later trailers describe the newer state, so search backwards.
	for pos := len(r.data); pos > 0; {
		i := bytes.LastIndex(r.data[:pos], []byte("trailer"))
		if i < 0 {
			break
		}
		pos = i
		p := &parser{data: r.data, pos: i + len("trailer"), r: r}
		d, err := p.next()
		if t, ok := d.(Dict); ok && err == nil {
			r.mergeTrailer(t)
		}
	}
	// A cross-reference stream carries the same entries in its own
	// dictionary, and a repaired file may have no trailer keyword at all.
	if r.trailer["Root"] == nil {
		for num := range r.xref {
			if !r.objectIsType(num, "XRef") {
				continue
			}
			if s, ok := r.resolve(Ref{Num: num}).(*rawStream); ok {
				r.mergeTrailer(s.dict)
			}
		}
	}
	if r.trailer["Root"] == nil {
		for num := range r.xref {
			if r.objectIsType(num, "Catalog") {
				r.trailer["Root"] = Ref{Num: num}
				break
			}
		}
	}
}

func (r *Reader) mergeTrailer(t Dict) {
	for _, k := range []Name{"Root", "Encrypt", "Info", "ID", "Size"} {
		if v, ok := t[k]; ok && r.trailer[k] == nil {
			r.trailer[k] = v
		}
	}
}
