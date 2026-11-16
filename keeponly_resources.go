package gopdf

// keeponly_resources.go — what a page draws with, kept by KeepOnly.
//
// A page's resources are kept only as far as its content uses them
// (keeponly_content.go says which), and each one only with the keys
// that make it draw: a picture's samples and colour, never its
// alternate copies or metadata; a font's program, widths and character
// map, never the metadata on its descriptor; a form's content, never
// its private data.

import "strings"

// resUse is what a content stream uses: per resource category, the
// names it uses.
type resUse map[Name]map[Name]bool

func (u resUse) add(cat, name Name) {
	if u[cat] == nil {
		u[cat] = map[Name]bool{}
	}
	u[cat][name] = true
}

// resources keeps of res the entries used names.
func (k *keeper) resources(res Dict, used resUse) Dict {
	// The default colour spaces apply to every device colour without
	// being named in the content.
	if cs, ok := k.r.resolve(res["ColorSpace"]).(Dict); ok {
		for _, n := range []Name{"DefaultGray", "DefaultRGB", "DefaultCMYK"} {
			if _, has := cs[n]; has {
				used.add("ColorSpace", n)
			}
		}
	}
	// A form, a pattern or a Type 3 font with no resources of its own
	// draws with the resources of what draws it.
	saved := k.inherit
	k.inherit = res
	defer func() { k.inherit = saved }()
	out := Dict{}
	cats := []struct {
		key  Name
		keep func(any) any
	}{
		{"Font", k.font}, {"XObject", k.xobject}, {"ColorSpace", k.colorSpace}, {"Pattern", k.pattern},
		{"Shading", k.shading}, {"ExtGState", k.extGState}, {"Properties", k.properties},
	}
	for _, c := range cats {
		names := used[c.key]
		if len(names) == 0 {
			continue
		}
		sub, _ := k.r.resolve(res[c.key]).(Dict)
		if sub == nil {
			continue
		}
		m := Dict{}
		for n := range names {
			if v, ok := sub[n]; ok {
				if kept := c.keep(v); kept != nil {
					m[n] = kept
				}
			}
		}
		if len(m) > 0 {
			out[c.key] = m
		}
	}
	if ps := k.namesOf(k.oneOf("PDF", "Text", "ImageB", "ImageC", "ImageI"))(res["ProcSet"]); ps != nil {
		out["ProcSet"] = ps
	}
	return out
}

// stream keeps a stream's data as stored, with the dictionary rebuilt
// from rules plus its filters.
func (k *keeper) stream(v any, rules map[Name]rule) any {
	stm, ok := v.(*rawStream)
	if !ok {
		return nil
	}
	all := map[Name]rule{"Filter": k.filterNames, "DecodeParms": k.decodeParms}
	for key, r := range rules {
		all[key] = r
	}
	d := k.dict(stm.dict, all)
	return &rawStream{dict: d, data: stm.data}
}

// filterNames keeps a stream's filters. A filter not known is a stream
// no viewer decodes; the copy is refused rather than carry it.
func (k *keeper) filterNames(v any) any {
	known := k.oneOf(filterNames...)
	switch t := k.r.resolve(v).(type) {
	case Name:
		if known(t) == nil {
			k.fail("a stream with a filter not known (%s)", t)
			return nil
		}
		return t
	case Array:
		out := Array{}
		for _, e := range t {
			n := known(e)
			if n == nil {
				k.fail("a stream with a filter not known")
				return nil
			}
			out = append(out, n)
		}
		return out
	}
	return nil
}

