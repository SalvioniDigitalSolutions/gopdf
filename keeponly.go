package gopdf

// keeponly.go — a copy that holds only what is known.
//
// A rewrite keeps every object the catalog still reaches, and every key
// of every one of them: what a redaction does not know of is carried
// across. KeepOnly writes the copy the other way round. The catalog, the
// page tree and everything they lead to are rebuilt from a list of
// entries this package knows how to clean, each with its rule; what is
// not on the list is not followed, so it is not reachable, so it is not
// written. A key added to the PDF format tomorrow, a private key of some
// application, a part of the file nothing points to, an earlier revision:
// none of them is in the copy.
//
// The walk fails closed. A value is either rebuilt from the list or left
// out; and once the copy is planned, every object it would write is
// checked to have been rebuilt — one that was not stops the copy instead
// of being written as it came.

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

// KeepPolicy is how KeepOnly cleans the text it keeps.
type KeepPolicy struct {
	// Text cleans a text string the copy keeps: a bookmark title, an
	// alternate description, an actual text, a page label prefix. Nil
	// keeps text as it is.
	Text func(s string) string
	// WebLink reports whether a web address may stay in a link. A link
	// whose address may not stay is removed; the text it was on stays.
	// Nil keeps every http(s) address.
	WebLink func(uri string) bool
}

// KeepReport says what KeepOnly left out that a person could notice.
type KeepReport struct {
	// Attachments: embedded files, wherever they were attached.
	Attachments int
	// Scripts: JavaScript, actions run on opening or on events.
	Scripts int
	// HiddenLayerContent: pieces of page content in layers hidden by
	// default.
	HiddenLayerContent int
	// Layers: the document had layers; the copy shows the visible ones
	// and can no longer switch them.
	Layers bool
	// Forms: the document had an interactive form.
	Forms bool
	// Signatures: the document was signed; a changed copy cannot be.
	Signatures bool
	// Annotations: annotations other than links that were still on the
	// pages (comments, stamps and the like).
	Annotations int
	// Links: links removed because of their target (an address that
	// holds a value, mail, a file, another document, a script).
	Links int
	// Undecodable: forms, pattern cells and glyphs whose content no
	// decoder reads, kept empty (they draw nothing in any viewer).
	Undecodable int
}

// errKeepOnly marks every refusal of KeepOnly.
var errKeepOnly = errors.New("gopdf: the copy could not be limited to what is known")

// KeepOnly writes r as a fresh file that holds only the entries listed
// in this file and its companions (keeponly_resources.go,
// keeponly_content.go), each cleaned by its rule. The document
// information dictionary is written empty and the file gets a new ID.
func KeepOnly(r *Reader, w io.Writer, p KeepPolicy) (KeepReport, error) {
	if r == nil {
		return KeepReport{}, fmt.Errorf("%w: no document", errKeepOnly)
	}
	k := &keeper{
		r: r, p: p, rw: newRewriter(r),
		kind:    map[int]string{},
		pages:   map[int]bool{},
		nextNum: r.maxObjectNumber() + 1,
	}
	if err := k.plan(); err != nil {
		return k.rep, err
	}
	if err := k.verify(); err != nil {
		return k.rep, err
	}
	k.rw.info = &Info{}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return k.rep, fmt.Errorf("%w: %v", errKeepOnly, err)
	}
	k.rw.id = Array{String(id), String(id)}
	_, err := k.rw.writeTo(w)
	return k.rep, err
}

// keeper plans the copy: for every object kept, the object rebuilt.
type keeper struct {
	r  *Reader
	p  KeepPolicy
	rw *rewriter
	// kind is what each object number was kept as; "-" is left out.
	kind  map[int]string
	pages map[int]bool
	oc    *ocVisibility
	rep   KeepReport
	err   error
	// nextNum numbers the objects the copy adds (a page's new content).
	nextNum int
	// inherit is the resources a form, pattern or Type 3 font without
	// its own draws with: those of what is being kept.
	inherit Dict
}

// newObject adds an object the copy writes, kept as kind.
func (k *keeper) newObject(kind string, v any) Ref {
	num := k.nextNum
	k.nextNum++
	k.kind[num] = kind
	k.rw.replace[num] = v
	return Ref{Num: num}
}

func (k *keeper) fail(format string, args ...any) {
	if k.err == nil {
		k.err = fmt.Errorf("%w: "+format, append([]any{errKeepOnly}, args...)...)
	}
}

