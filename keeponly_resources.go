package gopdf

// keeponly_resources.go — what a page draws with, kept by KeepOnly.
//
// A page's resources are kept only as far as its content uses them
// (keeponly_content.go says which), and each one only with the keys
// that make it draw: a picture's samples and colour, never its
// alternate copies or metadata; a font's program, widths and character
// map, never the metadata on its descriptor; a form's content, never
// its private data.

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
	if ps := k.plainArray(res["ProcSet"]); ps != nil {
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

func (k *keeper) filterNames(v any) any {
	switch t := k.r.resolve(v).(type) {
	case Name:
		return t
	case Array:
		return k.plainArray(t)
	}
	return nil
}

func (k *keeper) decodeParms(v any) any {
	one := func(e any) any {
		if k.r.resolve(e) == nil {
			return nil
		}
		return k.dict(e, map[Name]rule{
			"Predictor": k.number, "Colors": k.number, "BitsPerComponent": k.number, "Columns": k.number,
			"EarlyChange": k.number, "K": k.number, "EndOfLine": k.plain, "EncodedByteAlign": k.plain,
			"Rows": k.number, "EndOfBlock": k.plain, "BlackIs1": k.plain, "DamagedRowsBeforeError": k.number,
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
		"ImageMask": k.plain, "Intent": k.name, "Interpolate": k.plain, "SMaskInData": k.number,
		"Decode": k.plainArray, "StructParent": k.number,
		"ColorSpace": k.colorSpace,
		"Mask": func(v any) any {
			if arr, ok := k.r.resolve(v).(Array); ok {
				return k.plainArray(arr)
			}
			return k.value(v, "image", k.image)
		},
		"SMask": func(v any) any { return k.value(v, "image", k.image) },
		"Matte": k.plainArray,
	})
	if stm, ok := kept.(*rawStream); ok {
		// What a picture file carries besides its samples — a camera's
		// notes, a comment, a preview — is not drawn and is left out.
		stm.data = pictureWithoutNotes(stm.dict, stm.data)
	}
	return kept
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
		"FormType": k.number, "BBox": k.plainArray, "Matrix": k.plainArray,
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
			"Type": k.name, "Subtype": k.name, "BaseFont": k.name, "Name": k.name,
			"FirstChar": k.number, "LastChar": k.number, "Widths": k.plainArray,
			"DW": k.number, "W": k.plainArray, "DW2": k.plainArray, "W2": k.plainArray,
			"FontBBox": k.plainArray, "FontMatrix": k.plainArray,
			"FontDescriptor": func(v any) any { return k.value(v, "fontdescriptor", k.fontDescriptor) },
			"ToUnicode":      func(v any) any { return k.value(v, "cmap", k.cmap) },
			"Encoding": func(v any) any {
				switch t := k.r.resolve(v).(type) {
				case Name:
					return t
				case Dict:
					return k.value(v, "encoding", func(v any) any {
						return k.dict(v, map[Name]rule{"Type": k.name, "BaseEncoding": k.name, "Differences": k.plainArray})
					})
				case *rawStream:
					return k.value(v, "cmap", k.cmap)
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

// ownResources is what d draws with: its own resources, or else those
// of what draws it.
func (k *keeper) ownResources(d Dict) Dict {
	if res, ok := k.r.resolve(d["Resources"]).(Dict); ok {
		return res
	}
	return k.inherit
}

func (k *keeper) cidSystemInfo(v any) any {
	return k.dict(v, map[Name]rule{"Registry": k.technical, "Ordering": k.technical, "Supplement": k.number})
}

func (k *keeper) fontDescriptor(v any) any {
	fontFile := func(v any) any {
		return k.value(v, "fontfile", func(v any) any {
			return k.stream(v, map[Name]rule{"Length1": k.number, "Length2": k.number, "Length3": k.number, "Subtype": k.name})
		})
	}
	return k.dict(v, map[Name]rule{
		"Type": k.name, "FontName": k.name, "FontFamily": k.text, "FontStretch": k.name,
		"FontWeight": k.number, "Flags": k.number, "FontBBox": k.plainArray, "ItalicAngle": k.number,
		"Ascent": k.number, "Descent": k.number, "Leading": k.number, "CapHeight": k.number,
		"XHeight": k.number, "StemV": k.number, "StemH": k.number, "AvgWidth": k.number,
		"MaxWidth": k.number, "MissingWidth": k.number, "CharSet": k.technical, "Lang": k.name,
		"FontFile": fontFile, "FontFile2": fontFile, "FontFile3": fontFile,
		"CIDSet": func(v any) any { return k.value(v, "cidset", func(v any) any { return k.stream(v, nil) }) },
		"Style":  func(v any) any { return k.dict(v, map[Name]rule{"Panose": k.technical}) },
	})
}

// cmap keeps a character map (a ToUnicode map, an encoding CMap).
func (k *keeper) cmap(v any) any {
	return k.stream(v, map[Name]rule{
		"Type": k.name, "CMapName": k.name, "WMode": k.number,
		"CIDSystemInfo": k.cidSystemInfo,
		"UseCMap": func(v any) any {
			if n, ok := k.r.resolve(v).(Name); ok {
				return n
			}
			return k.value(v, "cmap", k.cmap)
		},
	})
}

// --- colour ---

func (k *keeper) colorSpace(v any) any {
	switch t := k.r.resolve(v).(type) {
	case Name:
		return t
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
				"N": k.number, "Range": k.plainArray, "Alternate": k.colorSpace,
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
			"WhitePoint": k.plainArray, "BlackPoint": k.plainArray, "Gamma": func(v any) any {
				if n := k.number(v); n != nil {
					return n
				}
				return k.plainArray(v)
			},
			"Matrix": k.plainArray, "Range": k.plainArray,
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
				"Subtype": k.name,
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
					return k.dict(v, map[Name]rule{"ColorSpace": k.colorSpace, "Components": k.plainArray})
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

// function keeps a function, or an array of them.
func (k *keeper) function(v any) any {
	if n, ok := k.r.resolve(v).(Name); ok {
		return n // Identity
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
		"FunctionType": k.number, "Domain": k.plainArray, "Range": k.plainArray, "Size": k.plainArray,
		"BitsPerSample": k.number, "Order": k.number, "Encode": k.plainArray, "Decode": k.plainArray,
		"C0": k.plainArray, "C1": k.plainArray, "N": k.number, "Bounds": k.plainArray,
		"Functions": k.function,
	}
	return k.value(v, "function", func(v any) any {
		if _, ok := v.(*rawStream); ok {
			return k.stream(v, rules)
		}
		return k.dict(v, rules)
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
				"Type": k.name, "PatternType": k.number, "PaintType": k.number, "TilingType": k.number,
				"BBox": k.plainArray, "XStep": k.number, "YStep": k.number, "Matrix": k.plainArray,
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
		return k.dict(v, map[Name]rule{
			"Type": k.name, "PatternType": k.number, "Matrix": k.plainArray,
			"Shading": k.shading, "ExtGState": k.extGState,
		})
	})
}

func (k *keeper) shading(v any) any {
	rules := map[Name]rule{
		"ShadingType": k.number, "ColorSpace": k.colorSpace, "Background": k.plainArray,
		"BBox": k.plainArray, "AntiAlias": k.plain, "Coords": k.plainArray, "Domain": k.plainArray,
		"Function": k.function, "Extend": k.plainArray, "Matrix": k.plainArray,
		"BitsPerCoordinate": k.number, "BitsPerComponent": k.number, "BitsPerFlag": k.number,
		"Decode": k.plainArray, "VerticesPerRow": k.number,
	}
	return k.value(v, "shading", func(v any) any {
		if _, ok := v.(*rawStream); ok {
			return k.stream(v, rules)
		}
		return k.dict(v, rules)
	})
}

func (k *keeper) extGState(v any) any {
	fnOrName := func(v any) any {
		if n, ok := k.r.resolve(v).(Name); ok {
			return n
		}
		return k.function(v)
	}
	return k.value(v, "extgstate", func(v any) any {
		return k.dict(v, map[Name]rule{
			"Type": k.name, "LW": k.number, "LC": k.number, "LJ": k.number, "ML": k.number,
			"D": k.plainArray, "RI": k.name, "OP": k.plain, "op": k.plain, "OPM": k.number,
			"FL": k.number, "SM": k.number, "SA": k.plain, "CA": k.number, "ca": k.number,
			"AIS": k.plain, "TK": k.plain, "UseBlackPtComp": k.name, "HT": k.name,
			"BG": fnOrName, "BG2": fnOrName, "UCR": fnOrName, "UCR2": fnOrName, "TR": fnOrName, "TR2": fnOrName,
			"BM": func(v any) any {
				if n, ok := k.r.resolve(v).(Name); ok {
					return n
				}
				return k.plainArray(v)
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
					return n
				}
				return k.dict(v, map[Name]rule{
					"Type": k.name, "S": k.name, "BC": k.plainArray, "TR": fnOrName,
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
		return k.dict(v, map[Name]rule{
			"MCID": k.number, "ActualText": k.text, "Alt": k.text, "E": k.text, "Lang": k.lang,
		})
	})
}
