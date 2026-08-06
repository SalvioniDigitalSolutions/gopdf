package gopdf

import (
	"fmt"
	"sort"
	"strings"
)

// FieldType classifies an interactive form field.
type FieldType string

const (
	FieldText      FieldType = "text"
	FieldCheckbox  FieldType = "checkbox"
	FieldRadio     FieldType = "radio"
	FieldChoice    FieldType = "choice" // list box or combo box
	FieldButton    FieldType = "button" // pushbutton: has no value
	FieldSignature FieldType = "signature"
	FieldUnknown   FieldType = "unknown"
)

// FormField describes one field of an interactive (AcroForm) document.
type FormField struct {
	// Name is the field's fully qualified name, the key used to fill it.
	Name string
	// Type is what kind of control the field is.
	Type FieldType
	// Value is the field's current value. Checkboxes and radio groups
	// report the name of the selected state, or "" when unselected.
	Value string
	// Options lists the permitted values of a choice field or the export
	// values of a radio group.
	Options []string
	// ReadOnly and Required mirror the field's flags.
	ReadOnly, Required bool
	// MaxLen is a text field's character limit, or 0 when unlimited.
	MaxLen int
	// Page is the 0-based index of the page the field appears on, or -1
	// if it has no widget on any page.
	Page int
	// Rect is the widget's rectangle in points, from the top-left of its
	// page.
	Rect [4]float64
}

// widget pairs a field with one of its on-page annotations.
type widget struct {
	field  *FormField
	dict   Dict // the widget annotation (may be the field dictionary)
	fieldD Dict // the field dictionary
	// The unresolved source values, so the importer can locate this
	// field's and widget's copies inside the output document.
	fieldNode  any
	widgetNode any
	page       int
	rect       [4]float64 // PDF coordinates, bottom-left origin
	da         string     // default appearance string
	quad       int        // 0 left, 1 centre, 2 right
	onState    Name       // the "on" state of a checkbox or radio button
}

// HasForm reports whether the document has an interactive form.
func (r *Reader) HasForm() bool {
	root, _ := r.resolve(r.trailer["Root"]).(Dict)
	acro, _ := r.resolve(root["AcroForm"]).(Dict)
	fields, _ := r.resolve(acro["Fields"]).(Array)
	return len(fields) > 0
}

