package gopdf

import (
	"fmt"
	"strings"
)

// writeAcroForm emits the interactive form dictionary for the catalog,
// combining fields authored here with any imported form.
func (d *Document) writeAcroForm(ow *offsetWriter, ctx *writeCtx, fontNums []fontRefs) {
	// An imported form is written as-is when nothing was authored.
	if len(d.acroFields) == 0 {
		writeValue(ow, d.acroForm, ctx)
		return
	}
	ow.str("<< /Fields [")
	sep := ""
	for _, f := range d.acroFields {
		ow.printf("%s%d 0 R", sep, f.num)
		sep = " "
	}
	// Fields carried over from an imported form join the same array.
	if form, ok := d.acroForm.(Dict); ok {
		if imported, ok := form["Fields"].(Array); ok {
			for _, e := range imported {
				ow.str(sep)
				writeValue(ow, e, ctx)
				sep = " "
			}
		}
	}
	ow.str("]")

	// The default resources must cover every font a /DA string names.
	used := map[int]bool{}
	for _, f := range d.acroFields {
		for _, w := range f.widgets {
			for _, idx := range w.fonts {
				used[idx] = true
			}
		}
	}
	if len(used) > 0 {
		ow.str(" /DR << /Font <<")
		for i := range d.fonts {
			if used[i] {
				ow.printf(" /F%d %d 0 R", i+1, fontNums[i].font)
			}
		}
		ow.str(" >> >>")
	}
	ow.str(" /DA ")
	ow.pdfString("/F1 0 Tf 0 g")
	// The appearances written below are complete, so viewers need not
	// regenerate them.
	ow.str(" /NeedAppearances false >>")
}

// writeAcroField emits a field, its widget annotations and their
// appearance streams.
func (d *Document) writeAcroField(ow *offsetWriter, ctx *writeCtx, f *acroField,
	beginObj func(int), endObj func()) error {

	single := len(f.widgets) == 1

	beginObj(f.num)
	ow.printf("<< /FT /%s /T ", f.ftype)
	ow.pdfString(f.name)
	if f.tooltip != "" {
		ow.str(" /TU ")
		ow.pdfString(f.tooltip)
	}
	if f.flags != 0 {
		ow.printf(" /Ff %d", f.flags)
	}
	if f.ftype == "Btn" {
		ow.printf(" /V /%s /DV /%s", escapeName(f.value), escapeName(f.value))
	} else {
		ow.str(" /V ")
		ow.pdfString(f.value)
	}
	if f.da != "" {
		ow.str(" /DA ")
		ow.pdfString(f.da)
	}
	if f.maxLen > 0 {
		ow.printf(" /MaxLen %d", f.maxLen)
	}
	if len(f.options) > 0 {
		ow.str(" /Opt [")
		for i, opt := range f.options {
			if i > 0 {
				ow.str(" ")
			}
			ow.pdfString(opt)
		}
		ow.str("]")
	}
	if single {
		// The field is also its own widget annotation.
		d.writeWidgetEntries(ow, f, f.widgets[0], 0)
	} else {
		ow.str(" /Kids [")
		for i, w := range f.widgets {
			if i > 0 {
				ow.str(" ")
			}
			ow.printf("%d 0 R", w.num)
		}
		ow.str("]")
	}
	ow.str(" >>\n")
	endObj()

	if !single {
		for _, w := range f.widgets {
			beginObj(w.num)
			ow.printf("<< /Parent %d 0 R", f.num)
			d.writeWidgetEntries(ow, f, w, f.num)
			ow.str(" >>\n")
			endObj()
		}
	}

	for _, w := range f.widgets {
		for _, state := range w.order {
			beginObj(w.apNums[state])
			var extra strings.Builder
			fmt.Fprintf(&extra, "/Type /XObject /Subtype /Form /BBox [0 0 %s %s] ",
				fl(w.rect[2]-w.rect[0]), fl(w.rect[3]-w.rect[1]))
			if len(w.fonts) > 0 {
				extra.WriteString("/Resources << /Font <<")
				for _, idx := range w.fonts {
					fmt.Fprintf(&extra, " /F%d %d 0 R", idx+1, d.fontObjNum(idx))
				}
				extra.WriteString(" >> >> ")
			}
			if err := ow.writeStream(extra.String(), w.states[state], d.Compress); err != nil {
				return err
			}
			endObj()
		}
	}
	return nil
}

// writeWidgetEntries emits the annotation half of a field's dictionary.
func (d *Document) writeWidgetEntries(ow *offsetWriter, f *acroField, w *acroWidget, parent int) {
	ow.printf(" /Type /Annot /Subtype /Widget /Rect [%s %s %s %s] /F 4",
		fl(w.rect[0]), fl(w.rect[1]), fl(w.rect[2]), fl(w.rect[3]))
	if w.onState != "" {
		state := w.current
		if state == "" {
			state = "Off"
		}
		ow.printf(" /AS /%s", escapeName(state))
		ow.str(" /AP << /N <<")
		for _, s := range w.order {
			ow.printf(" /%s %d 0 R", escapeName(s), w.apNums[s])
		}
		ow.str(" >> >>")
	} else {
		ow.printf(" /AP << /N %d 0 R >>", w.apNums[""])
	}
}

// fontObjNum is filled in by the serializer so appearance streams can
// reference the document's fonts.
func (d *Document) fontObjNum(idx int) int {
	if d.fontNums != nil && idx < len(d.fontNums) {
		return d.fontNums[idx]
	}
	return 0
}

// escapeName escapes a string for use as a PDF name.
func escapeName(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= ' ' || c > '~' || c == '#' || isDelim(c) {
			fmt.Fprintf(&b, "#%02X", c)
		} else {
			b.WriteByte(c)
		}
	}
	if b.Len() == 0 {
		return "Off"
	}
	return b.String()
}
