package gopdf

import (
	"fmt"
	"strings"
)

// resourceSet holds the object numbers assigned to a document's drawing
// resources — fonts, images, graphics states, gradients and form
// XObjects — and knows how to write both the objects themselves and the
// dictionary entries that name them.
//
// It is shared by the two writing paths: Document.WriteTo, which builds a
// file from scratch, and Updater, which appends drawing resources to an
// existing one.
type resourceSet struct {
	d               *Document
	fontNums        []fontRefs
	imageNums       []int
	smaskNums       []int
	gsNums          []int
	shadingNums     []int
	shadingFuncNums []int
	xobjNums        []int
}

// allocResources assigns an object number to every resource, using the
// caller's allocator so the numbers fit whatever file is being written.
func (d *Document) allocResources(alloc func() int) *resourceSet {
	rs := &resourceSet{
		d:               d,
		fontNums:        make([]fontRefs, len(d.fonts)),
		imageNums:       make([]int, len(d.images)),
		smaskNums:       make([]int, len(d.images)),
		gsNums:          make([]int, len(d.alphas)),
		shadingNums:     make([]int, len(d.shadings)),
		shadingFuncNums: make([]int, len(d.shadings)),
		xobjNums:        make([]int, len(d.xobjects)),
	}
	for i, f := range d.fonts {
		rs.fontNums[i] = fontRefs{font: alloc()}
		if f.ttf != nil {
			rs.fontNums[i].cid = alloc()
			rs.fontNums[i].descriptor = alloc()
			rs.fontNums[i].fontFile = alloc()
			rs.fontNums[i].toUnicode = alloc()
		}
	}
	for i, img := range d.images {
		rs.imageNums[i] = alloc()
		if img.smask != nil {
			rs.smaskNums[i] = alloc()
		}
	}
	for i := range d.alphas {
		rs.gsNums[i] = alloc()
	}
	for i := range d.shadings {
		rs.shadingNums[i] = alloc()
		rs.shadingFuncNums[i] = alloc()
	}
	for i := range d.xobjects {
		rs.xobjNums[i] = alloc()
	}
	return rs
}

// fontObjNum returns the object number of a font, for appearance streams
// and other places that reference one directly.
func (rs *resourceSet) fontObjNum(idx int) int {
	if idx < len(rs.fontNums) {
		return rs.fontNums[idx].font
	}
	return 0
}

// write emits every resource object.
func (rs *resourceSet) write(ow *offsetWriter, ctx *writeCtx,
	beginObj func(int), endObj func()) error {

	d := rs.d
	for i, f := range d.fonts {
		if f.ttf == nil {
			beginObj(rs.fontNums[i].font)
			ow.printf("<< /Type /Font /Subtype /Type1 /BaseFont /%s", f.name)
			if f.winAnsi {
				ow.str(" /Encoding /WinAnsiEncoding")
			}
			ow.str(" >>\n")
			endObj()
			continue
		}
		if err := d.writeEmbeddedFont(ow, f, rs.fontNums[i], d.fontUsage[i], beginObj, endObj); err != nil {
			return err
		}
	}

	for i, img := range d.images {
		beginObj(rs.imageNums[i])
		extra := fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /%s /BitsPerComponent 8 ",
			img.width, img.height, img.colorSpace)
		if img.smask != nil {
			extra += fmt.Sprintf("/SMask %d 0 R ", rs.smaskNums[i])
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
			return err
		}
		endObj()

		if img.smask != nil {
			beginObj(rs.smaskNums[i])
			extra := fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceGray /BitsPerComponent 8 ",
				img.width, img.height)
			if err := ow.writeStream(extra, img.smask, d.Compress); err != nil {
				return err
			}
			endObj()
		}
	}

	for i, a := range d.alphas {
		beginObj(rs.gsNums[i])
		ow.printf("<< /Type /ExtGState /ca %s /CA %s >>\n", fl(a.fill), fl(a.stroke))
		endObj()
	}

	for i, s := range d.shadings {
		beginObj(rs.shadingNums[i])
		d.writeShading(ow, s, rs.shadingFuncNums[i])
		endObj()
		beginObj(rs.shadingFuncNums[i])
		ow.str(shadingFunction(s) + "\n")
		endObj()
	}

	for i, xo := range d.xobjects {
		beginObj(rs.xobjNums[i])
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
			return err
		}
		endObj()
	}
	return nil
}

// entries renders the resource dictionary entries for one category, using
// the given name prefix.
func (rs *resourceSet) entries(prefix, category string) string {
	var b strings.Builder
	switch category {
	case "Font":
		for i, fr := range rs.fontNums {
			fmt.Fprintf(&b, " /%sF%d %d 0 R", prefix, i+1, fr.font)
		}
	case "XObject":
		for i, num := range rs.imageNums {
			fmt.Fprintf(&b, " /%sI%d %d 0 R", prefix, i+1, num)
		}
		for i, num := range rs.xobjNums {
			fmt.Fprintf(&b, " /%sX%d %d 0 R", prefix, i+1, num)
		}
	case "ExtGState":
		for i, num := range rs.gsNums {
			fmt.Fprintf(&b, " /%sGS%d %d 0 R", prefix, i+1, num)
		}
	case "Shading":
		for i, num := range rs.shadingNums {
			fmt.Fprintf(&b, " /%sSh%d %d 0 R", prefix, i+1, num)
		}
	}
	return b.String()
}

// resourceCategories lists the categories entries knows how to render.
var resourceCategories = []string{"Font", "XObject", "ExtGState", "Shading"}

// dictionary renders a complete resource dictionary for content drawn by
// this library, with no source resources to merge in.
func (rs *resourceSet) dictionary(prefix string) string {
	var b strings.Builder
	b.WriteString("/ProcSet [/PDF /Text /ImageB /ImageC]")
	for _, cat := range resourceCategories {
		if e := rs.entries(prefix, cat); e != "" {
			fmt.Fprintf(&b, " /%s <<%s >>", cat, e)
		}
	}
	return b.String()
}

// entryDict renders one category's entries as dictionary values, for
// merging into a resource dictionary that is written as a parsed value
// rather than as text.
func (rs *resourceSet) entryDict(prefix, category string) Dict {
	out := Dict{}
	add := func(name string, num int) { out[Name(prefix+name)] = Ref{Num: num} }
	switch category {
	case "Font":
		for i, fr := range rs.fontNums {
			add(fmt.Sprintf("F%d", i+1), fr.font)
		}
	case "XObject":
		for i, num := range rs.imageNums {
			add(fmt.Sprintf("I%d", i+1), num)
		}
		for i, num := range rs.xobjNums {
			add(fmt.Sprintf("X%d", i+1), num)
		}
	case "ExtGState":
		for i, num := range rs.gsNums {
			add(fmt.Sprintf("GS%d", i+1), num)
		}
	case "Shading":
		for i, num := range rs.shadingNums {
			add(fmt.Sprintf("Sh%d", i+1), num)
		}
	}
	return out
}