func (k *keeper) decodeParms(v any) any {
	one := func(e any) any {
		if k.r.resolve(e) == nil {
			return nil
		}
		return k.dictOrNil(e, map[Name]rule{
			"Predictor": k.number, "Colors": k.number, "BitsPerComponent": k.number, "Columns": k.number,
			"EarlyChange": k.number, "K": k.number, "EndOfLine": k.boolean, "EncodedByteAlign": k.boolean,
			"Rows": k.number, "EndOfBlock": k.boolean, "BlackIs1": k.boolean, "DamagedRowsBeforeError": k.number,
			"ColorTransform": k.number,
			"JBIG2Globals": func(v any) any {
				return k.value(v, "jbig2globals", func(v any) any { return k.stream(v, nil) })
			},
		})
	}
	if arr, ok := k.r.resolve(v).(Array); ok {
		out := make(Array, 0, len(arr))
		for _, e := range arr {
			kept := one(e)
			if kept == nil {
				out = append(out, nil)
				continue
			}
			out = append(out, kept)
		}
		return out
	}
	return one(v)
}

// --- pictures and forms ---

func (k *keeper) xobject(v any) any {
	stm, ok := k.r.resolve(v).(*rawStream)
	if !ok {
		return nil
	}
	switch sub, _ := k.r.resolve(stm.dict["Subtype"]).(Name); sub {
	case "Image":
		return k.value(v, "image", k.image)
	case "Form":
		return k.value(v, "form", k.form)
	}
	return nil
}

func (k *keeper) image(v any) any {
	kept := k.stream(v, map[Name]rule{
		"Type": fixed("XObject"), "Subtype": fixed("Image"),
		"Width": k.number, "Height": k.number, "BitsPerComponent": k.number,
		"ImageMask": k.boolean, "Intent": k.oneOf(riNames...), "Interpolate": k.boolean, "SMaskInData": k.number,
		"Decode": k.numbers, "StructParent": k.number,
		"ColorSpace": k.colorSpace,
		"Mask": func(v any) any {
			if arr, ok := k.r.resolve(v).(Array); ok {
				return k.numbers(arr)
			}
			return k.value(v, "image", k.image)
		},
		"SMask": func(v any) any { return k.value(v, "image", k.image) },
		"Matte": k.numbers,
	})
	stm, ok := kept.(*rawStream)
	if !ok {
		return nil
	}
	// A picture has a size, and samples of a depth (a mask's depth is
	// one; a JPEG 2000 file says its own). Anything else in a picture's
	// place is none.
	_, w := stm.dict["Width"]
	_, h := stm.dict["Height"]
	_, bpc := stm.dict["BitsPerComponent"]
	mask, _ := stm.dict["ImageMask"].(bool)
	jpx := false
	switch f := stm.dict["Filter"].(type) {
	case Name:
		jpx = f == "JPXDecode"
	case Array:
		jpx = len(f) > 0 && f[len(f)-1] == Name("JPXDecode")
	}
	if !w || !h || !(bpc || mask || jpx) {
		return nil
	}
	// What a picture file carries besides its samples — a camera's
	// notes, a comment, a preview — is not drawn and is left out.
	stm.data = pictureWithoutNotes(stm.dict, stm.data)
	return stm
}

func (k *keeper) form(v any) any {
	stm, ok := v.(*rawStream)
	if !ok {
		return nil
	}
	data := k.drawable(stm)
	res := k.ownResources(stm.dict)
	kept := k.content(data, res)
	if kept.err != nil {
		k.fail("a form: %v", kept.err)
		return nil
	}
	d := k.dict(stm.dict, map[Name]rule{
		"FormType": k.number, "BBox": k.numbers, "Matrix": k.numbers,
		"StructParent": k.number, "StructParents": k.number,
		"Group": func(v any) any { return k.value(v, "group", k.group) },
	})
	d["Type"] = Name("XObject")
	d["Subtype"] = Name("Form")
	if r := k.resources(res, kept.used); len(r) > 0 {
		d["Resources"] = r
	}
	out := compressedStream(kept.data)
	for key, val := range d {
		out.dict[key] = val
	}
	return out
}

// --- fonts ---