// indirect keeps the object behind ref as kind, rebuilt by build, and
// returns the reference to write. A reference to an object kept as
// something else, or left out, is not written (nil).
func (k *keeper) indirect(ref Ref, kind string, build func(v any) any) any {
	if ref.Num <= 0 {
		return nil
	}
	if got, seen := k.kind[ref.Num]; seen {
		if got == kind {
			return ref
		}
		return nil
	}
	k.kind[ref.Num] = kind
	obj, err := k.r.object(ref.Num)
	if err != nil {
		k.kind[ref.Num] = "-"
		return nil
	}
	built := build(obj)
	if built == nil {
		k.kind[ref.Num] = "-"
		k.rw.replace[ref.Num] = nil
		return nil
	}
	k.rw.replace[ref.Num] = built
	return ref
}

// value keeps v (direct or indirect) as kind: a reference stays a
// reference to the rebuilt object, a direct value is rebuilt in place.
func (k *keeper) value(v any, kind string, build func(v any) any) any {
	if ref, ok := v.(Ref); ok {
		return k.indirect(ref, kind, build)
	}
	if v == nil {
		return nil
	}
	built := build(v)
	if stm, ok := built.(*rawStream); ok {
		// A stream is written as an object of its own.
		return k.newObject(kind, stm)
	}
	return built
}

// --- plain values ---

func isNumber(v any) bool {
	switch v.(type) {
	case int, int64, float64, float32, uint64:
		return true
	}
	return false
}

// plain keeps a number, a name or a boolean; nothing else.
func (k *keeper) plain(v any) any {
	v = k.r.resolve(v)
	switch v.(type) {
	case Name, bool:
		return v
	}
	if isNumber(v) {
		return v
	}
	return nil
}

func (k *keeper) number(v any) any {
	v = k.r.resolve(v)
	if isNumber(v) {
		return v
	}
	return nil
}

func (k *keeper) name(v any) any {
	if n, ok := k.r.resolve(v).(Name); ok {
		return n
	}
	return nil
}

// plainArray keeps an array of numbers, names and booleans (a box, a
// matrix, a dash pattern, a list of names), written direct.
func (k *keeper) plainArray(v any) any {
	arr, ok := k.r.resolve(v).(Array)
	if !ok {
		return nil
	}
	out := make(Array, 0, len(arr))
	for _, e := range arr {
		if e2 := k.r.resolve(e); e2 != nil {
			if sub, isArr := e2.(Array); isArr {
				if kept := k.plainArray(sub); kept != nil {
					out = append(out, kept)
				}
				continue
			}
		}
		if kept := k.plain(e); kept != nil {
			out = append(out, kept)
		}
	}
	return out
}

// boolean keeps a boolean.
func (k *keeper) boolean(v any) any {
	if b, ok := k.r.resolve(v).(bool); ok {
		return b
	}
	return nil
}

// numbers keeps an array of numbers (arrays of numbers within it, and
// null, allowed: a dash pattern, a widths list). An array that holds
// anything else is not kept.
func (k *keeper) numbers(v any) any {
	arr, ok := k.r.resolve(v).(Array)
	if !ok {
		return nil
	}
	out := make(Array, 0, len(arr))
	for _, e := range arr {
		e = k.r.resolve(e)
		switch t := e.(type) {
		case nil:
			out = append(out, nil)
		case Array:
			sub := k.numbers(t)
			if sub == nil {
				return nil
			}
			out = append(out, sub)
		default:
			if !isNumber(e) {
				return nil
			}
			out = append(out, e)
		}
	}
	return out
}

// oneOf keeps a name the standard lists for the key, and no other.
func (k *keeper) oneOf(names ...Name) rule {
	set := map[Name]bool{}
	for _, n := range names {
		set[n] = true
	}
	return func(v any) any {
		if n, ok := k.r.resolve(v).(Name); ok && set[n] {
			return n
		}
		return nil
	}
}

// namesOf keeps an array of the names one allows.
func (k *keeper) namesOf(one rule) rule {
	return func(v any) any {
		arr, ok := k.r.resolve(v).(Array)
		if !ok {
			return nil
		}
		out := Array{}
		for _, e := range arr {
			if kept := one(e); kept != nil {
				out = append(out, kept)
			}
		}
		return out
	}
}

// freeName keeps a name a producer chooses freely (a structure type,
// a role, an attribute), renamed by the policy as a jump target is.
func (k *keeper) freeName(v any) any {
	if n, ok := k.r.resolve(v).(Name); ok {
		return k.destName(n)
	}
	return nil
}

// The names the standard lists, by key.
var (
	riNames    = []Name{"AbsoluteColorimetric", "RelativeColorimetric", "Saturation", "Perceptual"}
	boxNames   = []Name{"MediaBox", "CropBox", "BleedBox", "TrimBox", "ArtBox"}
	blendNames = []Name{"Normal", "Compatible", "Multiply", "Screen", "Overlay", "Darken", "Lighten", "ColorDodge",
		"ColorBurn", "HardLight", "SoftLight", "Difference", "Exclusion", "Hue", "Saturation", "Color", "Luminosity"}
	viewNames   = []Name{"XYZ", "Fit", "FitH", "FitV", "FitR", "FitB", "FitBH", "FitBV"}
	filterNames = []Name{"ASCIIHexDecode", "ASCII85Decode", "LZWDecode", "FlateDecode", "RunLengthDecode",
		"CCITTFaxDecode", "JBIG2Decode", "DCTDecode", "JPXDecode", "AHx", "A85", "LZW", "Fl", "RL", "CCF", "DCT"}
	versionRe = regexp.MustCompile(`^[0-9]\.[0-9]$`)
)