// FormFields lists the document's interactive form fields, in a stable
// order by name.
func (r *Reader) FormFields() []FormField {
	widgets := r.formWidgets()
	seen := make(map[string]bool)
	var out []FormField
	for _, w := range widgets {
		if seen[w.field.Name] {
			continue
		}
		seen[w.field.Name] = true
		out = append(out, *w.field)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// formWidgets walks the field tree and pairs every field with its
// on-page widget annotations.
func (r *Reader) formWidgets() []*widget {
	root, _ := r.resolve(r.trailer["Root"]).(Dict)
	acro, _ := r.resolve(root["AcroForm"]).(Dict)
	fields, _ := r.resolve(acro["Fields"]).(Array)
	if len(fields) == 0 {
		return nil
	}
	defaultDA, _ := r.resolve(acro["DA"]).(String)

	// Map every widget annotation to the page it sits on.
	pageOf := make(map[string]int)
	for i, pi := range r.pages {
		annots, _ := r.resolve(pi.dict["Annots"]).(Array)
		for _, a := range annots {
			if ref, ok := a.(Ref); ok {
				pageOf[fmt.Sprintf("%d_%d", ref.Num, ref.Gen)] = i
			}
		}
	}

	var out []*widget
	seen := make(map[Ref]bool)

	// inherited carries the attributes a field inherits from its parents.
	type inherited struct {
		ftype FieldType
		flags int
		value any
		da    string
		quad  int
	}

	var walk func(node any, prefix string, inh inherited, depth int)
	walk = func(node any, prefix string, inh inherited, depth int) {
		if depth > 32 || len(out) > 1<<14 {
			return
		}
		ref, isRef := node.(Ref)
		if isRef {
			if seen[ref] {
				return
			}
			seen[ref] = true
		}
		d, ok := r.resolve(node).(Dict)
		if !ok {
			return
		}
		name := prefix
		if t, ok := r.resolve(d["T"]).(String); ok {
			part := decodeTextString(t)
			if name == "" {
				name = part
			} else {
				name = name + "." + part
			}
		}
		if ft, ok := r.resolve(d["FT"]).(Name); ok {
			inh.ftype = fieldTypeOf(ft, r, d)
		}
		if f, ok := toInt(r.resolve(d["Ff"])); ok {
			inh.flags = f
		}
		if v, ok := d["V"]; ok {
			inh.value = r.resolve(v)
		}
		if da, ok := r.resolve(d["DA"]).(String); ok {
			inh.da = string(da)
		}
		if q, ok := toInt(r.resolve(d["Q"])); ok {
			inh.quad = q
		}

		kids, _ := r.resolve(d["Kids"]).(Array)
		// A kid is part of the field tree only if it names itself or has
		// its own kids; otherwise it is just this field's widget.
		var childFields []any
		var widgetDicts []any
		for _, kid := range kids {
			kd, ok := r.resolve(kid).(Dict)
			if !ok {
				continue
			}
			if _, named := kd["T"]; named {
				childFields = append(childFields, kid)
			} else {
				widgetDicts = append(widgetDicts, kid)
			}
		}
		for _, kid := range childFields {
			walk(kid, name, inh, depth+1)
		}
		if len(childFields) > 0 && len(widgetDicts) == 0 {
			return // an intermediate node in the tree
		}
		if len(widgetDicts) == 0 {
			widgetDicts = []any{node} // the field is its own widget
		}

		field := &FormField{
			Name:     name,
			Type:     inh.ftype,
			ReadOnly: inh.flags&1 != 0,
			Required: inh.flags&2 != 0,
			Page:     -1,
		}
		if ml, ok := toInt(r.resolve(d["MaxLen"])); ok {
			field.MaxLen = ml
		}
		field.Value = formValueString(inh.value)
		field.Options = formOptions(r, d)
		da := inh.da
		if da == "" {
			da = string(defaultDA)
		}

		for _, wd := range widgetDicts {
			ad, ok := r.resolve(wd).(Dict)
			if !ok {
				continue
			}
			page := -1
			if ref, ok := wd.(Ref); ok {
				if p, found := pageOf[fmt.Sprintf("%d_%d", ref.Num, ref.Gen)]; found {
					page = p
				}
			}
			rect := rectOf(r, ad["Rect"])
			if field.Page < 0 && page >= 0 {
				field.Page = page
				field.Rect = topLeftRect(r, rect, page)
			}
			out = append(out, &widget{
				field:      field,
				dict:       ad,
				fieldD:     d,
				fieldNode:  node,
				widgetNode: wd,
				page:       page,
				rect:       rect,
				da:         da,
				quad:       inh.quad,
				onState:    onStateOf(r, ad),
			})
		}
	}

	inh := inherited{ftype: FieldUnknown}
	for _, f := range fields {
		walk(f, "", inh, 0)
	}
	return out
}

func fieldTypeOf(ft Name, r *Reader, d Dict) FieldType {
	flags, _ := toInt(r.resolve(d["Ff"]))
	switch ft {
	case "Tx":
		return FieldText
	case "Btn":
		switch {
		case flags&(1<<16) != 0: // pushbutton
			return FieldButton
		case flags&(1<<15) != 0: // radio
			return FieldRadio
		default:
			return FieldCheckbox
		}
	case "Ch":
		return FieldChoice
	case "Sig":
		return FieldSignature
	}
	return FieldUnknown
}

func formValueString(v any) string {
	switch t := v.(type) {
	case String:
		return decodeTextString(t)
	case Name:
		if t == "Off" {
			return ""
		}
		return string(t)
	case Array:
		var parts []string
		for _, e := range t {
			if s, ok := e.(String); ok {
				parts = append(parts, decodeTextString(s))
			}
		}
		return strings.Join(parts, ",")
	}
	return ""
}

// formOptions reads a choice field's /Opt entries.
func formOptions(r *Reader, d Dict) []string {
	opts, ok := r.resolve(d["Opt"]).(Array)
	if !ok {
		return nil
	}
	var out []string
	for _, e := range opts {
		switch t := r.resolve(e).(type) {
		case String:
			out = append(out, decodeTextString(t))
		case Array:
			// [export display]: the export value is what gets stored.
			if len(t) > 0 {
				if s, ok := r.resolve(t[0]).(String); ok {
					out = append(out, decodeTextString(s))
				}
			}
		}
	}
	return out
}

// onStateOf finds the name a checkbox or radio widget uses for its
// selected state, which is the appearance key that is not "Off".
func onStateOf(r *Reader, widget Dict) Name {
	ap, ok := r.resolve(widget["AP"]).(Dict)
	if !ok {
		return ""
	}
	normal, ok := r.resolve(ap["N"]).(Dict)
	if !ok {
		return ""
	}
	names := make([]string, 0, len(normal))
	for k := range normal {
		if k != "Off" {
			names = append(names, string(k))
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return ""
	}
	return Name(names[0])
}

func rectOf(r *Reader, v any) [4]float64 {
	arr, ok := r.resolve(v).(Array)
	if !ok || len(arr) != 4 {
		return [4]float64{}
	}
	var out [4]float64
	for i, e := range arr {
		f, _ := toFloat(r.resolve(e))
		out[i] = f
	}
	if out[0] > out[2] {
		out[0], out[2] = out[2], out[0]
	}
	if out[1] > out[3] {
		out[1], out[3] = out[3], out[1]
	}
	return out
}

// topLeftRect converts a PDF rectangle to this package's top-left origin.
func topLeftRect(r *Reader, rect [4]float64, page int) [4]float64 {
	if page < 0 || page >= len(r.pages) {
		return rect
	}
	box := r.pages[page].mediaBox
	return [4]float64{
		rect[0] - box[0],
		box[3] - rect[3],
		rect[2] - box[0],
		box[3] - rect[1],
	}
}

// --- filling ---

// FillForm imports every page of an interactive document, fills the named
// fields with the given values, and flattens the result: values are drawn
// into the page content, so they display and print identically everywhere
// and can no longer be changed.
//
// Keys are fully qualified field names, as reported by Reader.FormFields.
// For a checkbox or radio group, pass the option's name to select it, or
// an empty string to clear it. Filling a name the document does not have
// is an error, so typos surface instead of silently doing nothing.
func (d *Document) FillForm(r *Reader, values map[string]string) (int, error) {
	widgets := r.formWidgets()
	if len(widgets) == 0 && len(values) > 0 {
		return 0, fmt.Errorf("gopdf: document has no interactive form fields")
	}
	known := make(map[string]bool, len(widgets))
	for _, w := range widgets {
		known[w.field.Name] = true
	}
	for name := range values {
		if !known[name] {
			return 0, fmt.Errorf("gopdf: no form field named %q", name)
		}
	}

	byPage := make(map[int][]*widget)
	for _, w := range widgets {
		if w.page >= 0 {
			byPage[w.page] = append(byPage[w.page], w)
		}
	}

	filled := 0
	for i := 0; i < r.NumPages(); i++ {
		page, err := d.EditPage(r, i)
		if err != nil {
			return filled, err
		}
		im := &importer{r: r, d: d, memo: d.importMemo(r)}
		for _, w := range byPage[i] {
			value, changed := values[w.field.Name]
			if changed {
				if w.field.ReadOnly {
					return filled, fmt.Errorf("gopdf: form field %q is read-only", w.field.Name)
				}
				if err := page.drawFieldValue(w, value); err != nil {
					return filled, err
				}
				filled++
			} else if err := page.placeExistingAppearance(im, w); err != nil {
				return filled, err
			}
		}
	}
	return filled, nil
}

// placeExistingAppearance draws a widget's own appearance stream into the
// page, so fields left untouched still show what they showed before the
// annotations were flattened away.
func (p *EditablePage) placeExistingAppearance(im *importer, w *widget) error {
	ap, ok := im.r.resolve(w.dict["AP"]).(Dict)
	if !ok {
		return nil
	}
	normal := im.r.resolve(ap["N"])
	if sub, ok := normal.(Dict); ok {
		// A state-keyed appearance: pick the widget's current state.
		state, _ := im.r.resolve(w.dict["AS"]).(Name)
		if state == "" {
			state = "Off"
		}
		normal = im.r.resolve(sub[state])
	}
	stm, ok := normal.(*rawStream)
	if !ok {
		return nil
	}
	copied, err := im.copy(ap["N"], 0)
	if err != nil {
		return err
	}
	// Re-resolve through the copy so nested state dictionaries work.
	var target any = copied
	if _, isDict := im.r.resolve(ap["N"]).(Dict); isDict {
		cp, ok := copied.(Dict)
		if !ok {
			if rr, isRef := copied.(rawRef); isRef {
				cp, _ = p.doc.raw[rr].(Dict)
			}
		}
		state, _ := im.r.resolve(w.dict["AS"]).(Name)
		if state == "" {
			state = "Off"
		}
		if cp == nil {
			return nil
		}
		target = cp[state]
	}
	name := p.addXObjectResource(target)
	if name == "" {
		return nil
	}

	// The appearance is mapped from its bounding box onto the widget
	// rectangle, exactly as a viewer would draw the annotation.
	bbox := rectOf(im.r, stm.dict["BBox"])
	m := identityMatrix
	if mArr, ok := im.r.resolve(stm.dict["Matrix"]).(Array); ok && len(mArr) == 6 {
		for i, e := range mArr {
			f, _ := toFloat(im.r.resolve(e))
			m[i] = f
		}
	}
	// Transform the bounding box and fit the result to the rectangle.
	x0, y0 := m.apply(bbox[0], bbox[1])
	x1, y1 := m.apply(bbox[2], bbox[3])
	bw, bh := absF(x1-x0), absF(y1-y0)
	sx, sy := 1.0, 1.0
	if bw > 0 {
		sx = (w.rect[2] - w.rect[0]) / bw
	}
	if bh > 0 {
		sy = (w.rect[3] - w.rect[1]) / bh
	}
	tx := w.rect[0] - minF(x0, x1)*sx
	ty := w.rect[1] - minF(y0, y1)*sy
	p.rawOp(fmt.Sprintf("q %s 0 0 %s %s %s cm /%s Do Q",
		fl(sx), fl(sy), fl(tx), fl(ty), name))
	return nil
}

func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// addXObjectResource registers a copied object in the page's own resource
// dictionary and returns the name to draw it with.
func (p *EditablePage) addXObjectResource(v any) string {
	if v == nil {
		return ""
	}
	if p.ownResources == nil {
		p.ownResources = Dict{}
	}
	sub, ok := p.ownResources["XObject"].(Dict)
	if !ok {
		sub = Dict{}
		p.ownResources["XObject"] = sub
	}
	p.apCount++
	name := fmt.Sprintf("%sAP%d", p.resPrefix, p.apCount)
	sub[Name(name)] = v
	return name
}

// drawFieldValue renders a new value into the page at the widget's
// rectangle, in the field's own default appearance where one is given.
func (p *EditablePage) drawFieldValue(w *widget, value string) error {
	switch w.field.Type {
	case FieldButton, FieldSignature:
		return fmt.Errorf("gopdf: form field %q is a %s and holds no value",
			w.field.Name, w.field.Type)
	case FieldCheckbox, FieldRadio:
		return p.drawCheck(w, value)
	}
	if w.field.MaxLen > 0 && len([]rune(value)) > w.field.MaxLen {
		return fmt.Errorf("gopdf: value for %q is %d characters, but the field allows %d",
			w.field.Name, len([]rune(value)), w.field.MaxLen)
	}
	if len(w.field.Options) > 0 && value != "" && w.field.Type == FieldChoice {
		valid := false
		for _, opt := range w.field.Options {
			if opt == value {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("gopdf: %q is not an option of choice field %q (have %v)",
				value, w.field.Name, w.field.Options)
		}
	}
	if value == "" {
		return nil
	}

	font, size, color := parseDefaultAppearance(w.da)
	height := w.rect[3] - w.rect[1]
	width := w.rect[2] - w.rect[0]
	if size <= 0 {
		// Auto-sized text: fit the box, then shrink to fit the width.
		size = height * 0.66
		if size > 12 {
			size = 12
		}
		for size > 4 && font.TextWidth(value, size) > width-4 {
			size -= 0.5
		}
	}
	// The page's coordinate system is top-left based; the widget
	// rectangle is in PDF coordinates, so convert through the media box.
	box := p.Page.mediaBox
	left := w.rect[0] - box[0] + 2
	right := w.rect[2] - box[0] - 2
	// Centre the text vertically on the field's baseline.
	baseline := box[3] - (w.rect[1] + (height-size*0.72)/2)

	p.Push()
	p.ClipRect(w.rect[0]-box[0], box[3]-w.rect[3], width, height)
	p.SetFont(font, size)
	p.SetFillColor(color)
	align := AlignLeft
	switch w.quad {
	case 1:
		align = AlignCenter
	case 2:
		align = AlignRight
	}
	p.TextAligned(left, baseline, right-left, align, value)
	p.Pop()
	return nil
}

// drawCheck marks or clears a checkbox or radio button.
func (p *EditablePage) drawCheck(w *widget, value string) error {
	on := value != "" && value != "Off"
	if on && w.onState != "" && value != string(w.onState) {
		// A radio group: only the widget whose state matches is marked.
		on = false
	}
	if !on {
		return nil
	}
	box := p.Page.mediaBox
	x := w.rect[0] - box[0]
	y := box[3] - w.rect[3]
	width := w.rect[2] - w.rect[0]
	height := w.rect[3] - w.rect[1]
	size := minF(width, height)

	p.Push()
	_, _, color := parseDefaultAppearance(w.da)
	p.SetFillColor(color)
	if w.field.Type == FieldRadio {
		p.Circle(x+width/2, y+height/2, size*0.3, Fill)
	} else {
		// ZapfDingbats '4' is a check mark, which every viewer has.
		p.SetFont(ZapfDingbats, size*0.8)
		p.Text(x+width*0.15, y+height*0.8, "4")
	}
	p.Pop()
	return nil
}

// parseDefaultAppearance reads the font, size and colour out of a /DA
// string such as "/Helv 9 Tf 0 g".
func parseDefaultAppearance(da string) (*Font, float64, Color) {
	font, size, color := Helvetica, 0.0, Black
	fields := strings.Fields(da)
	for i, tok := range fields {
		switch tok {
		case "Tf":
			if i >= 2 {
				if f, err := parseFloatToken(fields[i-1]); err == nil {
					size = f
				}
				font = standardFontByShortName(strings.TrimPrefix(fields[i-2], "/"))
			}
		case "g":
			if i >= 1 {
				if v, err := parseFloatToken(fields[i-1]); err == nil {
					color = Gray(uint8(clamp01(v) * 255))
				}
			}
		case "rg":
			if i >= 3 {
				var c [3]float64
				ok := true
				for k := 0; k < 3; k++ {
					v, err := parseFloatToken(fields[i-3+k])
					if err != nil {
						ok = false
						break
					}
					c[k] = clamp01(v)
				}
				if ok {
					color = RGB(uint8(c[0]*255), uint8(c[1]*255), uint8(c[2]*255))
				}
			}
		}
	}
	return font, size, color
}

// standardFontByShortName maps the abbreviations Acrobat uses in form
// resources onto the standard fonts.
func standardFontByShortName(name string) *Font {
	switch name {
	case "Helv", "Arial", "Helvetica":
		return Helvetica
	case "HeBo", "Helvetica-Bold", "Arial-Bold":
		return HelveticaBold
	case "TiRo", "Times", "Times-Roman":
		return TimesRoman
	case "TiBo", "Times-Bold":
		return TimesBold
	case "TiIt", "Times-Italic":
		return TimesItalic
	case "Cour", "Courier":
		return Courier
	case "CoBo", "Courier-Bold":
		return CourierBold
	case "ZaDb", "ZapfDingbats":
		return ZapfDingbats
	case "Symb", "Symbol":
		return Symbol
	}
	return Helvetica
}

func parseFloatToken(s string) (float64, error) {
	var f float64
	_, err := fmt.Sscanf(s, "%g", &f)
	return f, err
}
