package gopdf

import (
	"errors"
	"fmt"
)

// Authoring optional content.
//
// The renderer already honours layers a document switches off. This is
// the other half: putting content on a layer in the first place, and
// changing which layers a document starts with.
//
// A layer is a group object the catalog lists, and content joins it by
// being bracketed with /OC ... BDC and EMC. Everything about it lives in
// two places at once — the catalog's configuration says whether it is
// on, and the content stream says what belongs to it — so both are
// written here rather than leaving a caller to assemble them.

// Layer is a named piece of optional content: a watermark, a set of
// annotations for one audience, a language of labels.
type Layer struct {
	// Name is what a viewer shows in its layers panel.
	Name string
	// On is whether the layer starts visible.
	On bool

	// ref is the group object, once the document has one.
	ref  rawRef
	uref Ref
	set  bool
}

// AddLayer declares a layer on a document being built.
//
// Draw on it by bracketing content with BeginLayer and EndLayer; a layer
// nothing draws on still appears in a viewer's panel, which is what a
// caller wants for one they mean to fill in later.
func (d *Document) AddLayer(name string, on bool) (*Layer, error) {
	if name == "" {
		return nil, errors.New("gopdf: a layer needs a name")
	}
	l := &Layer{Name: name, On: on, set: true}
	l.ref = rawRef(len(d.raw))
	d.raw = append(d.raw, Dict{
		"Type": Name("OCG"),
		"Name": String(textStringBytes(name)),
	})
	d.layers = append(d.layers, l)
	return l, nil
}

// BeginLayer starts a run of content belonging to a layer. Every
// BeginLayer needs an EndLayer, and they may not overlap: a viewer
// reading a stream that closes them out of order draws the wrong things.
func (p *Page) BeginLayer(l *Layer) {
	if l == nil || !l.set {
		return
	}
	name := p.doc.layerResName(l)
	p.op("/OC /%s BDC", name)
	p.layerDepth++
}

// EndLayer closes the most recent BeginLayer.
func (p *Page) EndLayer() {
	if p.layerDepth == 0 {
		return
	}
	p.layerDepth--
	p.op("EMC")
}

// layerResName is the name a page's resources give a layer, assigned on
// first use so a document that declares a layer and never draws on it
// does not carry a resource for it.
func (d *Document) layerResName(l *Layer) string {
	for i, seen := range d.layerUsed {
		if seen == l {
			return layerName(i)
		}
	}
	d.layerUsed = append(d.layerUsed, l)
	return layerName(len(d.layerUsed) - 1)
}

func layerName(i int) string { return fmt.Sprintf("OC%d", i) }

// buildLayers returns the /OCProperties for the declared layers, or nil
// if there are none. Like the other catalog trees it runs before object
// numbers are handed out.
func (d *Document) buildLayers() any {
	if len(d.layers) == 0 {
		return nil
	}
	all := make(Array, 0, len(d.layers))
	var off Array
	for _, l := range d.layers {
		all = append(all, l.ref)
		if !l.On {
			off = append(off, l.ref)
		}
	}
	cfg := Dict{"Order": all}
	if len(off) > 0 {
		cfg["OFF"] = off
	}
	ref := rawRef(len(d.raw))
	d.raw = append(d.raw, Dict{"OCGs": all, "D": cfg})
	return ref
}

// layerProperties is the /Properties entry a page's resources need, or
// nil when no layer was drawn on.
func (d *Document) layerProperties() Dict {
	if len(d.layerUsed) == 0 {
		return nil
	}
	out := Dict{}
	for i, l := range d.layerUsed {
		out[Name(layerName(i))] = l.ref
	}
	return out
}

// --- reading and changing an existing document ---

// Layers lists the optional content an existing document defines, in the
// order its default configuration puts them.
func (r *Reader) Layers() []Layer {
	props, ok := r.resolve(r.Catalog()["OCProperties"]).(Dict)
	if !ok {
		return nil
	}
	hidden := hiddenLayers(r)
	seen := make(map[int]bool)
	var out []Layer

	add := func(v any) {
		ref, isRef := v.(Ref)
		if !isRef || seen[ref.Num] {
			return
		}
		d, ok := r.resolve(v).(Dict)
		if !ok || r.resolve(d["Type"]) != Name("OCG") {
			return
		}
		seen[ref.Num] = true
		l := Layer{On: !hidden[ref.Num], uref: ref, set: true}
		if n, ok := r.resolve(d["Name"]).(String); ok {
			l.Name = decodeTextString(n)
		}
		out = append(out, l)
	}
	// The configuration's order is what a viewer shows; anything it
	// misses is still a layer and comes after.
	if cfg, ok := r.resolve(props["D"]).(Dict); ok {
		var walk func(v any, depth int)
		walk = func(v any, depth int) {
			if depth > 16 {
				return
			}
			switch t := r.resolve(v).(type) {
			case Array:
				for _, e := range t {
					if _, isRef := e.(Ref); isRef {
						add(e)
						continue
					}
					walk(e, depth+1)
				}
			}
		}
		walk(cfg["Order"], 0)
	}
	for _, e := range arrayOf(r, props["OCGs"]) {
		add(e)
	}
	return out
}

// SetLayerVisible turns a layer of an existing document on or off,
// appended incrementally. The layer is named as Layers reports it.
func (u *Updater) SetLayerVisible(name string, on bool) error {
	props, ok := u.r.resolve(u.pendingValue(u.catalogEntry("OCProperties"))).(Dict)
	if !ok {
		return errors.New("gopdf: the document has no optional content")
	}
	var target Ref
	for _, l := range u.r.Layers() {
		if l.Name == name {
			target = l.uref
		}
	}
	if target.Num == 0 {
		return errors.New("gopdf: no layer called " + name)
	}
	cfg, _ := u.r.resolve(props["D"]).(Dict)
	newCfg := cfg.Clone()
	if newCfg == nil {
		newCfg = Dict{}
	}
	// The switched-off list is rebuilt rather than edited, since the one
	// in the file may name the layer more than once or not at all.
	var off Array
	for _, l := range u.r.Layers() {
		hide := !l.On
		if l.uref == target {
			hide = !on
		}
		if hide {
			off = append(off, l.uref)
		}
	}
	if len(off) == 0 {
		delete(newCfg, "OFF")
	} else {
		newCfg["OFF"] = off
	}
	// /BaseState OFF inverts the meaning of the lists; rebuilding them
	// under it would hide everything, so it is cleared and the state
	// said outright.
	delete(newCfg, "BaseState")
	delete(newCfg, "ON")

	newProps := props.Clone()
	newProps["D"] = newCfg
	return u.SetCatalogEntry("OCProperties", newProps)
}

// catalogEntry reads a catalog key, preferring what this update wrote.
func (u *Updater) catalogEntry(key Name) any {
	cat := u.r.Catalog()
	if pending, ok := u.changed[refNumOr(u.r.trailer["Root"])].(Dict); ok {
		cat = pending
	}
	return cat[key]
}