func (k *keeper) font(v any) any {
	return k.value(v, "font", func(v any) any {
		d, ok := v.(Dict)
		if !ok {
			return nil
		}
		out := k.dict(d, map[Name]rule{
			"Type":      k.oneOf("Font"),
			"Subtype":   k.oneOf("Type0", "Type1", "MMType1", "Type3", "TrueType", "CIDFontType0", "CIDFontType2"),
			"BaseFont":  k.freeName,
			"FirstChar": k.number, "LastChar": k.number, "Widths": k.numbers,
			"DW": k.number, "W": k.numbers, "DW2": k.numbers, "W2": k.numbers,
			"FontBBox": k.numbers, "FontMatrix": k.numbers,
			"FontDescriptor": func(v any) any { return k.value(v, "fontdescriptor", k.fontDescriptor) },
			"ToUnicode":      func(v any) any { return k.value(v, "cmap", func(v any) any { return k.cmapStream(v, true) }) },
			"Encoding": func(v any) any {
				switch t := k.r.resolve(v).(type) {
				case Name:
					return k.oneOf(fontEncodingNames...)(t)
				case Dict:
					return k.value(v, "encoding", func(v any) any {
						return k.dictOrNil(v, map[Name]rule{"Type": k.oneOf("Encoding"),
							"BaseEncoding": k.oneOf("StandardEncoding", "MacRomanEncoding", "WinAnsiEncoding", "MacExpertEncoding"),
							"Differences":  k.plainArray})
					})
				case *rawStream:
					return k.value(v, "cmap", func(v any) any { return k.cmapStream(v, false) })
				}
				return nil
			},
			"DescendantFonts": func(v any) any {
				arr, ok := k.r.resolve(v).(Array)
				if !ok {
					return nil
				}
				out := Array{}
				for _, e := range arr {
					if kept := k.font(e); kept != nil {
						out = append(out, kept)
					}
				}
				return out
			},
			"CIDSystemInfo": k.cidSystemInfo,
			"CIDToGIDMap": func(v any) any {
				if n, ok := k.r.resolve(v).(Name); ok {
					return n
				}
				return k.value(v, "cidtogid", func(v any) any { return k.stream(v, nil) })
			},
		})
		// A Type 3 font draws its glyphs with content of its own.
		if procs, ok := k.r.resolve(d["CharProcs"]).(Dict); ok {
			res := k.ownResources(d)
			used := resUse{}
			kept := Dict{}
			for glyph, proc := range procs {
				ref, isRef := proc.(Ref)
				build := func(v any) any {
					stm, ok := v.(*rawStream)
					if !ok {
						return nil
					}
					c := k.content(k.drawable(stm), res)
					if c.err != nil {
						k.fail("a glyph of a Type 3 font: %v", c.err)
						return nil
					}
					for cat, names := range c.used {
						for n := range names {
							used.add(cat, n)
						}
					}
					return compressedStream(c.data)
				}
				var g any
				if isRef {
					g = k.indirect(ref, "glyph", build)
				} else if built := build(proc); built != nil {
					g = k.newObject("glyph", built)
				}
				if g != nil {
					kept[glyph] = g
				}
			}
			out["CharProcs"] = kept
			if r := k.resources(res, used); len(r) > 0 {
				out["Resources"] = r
			}
		}
		return out
	})
}

// drawable is the content of a form, a pattern's cell or a glyph, as a
// viewer draws it. Content no decoder reads draws nothing in any
// viewer, and is kept as nothing: nothing of it is carried over.
func (k *keeper) drawable(stm *rawStream) []byte {
	data, err := k.r.decodeStream(stm.dict, stm.data)
	if err != nil {
		k.rep.Undecodable++
		return nil
	}
	return data
}

