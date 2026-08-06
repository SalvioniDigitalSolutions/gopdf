package gopdf

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"sort"
	"strings"
	"unicode/utf16"
)

// fontRefs holds the object numbers backing one registered font. Standard
// fonts use a single object; embedded TrueType fonts use five.
type fontRefs struct {
	font       int // Type1 dict, or Type0 dict for embedded fonts
	cid        int
	descriptor int
	fontFile   int
	toUnicode  int
}

// flatOutline is an outline tree node with its object-number wiring.
type flatOutline struct {
	o           *Outline
	num         int
	parent      int
	prev, next  int
	first, last int
	count       int
}

// WriteTo serializes the document as a complete PDF file.
func (d *Document) WriteTo(w io.Writer) (int64, error) {
	if len(d.pages) == 0 {
		return 0, errors.New("gopdf: document has no pages")
	}
	// Materialize pending text edits before anything is serialized.
	for _, e := range d.editables {
		if err := e.flush(); err != nil {
			return 0, err
		}
	}

	// Assign every object number up front so objects can reference each
	// other while being written in a single pass.
	const catalogNum, pagesNum, infoNum = 1, 2, 3
	next := 4
	alloc := func() int {
		n := next
		next++
		return n
	}

	docID := d.documentID()
	var crypt *stdCrypt
	encryptNum := 0
	if d.encryptSetup != nil {
		var err error
		if crypt, err = d.encryptSetup.build(docID); err != nil {
			return 0, err
		}
		encryptNum = alloc()
	}

	fontNums := make([]fontRefs, len(d.fonts))
	for i, f := range d.fonts {
		fontNums[i] = fontRefs{font: alloc()}
		if f.ttf != nil {
			fontNums[i].cid = alloc()
			fontNums[i].descriptor = alloc()
			fontNums[i].fontFile = alloc()
			fontNums[i].toUnicode = alloc()
		}
	}

	imageNums := make([]int, len(d.images))
	smaskNums := make([]int, len(d.images))
	for i, img := range d.images {
		imageNums[i] = alloc()
		if img.smask != nil {
			smaskNums[i] = alloc()
		}
	}

	gsNums := make([]int, len(d.alphas))
	for i := range d.alphas {
		gsNums[i] = alloc()
	}

	shadingNums := make([]int, len(d.shadings))
	shadingFuncNums := make([]int, len(d.shadings))
	for i := range d.shadings {
		shadingNums[i] = alloc()
		shadingFuncNums[i] = alloc()
	}

	xobjNums := make([]int, len(d.xobjects))
	for i := range d.xobjects {
		xobjNums[i] = alloc()
	}
	rawNums := make([]int, len(d.raw))
	for i := range d.raw {
		rawNums[i] = alloc()
	}
	rawNum := func(rr rawRef) int {
		if int(rr) < len(rawNums) {
			return rawNums[rr]
		}
		return 0
	}

	var outlineRootNum int
	var flatOutlines []*flatOutline
	if len(d.outlines) > 0 {
		outlineRootNum = alloc()
		var walk func(items []*Outline, parentNum int) (first, last, total int)
		walk = func(items []*Outline, parentNum int) (int, int, int) {
			nums := make([]int, len(items))
			entries := make([]*flatOutline, len(items))
			for i := range items {
				nums[i] = alloc()
			}
			for i, o := range items {
				e := &flatOutline{o: o, num: nums[i], parent: parentNum}
				if i > 0 {
					e.prev = nums[i-1]
				}
				if i < len(items)-1 {
					e.next = nums[i+1]
				}
				entries[i] = e
				flatOutlines = append(flatOutlines, e)
			}
			total := len(items)
			for i, o := range items {
				f, l, t := walk(o.children, nums[i])
				entries[i].first, entries[i].last, entries[i].count = f, l, t
				total += t
			}
			if len(nums) == 0 {
				return 0, 0, 0
			}
			return nums[0], nums[len(nums)-1], total
		}
		first, last, total := walk(d.outlines, outlineRootNum)
		// Stash the root's wiring in a sentinel entry written specially.
		flatOutlines = append([]*flatOutline{{num: outlineRootNum, first: first, last: last, count: total}}, flatOutlines...)
	}

	// Authored form fields: one object per field, plus a separate object
	// for each widget of a multi-widget field, plus its appearances.
	for _, f := range d.acroFields {
		f.num = alloc()
		for _, w := range f.widgets {
			if len(f.widgets) == 1 {
				w.num = f.num // the field is its own widget
			} else {
				w.num = alloc()
			}
			for _, state := range w.order {
				w.apNums[state] = alloc()
			}
		}
	}

	pageNums := make([]int, len(d.pages))
	contentNums := make([]int, len(d.pages))
	annotNums := make([][]int, len(d.pages))
	pageIndex := make(map[*Page]int, len(d.pages))
	for i, p := range d.pages {
		pageNums[i] = alloc()
		contentNums[i] = alloc()
		annotNums[i] = make([]int, len(p.links))
		for j := range p.links {
			annotNums[i][j] = alloc()
		}
		pageIndex[p] = i
	}
	totalObjs := next - 1

	ow := &offsetWriter{w: w, crypt: crypt}
	version := "1.4"
	if crypt != nil && crypt.r >= 6 {
		version = "2.0" // AES-256 requires PDF 2.0
	}
	// The second comment line carries high bytes so transfer tools treat
	// the file as binary.
	ow.printf("%%PDF-%s\n%%\xe2\xe3\xcf\xd3\n", version)

	offsets := make([]int64, totalObjs+1)
	beginObj := func(num int) {
		offsets[num] = ow.n
		ow.obj = num
		ow.printf("%d 0 obj\n", num)
	}
	endObj := func() {
		ow.str("endobj\n")
	}
	d.fontNums = make([]int, len(fontNums))
	for i, fr := range fontNums {
		d.fontNums[i] = fr.font
	}
	ctx := &writeCtx{
		num: rawNum,
		fontNum: func(fr docFontRef) int {
			if int(fr) < len(fontNums) {
				return fontNums[fr].font
			}
			return 0
		},
	}
	if crypt != nil {
		ctx.encrypt = func(b []byte) []byte {
			return ow.encryptBytes(b, ow.strMethod())
		}

		// The /Encrypt dictionary is never itself encrypted.
		beginObj(encryptNum)
		saved := ow.crypt
		ow.crypt = nil
		writeValue(ow, crypt.encDict, &writeCtx{})
		ow.crypt = saved
		ow.str("\n")
		endObj()
	}

	beginObj(catalogNum)
	ow.printf("<< /Type /Catalog /Pages %d 0 R", pagesNum)
	if outlineRootNum != 0 {
		ow.printf(" /Outlines %d 0 R /PageMode /UseOutlines", outlineRootNum)
	}
	if d.acroForm != nil || len(d.acroFields) > 0 {
		ow.str(" /AcroForm ")
		d.writeAcroForm(ow, ctx, fontNums)
	}
	ow.str(" >>\n")
	endObj()

	beginObj(pagesNum)
	ow.str("<< /Type /Pages /Kids [")
	for i, num := range pageNums {
		if i > 0 {
			ow.str(" ")
		}
		ow.printf("%d 0 R", num)
	}
	ow.printf("] /Count %d >>\n", len(d.pages))
	endObj()

	beginObj(infoNum)
	ow.str("<<")
	writeInfoEntry(ow, "Title", d.info.Title)
	writeInfoEntry(ow, "Author", d.info.Author)
	writeInfoEntry(ow, "Subject", d.info.Subject)
	writeInfoEntry(ow, "Keywords", d.info.Keywords)
	writeInfoEntry(ow, "Creator", d.info.Creator)
	writeInfoEntry(ow, "Producer", d.info.Producer)
	if !d.CreationDate.IsZero() {
		ow.printf(" /CreationDate %s", pdfDate(d.CreationDate))
	}
	ow.str(" >>\n")
	endObj()

	for i, f := range d.fonts {
		if f.ttf == nil {
			beginObj(fontNums[i].font)
			ow.printf("<< /Type /Font /Subtype /Type1 /BaseFont /%s", f.name)
			if f.winAnsi {
				ow.str(" /Encoding /WinAnsiEncoding")
			}
			ow.str(" >>\n")
			endObj()
			continue
		}
		if err := d.writeEmbeddedFont(ow, f, fontNums[i], d.fontUsage[i], beginObj, endObj); err != nil {
			return ow.n, err
		}
	}

	for i, img := range d.images {
		beginObj(imageNums[i])
		extra := fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /%s /BitsPerComponent 8 ",
			img.width, img.height, img.colorSpace)
		if img.smask != nil {
			extra += fmt.Sprintf("/SMask %d 0 R ", smaskNums[i])
		}
		if img.invert {
			extra += "/Decode [1 0 1 0 1 0 1 0] "
		}
		if img.dct {
			// JPEG data is embedded as-is and must not be
			// double-compressed (but is still encrypted).
			extra += "/Filter /DCTDecode "
			data := ow.encryptBytes(img.data, ow.stmMethod())
			ow.printf("<< %s/Length %d >>\nstream\n", extra, len(data))
			ow.Write(data)
			ow.str("\nendstream\n")
		} else if err := ow.writeStream(extra, img.data, d.Compress); err != nil {
			return ow.n, err
		}
		endObj()

		if img.smask != nil {
			beginObj(smaskNums[i])
			extra := fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceGray /BitsPerComponent 8 ",
				img.width, img.height)
			if err := ow.writeStream(extra, img.smask, d.Compress); err != nil {
				return ow.n, err
			}
			endObj()
		}
	}

	for i, a := range d.alphas {
		beginObj(gsNums[i])
		ow.printf("<< /Type /ExtGState /ca %s /CA %s >>\n", fl(a.fill), fl(a.stroke))
		endObj()
	}

	for i, s := range d.shadings {
		beginObj(shadingNums[i])
		d.writeShading(ow, s, shadingFuncNums[i])
		endObj()
		beginObj(shadingFuncNums[i])
		ow.str(shadingFunction(s) + "\n")
		endObj()
	}

	for i, xo := range d.xobjects {
		beginObj(xobjNums[i])
		var extra strings.Builder
		fmt.Fprintf(&extra, "/Type /XObject /Subtype /Form /BBox [%s %s %s %s] ",
			fl(xo.bbox[0]), fl(xo.bbox[1]), fl(xo.bbox[2]), fl(xo.bbox[3]))
		if xo.resources != nil {
			extra.WriteString("/Resources ")
			writeValue(&extra, xo.resources, ctx)
			extra.WriteString(" ")
		}
		if xo.group != nil {
			extra.WriteString("/Group ")
			writeValue(&extra, xo.group, ctx)
			extra.WriteString(" ")
		}
		if err := ow.writeStream(extra.String(), xo.content, d.Compress); err != nil {
			return ow.n, err
		}
		endObj()
	}

	for i, v := range d.raw {
		beginObj(rawNums[i])
		if rs, ok := v.(*rawStream); ok {
			// Copied streams keep their original encoding; only the
			// length is recomputed.
			ow.str("<<")
			for _, k := range sortedKeys(rs.dict) {
				ow.str(" ")
				writeName(ow, k)
				ow.str(" ")
				writeValue(ow, rs.dict[k], ctx)
			}
			// Imported streams keep their original encoding; only the
			// length is recomputed (and encryption re-applied).
			data := ow.encryptBytes(rs.data, ow.stmMethod())
			ow.printf(" /Length %d >>\nstream\n", len(data))
			ow.Write(data)
			ow.str("\nendstream\n")
		} else {
			writeValue(ow, v, ctx)
			ow.str("\n")
		}
		endObj()
	}

	for i, e := range flatOutlines {
		beginObj(e.num)
		if i == 0 { // sentinel: the outline tree root
			ow.printf("<< /Type /Outlines /First %d 0 R /Last %d 0 R /Count %d >>\n", e.first, e.last, e.count)
			endObj()
			continue
		}
		ow.str("<< /Title ")
		ow.pdfString(e.o.title)
		ow.printf(" /Parent %d 0 R", e.parent)
		if e.prev != 0 {
			ow.printf(" /Prev %d 0 R", e.prev)
		}
		if e.next != 0 {
			ow.printf(" /Next %d 0 R", e.next)
		}
		if e.first != 0 {
			ow.printf(" /First %d 0 R /Last %d 0 R /Count %d", e.first, e.last, e.count)
		}
		if idx, ok := pageIndex[e.o.page]; ok {
			ow.printf(" /Dest [%d 0 R /XYZ null %s null]", pageNums[idx], fl(e.o.page.h-e.o.y))
		}
		ow.str(" >>\n")
		endObj()
	}

	// ownEntries lists this library's resources for one category, using
	// the page's name prefix.
	ownEntries := func(prefix, category string) string {
		var b strings.Builder
		switch category {
		case "Font":
			for i, fr := range fontNums {
				fmt.Fprintf(&b, " /%s%s%d %d 0 R", prefix, "F", i+1, fr.font)
			}
		case "XObject":
			for i, num := range imageNums {
				fmt.Fprintf(&b, " /%s%s%d %d 0 R", prefix, "I", i+1, num)
			}
			for i, num := range xobjNums {
				fmt.Fprintf(&b, " /%s%s%d %d 0 R", prefix, "X", i+1, num)
			}
		case "ExtGState":
			for i, num := range gsNums {
				fmt.Fprintf(&b, " /%s%s%d %d 0 R", prefix, "GS", i+1, num)
			}
		case "Shading":
			for i, num := range shadingNums {
				fmt.Fprintf(&b, " /%s%s%d %d 0 R", prefix, "Sh", i+1, num)
			}
		}
		return b.String()
	}

	// Pages created by this library share one resource dictionary listing
	// every font, image and graphics state in the document.
	var res strings.Builder
	res.WriteString("/ProcSet [/PDF /Text /ImageB /ImageC]")
	if len(d.fonts) > 0 {
		res.WriteString(" /Font <<" + ownEntries("", "Font") + " >>")
	}
	if len(d.images)+len(d.xobjects) > 0 {
		res.WriteString(" /XObject <<" + ownEntries("", "XObject") + " >>")
	}
	if len(d.alphas) > 0 {
		res.WriteString(" /ExtGState <<" + ownEntries("", "ExtGState") + " >>")
	}
	if len(d.shadings) > 0 {
		res.WriteString(" /Shading <<" + ownEntries("", "Shading") + " >>")
	}

	// writePageResources emits the resource dictionary for one page. An
	// imported page keeps its own dictionary, with this library's entries
	// merged in under prefixed names.
	writePageResources := func(p *Page) {
		if p.ownResources == nil {
			ow.printf("<< %s >>", res.String())
			return
		}
		ow.str("<<")
		for _, k := range sortedKeys(p.ownResources) {
			own := ownEntries(p.resPrefix, string(k))
			sub, isDict := p.ownResources[k].(Dict)
			if own == "" || !isDict {
				ow.str(" ")
				writeName(ow, k)
				ow.str(" ")
				writeValue(ow, p.ownResources[k], ctx)
				continue
			}
			// Merge our entries into the source's category dictionary.
			ow.str(" ")
			writeName(ow, k)
			ow.str(" <<")
			for _, sk := range sortedKeys(sub) {
				ow.str(" ")
				writeName(ow, sk)
				ow.str(" ")
				writeValue(ow, sub[sk], ctx)
			}
			ow.printf("%s >>", own)
		}
		for _, cat := range []string{"Font", "XObject", "ExtGState", "Shading"} {
			if _, exists := p.ownResources[Name(cat)]; exists {
				continue
			}
			if own := ownEntries(p.resPrefix, cat); own != "" {
				ow.printf(" /%s <<%s >>", cat, own)
			}
		}
		if _, exists := p.ownResources["ProcSet"]; !exists {
			ow.str(" /ProcSet [/PDF /Text /ImageB /ImageC]")
		}
		ow.str(" >>")
	}

	for i, p := range d.pages {
		beginObj(pageNums[i])
		box := [4]float64{0, 0, p.w, p.h}
		if p.mediaBox != nil {
			box = *p.mediaBox
		}
		ow.printf("<< /Type /Page /Parent %d 0 R /MediaBox [%s %s %s %s] /Resources ",
			pagesNum, fl(box[0]), fl(box[1]), fl(box[2]), fl(box[3]))
		writePageResources(p)
		ow.printf(" /Contents %d 0 R", contentNums[i])
		if p.rotate != 0 {
			ow.printf(" /Rotate %d", p.rotate)
		}
		// Widgets of authored form fields belong to this page's /Annots.
		var fieldWidgets []int
		for _, f := range d.acroFields {
			for _, wd := range f.widgets {
				if wd.page == p {
					fieldWidgets = append(fieldWidgets, wd.num)
				}
			}
		}
		if len(p.links)+len(p.rawAnnots)+len(fieldWidgets) > 0 {
			ow.str(" /Annots [")
			sep := ""
			for _, num := range annotNums[i] {
				ow.printf("%s%d 0 R", sep, num)
				sep = " "
			}
			for _, a := range p.rawAnnots {
				ow.str(sep)
				writeValue(ow, a, ctx)
				sep = " "
			}
			for _, num := range fieldWidgets {
				ow.printf("%s%d 0 R", sep, num)
				sep = " "
			}
			ow.str("]")
		}
		ow.str(" >>\n")
		endObj()

		beginObj(contentNums[i])
		if err := ow.writeStream("", p.content(), d.Compress); err != nil {
			return ow.n, err
		}
		endObj()

		for j, l := range p.links {
			beginObj(annotNums[i][j])
			ow.printf("<< /Type /Annot /Subtype /Link /Rect [%s %s %s %s] /Border [0 0 0]",
				fl(l.x), fl(p.h-l.y-l.h), fl(l.x+l.w), fl(p.h-l.y))
			if l.url != "" {
				ow.str(" /A << /S /URI /URI ")
				ow.pdfString(l.url)
				ow.str(" >>")
			} else if idx, ok := pageIndex[l.target]; ok {
				ow.printf(" /Dest [%d 0 R /XYZ null %s null]", pageNums[idx], fl(l.target.h-l.targetY))
			}
			ow.str(" >>\n")
			endObj()
		}
	}

	for _, f := range d.acroFields {
		if err := d.writeAcroField(ow, ctx, f, beginObj, endObj); err != nil {
			return ow.n, err
		}
	}

	xrefOffset := ow.n
	ow.printf("xref\n0 %d\n", totalObjs+1)
	ow.str("0000000000 65535 f \n")
	for num := 1; num <= totalObjs; num++ {
		ow.printf("%010d 00000 n \n", offsets[num])
	}
	ow.printf("trailer\n<< /Size %d /Root %d 0 R /Info %d 0 R",
		totalObjs+1, catalogNum, infoNum)
	// The file identifier is not encrypted, even in a protected document.
	ow.printf(" /ID [<%X> <%X>]", docID, docID)
	if crypt != nil {
		ow.printf(" /Encrypt %d 0 R", encryptNum)
	}
	ow.printf(" >>\nstartxref\n%d\n%%%%EOF\n", xrefOffset)

	return ow.n, ow.err
}

