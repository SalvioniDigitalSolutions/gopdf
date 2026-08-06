package gopdf

import (
	"fmt"
	"strings"
)

// validateFormValues checks every requested change against the document's
// fields before anything is modified.
func validateFormValues(widgets []*widget, values map[string]string) error {
	if len(widgets) == 0 && len(values) > 0 {
		return fmt.Errorf("gopdf: document has no interactive form fields")
	}
	byName := make(map[string]*FormField, len(widgets))
	for _, w := range widgets {
		byName[w.field.Name] = w.field
	}
	for name, value := range values {
		f, ok := byName[name]
		if !ok {
			return fmt.Errorf("gopdf: no form field named %q", name)
		}
		if f.ReadOnly {
			return fmt.Errorf("gopdf: form field %q is read-only", name)
		}
		switch f.Type {
		case FieldButton, FieldSignature:
			return fmt.Errorf("gopdf: form field %q is a %s and holds no value", name, f.Type)
		case FieldText:
			if f.MaxLen > 0 && len([]rune(value)) > f.MaxLen {
				return fmt.Errorf("gopdf: value for %q is %d characters, but the field allows %d",
					name, len([]rune(value)), f.MaxLen)
			}
		case FieldChoice:
			if value == "" || len(f.Options) == 0 {
				break
			}
			for _, opt := range f.Options {
				if opt == value {
					return nil
				}
			}
			return fmt.Errorf("gopdf: %q is not an option of choice field %q (have %v)",
				value, name, f.Options)
		}
	}
	return nil
}

// FillFormInteractive fills the named fields and keeps the form editable:
// the output still has interactive fields, with the new values in place
// and freshly generated appearance streams so they display correctly
// before a viewer touches them.
//
// Use FillForm instead when the result should be final — flattening makes
// the values part of the page and stops the recipient changing them.
func (d *Document) FillFormInteractive(r *Reader, values map[string]string) (int, error) {
	widgets := r.formWidgets()
	if err := validateFormValues(widgets, values); err != nil {
		return 0, err
	}
	root, _ := r.resolve(r.trailer["Root"]).(Dict)
	acro, _ := r.resolve(root["AcroForm"]).(Dict)
	if acro == nil {
		if len(values) > 0 {
			return 0, fmt.Errorf("gopdf: document has no interactive form")
		}
		return 0, d.AppendPDF(r)
	}
	im := &importer{r: r, d: d, memo: d.importMemo(r)}

	// Copy the form definition: the field tree, the default appearance
	// and the default resources its /DA strings refer to.
	fieldsCopy, err := im.copy(acro["Fields"], 0)
	if err != nil {
		return 0, err
	}
	form := Dict{"Fields": fieldsCopy}
	for _, key := range []Name{"DA", "DR", "Q", "SigFlags", "CO"} {
		if v, ok := acro[key]; ok {
			cp, err := im.copy(v, 0)
			if err != nil {
				return 0, err
			}
			form[key] = cp
		}
	}

	filled := 0
	updatedFields := make(map[*FormField]bool)
	for _, w := range widgets {
		value, changed := values[w.field.Name]
		if !changed {
			continue
		}
		if err := im.applyFieldValue(w, value, acro, form); err != nil {
			return filled, err
		}
		if !updatedFields[w.field] {
			updatedFields[w.field] = true
			filled++
		}
	}

	// Rebuild the pages, carrying their widget annotations across.
	for i := 0; i < r.NumPages(); i++ {
		page, err := d.EditPage(r, i)
		if err != nil {
			return filled, err
		}
		annots, _ := r.resolve(r.pages[i].dict["Annots"]).(Array)
		for _, a := range annots {
			ad, ok := r.resolve(a).(Dict)
			if !ok {
				continue
			}
			if r.resolve(ad["Subtype"]) != Name("Widget") {
				continue
			}
			cp, err := im.copy(a, 0)
			if err != nil {
				return filled, err
			}
			// /P names the page the widget sits on; the annotation is
			// reachable through this page's /Annots, and the new page
			// object cannot be referenced from a copied dictionary.
			if dict := im.deref(cp); dict != nil {
				delete(dict, "P")
			}
			page.rawAnnots = append(page.rawAnnots, cp)
		}
	}
	d.acroForm = form
	return filled, nil
}

// deref returns the dictionary a copied value denotes, following a
// writer-side reference into the document's object table.
func (im *importer) deref(v any) Dict {
	switch t := v.(type) {
	case Dict:
		return t
	case rawRef:
		if int(t) < len(im.d.raw) {
			d, _ := im.d.raw[t].(Dict)
			return d
		}
	}
	return nil
}

// applyFieldValue writes a new value into the copied field and widget
// dictionaries, regenerating the widget's appearance where one is needed.
func (im *importer) applyFieldValue(w *widget, value string, acro, form Dict) error {
	fieldCopy := im.deref(mustCopy(im, w.fieldNode))
	widgetCopy := im.deref(mustCopy(im, w.widgetNode))
	if fieldCopy == nil {
		return fmt.Errorf("gopdf: cannot update form field %q", w.field.Name)
	}

	switch w.field.Type {
	case FieldCheckbox, FieldRadio:
		state := Name("Off")
		if value != "" && value != "Off" {
			// For a radio group only the widget whose own state matches
			// the value is switched on.
			if w.onState == "" || string(w.onState) == value {
				state = Name(value)
				if w.onState != "" {
					state = w.onState
				}
			}
		}
		fieldCopy["V"] = Name(valueOrOff(value))
		if widgetCopy != nil {
			widgetCopy["AS"] = state
		}
		return nil
	}

	fieldCopy["V"] = String(textStringBytes(value))
	if widgetCopy == nil {
		return nil
	}
	ap, err := im.buildTextAppearance(w, value, acro, form)
	if err != nil {
		return err
	}
	if ap != nil {
		widgetCopy["AP"] = Dict{"N": ap}
	}
	return nil
}

