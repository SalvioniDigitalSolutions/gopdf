package gopdf

// Optional content.
//
// A PDF can carry layers that are switched off: a draft stamp kept for
// later, a set of dimensions meant for one audience, a translation of
// the labels on a diagram. The document says which are off, and a
// renderer that ignores it does not merely miss a nicety — it paints
// things the author decided nobody should see, over the things they
// should.
//
// Two places say a layer is in play. Marked content brackets a run of
// operators with /OC ... BDC and EMC, and an XObject can carry an /OC of
// its own. Both are checked against the same set.

// hiddenLayers collects the optional-content groups the document's
// default configuration switches off.
//
// Groups are named by reference, because that is how content refers to
// them: a name in the resources leads to a group object, and it is the
// object that is on or off.
func hiddenLayers(r *Reader) map[int]bool {
	props, ok := r.resolve(r.Catalog()["OCProperties"]).(Dict)
	if !ok {
		return nil
	}
	cfg, ok := r.resolve(props["D"]).(Dict)
	if !ok {
		return nil
	}
	off := make(map[int]bool)
	// /BaseState OFF turns everything off, and /ON then names the
	// exceptions. It is rare, and getting it backwards would hide the
	// whole document.
	if r.resolve(cfg["BaseState"]) == Name("OFF") {
		if all, ok := r.resolve(props["OCGs"]).(Array); ok {
			for _, e := range all {
				if ref, ok := e.(Ref); ok {
					off[ref.Num] = true
				}
			}
		}
		for _, e := range arrayOf(r, cfg["ON"]) {
			if ref, ok := e.(Ref); ok {
				delete(off, ref.Num)
			}
		}
		return off
	}
	for _, e := range arrayOf(r, cfg["OFF"]) {
		if ref, ok := e.(Ref); ok {
			off[ref.Num] = true
		}
	}
	if len(off) == 0 {
		return nil
	}
	return off
}

func arrayOf(r *Reader, v any) Array {
	a, _ := r.resolve(v).(Array)
	return a
}

// layerHidden reports whether an /OC entry names something switched off.
//
// The entry is either a group or a membership dictionary, which combines
// several groups under a policy. The policies are read as written; an
// unrecognised one shows its content, because the failure that matters
// is hiding something that should be seen.
func (rn *renderer) layerHidden(v any) bool {
	if len(rn.hidden) == 0 || v == nil {
		return false
	}
	return rn.hiddenValue(v, 0)
}

func (rn *renderer) hiddenValue(v any, depth int) bool {
	if depth > 8 {
		return false
	}
	if ref, ok := v.(Ref); ok && rn.hidden[ref.Num] {
		return true
	}
	d, ok := rn.r.resolve(v).(Dict)
	if !ok {
		return false
	}
	if rn.r.resolve(d["Type"]) != Name("OCMD") {
		return false // a plain group, already checked by reference
	}
	// A membership dictionary gathers groups under a policy.
	var groups []any
	switch t := d["OCGs"].(type) {
	case Ref:
		groups = []any{t}
	default:
		for _, e := range arrayOf(rn.r, d["OCGs"]) {
			groups = append(groups, e)
		}
	}
	if len(groups) == 0 {
		return false
	}
	on := 0
	for _, g := range groups {
		if !rn.hiddenValue(g, depth+1) {
			on++
		}
	}
	switch rn.r.resolve(d["P"]) {
	case Name("AllOn"):
		return on != len(groups)
	case Name("AnyOff"):
		return on == len(groups)
	case Name("AllOff"):
		return on != 0
	default: // AnyOn, and anything unrecognised
		return on == 0
	}
}

// ocHidden resolves the operand of an /OC ... BDC against the resources,
// which is where a name is turned into the group it stands for.
func (rn *renderer) ocHidden(res Dict, operand any) bool {
	if len(rn.hidden) == 0 {
		return false
	}
	name, ok := operand.(Name)
	if !ok {
		return rn.layerHidden(operand)
	}
	props, ok := rn.r.resolve(res["Properties"]).(Dict)
	if !ok {
		return false
	}
	return rn.layerHidden(props[name])
}