// documentID derives the file identifier from the document's content, so
// that saving the same document twice yields the same identifier.
func (d *Document) documentID() []byte {
	h := sha256.New()
	fmt.Fprintf(h, "gopdf|%s|%s|%s|%s|%s|%s|%d|%d|%d",
		d.info.Title, d.info.Author, d.info.Subject, d.info.Keywords,
		d.info.Creator, d.info.Producer,
		d.CreationDate.Unix(), len(d.pages), len(d.raw))
	for _, p := range d.pages {
		content := p.content()
		fmt.Fprintf(h, "|%s %s %d", fl(p.w), fl(p.h), len(content))
		h.Write(content)
	}
	return h.Sum(nil)[:16]
}

func writeInfoEntry(ow *offsetWriter, key, value string) {
	if value != "" {
		ow.printf(" /%s ", key)
		ow.pdfString(value)
	}
}

// writeEmbeddedFont emits the five objects backing an embedded TrueType
// font: a Type0 dictionary, its CIDFontType2 descendant, the font
// descriptor, the subset font program, and a ToUnicode CMap.
func (d *Document) writeEmbeddedFont(ow *offsetWriter, f *Font, refs fontRefs, usage map[uint16]rune, beginObj func(int), endObj func()) error {
	t := f.ttf
	gids := make([]uint16, 0, len(usage))
	used := make(map[uint16]bool, len(usage))
	for gid := range usage {
		gids = append(gids, gid)
		used[gid] = true
	}
	sort.Slice(gids, func(i, j int) bool { return gids[i] < gids[j] })

	subsetBytes, err := t.subset(used)
	if err != nil {
		return err
	}
	base := subsetTag(f.name, gids) + "+" + f.name

	beginObj(refs.font)
	ow.printf("<< /Type /Font /Subtype /Type0 /BaseFont /%s /Encoding /Identity-H /DescendantFonts [%d 0 R] /ToUnicode %d 0 R >>\n",
		base, refs.cid, refs.toUnicode)
	endObj()

	// CFF outlines are described as CIDFontType0; TrueType as type 2.
	cidSubtype := "CIDFontType2"
	if t.cff {
		cidSubtype = "CIDFontType0"
	}
	beginObj(refs.cid)
	ow.printf("<< /Type /Font /Subtype /%s /BaseFont /%s /CIDSystemInfo << /Registry ", cidSubtype, base)
	ow.pdfString("Adobe")
	ow.str(" /Ordering ")
	ow.pdfString("Identity")
	ow.printf(" /Supplement 0 >> /FontDescriptor %d 0 R /DW 1000", refs.descriptor)
	if !t.cff {
		// CIDToGIDMap applies only to TrueType-outlined CID fonts.
		ow.str(" /CIDToGIDMap /Identity")
	}
	if len(gids) > 0 {
		// Group consecutive glyph IDs into single /W entries.
		ow.str(" /W [")
		for i := 0; i < len(gids); {
			j := i
			for j+1 < len(gids) && gids[j+1] == gids[j]+1 {
				j++
			}
			ow.printf("%d [", gids[i])
			for k := i; k <= j; k++ {
				if k > i {
					ow.str(" ")
				}
				ow.printf("%d", t.toEm(int(t.advances[gids[k]])))
			}
			ow.str("] ")
			i = j + 1
		}
		ow.str("]")
	}
	ow.str(" >>\n")
	endObj()

	flags := 4 // symbolic: the font carries its own character map
	if t.fixedPitch {
		flags |= 1
	}
	fontFileKey := "FontFile2"
	if t.cff {
		fontFileKey = "FontFile3"
	}
	beginObj(refs.descriptor)
	ow.printf("<< /Type /FontDescriptor /FontName /%s /Flags %d /FontBBox [%d %d %d %d] /ItalicAngle %s /Ascent %d /Descent %d /CapHeight %d /StemV 80 /%s %d 0 R >>\n",
		base, flags,
		t.toEm(t.bbox[0]), t.toEm(t.bbox[1]), t.toEm(t.bbox[2]), t.toEm(t.bbox[3]),
		fl(t.italicAngle), t.toEm(t.ascent), t.toEm(t.descent), t.toEm(t.capHeight),
		fontFileKey, refs.fontFile)
	endObj()

	beginObj(refs.fontFile)
	// A TrueType program declares its unencoded length; an OpenType one
	// declares its flavour instead.
	streamDict := fmt.Sprintf("/Length1 %d ", len(subsetBytes))
	if t.cff {
		streamDict = "/Subtype /OpenType "
	}
	if err := ow.writeStream(streamDict, subsetBytes, d.Compress); err != nil {
		return err
	}
	endObj()

	beginObj(refs.toUnicode)
	if err := ow.writeStream("", buildToUnicode(usage, gids), d.Compress); err != nil {
		return err
	}
	endObj()
	return nil
}

