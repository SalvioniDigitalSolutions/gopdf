package gopdf

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"unicode/utf16"
)

// Reader provides read access to an existing PDF file: page inspection,
// text extraction, and importing pages into a Document.
//
// A Reader is not safe for concurrent use.
type Reader struct {
	data    []byte
	xref    map[int]xrefEntry
	trailer Dict
	cache   map[int]any
	loading map[int]bool
	objStms map[int]*objStm
	pages   []pageInfo

	// Set for encrypted files. encryptNum is the object number of the
	// /Encrypt dictionary, whose own strings are never encrypted.
	crypt      *stdCrypt
	encryptNum int

	// startXref is the offset of the most recent cross-reference section
	// and xrefIsStream records its form, so an incremental update can
	// chain onto it in the same style.
	startXref    int64
	xrefIsStream bool

	// pageRefs holds each page's object number, in page order.
	pageRefs []int

	// repaired records that the file's cross-reference table was unusable
	// and the objects were found by scanning instead.
	repaired bool
}

// maxObjectNumber returns the highest object number the file defines.
func (r *Reader) maxObjectNumber() int {
	max := 0
	for num := range r.xref {
		if num > max {
			max = num
		}
	}
	if size, ok := toInt(r.resolve(r.trailer["Size"])); ok && size-1 > max {
		max = size - 1
	}
	return max
}

// pageObjectNumber returns the object number of a page, if the page is an
// indirect object (which it is in every real document).
func (r *Reader) pageObjectNumber(index int) (int, bool) {
	if index < 0 || index >= len(r.pageRefs) || r.pageRefs[index] == 0 {
		return 0, false
	}
	return r.pageRefs[index], true
}

// xrefEntry locates one object: either a byte offset in the file, or a
// position inside an object stream. offset < 0 marks a freed object.
type xrefEntry struct {
	offset   int64
	inObjStm bool
	stmNum   int
	stmIdx   int
}

// objStm is a parsed /ObjStm container.
type objStm struct {
	data    []byte
	first   int
	offsets map[int]int // object number -> offset after first
}

// pageInfo is one leaf of the page tree with inherited attributes applied.
type pageInfo struct {
	dict      Dict
	mediaBox  [4]float64
	resources any
	rotate    int
}

const maxObjects = 1 << 23

// Open reads a PDF file from disk. Encrypted files open when they have an
// empty user password; otherwise Open returns ErrPasswordRequired and the
// file must be opened with OpenPassword.
func Open(path string) (*Reader, error) {
	return OpenPassword(path, "")
}

// OpenPassword reads an encrypted PDF file from disk. Either the user or
// the owner password is accepted.
func OpenPassword(path, password string) (*Reader, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	r, err := NewReaderPassword(data, password)
	if err != nil {
		return nil, fmt.Errorf("%w (%s)", err, path)
	}
	return r, nil
}

// NewReader parses a PDF file held in memory. The Reader keeps a reference
// to data; it must not be modified afterwards.
func NewReader(data []byte) (*Reader, error) {
	return NewReaderPassword(data, "")
}

// NewReaderPassword parses an encrypted PDF file held in memory. Either
// the user or the owner password is accepted.
func NewReaderPassword(data []byte, password string) (*Reader, error) {
	if !bytes.Contains(head(data, 1024), []byte("%PDF-")) {
		return nil, errors.New("gopdf: not a PDF file")
	}
	r := &Reader{
		data:    data,
		xref:    make(map[int]xrefEntry),
		trailer: make(Dict),
		cache:   make(map[int]any),
		loading: make(map[int]bool),
		objStms: make(map[int]*objStm),
	}
	if err := r.load(password); err != nil {
		return nil, err
	}
	return r, nil
}

// load reads the cross-reference table and the page tree. A file whose
// table is missing, unparseable or simply wrong is repaired by scanning
// for the objects, which is the only way some real files can be read at
// all. The error reported on total failure is the first one, since it
// describes the file as its producer left it.
func (r *Reader) load(password string) error {
	err := r.parseXrefChain()
	if err == nil {
		if r.patchXref() {
			r.repaired = true
		}
		err = r.finishLoad(password)
	}
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrPasswordRequired) {
		return err // repairing cannot supply a password
	}
	if rerr := r.repair(password); rerr != nil {
		return err
	}
	r.repaired = true
	return nil
}