// fontEncodingNames are the encodings a font may name: the standard ones and
// the predefined character maps (ISO 32000-1, 9.6.6 and table 118).
var fontEncodingNames = func() []Name {
	var out []Name
	for _, n := range strings.Fields(`StandardEncoding MacRomanEncoding WinAnsiEncoding MacExpertEncoding PDFDocEncoding
		Identity-H Identity-V GB-EUC-H GB-EUC-V GBpc-EUC-H GBpc-EUC-V GBK-EUC-H GBK-EUC-V GBKp-EUC-H GBKp-EUC-V
		GBK2K-H GBK2K-V UniGB-UCS2-H UniGB-UCS2-V UniGB-UTF16-H UniGB-UTF16-V B5pc-H B5pc-V HKscs-B5-H HKscs-B5-V
		ETen-B5-H ETen-B5-V ETenms-B5-H ETenms-B5-V CNS-EUC-H CNS-EUC-V UniCNS-UCS2-H UniCNS-UCS2-V UniCNS-UTF16-H
		UniCNS-UTF16-V 83pv-RKSJ-H 90ms-RKSJ-H 90ms-RKSJ-V 90msp-RKSJ-H 90msp-RKSJ-V 90pv-RKSJ-H Add-RKSJ-H
		Add-RKSJ-V EUC-H EUC-V Ext-RKSJ-H Ext-RKSJ-V H V UniJIS-UCS2-H UniJIS-UCS2-V UniJIS-UCS2-HW-H
		UniJIS-UCS2-HW-V UniJIS-UTF16-H UniJIS-UTF16-V KSC-EUC-H KSC-EUC-V KSCms-UHC-H KSCms-UHC-V KSCms-UHC-HW-H
		KSCms-UHC-HW-V KSCpc-EUC-H UniKS-UCS2-H UniKS-UCS2-V UniKS-UTF16-H UniKS-UTF16-V`) {
		out = append(out, Name(n))
	}
	return out
}()

// booleans keeps an array of booleans.
func (k *keeper) booleans(v any) any {
	arr, ok := k.r.resolve(v).(Array)
	if !ok {
		return nil
	}
	out := Array{}
	for _, e := range arr {
		b, ok := k.r.resolve(e).(bool)
		if !ok {
			return nil
		}
		out = append(out, b)
	}
	return out
}

// ownResources is what d draws with: its own resources, or else those
// of what draws it.
func (k *keeper) ownResources(d Dict) Dict {
	if res, ok := k.r.resolve(d["Resources"]).(Dict); ok {
		return res
	}
	return k.inherit
}

func (k *keeper) cidSystemInfo(v any) any {
	return k.dictOrNil(v, map[Name]rule{"Registry": k.technical, "Ordering": k.technical, "Supplement": k.number})
}

func (k *keeper) fontDescriptor(v any) any {
	fontFile := func(key Name) rule {
		return func(v any) any {
			return k.value(v, "fontfile", func(v any) any {
				stm, ok := v.(*rawStream)
				if !ok {
					return nil
				}
				data, err := k.r.decodeStream(stm.dict, stm.data)
				sub, _ := k.r.resolve(stm.dict["Subtype"]).(Name)
				if err != nil || !fontProgramKind(key, sub, data) {
					return nil // no font program: the viewer draws with its own
				}
				return k.stream(v, map[Name]rule{"Length1": k.number, "Length2": k.number, "Length3": k.number,
					"Subtype": k.oneOf("Type1C", "CIDFontType0C", "OpenType")})
			})
		}
	}
	return k.dictOrNil(v, map[Name]rule{
		"Type": k.oneOf("FontDescriptor"), "FontName": k.freeName, "FontFamily": k.text,
		"FontStretch": k.oneOf("UltraCondensed", "ExtraCondensed", "Condensed", "SemiCondensed", "Normal",
			"SemiExpanded", "Expanded", "ExtraExpanded", "UltraExpanded"),
		"FontWeight": k.number, "Flags": k.number, "FontBBox": k.numbers, "ItalicAngle": k.number,
		"Ascent": k.number, "Descent": k.number, "Leading": k.number, "CapHeight": k.number,
		"XHeight": k.number, "StemV": k.number, "StemH": k.number, "AvgWidth": k.number,
		"MaxWidth": k.number, "MissingWidth": k.number, "CharSet": k.technical,
		"Lang": func(v any) any {
			if n, ok := k.r.resolve(v).(Name); ok && langTagRe.MatchString(string(n)) {
				return n
			}
			return nil
		},
		"FontFile": fontFile("FontFile"), "FontFile2": fontFile("FontFile2"), "FontFile3": fontFile("FontFile3"),
		"Style": func(v any) any { return k.dictOrNil(v, map[Name]rule{"Panose": k.technical}) },
	})
}

