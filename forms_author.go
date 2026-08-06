package gopdf

import (
	"fmt"
	"strings"
)

// FieldOptions configures a form field being added to a page. The zero
// value is valid: a left-aligned, black, auto-sized Helvetica field with
// a thin grey border and no initial value.
type FieldOptions struct {
	// Value is the field's initial value. For a checkbox or radio button
	// use the Selected flag instead.
	Value string
	// Font and FontSize set the text appearance. A zero FontSize means
	// the text is sized to fit the field.
	Font     *Font
	FontSize float64
	// Color is the text colour.
	Color Color
	// Align sets the horizontal alignment of the value.
	Align Align
	// MaxLen limits a text field to a number of characters; 0 is
	// unlimited.
	MaxLen int
	// Multiline makes a text field accept line breaks.
	Multiline bool
	// ReadOnly and Required set the corresponding field flags.
	ReadOnly, Required bool
	// Border is the colour of the field's outline. Set NoBorder to omit
	// it entirely.
	Border   Color
	NoBorder bool
	// Background fills the field when non-nil.
	Background *Color
	// Tooltip is the text a viewer shows on hover.
	Tooltip string
	// Selected marks a checkbox or radio button as initially chosen.
	Selected bool
}

func (o FieldOptions) font() *Font {
	if o.Font != nil {
		return o.Font
	}
	return Helvetica
}

func (o FieldOptions) borderColor() (Color, bool) {
	if o.NoBorder {
		return Color{}, false
	}
	if o.Border == (Color{}) {
		return Gray(128), true // a visible default rather than invisible black
	}
	return o.Border, true
}

// acroField is a form field this library authors.
type acroField struct {
	name    string
	ftype   Name
	da      string
	flags   int
	maxLen  int
	options []string
	value   string
	tooltip string
	widgets []*acroWidget

	num int // object number, assigned at write time
}

// acroWidget is one on-page control belonging to a field.
type acroWidget struct {
	page  *Page
	rect  [4]float64 // PDF coordinates
	fonts []int      // document font indexes used by the appearances
	// states maps an appearance state to its content stream. A text or
	// choice field has the single state "".
	states  map[string][]byte
	order   []string
	onState string
	current string

	num    int
	apNums map[string]int
}

// addField registers a new field, rejecting a name already in use.
func (d *Document) addField(f *acroField) error {
	for _, existing := range d.acroFields {
		if existing.name == f.name {
			return fmt.Errorf("gopdf: a form field named %q already exists", f.name)
		}
	}
	d.acroFields = append(d.acroFields, f)
	return nil
}

func (d *Document) findField(name string) *acroField {
	for _, f := range d.acroFields {
		if f.name == name {
			return f
		}
	}
	return nil
}

// fieldFlags builds the /Ff value from the common options.
func fieldFlags(o FieldOptions) int {
	flags := 0
	if o.ReadOnly {
		flags |= 1
	}
	if o.Required {
		flags |= 2
	}
	return flags
}

// pdfRect converts a top-left rectangle on the page to PDF coordinates.
func (p *Page) pdfRect(x, y, w, h float64) [4]float64 {
	ox, oy := 0.0, 0.0
	top := p.h
	if p.mediaBox != nil {
		ox, oy = p.mediaBox[0], p.mediaBox[1]
		top = p.mediaBox[3]
		_ = oy
	}
	return [4]float64{ox + x, top - y - h, ox + x + w, top - y}
}

// AddTextField adds an interactive text field with its top-left corner at
// (x, y). The document becomes an interactive form.
func (p *Page) AddTextField(name string, x, y, w, h float64, opts FieldOptions) error {
	if name == "" {
		return fmt.Errorf("gopdf: a form field needs a name")
	}
	if w <= 0 || h <= 0 {
		return fmt.Errorf("gopdf: form field %q has an empty rectangle", name)
	}
	if opts.MaxLen > 0 && len([]rune(opts.Value)) > opts.MaxLen {
		return fmt.Errorf("gopdf: initial value for %q exceeds its MaxLen of %d",
			name, opts.MaxLen)
	}
	flags := fieldFlags(opts)
	if opts.Multiline {
		flags |= 1 << 12
	}
	return p.addTextLike(name, "Tx", x, y, w, h, opts, flags, nil)
}