// repair throws away what was read and rebuilds it from a scan.
func (r *Reader) repair(password string) error {
	r.xref = make(map[int]xrefEntry)
	r.trailer = make(Dict)
	r.cache = make(map[int]any)
	r.loading = make(map[int]bool)
	r.objStms = make(map[int]*objStm)
	r.crypt, r.encryptNum = nil, 0
	r.pages, r.pageRefs = nil, nil
	if err := r.reconstructXref(); err != nil {
		return err
	}
	return r.finishLoad(password)
}

// finishLoad sets up decryption and walks the page tree.
func (r *Reader) finishLoad(password string) error {
	if encRef := r.trailer["Encrypt"]; encRef != nil {
		if ref, ok := encRef.(Ref); ok {
			r.encryptNum = ref.Num
		}
		// Resolved while r.crypt is still nil, so the dictionary's own
		// strings are read (and cached) undecrypted, as required.
		encDict, ok := r.resolve(encRef).(Dict)
		if !ok {
			return errors.New("gopdf: malformed /Encrypt entry")
		}
		crypt, err := r.newStdCrypt(encDict, password)
		if err != nil {
			return err
		}
		r.crypt = crypt
		// Objects cached during setup were read without decryption.
		for num := range r.cache {
			if num != r.encryptNum {
				delete(r.cache, num)
			}
		}
		r.objStms = make(map[int]*objStm)
	}
	return r.loadPages()
}

// IsEncrypted reports whether the source file was encrypted.
func (r *Reader) IsEncrypted() bool { return r.crypt != nil }

// Repaired reports whether the file's cross-reference table was missing
// or wrong, and the objects had to be found by scanning the file. Such a
// document reads normally, but it was damaged, and anything the scan
// could not reach is gone.
func (r *Reader) Repaired() bool { return r.repaired }

func head(b []byte, n int) []byte {
	if len(b) < n {
		return b
	}
	return b[:n]
}

// NumPages returns the number of pages in the file.
func (r *Reader) NumPages() int {
	return len(r.pages)
}

// PageSize returns the display size of a page in points, accounting for
// the page's rotation.
func (r *Reader) PageSize(index int) (PageSize, error) {
	if index < 0 || index >= len(r.pages) {
		return PageSize{}, fmt.Errorf("gopdf: page %d out of range", index)
	}
	p := r.pages[index]
	w := p.mediaBox[2] - p.mediaBox[0]
	h := p.mediaBox[3] - p.mediaBox[1]
	if p.rotate == 90 || p.rotate == 270 {
		w, h = h, w
	}
	return PageSize{W: w, H: h}, nil
}

// Info returns the document metadata.
func (r *Reader) Info() Info {
	info, _ := r.resolve(r.trailer["Info"]).(Dict)
	get := func(key Name) string {
		s, _ := r.resolve(info[key]).(String)
		return decodeTextString(s)
	}
	return Info{
		Title:    get("Title"),
		Author:   get("Author"),
		Subject:  get("Subject"),
		Keywords: get("Keywords"),
		Creator:  get("Creator"),
		Producer: get("Producer"),
	}
}

// decodeTextString decodes a PDF text string: UTF-16BE when it carries a
// BOM, otherwise (approximately) Latin-1.
func decodeTextString(s String) string {
	if len(s) >= 2 && s[0] == 0xFE && s[1] == 0xFF {
		units := make([]uint16, 0, len(s)/2)
		for i := 2; i+1 < len(s); i += 2 {
			units = append(units, uint16(s[i])<<8|uint16(s[i+1]))
		}
		return string(utf16.Decode(units))
	}
	runes := make([]rune, len(s))
	for i, b := range s {
		runes[i] = rune(b)
	}
	return string(runes)
}

// --- xref parsing ---