// cmapStream keeps a character map (a ToUnicode map, an encoding
// CMap), written anew from its mappings (keeponly_programs.go).
func (k *keeper) cmapStream(v any, toUnicode bool) any {
	stm, ok := v.(*rawStream)
	if !ok {
		return nil
	}
	data, err := k.r.decodeStream(stm.dict, stm.data)
	if err != nil {
		return nil
	}
	registry, ordering, supplement := String("Adobe"), String("UCS"), any(0)
	if !toUnicode {
		ordering = String("Identity")
		if info, ok := k.cidSystemInfo(stm.dict["CIDSystemInfo"]).(Dict); ok {
			if r, ok := info["Registry"].(String); ok {
				registry = r
			}
			if o, ok := info["Ordering"].(String); ok {
				ordering = o
			}
			if sup := info["Supplement"]; sup != nil {
				supplement = sup
			}
		}
	}
	rebuilt := rebuildCMap(data, toUnicode, registry, ordering, supplement)
	if rebuilt == nil {
		return nil
	}
	out := compressedStream(rebuilt)
	if !toUnicode {
		out.dict["Type"] = Name("CMap")
		out.dict["CMapName"] = Name("Celatum-Encoding")
		out.dict["CIDSystemInfo"] = Dict{"Registry": registry, "Ordering": ordering, "Supplement": supplement}
		if wm := k.number(stm.dict["WMode"]); wm != nil {
			out.dict["WMode"] = wm
		}
		if n, ok := k.r.resolve(stm.dict["UseCMap"]).(Name); ok && knownCMapName(n) {
			out.dict["UseCMap"] = n
		}
	}
	return out
}

// --- colour ---

// colorSpace keeps a colour space: a device space by its name, any
// other by its array. (A colour space is named only for a device space;
// the names an inline picture abbreviates are read in the content.)
func (k *keeper) colorSpace(v any) any {
	switch t := k.r.resolve(v).(type) {
	case Name:
		return k.oneOf("DeviceGray", "DeviceRGB", "DeviceCMYK", "Pattern")(t)
	case Array:
		return k.value(v, "colorspace", func(any) any { return k.colorSpaceArray(t) })
	}
	return nil
}

func (k *keeper) colorSpaceArray(arr Array) any {
	if len(arr) == 0 {
		return nil
	}
	family, _ := k.r.resolve(arr[0]).(Name)
	switch family {
	case "DeviceGray", "DeviceRGB", "DeviceCMYK":
		return Array{family}
	case "Pattern":
		if len(arr) > 1 {
			if base := k.colorSpace(arr[1]); base != nil {
				return Array{family, base}
			}
		}
		return Array{family}
	case "Indexed":
		if len(arr) < 4 {
			return nil
		}
		base := k.colorSpace(arr[1])
		hival := k.number(arr[2])
		var lookup any
		switch l := k.r.resolve(arr[3]).(type) {
		case String:
			lookup = l
		case *rawStream:
			lookup = k.value(arr[3], "lookup", func(v any) any { return k.stream(v, nil) })
		}
		if base == nil || hival == nil || lookup == nil {
			return nil
		}
		return Array{family, base, hival, lookup}
	case "ICCBased":
		if len(arr) < 2 {
			return nil
		}
		icc := k.value(arr[1], "icc", func(v any) any {
			return k.stream(v, map[Name]rule{
				"N": k.number, "Range": k.numbers, "Alternate": k.colorSpace,
			})
		})
		if icc == nil {
			return nil
		}
		return Array{family, icc}
	case "CalGray", "CalRGB", "Lab":
		if len(arr) < 2 {
			return nil
		}
		params := k.dict(arr[1], map[Name]rule{
			"WhitePoint": k.numbers, "BlackPoint": k.numbers, "Gamma": func(v any) any {
				if n := k.number(v); n != nil {
					return n
				}
				return k.numbers(v)
			},
			"Matrix": k.numbers, "Range": k.numbers,
		})
		return Array{family, params}
	case "Separation":
		if len(arr) < 4 {
			return nil
		}
		name, alt, tint := k.name(arr[1]), k.colorSpace(arr[2]), k.function(arr[3])
		if name == nil || alt == nil || tint == nil {
			return nil
		}
		return Array{family, name, alt, tint}
	case "DeviceN":
		if len(arr) < 4 {
			return nil
		}
		names, alt, tint := k.plainArray(arr[1]), k.colorSpace(arr[2]), k.function(arr[3])
		if names == nil || alt == nil || tint == nil {
			return nil
		}
		out := Array{family, names, alt, tint}
		if len(arr) > 4 {
			attrs := k.dict(arr[4], map[Name]rule{
				"Subtype": k.oneOf("DeviceN", "NChannel"),
				"Colorants": func(v any) any {
					d, ok := k.r.resolve(v).(Dict)
					if !ok {
						return nil
					}
					m := Dict{}
					for n, cs := range d {
						if kept := k.colorSpace(cs); kept != nil {
							m[n] = kept
						}
					}
					return m
				},
				"Process": func(v any) any {
					return k.dictOrNil(v, map[Name]rule{"ColorSpace": k.colorSpace, "Components": k.plainArray})
				},
			})
			if attrs != nil {
				out = append(out, attrs)
			}
		}
		return out
	}
	return nil
}