// AddChoiceField adds a drop-down list of the given options.
func (p *Page) AddChoiceField(name string, x, y, w, h float64, options []string, opts FieldOptions) error {
	if len(options) == 0 {
		return fmt.Errorf("gopdf: choice field %q needs at least one option", name)
	}
	if opts.Value != "" {
		found := false
		for _, o := range options {
			if o == opts.Value {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("gopdf: %q is not one of the options of choice field %q",
				opts.Value, name)
		}
	}
	// Combo box, so the value shows even when the list is closed.
	flags := fieldFlags(opts) | 1<<17
	return p.addTextLike(name, "Ch", x, y, w, h, opts, flags, options)
}

// addTextLike creates a text or choice field, which share an appearance.
func (p *Page) addTextLike(name string, ftype Name, x, y, w, h float64,
	opts FieldOptions, flags int, options []string) error {

	font := opts.font()
	fontIdx := p.doc.addFont(font)
	size := opts.FontSize
	if size <= 0 {
		size = h * 0.62
		if size > 12 {
			size = 12
		}
		for size > 4 && opts.Value != "" && font.TextWidth(opts.Value, size) > w-4 {
			size -= 0.5
		}
	}
	da := fmt.Sprintf("/F%d %s Tf %s rg", fontIdx+1, fl(size), opts.Color.components())

	field := &acroField{
		name: name, ftype: ftype, da: da, flags: flags,
		maxLen: opts.MaxLen, options: options, value: opts.Value,
		tooltip: opts.Tooltip,
	}
	ap := textFieldAppearance(w, h, size, fontIdx, font, opts)
	field.widgets = []*acroWidget{{
		page:   p,
		rect:   p.pdfRect(x, y, w, h),
		fonts:  []int{fontIdx},
		states: map[string][]byte{"": ap},
		order:  []string{""},
		apNums: map[string]int{},
	}}
	return p.doc.addField(field)
}

// AddCheckbox adds a square checkbox with its top-left corner at (x, y).
func (p *Page) AddCheckbox(name string, x, y, size float64, opts FieldOptions) error {
	if name == "" {
		return fmt.Errorf("gopdf: a form field needs a name")
	}
	if size <= 0 {
		return fmt.Errorf("gopdf: checkbox %q has an empty rectangle", name)
	}
	dingbats := p.doc.addFont(ZapfDingbats)
	value := "Off"
	if opts.Selected {
		value = "Yes"
	}
	field := &acroField{
		name: name, ftype: "Btn", flags: fieldFlags(opts),
		da:      fmt.Sprintf("/F%d 0 Tf %s rg", dingbats+1, opts.Color.components()),
		value:   value,
		tooltip: opts.Tooltip,
	}
	off, on := checkboxAppearances(size, dingbats, opts)
	field.widgets = []*acroWidget{{
		page:    p,
		rect:    p.pdfRect(x, y, size, size),
		fonts:   []int{dingbats},
		states:  map[string][]byte{"Off": off, "Yes": on},
		order:   []string{"Off", "Yes"},
		onState: "Yes",
		current: value,
		apNums:  map[string]int{},
	}}
	return p.doc.addField(field)
}

// AddRadioButton adds one button of a radio group. Call it once per
// choice, with the same group name and a distinct value; only the button
// whose value matches the group's selection is filled in.
func (p *Page) AddRadioButton(group, value string, x, y, size float64, opts FieldOptions) error {
	if group == "" || value == "" {
		return fmt.Errorf("gopdf: a radio button needs a group name and a value")
	}
	if value == "Off" {
		return fmt.Errorf("gopdf: %q is reserved for the unselected state", value)
	}
	if size <= 0 {
		return fmt.Errorf("gopdf: radio button %q has an empty rectangle", value)
	}
	field := p.doc.findField(group)
	if field == nil {
		field = &acroField{
			name: group, ftype: "Btn",
			// Radio, and buttons in a set turn each other off.
			flags:   fieldFlags(opts) | 1<<15 | 1<<14,
			da:      fmt.Sprintf("0 Tf %s rg", opts.Color.components()),
			value:   "Off",
			tooltip: opts.Tooltip,
		}
		if err := p.doc.addField(field); err != nil {
			return err
		}
	} else if field.ftype != "Btn" || field.flags&(1<<15) == 0 {
		return fmt.Errorf("gopdf: form field %q is not a radio group", group)
	}
	for _, w := range field.widgets {
		if w.onState == value {
			return fmt.Errorf("gopdf: radio group %q already has a button %q", group, value)
		}
	}
	if opts.Selected {
		field.value = value
	}
	off, on := radioAppearances(size, opts)
	current := "Off"
	if opts.Selected {
		current = value
	}
	field.widgets = append(field.widgets, &acroWidget{
		page:    p,
		rect:    p.pdfRect(x, y, size, size),
		states:  map[string][]byte{"Off": off, value: on},
		order:   []string{"Off", value},
		onState: value,
		current: current,
		apNums:  map[string]int{},
	})
	return nil
}

// --- appearance streams ---

// fieldFrame draws a field's background and border, and clips to it.
func fieldFrame(b *strings.Builder, w, h float64, opts FieldOptions) {
	if bg := opts.Background; bg != nil {
		fmt.Fprintf(b, "%s rg 0 0 %s %s re f\n", bg.components(), fl(w), fl(h))
	}
	if border, ok := opts.borderColor(); ok {
		fmt.Fprintf(b, "%s RG 1 w 0.5 0.5 %s %s re S\n",
			border.components(), fl(w-1), fl(h-1))
	}
}

func textFieldAppearance(w, h, size float64, fontIdx int, font *Font, opts FieldOptions) []byte {
	var b strings.Builder
	b.WriteString("/Tx BMC\nq\n")
	fieldFrame(&b, w, h, opts)
	if opts.Value != "" {
		// Clip so an over-long value cannot spill outside the field.
		fmt.Fprintf(&b, "1 1 %s %s re W n\n", fl(w-2), fl(h-2))
		x := 2.0
		switch opts.Align {
		case AlignCenter:
			x = (w - font.TextWidth(opts.Value, size)) / 2
		case AlignRight:
			x = w - 2 - font.TextWidth(opts.Value, size)
		}
		if x < 2 {
			x = 2
		}
		y := (h - size*0.72) / 2
		if opts.Multiline {
			y = h - size
		}
		fmt.Fprintf(&b, "BT\n/F%d %s Tf\n%s rg\n%s %s Td\n(%s) Tj\nET\n",
			fontIdx+1, fl(size), opts.Color.components(), fl(x), fl(y),
			escapeString(winAnsiEncode(opts.Value)))
	}
	b.WriteString("Q\nEMC")
	return []byte(b.String())
}

func checkboxAppearances(size float64, dingbats int, opts FieldOptions) (off, on []byte) {
	var b strings.Builder
	b.WriteString("q\n")
	fieldFrame(&b, size, size, opts)
	b.WriteString("Q")
	off = []byte(b.String())

	b.Reset()
	b.WriteString("q\n")
	fieldFrame(&b, size, size, opts)
	mark := size * 0.8
	// ZapfDingbats '4' is a check mark, present in every viewer.
	fmt.Fprintf(&b, "BT\n/F%d %s Tf\n%s rg\n%s %s Td\n(4) Tj\nET\nQ",
		dingbats+1, fl(mark), opts.Color.components(),
		fl(size*0.15), fl(size*0.2))
	on = []byte(b.String())
	return off, on
}

func radioAppearances(size float64, opts FieldOptions) (off, on []byte) {
	r := size/2 - 0.5
	cx, cy := size/2, size/2
	circle := func(b *strings.Builder, radius float64, op string) {
		k := radius * kappa
		fmt.Fprintf(b, "%s %s m\n", fl(cx+radius), fl(cy))
		fmt.Fprintf(b, "%s %s %s %s %s %s c\n", fl(cx+radius), fl(cy+k), fl(cx+k), fl(cy+radius), fl(cx), fl(cy+radius))
		fmt.Fprintf(b, "%s %s %s %s %s %s c\n", fl(cx-k), fl(cy+radius), fl(cx-radius), fl(cy+k), fl(cx-radius), fl(cy))
		fmt.Fprintf(b, "%s %s %s %s %s %s c\n", fl(cx-radius), fl(cy-k), fl(cx-k), fl(cy-radius), fl(cx), fl(cy-radius))
		fmt.Fprintf(b, "%s %s %s %s %s %s c\n%s\n", fl(cx+k), fl(cy-radius), fl(cx+radius), fl(cy-k), fl(cx+radius), fl(cy), op)
	}

	var b strings.Builder
	b.WriteString("q\n")
	if bg := opts.Background; bg != nil {
		fmt.Fprintf(&b, "%s rg\n", bg.components())
		circle(&b, r, "f")
	}
	if border, ok := opts.borderColor(); ok {
		fmt.Fprintf(&b, "%s RG 1 w\n", border.components())
		circle(&b, r, "S")
	}
	b.WriteString("Q")
	off = []byte(b.String())

	b.Reset()
	b.WriteString(string(off[:len(off)-1])) // reuse the frame, drop the trailing Q
	fmt.Fprintf(&b, "%s rg\n", opts.Color.components())
	circle(&b, r*0.5, "f")
	b.WriteString("Q")
	on = []byte(b.String())
	return off, on
}
