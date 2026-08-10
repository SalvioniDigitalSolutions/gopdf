package gopdf

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// Updater modifies an existing PDF by appending an incremental update:
// the original bytes are written out unchanged and the modifications
// follow, referenced by a new cross-reference section.
//
// This is the highest-fidelity way to change a document. Everything the
// original contains — structure trees, embedded files, optional content,
// annotations, scripts, anything this library does not model — survives
// byte for byte, because it is never rewritten. Rebuilding a document
// with Document.EditPage or ImportPage, by contrast, keeps only what the
// library understands.
//
// An Updater is not safe for concurrent use.
type Updater struct {
	r *Reader

	// changed maps an object number to its replacement value.
	changed map[int]any
	// order preserves a deterministic write order.
	order  []int
	nextID int

	pages     map[int]*UpdatablePage
	info      *Info
	fit       FitMode
	maxExtra  int
	formDirty bool

	// res owns resources created by drawing on updated pages; rs holds
	// their object numbers once the update is being written.
	res *Document
	rs  *resourceSet

	// pageOrder, when set, is the document's new page order.
	pageOrder []int

	// signing holds a pending signature; sigValueNum and sigFieldNum are
	// the objects created for it.
	signing                  *SignOptions
	sigValueNum, sigFieldNum int
}

// Update opens a parsed document for incremental modification.
func Update(r *Reader) *Updater {
	return &Updater{
		r:       r,
		changed: make(map[int]any),
		nextID:  r.maxObjectNumber() + 1,
		pages:   make(map[int]*UpdatablePage),
		fit:     FitAdvance,
	}
}

// set records a replacement for an existing object.
func (u *Updater) set(num int, v any) {
	if _, seen := u.changed[num]; !seen {
		u.order = append(u.order, num)
	}
	u.changed[num] = v
}

// add appends a brand-new object and returns its number.
func (u *Updater) add(v any) int {
	num := u.nextID
	u.nextID++
	u.set(num, v)
	return num
}

// SetFitMode selects how text replacements of a different width are
// fitted back into the layout.
func (u *Updater) SetFitMode(m FitMode) { u.fit = m }

// SetMaxExtraLines allows reflowed paragraphs to grow by up to n lines.
func (u *Updater) SetMaxExtraLines(n int) {
	if n < 0 {
		n = 0
	}
	u.maxExtra = n
}

// SetInfo replaces the document information dictionary.
func (u *Updater) SetInfo(info Info) {
	if info.Producer == "" {
		info.Producer = "gopdf"
	}
	u.info = &info
}

// SetPageRotation sets a page's display rotation in degrees clockwise.
func (u *Updater) SetPageRotation(index, deg int) error {
	if index < 0 || index >= u.r.NumPages() {
		return fmt.Errorf("gopdf: page %d out of range", index)
	}
	num, ok := u.r.pageObjectNumber(index)
	if !ok {
		return fmt.Errorf("gopdf: page %d is not an indirect object", index)
	}
	dict := cloneDict(u.pageDict(num, index))
	dict["Rotate"] = int64(((deg % 360) + 360) % 360 / 90 * 90)
	u.set(num, dict)
	return nil
}

// pageDict returns the working copy of a page dictionary, preferring one
// already modified in this update.
func (u *Updater) pageDict(num, index int) Dict {
	if v, ok := u.changed[num]; ok {
		if d, ok := v.(Dict); ok {
			return d
		}
	}
	return u.r.pages[index].dict
}

func cloneDict(d Dict) Dict {
	out := make(Dict, len(d))
	for k, v := range d {
		out[k] = v
	}
	return out
}

// --- text editing ---

// UpdatablePage is one page of a document being updated incrementally.
type UpdatablePage struct {
	// Page carries the drawing API. Anything drawn is appended to the
	// page as an extra content stream, leaving the original untouched.
	*Page

	u       *Updater
	index   int
	pageNum int
	targets []*editTarget
	runs    []*TextRun
	// contentTarget is the page's own content stream.
	contentTarget *editTarget
	// sourceResources is the page's resource dictionary as the file has
	// it, for merging in anything drawn.
	sourceResources any
	// removed holds the object numbers of annotations this update drops
	// from the page.
	removed map[int]bool
}