// text keeps a text string, cleaned by the policy.
func (k *keeper) text(v any) any {
	s, ok := k.r.resolve(v).(String)
	if !ok {
		return nil
	}
	if k.p.Text == nil {
		return s
	}
	t := decodeTextString(s)
	if c := k.p.Text(t); c != t {
		return String(textStringBytes(c))
	}
	return s
}

// technical keeps a string that is not text a person wrote (a font's
// registry, a character set): as it is.
func (k *keeper) technical(v any) any {
	if s, ok := k.r.resolve(v).(String); ok {
		return s
	}
	return nil
}

// langTagRe is a language tag of the shape documents use: a language,
// and a script and a region where given (de, de-CH, zh-Hans-CN, gsw).
// A tag of any other shape — a variant, an extension, a private part —
// has room for a word and is left out.
var langTagRe = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z]{4})?(-([A-Za-z]{2}|[0-9]{3}))?$`)

// lang keeps a language tag, and only one.
func (k *keeper) lang(v any) any {
	s, ok := k.r.resolve(v).(String)
	if !ok || !langTagRe.MatchString(decodeTextString(s)) {
		return nil
	}
	return s
}

// rule is how one key of a dictionary is kept.
type rule func(v any) any

// dict rebuilds d with the keys of rules, each kept by its rule; a key
// whose rule keeps nothing is left out, and so is every key not listed.
func (k *keeper) dict(v any, rules map[Name]rule) Dict {
	d, ok := k.r.resolve(v).(Dict)
	if !ok {
		return nil
	}
	out := Dict{}
	for key, val := range d {
		r, listed := rules[key]
		if !listed || r == nil {
			continue
		}
		if kept := r(val); kept != nil {
			out[key] = kept
		}
	}
	return out
}

func fixed(n Name) rule { return func(any) any { return n } }

// dictOrNil is dict for a rule: no dictionary is no value. (A nil Dict
// handed back as an interface is not nil, and was kept as an empty
// dictionary — an array of things that are no functions passed as one.)
func (k *keeper) dictOrNil(v any, rules map[Name]rule) any {
	if d := k.dict(v, rules); d != nil {
		return d
	}
	return nil
}

// --- the catalog ---

func (k *keeper) plan() error {
	rootRef, ok := k.r.trailer["Root"].(Ref)
	if !ok {
		return fmt.Errorf("%w: the catalog is not an indirect object", errKeepOnly)
	}
	cat, ok := k.r.resolve(rootRef).(Dict)
	if !ok {
		return fmt.Errorf("%w: no catalog", errKeepOnly)
	}
	k.noteDropped(cat)
	k.oc = newOCVisibility(k.r, cat)
	if k.oc.any {
		k.rep.Layers = true
	}
	for _, num := range k.r.pageRefs {
		k.pages[num] = true
	}
	pagesRef, ok := cat["Pages"].(Ref)
	if !ok {
		return fmt.Errorf("%w: the page tree is not an indirect object", errKeepOnly)
	}

	k.kind[rootRef.Num] = "catalog"
	out := Dict{"Type": Name("Catalog")}
	out["Pages"] = k.indirect(pagesRef, "pages", func(any) any { return k.pageTree(pagesRef) })
	if out["Pages"] == nil {
		return fmt.Errorf("%w: the page tree could not be kept", errKeepOnly)
	}
	if v, ok := k.name(cat["Version"]).(Name); ok && versionRe.MatchString(string(v)) {
		out["Version"] = v
	}
	if v := k.value(cat["Outlines"], "outlines", k.outlines); v != nil {
		out["Outlines"] = v
	}
	if v := k.value(cat["Names"], "names", k.names); v != nil {
		out["Names"] = v
	}
	if v := k.value(cat["Dests"], "dests", k.destsDict); v != nil {
		out["Dests"] = v
	}
	if v := k.value(cat["PageLabels"], "pagelabels", func(v any) any { return k.numberTree(v, k.pageLabel) }); v != nil {
		out["PageLabels"] = v
	}
	if v := k.value(cat["StructTreeRoot"], "structroot", k.structTreeRoot); v != nil {
		out["StructTreeRoot"] = v
	}
	if v := k.dict(cat["MarkInfo"], map[Name]rule{"Marked": k.boolean, "UserProperties": k.boolean, "Suspects": k.boolean}); len(v) > 0 {
		out["MarkInfo"] = v
	}
	if v := k.lang(cat["Lang"]); v != nil {
		out["Lang"] = v
	}
	if v := k.oneOf("UseNone", "UseOutlines", "UseThumbs", "FullScreen", "UseOC", "UseAttachments")(cat["PageMode"]); v != nil {
		out["PageMode"] = v
	}
	if v := k.oneOf("SinglePage", "OneColumn", "TwoColumnLeft", "TwoColumnRight", "TwoPageLeft", "TwoPageRight")(cat["PageLayout"]); v != nil {
		out["PageLayout"] = v
	}
	if v := k.viewerPreferences(cat["ViewerPreferences"]); len(v) > 0 {
		out["ViewerPreferences"] = v
	}
	k.rw.replace[rootRef.Num] = out
	return k.err
}

// noteDropped counts what the copy leaves out that a person may miss.
func (k *keeper) noteDropped(cat Dict) {
	if names, ok := k.r.resolve(cat["Names"]).(Dict); ok {
		if _, has := names["EmbeddedFiles"]; has {
			k.rep.Attachments++
		}
		if _, has := names["JavaScript"]; has {
			k.rep.Scripts++
		}
	}
	if _, has := cat["AF"]; has {
		k.rep.Attachments++
	}
	if _, has := cat["OpenAction"]; has {
		k.rep.Scripts++
	}
	if _, has := cat["AA"]; has {
		k.rep.Scripts++
	}
	if form, ok := k.r.resolve(cat["AcroForm"]).(Dict); ok {
		k.rep.Forms = true
		if flags, _ := toInt(k.r.resolve(form["SigFlags"])); flags != 0 {
			k.rep.Signatures = true
		}
	}
	if _, has := cat["Perms"]; has {
		k.rep.Signatures = true
	}
}

func (k *keeper) viewerPreferences(v any) Dict {
	rules := map[Name]rule{}
	for _, key := range []Name{"HideToolbar", "HideMenubar", "HideWindowUI", "FitWindow", "CenterWindow",
		"DisplayDocTitle", "PickTrayByPDFSize"} {
		rules[key] = k.boolean
	}
	rules["NonFullScreenPageMode"] = k.oneOf("UseNone", "UseOutlines", "UseThumbs", "UseOC")
	rules["Direction"] = k.oneOf("L2R", "R2L")
	for _, key := range []Name{"ViewArea", "ViewClip", "PrintArea", "PrintClip"} {
		rules[key] = k.oneOf(boxNames...)
	}
	rules["PrintScaling"] = k.oneOf("None", "AppDefault")
	rules["Duplex"] = k.oneOf("Simplex", "DuplexFlipShortEdge", "DuplexFlipLongEdge")
	rules["NumCopies"] = k.number
	rules["PrintPageRange"] = k.numbers
	rules["Enforce"] = k.namesOf(k.oneOf("PrintScaling"))
	return k.dict(v, rules)
}

// --- pages ---

// pageTree is the page tree rebuilt flat: one root holding every page,
// each with what it inherited written on it.
func (k *keeper) pageTree(rootRef Ref) any {
	kids := make(Array, 0, len(k.r.pageRefs))
	for i, num := range k.r.pageRefs {
		ref := Ref{Num: num}
		i := i
		kept := k.indirect(ref, "page", func(v any) any { return k.page(i, rootRef, v) })
		if kept == nil {
			k.fail("page %d could not be kept", i+1)
			return nil
		}
		kids = append(kids, kept)
	}
	return Dict{"Type": Name("Pages"), "Kids": kids, "Count": len(kids)}
}

// inherited looks a page key up the page tree.
func (k *keeper) inherited(page Dict, key Name) any {
	d := page
	for depth := 0; d != nil && depth < 64; depth++ {
		if v, ok := d[key]; ok {
			return v
		}
		d, _ = k.r.resolve(d["Parent"]).(Dict)
	}
	return nil
}

func (k *keeper) page(index int, parent Ref, v any) any {
	pg, ok := v.(Dict)
	if !ok {
		return nil
	}
	for _, key := range []Name{"AA", "Thumb", "PieceInfo", "Metadata", "B", "Trans", "Dur", "SeparationInfo", "AF"} {
		if _, has := pg[key]; has {
			switch key {
			case "AA":
				k.rep.Scripts++
			case "AF":
				k.rep.Attachments++
			}
		}
	}
	out := Dict{"Type": Name("Page"), "Parent": parent}
	for _, key := range []Name{"MediaBox", "CropBox"} {
		if box := k.numbers(k.inherited(pg, key)); box != nil {
			out[key] = box
		}
	}
	for _, key := range []Name{"BleedBox", "TrimBox", "ArtBox"} {
		if box := k.numbers(pg[key]); box != nil {
			out[key] = box
		}
	}
	if rot := k.number(k.inherited(pg, "Rotate")); rot != nil {
		out["Rotate"] = rot
	}
	for _, key := range []Name{"UserUnit", "StructParents"} {
		if n := k.number(pg[key]); n != nil {
			out[key] = n
		}
	}
	if t := k.oneOf("R", "C", "S", "A", "W")(pg["Tabs"]); t != nil {
		out["Tabs"] = t
	}
	if g := k.value(pg["Group"], "group", k.group); g != nil {
		out["Group"] = g
	}

	content, err := k.r.pageContent(pg)
	if err != nil {
		k.fail("the content of page %d cannot be read: %v", index+1, err)
		return nil
	}
	res, _ := k.r.resolve(k.inherited(pg, "Resources")).(Dict)
	kept := k.content(content, res)
	if kept.err != nil {
		k.fail("page %d: %v", index+1, kept.err)
		return nil
	}
	out["Contents"] = k.newObject("content", compressedStream(kept.data))
	if r := k.resources(res, kept.used); len(r) > 0 {
		out["Resources"] = r
	}
	if annots := k.annotations(pg["Annots"]); len(annots) > 0 {
		out["Annots"] = annots
	}
	return out
}

// group keeps a transparency group's attributes.
func (k *keeper) group(v any) any {
	return k.dictOrNil(v, map[Name]rule{
		"Type": k.oneOf("Group"), "S": k.oneOf("Transparency"), "I": k.boolean, "K": k.boolean,
		"CS": func(v any) any { return k.colorSpace(v) },
	})
}

// --- links ---

// annotations keeps the page's links; every other annotation still on
// the page is left out (forms and comments are drawn into the page
// before this runs).
func (k *keeper) annotations(v any) Array {
	arr, ok := k.r.resolve(v).(Array)
	if !ok {
		return nil
	}
	var out Array
	for _, a := range arr {
		d, ok := k.r.resolve(a).(Dict)
		if !ok {
			continue
		}
		sub, _ := k.r.resolve(d["Subtype"]).(Name)
		if sub != "Link" {
			k.rep.Annotations++
			if sub == "FileAttachment" {
				k.rep.Attachments++
			}
			continue
		}
		if k.oc.hidden(d["OC"]) {
			k.rep.HiddenLayerContent++
			continue
		}
		if !k.linkTargetKept(d) {
			k.rep.Links++
			continue
		}
		kept := k.value(a, "annot", k.link)
		if kept != nil {
			out = append(out, kept)
		}
	}
	return out
}

// linkTargetKept decides, before anything is kept, whether a link's
// target may stay.
func (k *keeper) linkTargetKept(d Dict) bool {
	if dest, has := d["Dest"]; has {
		return k.dest(dest) != nil
	}
	return k.action(d["A"], true) != nil
}

func (k *keeper) link(v any) any {
	d, ok := v.(Dict)
	if !ok {
		return nil
	}
	out := k.dict(d, map[Name]rule{
		"Rect": k.numbers, "Border": k.numbers, "C": k.numbers, "QuadPoints": k.numbers,
		"H": k.oneOf("N", "I", "O", "P"), "StructParent": k.number, "F": k.number,
		"BS": func(v any) any {
			return k.dictOrNil(v, map[Name]rule{"Type": k.oneOf("Border"), "W": k.number, "S": k.oneOf("S", "D", "B", "I", "U"), "D": k.numbers})
		},
		"Dest": k.dest,
		"A":    func(v any) any { return k.action(v, true) },
	})
	out["Type"] = Name("Annot")
	out["Subtype"] = Name("Link")
	return out
}

// action keeps a jump within the document, and — for a link, not a
// bookmark — a web address the policy lets stay. Anything else is left
// out: mail, files, other documents, scripts, forms, sounds, launches.
func (k *keeper) action(v any, web bool) any {
	d, ok := k.r.resolve(v).(Dict)
	if !ok {
		return nil
	}
	switch s, _ := k.r.resolve(d["S"]).(Name); s {
	case "GoTo":
		if dest := k.dest(d["D"]); dest != nil {
			return Dict{"S": Name("GoTo"), "D": dest}
		}
	case "URI":
		uri, ok := k.r.resolve(d["URI"]).(String)
		if !ok || !web {
			return nil
		}
		addr := string(uri)
		low := strings.ToLower(strings.TrimSpace(addr))
		if !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") {
			return nil
		}
		if k.p.WebLink != nil && !k.p.WebLink(addr) {
			return nil
		}
		return Dict{"S": Name("URI"), "URI": uri}
	case "JavaScript":
		k.rep.Scripts++
	}
	return nil
}

// dest keeps a destination within the document: a page of this
// document with a view, or a name (renamed by the caller's scrubber).
func (k *keeper) dest(v any) any {
	switch t := k.r.resolve(v).(type) {
	case Name:
		return k.destName(t)
	case String:
		return k.destString(t)
	case Array:
		if len(t) == 0 {
			return nil
		}
		ref, ok := t[0].(Ref)
		if !ok || !k.pages[ref.Num] {
			return nil
		}
		out := Array{ref}
		for _, e := range t[1:] {
			e = k.r.resolve(e)
			if e == nil {
				out = append(out, nil)
				continue
			}
			if n, isName := e.(Name); isName {
				if v := k.oneOf(viewNames...)(n); v != nil {
					out = append(out, v)
				}
				continue
			}
			if n := k.number(e); n != nil {
				out = append(out, n)
			}
		}
		return out
	case Dict:
		if d := k.dest(t["D"]); d != nil {
			return Dict{"D": d}
		}
	}
	return nil
}

// destString and destName rename a named jump target by the policy,
// the same way wherever the name is written, so a link still finds it.
func (k *keeper) destString(s String) String {
	if k.p.Text == nil {
		return s
	}
	t := decodeTextString(s)
	if c := k.p.Text(t); c != t {
		return String(textStringBytes(c))
	}
	return s
}

func (k *keeper) destName(n Name) Name {
	if k.p.Text == nil {
		return n
	}
	if c := k.p.Text(string(n)); c != string(n) {
		return Name(c)
	}
	return n
}

// --- bookmarks, jump targets, page labels ---

func (k *keeper) outlines(v any) any {
	d, ok := v.(Dict)
	if !ok {
		return nil
	}
	out := Dict{"Type": Name("Outlines")}
	for _, key := range []Name{"First", "Last"} {
		if ref, ok := d[key].(Ref); ok {
			if kept := k.indirect(ref, "outline", k.outlineItem); kept != nil {
				out[key] = kept
			}
		}
	}
	if n := k.number(d["Count"]); n != nil {
		out["Count"] = n
	}
	return out
}

func (k *keeper) outlineItem(v any) any {
	d, ok := v.(Dict)
	if !ok {
		return nil
	}
	out := Dict{}
	title := k.text(d["Title"])
	if title == nil {
		title = String("")
	}
	out["Title"] = title
	for _, key := range []Name{"Parent", "Prev", "Next", "First", "Last"} {
		ref, ok := d[key].(Ref)
		if !ok {
			continue
		}
		var kept any
		if key == "Parent" {
			if k.kind[ref.Num] == "outlines" || k.kind[ref.Num] == "outline" {
				kept = ref
			} else if obj, ok := k.r.resolve(ref).(Dict); ok && obj["Title"] == nil {
				kept = k.indirect(ref, "outlines", k.outlines)
			} else {
				kept = k.indirect(ref, "outline", k.outlineItem)
			}
		} else {
			kept = k.indirect(ref, "outline", k.outlineItem)
		}
		if kept != nil {
			out[key] = kept
		}
	}
	if n := k.number(d["Count"]); n != nil {
		out["Count"] = n
	}
	if n := k.number(d["F"]); n != nil {
		out["F"] = n
	}
	if c := k.numbers(d["C"]); c != nil {
		out["C"] = c
	}
	if dest, has := d["Dest"]; has {
		if kept := k.dest(dest); kept != nil {
			out["Dest"] = kept
		}
	} else if a := k.action(d["A"], false); a != nil {
		out["A"] = a
	} else if d["A"] != nil {
		k.rep.Links++
	}
	return out
}

// names keeps the jump targets of the name dictionary and nothing else.
func (k *keeper) names(v any) any {
	d, ok := v.(Dict)
	if !ok {
		return nil
	}
	dests := k.destTree(d["Dests"])
	if dests == nil {
		return nil
	}
	return Dict{"Dests": dests}
}

// destsDict keeps the old form of named jump targets.
func (k *keeper) destsDict(v any) any {
	d, ok := v.(Dict)
	if !ok {
		return nil
	}
	out := Dict{}
	for name, dest := range d {
		if kept := k.dest(dest); kept != nil {
			out[k.destName(name)] = kept
		}
	}
	return out
}

// destTree keeps the name tree of jump targets, written as one node: the
// names are renamed, so their order is made anew.
func (k *keeper) destTree(v any) any {
	type pair struct {
		key String
		val any
	}
	var pairs []pair
	seen := map[int]bool{}
	var walk func(v any, depth int)
	walk = func(v any, depth int) {
		if depth > 64 {
			return
		}
		if ref, ok := v.(Ref); ok {
			if seen[ref.Num] {
				return
			}
			seen[ref.Num] = true
		}
		d, ok := k.r.resolve(v).(Dict)
		if !ok {
			return
		}
		if kids, ok := k.r.resolve(d["Kids"]).(Array); ok {
			for _, kid := range kids {
				walk(kid, depth+1)
			}
		}
		if names, ok := k.r.resolve(d["Names"]).(Array); ok {
			for i := 0; i+1 < len(names); i += 2 {
				key, ok := k.r.resolve(names[i]).(String)
				if !ok {
					continue
				}
				if val := k.dest(names[i+1]); val != nil {
					pairs = append(pairs, pair{k.destString(key), val})
				}
			}
		}
	}
	walk(v, 0)
	if len(pairs) == 0 {
		return nil
	}
	sort.SliceStable(pairs, func(i, j int) bool { return string(pairs[i].key) < string(pairs[j].key) })
	arr := make(Array, 0, 2*len(pairs))
	for i, p := range pairs {
		if i > 0 && string(p.key) == string(pairs[i-1].key) {
			continue // two names renamed alike: the first stays
		}
		arr = append(arr, p.key, p.val)
	}
	return k.newObject("nametree", Dict{"Names": arr})
}

// numberTree keeps a number tree, each value kept by leaf.
func (k *keeper) numberTree(v any, leaf func(any) any) any {
	d, ok := k.r.resolve(v).(Dict)
	if !ok {
		return nil
	}
	out := Dict{}
	if kids, ok := k.r.resolve(d["Kids"]).(Array); ok {
		var kept Array
		for _, kid := range kids {
			if ref, ok := kid.(Ref); ok {
				if kk := k.indirect(ref, "numbertree", func(v any) any { return k.numberTree(v, leaf) }); kk != nil {
					kept = append(kept, kk)
				}
			}
		}
		out["Kids"] = kept
	}
	if nums, ok := k.r.resolve(d["Nums"]).(Array); ok {
		var kept Array
		for i := 0; i+1 < len(nums); i += 2 {
			key := k.number(nums[i])
			if key == nil {
				continue
			}
			if val := leaf(nums[i+1]); val != nil {
				kept = append(kept, key, val)
			}
		}
		out["Nums"] = kept
	}
	if lim := k.numbers(d["Limits"]); lim != nil {
		out["Limits"] = lim
	}
	return out
}

func (k *keeper) pageLabel(v any) any {
	return k.value(v, "pagelabel", func(v any) any {
		return k.dictOrNil(v, map[Name]rule{"Type": k.oneOf("PageLabel"), "S": k.oneOf("D", "R", "r", "A", "a"), "P": k.text, "St": k.number})
	})
}

// --- tags ---

func (k *keeper) structTreeRoot(v any) any {
	d, ok := v.(Dict)
	if !ok {
		return nil
	}
	out := Dict{"Type": Name("StructTreeRoot")}
	if kids := k.structKids(d["K"]); kids != nil {
		out["K"] = kids
	}
	if pt := k.value(d["ParentTree"], "parenttree", func(v any) any { return k.numberTree(v, k.parentTreeValue) }); pt != nil {
		out["ParentTree"] = pt
	}
	if n := k.number(d["ParentTreeNextKey"]); n != nil {
		out["ParentTreeNextKey"] = n
	}
	if rm, ok := k.r.resolve(d["RoleMap"]).(Dict); ok {
		m := Dict{}
		for key, val := range rm {
			if n := k.freeName(val); n != nil {
				m[k.destName(key)] = n
			}
		}
		if len(m) > 0 {
			out["RoleMap"] = m
		}
	}
	return out
}

func (k *keeper) parentTreeValue(v any) any {
	if arr, ok := k.r.resolve(v).(Array); ok {
		out := make(Array, 0, len(arr))
		for _, e := range arr {
			if ref, ok := e.(Ref); ok {
				out = append(out, k.indirect(ref, "structelem", k.structElem))
			} else {
				out = append(out, nil)
			}
		}
		return out
	}
	if ref, ok := v.(Ref); ok {
		return k.indirect(ref, "structelem", k.structElem)
	}
	return nil
}

// structKids keeps the children of a structure element: elements,
// marked-content numbers, and references to marked content and to the
// links the copy keeps.
func (k *keeper) structKids(v any) any {
	keepOne := func(e any) any {
		switch t := e.(type) {
		case Ref:
			obj, _ := k.r.resolve(t).(Dict)
			if typ, _ := obj["Type"].(Name); typ == "MCR" || typ == "OBJR" {
				return k.value(t, "structref", k.structRef)
			}
			return k.indirect(t, "structelem", k.structElem)
		case Dict:
			if typ, _ := t["Type"].(Name); typ == "MCR" || typ == "OBJR" {
				return k.structRef(t)
			}
			return k.structElem(t)
		}
		return k.number(e)
	}
	if arr, ok := k.r.resolve(v).(Array); ok {
		out := Array{}
		for _, e := range arr {
			if kept := keepOne(e); kept != nil {
				out = append(out, kept)
			}
		}
		return out
	}
	if v == nil {
		return nil
	}
	return keepOne(v)
}

func (k *keeper) structRef(v any) any {
	d, ok := k.r.resolve(v).(Dict)
	if !ok {
		return nil
	}
	typ, _ := d["Type"].(Name)
	out := Dict{"Type": typ}
	if pg, ok := d["Pg"].(Ref); ok && k.pages[pg.Num] {
		out["Pg"] = pg
	}
	switch typ {
	case "MCR":
		if n := k.number(d["MCID"]); n != nil {
			out["MCID"] = n
		}
		// A marked-content reference into a form's stream: kept when
		// the form is one the copy keeps.
		if stm, ok := d["Stm"].(Ref); ok {
			if k.kind[stm.Num] != "form" {
				return nil
			}
			out["Stm"] = stm
		}
	case "OBJR":
		obj, ok := d["Obj"].(Ref)
		if !ok || k.kind[obj.Num] != "annot" {
			return nil
		}
		out["Obj"] = obj
	default:
		return nil
	}
	return out
}

func (k *keeper) structElem(v any) any {
	d, ok := k.r.resolve(v).(Dict)
	if !ok {
		return nil
	}
	out := Dict{"Type": Name("StructElem")}
	if s := k.freeName(d["S"]); s != nil {
		out["S"] = s
	}
	if p, ok := d["P"].(Ref); ok {
		switch k.kind[p.Num] {
		case "structelem", "structroot":
			out["P"] = p
		default:
			if obj, ok := k.r.resolve(p).(Dict); ok {
				if typ, _ := obj["Type"].(Name); typ == "StructTreeRoot" {
					out["P"] = k.indirect(p, "structroot", k.structTreeRoot)
				} else {
					out["P"] = k.indirect(p, "structelem", k.structElem)
				}
			}
		}
	}
	if pg, ok := d["Pg"].(Ref); ok && k.pages[pg.Num] {
		out["Pg"] = pg
	}
	if kids := k.structKids(d["K"]); kids != nil {
		out["K"] = kids
	}
	for _, key := range []Name{"Alt", "ActualText", "E", "T"} {
		if t := k.text(d[key]); t != nil {
			out[key] = t
		}
	}
	if l := k.lang(d["Lang"]); l != nil {
		out["Lang"] = l
	}
	if r := k.number(d["R"]); r != nil {
		out["R"] = r
	}
	// C names classes of the class map, which the copy does not keep.
	if a := k.attributes(d["A"]); a != nil {
		out["A"] = a
	}
	return out
}

// attributes keeps a structure element's attribute objects: their
// owners and their layout values — numbers, booleans and names, the
// names renamed by the policy — never a string.
func (k *keeper) attributes(v any) any {
	var value func(v any, depth int) any
	value = func(v any, depth int) any {
		v = k.r.resolve(v)
		switch t := v.(type) {
		case bool:
			return t
		case Name:
			return k.destName(t)
		case Array:
			if depth > 4 {
				return nil
			}
			out := Array{}
			for _, e := range t {
				if kept := value(e, depth+1); kept != nil {
					out = append(out, kept)
				}
			}
			return out
		}
		if isNumber(v) {
			return v
		}
		return nil
	}
	one := func(e any) any {
		d, ok := k.r.resolve(e).(Dict)
		if !ok {
			return nil
		}
		out := Dict{}
		for key, val := range d {
			if kept := value(val, 0); kept != nil {
				out[k.destName(key)] = kept
			}
		}
		return out
	}
	if arr, ok := k.r.resolve(v).(Array); ok {
		out := Array{}
		for _, e := range arr {
			if kept := one(e); kept != nil {
				out = append(out, kept)
			} else if n := k.number(e); n != nil {
				out = append(out, n)
			}
		}
		return out
	}
	return one(v)
}

// --- the last check ---

// verify proves the plan: every object the copy would write was
// rebuilt by this walk. One that was not stops the copy.
func (k *keeper) verify() error {
	if k.err != nil {
		return k.err
	}
	rootRef := k.r.trailer["Root"].(Ref)
	live, err := k.rw.reachable([]any{rootRef})
	if err != nil {
		return fmt.Errorf("%w: %v", errKeepOnly, err)
	}
	var stray []int
	for num := range live {
		kind, planned := k.kind[num]
		if !planned {
			stray = append(stray, num)
			continue
		}
		if kind == "-" {
			// Left out after something already pointed at it: it is
			// written as null, which holds nothing.
			k.rw.replace[num] = nil
			continue
		}
		if _, rebuilt := k.rw.replace[num]; !rebuilt {
			stray = append(stray, num)
		}
	}
	if len(stray) > 0 {
		sort.Ints(stray)
		return fmt.Errorf("%w: objects %v would be written as they came", errKeepOnly, stray)
	}
	return nil
}