func (r *Reader) parseXrefChain() error {
	off, err := r.findStartXref()
	if err != nil {
		return err
	}
	r.startXref = off
	visited := make(map[int64]bool)
	first := true
	for off >= 0 {
		if visited[off] || off >= int64(len(r.data)) {
			break
		}
		visited[off] = true
		p := &parser{data: r.data, pos: int(off), r: r}
		p.skipWS()
		var trailer Dict
		isTable := bytes.HasPrefix(r.data[p.pos:], []byte("xref"))
		if first {
			r.xrefIsStream = !isTable
			first = false
		}
		if isTable {
			trailer, err = r.parseClassicXref(p)
		} else {
			trailer, err = r.parseXrefStreamAt(int(off))
		}
		if err != nil {
			return err
		}
		for k, v := range trailer {
			if _, ok := r.trailer[k]; !ok {
				r.trailer[k] = v
			}
		}
		off = -1
		if prev, ok := toInt(trailer["Prev"]); ok {
			off = int64(prev)
		}
	}
	if r.trailer["Root"] == nil {
		return errors.New("gopdf: no document catalog found")
	}
	return nil
}

func (r *Reader) findStartXref() (int64, error) {
	tail := r.data
	if len(tail) > 2048 {
		tail = tail[len(tail)-2048:]
	}
	i := bytes.LastIndex(tail, []byte("startxref"))
	if i < 0 {
		return 0, errors.New("gopdf: startxref not found")
	}
	p := &parser{data: tail, pos: i + len("startxref")}
	off, err := p.expectInt()
	if err != nil || off < 0 {
		return 0, errors.New("gopdf: invalid startxref")
	}
	return off, nil
}

// setEntry records an xref entry unless a newer table already defined it.
func (r *Reader) setEntry(num int, e xrefEntry) {
	if num < 0 || num > maxObjects {
		return
	}
	if _, ok := r.xref[num]; !ok {
		r.xref[num] = e
	}
}

func (r *Reader) parseClassicXref(p *parser) (Dict, error) {
	if err := p.expectKeyword("xref"); err != nil {
		return nil, err
	}
	for {
		p.skipWS()
		if bytes.HasPrefix(r.data[p.pos:], []byte("trailer")) {
			break
		}
		start, err := p.expectInt()
		if err != nil {
			return nil, errSyntax
		}
		count, err := p.expectInt()
		if err != nil || count < 0 || count > maxObjects {
			return nil, errSyntax
		}
		for i := int64(0); i < count; i++ {
			off, err := p.expectInt()
			if err != nil {
				return nil, errSyntax
			}
			if _, err := p.expectInt(); err != nil { // generation
				return nil, errSyntax
			}
			kind, err := p.next()
			if err != nil {
				return nil, errSyntax
			}
			switch kind {
			case opKeyword("n"):
				// Offset zero is where the header lives, so an in-use
				// entry pointing there names an object that does not
				// exist. Quartz writes these; they read as null.
				if off == 0 {
					off = -1
				}
				r.setEntry(int(start+i), xrefEntry{offset: off})
			case opKeyword("f"):
				r.setEntry(int(start+i), xrefEntry{offset: -1})
			default:
				return nil, errSyntax
			}
		}
	}
	if err := p.expectKeyword("trailer"); err != nil {
		return nil, err
	}
	v, err := p.next()
	if err != nil {
		return nil, err
	}
	trailer, ok := v.(Dict)
	if !ok {
		return nil, errSyntax
	}
	// Hybrid-reference files carry additional entries in an xref stream.
	if xs, ok := toInt(trailer["XRefStm"]); ok {
		if _, err := r.parseXrefStreamAt(xs); err != nil {
			return nil, err
		}
	}
	return trailer, nil
}