// valueOrOff normalizes a button value to the name stored in /V.
func valueOrOff(value string) string {
	if value == "" {
		return "Off"
	}
	return value
}

func mustCopy(im *importer, node any) any {
	cp, err := im.copy(node, 0)
	if err != nil {
		return nil
	}
	return cp
}

// buildTextAppearance creates the form XObject a viewer draws for a text
// or choice field, matching the field's own default appearance.
func (im *importer) buildTextAppearance(w *widget, value string, acro, form Dict) (any, error) {
	width := w.rect[2] - w.rect[0]
	height := w.rect[3] - w.rect[1]
	if width <= 0 || height <= 0 {
		return nil, nil
	}
	font, size, color := parseDefaultAppearance(w.da)
	daName, daFontRef := im.defaultAppearanceFont(w.da, acro)

	// Measure with the real font where the form supplies one, so the
	// text is positioned and auto-sized the way a viewer would.
	measure := func(s string, at float64) float64 { return font.TextWidth(s, at) }
	if daFontRef != nil {
		if fi := im.fontInfoFor(daFontRef); fi != nil {
			measure = func(s string, at float64) float64 {
				codes, err := fi.encodeText(s)
				if err != nil {
					return font.TextWidth(s, at)
				}
				return fi.stringWidth(codes, 0, 0, at) / 1000 * at
			}
		}
	}
	if size <= 0 {
		size = height * 0.66
		if size > 12 {
			size = 12
		}
		for size > 4 && measure(value, size) > width-4 {
			size -= 0.5
		}
	}

	// Resource name and object for the appearance's own font dictionary.
	resFont := Dict{}
	name := daName
	if daFontRef != nil {
		resFont[Name(name)] = daFontRef
	} else {
		name = "GpHelv"
		resFont[Name(name)] = docFontRef(im.d.addFont(font))
	}

	x := 2.0
	switch w.quad {
	case 1:
		x = (width - measure(value, size)) / 2
	case 2:
		x = width - 2 - measure(value, size)
	}
	if x < 2 {
		x = 2
	}
	y := (height - size*0.72) / 2

	var content strings.Builder
	// The marked-content tags are what viewers look for when they decide
	// whether an appearance is theirs to regenerate.
	content.WriteString("/Tx BMC\nq\n")
	fmt.Fprintf(&content, "BT\n/%s %s Tf\n%s rg\n%s %s Td\n(%s) Tj\nET\n",
		name, fl(size), color.components(), fl(x), fl(y),
		escapeString(winAnsiEncode(value)))
	content.WriteString("Q\nEMC")

	data := []byte(content.String())
	dict := Dict{
		"Type": Name("XObject"), "Subtype": Name("Form"),
		"BBox":      Array{float64(0), float64(0), width, height},
		"Resources": Dict{"Font": resFont},
	}
	if im.d.Compress {
		compressed, err := flateCompress(data)
		if err != nil {
			return nil, err
		}
		data = compressed
		dict["Filter"] = Name("FlateDecode")
	}
	stream := &rawStream{dict: dict, data: data}
	rr := rawRef(len(im.d.raw))
	im.d.raw = append(im.d.raw, stream)
	return rr, nil
}

// defaultAppearanceFont resolves the font a /DA string names against the
// form's default resources, returning the resource name and a copy of the
// font object (nil when the form does not supply one).
func (im *importer) defaultAppearanceFont(da string, acro Dict) (string, any) {
	name := ""
	fields := strings.Fields(da)
	for i, tok := range fields {
		if tok == "Tf" && i >= 2 {
			name = strings.TrimPrefix(fields[i-2], "/")
		}
	}
	if name == "" {
		return "", nil
	}
	dr, ok := im.r.resolve(acro["DR"]).(Dict)
	if !ok {
		return name, nil
	}
	fonts, ok := im.r.resolve(dr["Font"]).(Dict)
	if !ok {
		return name, nil
	}
	entry, ok := fonts[Name(name)]
	if !ok {
		return name, nil
	}
	cp, err := im.copy(entry, 0)
	if err != nil {
		return name, nil
	}
	im.daFontSource = entry
	return name, cp
}

// fontInfoFor builds measurement tables for the form's default font.
func (im *importer) fontInfoFor(_ any) *fontInfo {
	if im.daFontSource == nil {
		return nil
	}
	dict, ok := im.r.resolve(im.daFontSource).(Dict)
	if !ok {
		return nil
	}
	fd := &fontDecoders{r: im.r, cache: map[Name]*fontDecoder{}}
	dec := &fontDecoder{}
	if im.r.resolve(dict["Subtype"]) == Name("Type0") {
		return nil // composite form fonts are rare; fall back to metrics
	}
	dec.encoding = simpleEncoding(im.r, dict["Encoding"])
	_ = fd
	return newFontInfo(im.r, "DA", dict, dec)
}