// function keeps a function, or an array of them: one of the four
// kinds the standard has, with what its kind needs; a calculator's
// program written anew from its numbers and operators.
func (k *keeper) function(v any) any {
	if n, ok := k.r.resolve(v).(Name); ok {
		return k.oneOf("Identity")(n)
	}
	if arr, ok := k.r.resolve(v).(Array); ok {
		out := Array{}
		for _, e := range arr {
			kept := k.function(e)
			if kept == nil {
				return nil
			}
			out = append(out, kept)
		}
		return out
	}
	rules := map[Name]rule{
		"FunctionType": k.number, "Domain": k.numbers, "Range": k.numbers, "Size": k.numbers,
		"BitsPerSample": k.number, "Order": k.number, "Encode": k.numbers, "Decode": k.numbers,
		"C0": k.numbers, "C1": k.numbers, "N": k.number, "Bounds": k.numbers,
		"Functions": k.function,
	}
	return k.value(v, "function", func(v any) any {
		var d Dict
		stm, isStream := v.(*rawStream)
		if isStream {
			d = stm.dict
		} else if dd, ok := v.(Dict); ok {
			d = dd
		} else {
			return nil
		}
		if _, ok := d["Domain"]; !ok {
			return nil
		}
		switch ft, _ := toInt(k.r.resolve(d["FunctionType"])); ft {
		case 0:
			if isStream {
				return k.stream(v, rules)
			}
		case 2, 3:
			if !isStream {
				return k.dictOrNil(v, rules)
			}
		case 4:
			if !isStream {
				return nil
			}
			data, err := k.r.decodeStream(stm.dict, stm.data)
			if err != nil {
				return nil
			}
			prog := rebuildCalculator(data)
			if prog == nil {
				return nil
			}
			out := compressedStream(prog)
			for key, val := range k.dict(stm.dict, rules) {
				out.dict[key] = val
			}
			return out
		}
		return nil
	})
}