func (r *Reader) parseXrefStreamAt(off int) (Dict, error) {
	obj, _, err := r.parseIndirectAt(int64(off))
	if err != nil {
		return nil, err
	}
	stm, ok := obj.(*rawStream)
	if !ok {
		return nil, errors.New("gopdf: xref offset does not point at a table or stream")
	}
	data, err := r.decodeStream(stm.dict, stm.data)
	if err != nil {
		return nil, err
	}
	wArr, ok := r.resolve(stm.dict["W"]).(Array)
	if !ok || len(wArr) < 3 {
		return nil, errSyntax
	}
	var w [3]int
	for i := 0; i < 3; i++ {
		v, ok := toInt(r.resolve(wArr[i]))
		if !ok || v < 0 || v > 8 {
			return nil, errSyntax
		}
		w[i] = v
	}
	rowLen := w[0] + w[1] + w[2]
	if rowLen == 0 {
		return nil, errSyntax
	}
	size, _ := toInt(r.resolve(stm.dict["Size"]))
	index := Array{int64(0), int64(size)}
	if ia, ok := r.resolve(stm.dict["Index"]).(Array); ok {
		index = ia
	}
	pos := 0
	readField := func(n, def int) int64 {
		if n == 0 {
			return int64(def)
		}
		var v int64
		for i := 0; i < n; i++ {
			v = v<<8 | int64(data[pos])
			pos++
		}
		return v
	}
	for i := 0; i+1 < len(index); i += 2 {
		start, ok1 := toInt(index[i])
		count, ok2 := toInt(index[i+1])
		if !ok1 || !ok2 || count < 0 || count > maxObjects {
			return nil, errSyntax
		}
		for j := 0; j < count && pos+rowLen <= len(data); j++ {
			typ := readField(w[0], 1)
			f2 := readField(w[1], 0)
			f3 := readField(w[2], 0)
			num := start + j
			switch typ {
			case 0:
				r.setEntry(num, xrefEntry{offset: -1})
			case 1:
				r.setEntry(num, xrefEntry{offset: f2})
			case 2:
				r.setEntry(num, xrefEntry{inObjStm: true, stmNum: int(f2), stmIdx: int(f3)})
			}
		}
	}
	return stm.dict, nil
}

// --- object loading ---

// object loads and caches the indirect object with the given number.
func (r *Reader) object(num int) (any, error) {
	if v, ok := r.cache[num]; ok {
		return v, nil
	}
	entry, ok := r.xref[num]
	if !ok || (!entry.inObjStm && entry.offset < 0) {
		return nil, nil // free or unknown objects read as null
	}
	if r.loading[num] {
		return nil, errors.New("gopdf: circular object reference")
	}
	r.loading[num] = true
	defer delete(r.loading, num)

	var v any
	var err error
	if entry.inObjStm {
		// Objects inside an object stream are covered by the container's
		// encryption and must not be decrypted again.
		v, err = r.objStmObject(entry.stmNum, num)
	} else {
		var gen int
		v, gen, err = r.parseIndirectAt(entry.offset)
		if err == nil && r.crypt != nil && num != r.encryptNum {
			v, err = r.decryptObject(v, num, gen)
		}
	}
	if err != nil {
		return nil, err
	}
	r.cache[num] = v
	return v, nil
}

// decryptObject decrypts every string in a loaded object, and the data of
// a stream object. Cross-reference streams are exempt by specification.
func (r *Reader) decryptObject(v any, num, gen int) (any, error) {
	switch t := v.(type) {
	case String:
		out, err := r.crypt.decrypt(num, gen, []byte(t), r.crypt.strF)
		if err != nil {
			return nil, err
		}
		return String(out), nil
	case Array:
		for i, e := range t {
			cp, err := r.decryptObject(e, num, gen)
			if err != nil {
				return nil, err
			}
			t[i] = cp
		}
		return t, nil
	case Dict:
		for k, e := range t {
			cp, err := r.decryptObject(e, num, gen)
			if err != nil {
				return nil, err
			}
			t[k] = cp
		}
		return t, nil
	case *rawStream:
		if _, err := r.decryptObject(t.dict, num, gen); err != nil {
			return nil, err
		}
		if r.resolveShallow(t.dict["Type"]) == Name("XRef") {
			return t, nil
		}
		data, err := r.crypt.decrypt(num, gen, t.data, r.crypt.stmF)
		if err != nil {
			return nil, err
		}
		return &rawStream{dict: t.dict, data: data}, nil
	default:
		return v, nil
	}
}