// Page prepares a page for text editing. The page's content stream and
// any form XObjects it draws become editable; every other object in the
// file is left alone.
func (u *Updater) Page(index int) (*UpdatablePage, error) {
	if index < 0 || index >= u.r.NumPages() {
		return nil, fmt.Errorf("gopdf: page %d out of range (document has %d pages)",
			index, u.r.NumPages())
	}
	if p, ok := u.pages[index]; ok {
		return p, nil
	}
	pi := u.r.pages[index]
	pageNum, ok := u.r.pageObjectNumber(index)
	if !ok {
		return nil, fmt.Errorf("gopdf: page %d is not an indirect object", index)
	}
	content, err := u.r.pageContent(pi.dict)
	if err != nil {
		return nil, fmt.Errorf("gopdf: reading page %d: %w", index, err)
	}

	p := &UpdatablePage{u: u, index: index, pageNum: pageNum,
		sourceResources: pi.resources}
	p.contentTarget = &editTarget{content: content, resources: pi.resources}
	srcRes, _ := u.r.resolve(pi.resources).(Dict)
	p.Page = u.newDrawingPage(pi, srcRes)

	box := pi.mediaBox
	sc := &runScanner{
		r:        u.r,
		mediaBox: &box,
		targets:  []*editTarget{p.contentTarget},
		seen:     make(map[Ref]bool),
		infos:    make(map[any]*fontInfo),
		// Editing a form XObject rewrites that object in place.
		adoptForm: func(entry any) *rawStream {
			ref, ok := entry.(Ref)
			if !ok {
				return nil // a direct stream cannot be replaced on its own
			}
			if v, seen := u.changed[ref.Num]; seen {
				stm, _ := v.(*rawStream)
				return stm
			}
			src, ok := u.r.resolve(entry).(*rawStream)
			if !ok {
				return nil
			}
			clone := &rawStream{dict: cloneDict(src.dict), data: src.data}
			u.set(ref.Num, clone)
			return clone
		},
	}
	sc.scan(p.contentTarget, pi.resources, identityMatrix, 0)
	p.targets, p.runs = sc.targets, sc.runs
	for _, run := range p.runs {
		run.owner = p
	}

	u.pages[index] = p
	return p, nil
}

// Runs returns the page's text runs, in content order.
func (p *UpdatablePage) Runs() []*TextRun { return p.runs }

// ReplaceText rewrites every occurrence of old on this page, using the
// page's own font so the result renders identically.
func (p *UpdatablePage) ReplaceText(old, new string) (int, error) {
	if old == "" {
		return 0, errors.New("gopdf: ReplaceText called with empty search text")
	}
	return p.ReplaceFunc(func(run *TextRun) (string, bool) {
		if !strings.Contains(run.Text, old) {
			return "", false
		}
		return strings.ReplaceAll(run.Text, old, new), true
	})
}

// ReplaceFunc rewrites the runs for which fn returns true. On error
// nothing is changed.
func (p *UpdatablePage) ReplaceFunc(fn func(*TextRun) (string, bool)) (int, error) {
	n, err := replaceRuns(p.runs, fn, p.u.fit)
	if err != nil {
		return 0, err
	}
	if n > 0 {
		p.u.markContentDirty(p)
	}
	return n, nil
}

// Blocks groups the page's runs into paragraphs for reflowing.
func (p *UpdatablePage) Blocks() []*TextBlock {
	blocks := groupBlocks(p.runs, nil)
	for _, b := range blocks {
		b.onChange = func() { p.u.markContentDirty(p) }
		b.maxExtra = p.u.maxExtra
		b.fit = p.u.fit
	}
	return blocks
}