func (k *keeper) pattern(v any) any {
	return k.value(v, "pattern", func(v any) any {
		if stm, ok := v.(*rawStream); ok {
			data := k.drawable(stm)
			res := k.ownResources(stm.dict)
			kept := k.content(data, res)
			if kept.err != nil {
				k.fail("a pattern: %v", kept.err)
				return nil
			}
			d := k.dict(stm.dict, map[Name]rule{
				"Type": k.oneOf("Pattern"), "PatternType": k.number, "PaintType": k.number, "TilingType": k.number,
				"BBox": k.numbers, "XStep": k.number, "YStep": k.number, "Matrix": k.numbers,
			})
			if r := k.resources(res, kept.used); len(r) > 0 {
				d["Resources"] = r
			}
			out := compressedStream(kept.data)
			for key, val := range d {
				out.dict[key] = val
			}
			return out
		}
		return k.dictOrNil(v, map[Name]rule{
			"Type": k.oneOf("Pattern"), "PatternType": k.number, "Matrix": k.numbers,
			"Shading": k.shading, "ExtGState": k.extGState,
		})
	})
}

func (k *keeper) shading(v any) any {
	rules := map[Name]rule{
		"ShadingType": k.number, "ColorSpace": k.colorSpace, "Background": k.numbers,
		"BBox": k.numbers, "AntiAlias": k.boolean, "Coords": k.numbers, "Domain": k.numbers,
		"Function": k.function, "Extend": k.booleans, "Matrix": k.numbers,
		"BitsPerCoordinate": k.number, "BitsPerComponent": k.number, "BitsPerFlag": k.number,
		"Decode": k.numbers, "VerticesPerRow": k.number,
	}
	return k.value(v, "shading", func(v any) any {
		if _, ok := v.(*rawStream); ok {
			return k.stream(v, rules)
		}
		return k.dictOrNil(v, rules)
	})
}

func (k *keeper) extGState(v any) any {
	fnOrName := func(v any) any {
		if n, ok := k.r.resolve(v).(Name); ok {
			return k.oneOf("Identity", "Default")(n)
		}
		return k.function(v)
	}
	return k.value(v, "extgstate", func(v any) any {
		return k.dictOrNil(v, map[Name]rule{
			"Type": k.oneOf("ExtGState"), "LW": k.number, "LC": k.number, "LJ": k.number, "ML": k.number,
			"D": k.numbers, "RI": k.oneOf(riNames...), "OP": k.boolean, "op": k.boolean, "OPM": k.number,
			"FL": k.number, "SM": k.number, "SA": k.boolean, "CA": k.number, "ca": k.number,
			"AIS": k.boolean, "TK": k.boolean, "UseBlackPtComp": k.oneOf("ON", "OFF", "Default"), "HT": k.oneOf("Default"),
			"BG": fnOrName, "BG2": fnOrName, "UCR": fnOrName, "UCR2": fnOrName, "TR": fnOrName, "TR2": fnOrName,
			"BM": func(v any) any {
				if n, ok := k.r.resolve(v).(Name); ok {
					return k.oneOf(blendNames...)(n)
				}
				return k.namesOf(k.oneOf(blendNames...))(v)
			},
			"Font": func(v any) any {
				arr, ok := k.r.resolve(v).(Array)
				if !ok || len(arr) != 2 {
					return nil
				}
				f, size := k.font(arr[0]), k.number(arr[1])
				if f == nil || size == nil {
					return nil
				}
				return Array{f, size}
			},
			"SMask": func(v any) any {
				if n, ok := k.r.resolve(v).(Name); ok {
					return k.oneOf("None")(n)
				}
				return k.dictOrNil(v, map[Name]rule{
					"Type": k.oneOf("Mask"), "S": k.oneOf("Alpha", "Luminosity"), "BC": k.numbers, "TR": fnOrName,
					"G": func(v any) any { return k.value(v, "form", k.form) },
				})
			},
		})
	})
}

// properties keeps a marked-content property list: its number in the
// tags, its texts cleaned, its language.
func (k *keeper) properties(v any) any {
	d, ok := k.r.resolve(v).(Dict)
	if !ok {
		return nil
	}
	if typ, _ := d["Type"].(Name); typ == "OCG" || typ == "OCMD" {
		return nil // layer markings are taken off the content
	}
	return k.value(v, "properties", func(v any) any {
		return k.dictOrNil(v, map[Name]rule{
			"MCID": k.number, "ActualText": k.text, "Alt": k.text, "E": k.text, "Lang": k.lang,
		})
	})
}