// resolveShallow resolves a value without recursing into the object cache,
// avoiding reentrancy while an object is still being decrypted.
func (r *Reader) resolveShallow(v any) any {
	if _, ok := v.(Ref); ok {
		return nil
	}
	return v
}

// resolve follows indirect references until a direct value is reached.
func (r *Reader) resolve(v any) any {
	for i := 0; i < 32; i++ {
		ref, ok := v.(Ref)
		if !ok {
			return v
		}
		obj, err := r.object(ref.Num)
		if err != nil {
			return nil
		}
		v = obj
	}
	return nil
}

// parseIndirectAt parses "N G obj ... endobj" at a byte offset, returning
// the contained value (or a *rawStream) and the object's generation
// number, which per-object encryption keys depend on.
func (r *Reader) parseIndirectAt(off int64) (any, int, error) {
	if off < 0 || off >= int64(len(r.data)) {
		return nil, 0, errSyntax
	}
	p := &parser{data: r.data, pos: int(off), r: r}
	if _, err := p.expectInt(); err != nil {
		return nil, 0, errSyntax
	}
	gen64, err := p.expectInt()
	if err != nil {
		return nil, 0, errSyntax
	}
	gen := int(gen64)
	if err := p.expectKeyword("obj"); err != nil {
		return nil, 0, err
	}
	v, err := p.next()
	if err != nil {
		return nil, 0, errSyntax
	}
	p.skipWS()
	if !bytes.HasPrefix(r.data[p.pos:], []byte("stream")) {
		return v, gen, nil
	}
	dict, ok := v.(Dict)
	if !ok {
		return nil, 0, errSyntax
	}
	p.pos += len("stream")
	if p.pos < len(r.data) && r.data[p.pos] == '\r' {
		p.pos++
	}
	if p.pos < len(r.data) && r.data[p.pos] == '\n' {
		p.pos++
	}
	start := p.pos

	length, hasLen := toInt(r.resolve(dict["Length"]))
	end := start + length
	if !hasLen || length < 0 || end > len(r.data) || !followedByEndstream(r.data, end) {
		// Broken /Length: recover by scanning for endstream.
		i := bytes.Index(r.data[start:], []byte("endstream"))
		if i < 0 {
			return nil, 0, errSyntax
		}
		end = start + i
		for end > start && (r.data[end-1] == '\n' || r.data[end-1] == '\r') {
			end--
		}
	}
	return &rawStream{dict: dict, data: r.data[start:end]}, gen, nil
}

func followedByEndstream(data []byte, pos int) bool {
	for pos < len(data) && isWS(data[pos]) {
		pos++
	}
	return bytes.HasPrefix(data[pos:], []byte("endstream"))
}

// loadObjStm decodes an /ObjStm container and indexes the objects in it.
func (r *Reader) loadObjStm(stmNum int) (*objStm, error) {
	if stm, ok := r.objStms[stmNum]; ok {
		return stm, nil
	}
	obj, err := r.object(stmNum)
	if err != nil {
		return nil, err
	}
	raw, isStm := obj.(*rawStream)
	if !isStm {
		return nil, errors.New("gopdf: object stream not found")
	}
	data, err := r.decodeStream(raw.dict, raw.data)
	if err != nil {
		return nil, err
	}
	n, _ := toInt(r.resolve(raw.dict["N"]))
	first, _ := toInt(r.resolve(raw.dict["First"]))
	if n < 0 || n > maxObjects || first < 0 || first > len(data) {
		return nil, errSyntax
	}
	stm := &objStm{data: data, first: first, offsets: make(map[int]int, n)}
	hp := &parser{data: data[:first]}
	for i := 0; i < n; i++ {
		num, err1 := hp.expectInt()
		off, err2 := hp.expectInt()
		if err1 != nil || err2 != nil {
			break
		}
		stm.offsets[int(num)] = int(off)
	}
	r.objStms[stmNum] = stm
	return stm, nil
}