// subsetTag derives a deterministic six-letter subset prefix from the font
// name and glyph set.
func subsetTag(name string, gids []uint16) string {
	h := fnv.New64a()
	io.WriteString(h, name)
	for _, g := range gids {
		h.Write([]byte{byte(g >> 8), byte(g)})
	}
	sum := h.Sum64()
	tag := make([]byte, 6)
	for i := range tag {
		tag[i] = byte('A' + sum%26)
		sum /= 26
	}
	return string(tag)
}

// buildToUnicode builds a ToUnicode CMap so text in an embedded font can be
// extracted, searched and copied.
func buildToUnicode(usage map[uint16]rune, gids []uint16) []byte {
	var b strings.Builder
	b.WriteString(`/CIDInit /ProcSet findresource begin
12 dict begin
begincmap
/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def
/CMapName /Adobe-Identity-UCS def
/CMapType 2 def
1 begincodespacerange
<0000> <FFFF>
endcodespacerange
`)
	for start := 0; start < len(gids); start += 100 {
		end := start + 100
		if end > len(gids) {
			end = len(gids)
		}
		fmt.Fprintf(&b, "%d beginbfchar\n", end-start)
		for _, gid := range gids[start:end] {
			fmt.Fprintf(&b, "<%04X> <", gid)
			for _, u := range utf16.Encode([]rune{usage[gid]}) {
				fmt.Fprintf(&b, "%04X", u)
			}
			b.WriteString(">\n")
		}
		b.WriteString("endbfchar\n")
	}
	b.WriteString(`endcmap
CMapName currentdict /CMap defineresource pop
end
end
`)
	return []byte(b.String())
}