// ReplaceTextReflow rewrites paragraphs containing old, re-wrapping each
// one across the lines it already occupies.
func (p *UpdatablePage) ReplaceTextReflow(old, new string) (int, error) {
	if old == "" {
		return 0, errors.New("gopdf: ReplaceTextReflow called with empty search text")
	}
	n := 0
	for _, block := range p.Blocks() {
		if !strings.Contains(block.Text, old) {
			continue
		}
		if err := block.SetText(strings.ReplaceAll(block.Text, old, new)); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// markContentDirty records that the page's own content stream changed.
func (u *Updater) markContentDirty(p *UpdatablePage) {
	u.pages[p.index] = p
}

// --- form filling ---

// SetFormValues fills interactive form fields, keeping the form editable.
// Field names are the fully qualified names Reader.FormFields reports.
func (u *Updater) SetFormValues(values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	widgets := u.r.formWidgets()
	if err := validateFormValues(widgets, values); err != nil {
		return err
	}
	root, _ := u.r.resolve(u.r.trailer["Root"]).(Dict)
	acro, _ := u.r.resolve(root["AcroForm"]).(Dict)

	for _, w := range widgets {
		value, changed := values[w.field.Name]
		if !changed {
			continue
		}
		if err := u.applyWidgetValue(w, value, acro); err != nil {
			return err
		}
	}
	u.formDirty = true
	return nil
}

// applyWidgetValue updates a field and its widget in place.
func (u *Updater) applyWidgetValue(w *widget, value string, acro Dict) error {
	fieldRef, okField := w.fieldNode.(Ref)
	widgetRef, okWidget := w.widgetNode.(Ref)
	if !okField {
		return fmt.Errorf("gopdf: form field %q is not an indirect object and cannot be updated in place",
			w.field.Name)
	}
	field := cloneDict(u.workingDict(fieldRef.Num, w.fieldD))
	sameObject := okWidget && widgetRef.Num == fieldRef.Num

	switch w.field.Type {
	case FieldCheckbox, FieldRadio:
		field["V"] = Name(valueOrOff(value))
		state := Name("Off")
		if value != "" && value != "Off" && (w.onState == "" || string(w.onState) == value) {
			state = w.onState
			if state == "" {
				state = Name(value)
			}
		}
		if sameObject {
			field["AS"] = state
			u.set(fieldRef.Num, field)
			return nil
		}
		u.set(fieldRef.Num, field)
		if okWidget {
			wd := cloneDict(u.workingDict(widgetRef.Num, w.dict))
			wd["AS"] = state
			u.set(widgetRef.Num, wd)
		}
		return nil
	}

	field["V"] = String(textStringBytes(value))
	ap, err := u.buildAppearance(w, value, acro)
	if err != nil {
		return err
	}
	if sameObject {
		if ap != nil {
			field["AP"] = Dict{"N": ap}
		}
		u.set(fieldRef.Num, field)
		return nil
	}
	u.set(fieldRef.Num, field)
	if okWidget && ap != nil {
		wd := cloneDict(u.workingDict(widgetRef.Num, w.dict))
		wd["AP"] = Dict{"N": ap}
		u.set(widgetRef.Num, wd)
	}
	return nil
}

// workingDict returns the version of an object already modified in this
// update, or the original.
func (u *Updater) workingDict(num int, fallback Dict) Dict {
	if v, ok := u.changed[num]; ok {
		if d, ok := v.(Dict); ok {
			return d
		}
	}
	return fallback
}

// buildAppearance creates the appearance stream for a filled text or
// choice field, referencing the form's own font directly — in an
// incremental update the original objects are still addressable.
func (u *Updater) buildAppearance(w *widget, value string, acro Dict) (any, error) {
	width := w.rect[2] - w.rect[0]
	height := w.rect[3] - w.rect[1]
	if width <= 0 || height <= 0 {
		return nil, nil
	}
	font, size, color := parseDefaultAppearance(w.da)

	// Resolve the /DA font against the form's default resources.
	name, fontRef := "", any(nil)
	fields := strings.Fields(w.da)
	for i, tok := range fields {
		if tok == "Tf" && i >= 2 {
			name = strings.TrimPrefix(fields[i-2], "/")
		}
	}
	if dr, ok := u.r.resolve(acro["DR"]).(Dict); ok {
		if fonts, ok := u.r.resolve(dr["Font"]).(Dict); ok && name != "" {
			if entry, ok := fonts[Name(name)]; ok {
				fontRef = entry
			}
		}
	}
	if fontRef == nil {
		// Fall back to a Type 1 base font written as a new object.
		name = "GpHelv"
		fontRef = Ref{Num: u.add(Dict{
			"Type": Name("Font"), "Subtype": Name("Type1"),
			"BaseFont": Name(font.name), "Encoding": Name("WinAnsiEncoding"),
		})}
	}

	if size <= 0 {
		size = height * 0.66
		if size > 12 {
			size = 12
		}
		for size > 4 && font.TextWidth(value, size) > width-4 {
			size -= 0.5
		}
	}
	x := 2.0
	switch w.quad {
	case 1:
		x = (width - font.TextWidth(value, size)) / 2
	case 2:
		x = width - 2 - font.TextWidth(value, size)
	}
	if x < 2 {
		x = 2
	}
	y := (height - size*0.72) / 2

	var content strings.Builder
	content.WriteString("/Tx BMC\nq\n")
	fmt.Fprintf(&content, "BT\n/%s %s Tf\n%s rg\n%s %s Td\n(%s) Tj\nET\n",
		name, fl(size), color.components(), fl(x), fl(y),
		escapeString(winAnsiEncode(value)))
	content.WriteString("Q\nEMC")

	stream := &rawStream{
		dict: Dict{
			"Type": Name("XObject"), "Subtype": Name("Form"),
			"BBox":      Array{float64(0), float64(0), width, height},
			"Resources": Dict{"Font": Dict{Name(name): fontRef}},
		},
		data: []byte(content.String()),
	}
	return Ref{Num: u.add(stream)}, nil
}

// --- writing ---

// Save writes the updated document to a file. Pass the path the document
// was read from to update it in place.
func (u *Updater) Save(path string) error {
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// WriteTo writes the original file followed by the appended changes.
func (u *Updater) WriteTo(w io.Writer) (int64, error) {
	if u.signing != nil {
		// A signature covers the finished file, so it is laid out in
		// memory, measured, and patched before anything is emitted.
		var buf bytes.Buffer
		if _, err := u.writeAll(&buf); err != nil {
			return 0, err
		}
		signed, err := u.signBuffer(buf.Bytes(), len(u.r.data))
		if err != nil {
			return 0, err
		}
		n, err := w.Write(signed)
		return int64(n), err
	}
	return u.writeAll(w)
}

// writeAll emits the original bytes followed by the update.
func (u *Updater) writeAll(w io.Writer) (int64, error) {
	// Drawing resources need object numbers before the pages that name
	// them are rebuilt.
	if u.needsResources() {
		u.rs = u.scratch().allocResources(func() int {
			n := u.nextID
			u.nextID++
			return n
		})
	}
	if u.signing != nil && u.sigValueNum == 0 {
		if err := u.prepareSignature(); err != nil {
			return 0, err
		}
	}
	if err := u.materialize(); err != nil {
		return 0, err
	}
	if err := u.rebuildPageTree(); err != nil {
		return 0, err
	}
	if len(u.changed) == 0 && u.info == nil {
		// Nothing changed: the original file is the answer.
		n, err := w.Write(u.r.data)
		return int64(n), err
	}

	ow := &offsetWriter{w: w, crypt: u.r.crypt}
	// The original bytes are reproduced exactly; everything after them is
	// the update.
	ow.Write(u.r.data)
	if ow.err != nil {
		return ow.n, ow.err
	}
	if u.r.data[len(u.r.data)-1] != '\n' {
		ow.str("\n")
	}

	infoNum := 0
	if u.info != nil {
		infoNum = u.nextID
		u.nextID++
	}

	ctx := &writeCtx{refsAreLiteral: true}
	if u.r.crypt != nil {
		ctx.encrypt = func(b []byte) []byte {
			return ow.encryptBytes(b, ow.strMethod())
		}
	}

	nums := append([]int(nil), u.order...)
	sort.Ints(nums)
	offsets := make(map[int]int64, len(nums)+1)

	for _, num := range nums {
		offsets[num] = ow.n
		ow.obj = num
		ow.printf("%d 0 obj\n", num)
		if num == u.sigValueNum && u.signing != nil {
			u.writeSignatureValue(ow, ctx, u.changed[num].(Dict))
			ow.str("endobj\n")
			continue
		}
		switch v := u.changed[num].(type) {
		case *rawStream:
			data := v.data
			dict := cloneDict(v.dict)
			delete(dict, "Length")
			data = ow.encryptBytes(data, ow.stmMethod())
			ow.str("<<")
			for _, k := range sortedKeys(dict) {
				ow.str(" ")
				writeName(ow, k)
				ow.str(" ")
				writeValue(ow, dict[k], ctx)
			}
			ow.printf(" /Length %d >>\nstream\n", len(data))
			ow.Write(data)
			ow.str("\nendstream\n")
		default:
			writeValue(ow, v, ctx)
			ow.str("\n")
		}
		ow.str("endobj\n")
	}

	if u.rs != nil {
		begin := func(num int) {
			offsets[num] = ow.n
			ow.obj = num
			nums = append(nums, num)
			ow.printf("%d 0 obj\n", num)
		}
		end := func() { ow.str("endobj\n") }
		if err := u.rs.write(ow, ctx, begin, end); err != nil {
			return ow.n, err
		}
	}

	if infoNum != 0 {
		offsets[infoNum] = ow.n
		ow.obj = infoNum
		ow.printf("%d 0 obj\n<<", infoNum)
		writeInfoEntry(ow, "Title", u.info.Title)
		writeInfoEntry(ow, "Author", u.info.Author)
		writeInfoEntry(ow, "Subject", u.info.Subject)
		writeInfoEntry(ow, "Keywords", u.info.Keywords)
		writeInfoEntry(ow, "Creator", u.info.Creator)
		writeInfoEntry(ow, "Producer", u.info.Producer)
		ow.str(" >>\nendobj\n")
		nums = append(nums, infoNum)
	}

	if err := u.writeXref(ow, nums, offsets, infoNum); err != nil {
		return ow.n, err
	}
	return ow.n, ow.err
}

// writeXref appends the cross-reference section for the update, in the
// same style the original file used.
func (u *Updater) writeXref(ow *offsetWriter, nums []int, offsets map[int]int64, infoNum int) error {
	sort.Ints(nums)
	xrefOffset := ow.n
	size := u.nextID

	rootRef := u.r.trailer["Root"]
	infoRef := u.r.trailer["Info"]
	if infoNum != 0 {
		infoRef = Ref{Num: infoNum}
	}
	idArr, _ := u.r.resolve(u.r.trailer["ID"]).(Array)

	if u.r.xrefIsStream {
		return u.writeXrefStream(ow, nums, offsets, xrefOffset, size, rootRef, infoRef, idArr)
	}

	ow.str("xref\n")
	// Group consecutive object numbers into subsections.
	for i := 0; i < len(nums); {
		j := i
		for j+1 < len(nums) && nums[j+1] == nums[j]+1 {
			j++
		}
		ow.printf("%d %d\n", nums[i], j-i+1)
		for k := i; k <= j; k++ {
			ow.printf("%010d 00000 n \n", offsets[nums[k]])
		}
		i = j + 1
	}
	ow.printf("trailer\n<< /Size %d", size)
	ctx := &writeCtx{refsAreLiteral: true}
	if rootRef != nil {
		ow.str(" /Root ")
		writeValue(ow, rootRef, ctx)
	}
	if infoRef != nil {
		ow.str(" /Info ")
		writeValue(ow, infoRef, ctx)
	}
	if len(idArr) > 0 {
		ow.str(" /ID ")
		writeValue(ow, idArr, ctx)
	}
	if enc := u.r.trailer["Encrypt"]; enc != nil {
		ow.str(" /Encrypt ")
		writeValue(ow, enc, ctx)
	}
	ow.printf(" /Prev %d >>\nstartxref\n%d\n%%%%EOF\n", u.r.startXref, xrefOffset)
	return ow.err
}

// writeXrefStream appends a cross-reference stream, used when the
// original file has one so the update matches its structure.
func (u *Updater) writeXrefStream(ow *offsetWriter, nums []int, offsets map[int]int64,
	xrefOffset int64, size int, rootRef, infoRef any, idArr Array) error {

	// The xref stream is itself an object, listed in its own table.
	streamNum := u.nextID
	u.nextID++
	size = u.nextID
	entries := append(append([]int(nil), nums...), streamNum)
	sort.Ints(entries)
	offsets[streamNum] = xrefOffset

	var data bytes.Buffer
	var index strings.Builder
	for i := 0; i < len(entries); {
		j := i
		for j+1 < len(entries) && entries[j+1] == entries[j]+1 {
			j++
		}
		fmt.Fprintf(&index, "%d %d ", entries[i], j-i+1)
		for k := i; k <= j; k++ {
			off := offsets[entries[k]]
			data.Write([]byte{
				1,
				byte(off >> 24), byte(off >> 16), byte(off >> 8), byte(off),
				0, 0,
			})
		}
		i = j + 1
	}

	payload := data.Bytes()
	filter := ""
	if compressed, err := flateCompress(payload); err == nil {
		payload = compressed
		filter = "/Filter /FlateDecode "
	}

	ow.obj = streamNum
	ow.printf("%d 0 obj\n<< /Type /XRef /W [1 4 2] /Index [%s] /Size %d",
		streamNum, strings.TrimSpace(index.String()), size)
	ctx := &writeCtx{refsAreLiteral: true}
	if rootRef != nil {
		ow.str(" /Root ")
		writeValue(ow, rootRef, ctx)
	}
	if infoRef != nil {
		ow.str(" /Info ")
		writeValue(ow, infoRef, ctx)
	}
	if len(idArr) > 0 {
		ow.str(" /ID ")
		writeValue(ow, idArr, ctx)
	}
	if enc := u.r.trailer["Encrypt"]; enc != nil {
		ow.str(" /Encrypt ")
		writeValue(ow, enc, ctx)
	}
	// A cross-reference stream is never encrypted.
	ow.printf(" /Prev %d %s/Length %d >>\nstream\n", u.r.startXref, filter, len(payload))
	ow.Write(payload)
	ow.str("\nendstream\nendobj\n")
	ow.printf("startxref\n%d\n%%%%EOF\n", xrefOffset)
	return ow.err
}

// materialize applies pending text edits to the objects they belong to.
func (u *Updater) materialize() error {
	for _, p := range u.pages {
		for _, t := range p.targets {
			if len(t.splices) == 0 {
				continue
			}
			content, err := applySplices(t.content, t.splices)
			if err != nil {
				return err
			}
			t.content = content
			t.splices = nil
			if t.stream != nil {
				// A form XObject: rewrite the object's own stream.
				t.stream.data = content
				delete(t.stream.dict, "Filter")
				delete(t.stream.dict, "DecodeParms")
				if u.r.crypt == nil {
					if compressed, err := flateCompress(content); err == nil {
						t.stream.data = compressed
						t.stream.dict["Filter"] = Name("FlateDecode")
					}
				}
			}
		}
		edited, drawn := p.contentDirty(), p.hasDrawing()
		annotated := p.hasAnnots() || len(p.removed) > 0
		if !edited && !drawn && !annotated {
			continue
		}
		dict := cloneDict(u.pageDict(p.pageNum, p.index))

		if annotated {
			dict["Annots"] = append(p.existingAnnots(), u.annotObjects(p)...)
		}

		// Text edits replace the page's own stream; drawing is appended
		// as a further one, so an untouched original stays untouched.
		if edited {
			dict["Contents"] = Ref{Num: u.add(u.contentStream(p.contentTarget.content))}
		}
		if drawn {
			extra := Ref{Num: u.add(u.contentStream(p.drawnContent()))}
			dict["Contents"] = p.contentsWith(dict["Contents"], extra)
		}
		if u.rs != nil {
			dict["Resources"] = p.mergedResources(u.rs)
		}
		u.set(p.pageNum, dict)
	}
	return nil
}

// contentStream wraps page content as a stream object, compressing it
// unless the document is encrypted, where compression happens before the
// encryption applied at write time.
func (u *Updater) contentStream(data []byte) *rawStream {
	stream := &rawStream{dict: Dict{}, data: data}
	if u.scratch().Compress {
		if compressed, err := flateCompress(data); err == nil {
			stream.data = compressed
			stream.dict["Filter"] = Name("FlateDecode")
		}
	}
	return stream
}

// contentDirty reports whether the page's own stream was rewritten.
func (p *UpdatablePage) contentDirty() bool {
	for _, run := range p.runs {
		if run.replaced && run.target == p.contentTarget {
			return true
		}
	}
	return false
}