// objStmObject extracts an object from an /ObjStm container.
func (r *Reader) objStmObject(stmNum, objNum int) (any, error) {
	stm, err := r.loadObjStm(stmNum)
	if err != nil {
		return nil, err
	}
	off, ok := stm.offsets[objNum]
	if !ok || stm.first+off >= len(stm.data) {
		return nil, nil
	}
	p := &parser{data: stm.data, pos: stm.first + off, r: r}
	v, err := p.next()
	if err != nil {
		return nil, errSyntax
	}
	return v, nil
}

// --- page tree ---

func (r *Reader) loadPages() error {
	root, _ := r.resolve(r.trailer["Root"]).(Dict)
	if root == nil {
		return errors.New("gopdf: invalid document catalog")
	}
	visited := make(map[Ref]bool)
	defaultBox := [4]float64{0, 0, Letter.W, Letter.H}
	var walk func(node any, box [4]float64, res any, rotate int, depth int) error
	walk = func(node any, box [4]float64, res any, rotate int, depth int) error {
		if depth > 64 || len(r.pages) > 1<<16 {
			return errors.New("gopdf: page tree too deep or too large")
		}
		if ref, ok := node.(Ref); ok {
			if visited[ref] {
				return nil
			}
			visited[ref] = true
		}
		d, ok := r.resolve(node).(Dict)
		if !ok {
			return nil
		}
		if mb := r.mediaBox(d["MediaBox"]); mb != nil {
			box = *mb
		}
		if v, ok := d["Resources"]; ok {
			res = v
		}
		if v, ok := toInt(r.resolve(d["Rotate"])); ok {
			rotate = ((v % 360) + 360) % 360
			if rotate%90 != 0 {
				rotate = 0
			}
		}
		kids, hasKids := r.resolve(d["Kids"]).(Array)
		if hasKids && r.resolve(d["Type"]) != Name("Page") {
			for _, kid := range kids {
				if err := walk(kid, box, res, rotate, depth+1); err != nil {
					return err
				}
			}
			return nil
		}
		r.pages = append(r.pages, pageInfo{
			dict: d, mediaBox: box, resources: res, rotate: rotate,
		})
		num := 0
		if ref, ok := node.(Ref); ok {
			num = ref.Num
		}
		r.pageRefs = append(r.pageRefs, num)
		return nil
	}
	if err := walk(root["Pages"], defaultBox, nil, 0, 0); err != nil {
		return err
	}
	if len(r.pages) == 0 {
		return errors.New("gopdf: document has no pages")
	}
	return nil
}

func (r *Reader) mediaBox(v any) *[4]float64 {
	arr, ok := r.resolve(v).(Array)
	if !ok || len(arr) != 4 {
		return nil
	}
	var box [4]float64
	for i, e := range arr {
		f, ok := toFloat(r.resolve(e))
		if !ok {
			return nil
		}
		box[i] = f
	}
	if box[0] > box[2] {
		box[0], box[2] = box[2], box[0]
	}
	if box[1] > box[3] {
		box[1], box[3] = box[3], box[1]
	}
	if box[2]-box[0] <= 0 || box[3]-box[1] <= 0 {
		return nil
	}
	return &box
}

// pageContent returns the page's decoded content streams, concatenated.
func (r *Reader) pageContent(page Dict) ([]byte, error) {
	var parts [][]byte
	appendStream := func(v any) error {
		stm, ok := r.resolve(v).(*rawStream)
		if !ok {
			return nil
		}
		data, err := r.decodeStream(stm.dict, stm.data)
		if err != nil {
			return err
		}
		parts = append(parts, data)
		return nil
	}
	switch c := r.resolve(page["Contents"]).(type) {
	case Array:
		for _, e := range c {
			if err := appendStream(e); err != nil {
				return nil, err
			}
		}
	default:
		if err := appendStream(page["Contents"]); err != nil {
			return nil, err
		}
	}
	return bytes.Join(parts, []byte("\n")), nil
}
