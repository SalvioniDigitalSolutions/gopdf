package gopdf

// The structure tree.
//
// A tagged PDF says what its content is, not just where it sits: this
// run of glyphs is a heading, that one is a table cell in the second
// row, this image means "quarterly revenue, rising". The page tells you
// none of that — the words come off it in the order the operators drew
// them, which for a two-column page is both columns interleaved.
//
// Reading the tree is what makes the difference between extracting the
// text of a document and extracting the document. It is also the only
// place the alternate text of an image lives, which is the whole of what
// a screen reader has to work with.

// StructNode is one element of the structure tree.
type StructNode struct {
	// Type is the element's role: "P", "H1", "Table", "Figure". A
	// document may define its own and map them to the standard ones,
	// which Role resolves.
	Type string
	// Role is Type mapped through the document's role map to a standard
	// type, or Type itself where there is no mapping.
	Role string
	// Title is the element's own title, where it has one.
	Title string
	// Alt is the alternate text: what an image means, for a reader that
	// cannot see it.
	Alt string
	// ActualText is what the element really says, where the glyphs do
	// not spell it — a ligature drawn as one glyph, a word broken by a
	// decorative rule.
	ActualText string
	// Lang is the element's language, where it differs from the
	// document's.
	Lang string
	// Page is the page the element's content sits on, or -1 when the
	// element has no content of its own.
	Page int
	// Children are the elements below this one.
	Children []*StructNode
}

// Tagged reports whether the document carries a structure tree.
func (r *Reader) Tagged() bool {
	_, ok := r.resolve(r.Catalog()["StructTreeRoot"]).(Dict)
	return ok
}

// MarkedTagged reports whether the document claims to be a tagged PDF,
// which is a stronger statement than merely carrying a tree: it says the
// tree covers the content in reading order.
func (r *Reader) MarkedTagged() bool {
	mark, ok := r.resolve(r.Catalog()["MarkInfo"]).(Dict)
	if !ok {
		return false
	}
	v, _ := r.resolve(mark["Marked"]).(bool)
	return v
}

// Structure returns the document's structure tree, or nil if it has
// none.
func (r *Reader) Structure() []*StructNode {
	root, ok := r.resolve(r.Catalog()["StructTreeRoot"]).(Dict)
	if !ok {
		return nil
	}
	roles := map[string]string{}
	if m, ok := r.resolve(root["RoleMap"]).(Dict); ok {
		for k, v := range m {
			if to, ok := r.resolve(v).(Name); ok {
				roles[string(k)] = string(to)
			}
		}
	}
	s := &structReader{r: r, roles: roles, seen: map[int]bool{}}
	return s.children(root["K"], 0)
}

// structReader walks the tree, remembering where it has been.
type structReader struct {
	r     *Reader
	roles map[string]string
	seen  map[int]bool
}

func (s *structReader) children(v any, depth int) []*StructNode {
	if depth > 64 || v == nil {
		return nil
	}
	switch t := s.r.resolve(v).(type) {
	case Array:
		var out []*StructNode
		for _, e := range t {
			out = append(out, s.children(e, depth+1)...)
		}
		return out
	case Dict:
		// A marked-content reference or an object reference points at
		// content rather than describing it, and has no element of its
		// own.
		if ty := s.r.resolve(t["Type"]); ty == Name("MCR") || ty == Name("OBJR") {
			return nil
		}
		if t["S"] == nil {
			return nil
		}
		if ref, ok := v.(Ref); ok {
			if s.seen[ref.Num] {
				return nil
			}
			s.seen[ref.Num] = true
		}
		return []*StructNode{s.node(t, depth)}
	}
	return nil
}

func (s *structReader) node(d Dict, depth int) *StructNode {
	n := &StructNode{Page: -1}
	if ty, ok := s.r.resolve(d["S"]).(Name); ok {
		n.Type = string(ty)
		n.Role = n.Type
		if mapped, ok := s.roles[n.Type]; ok {
			n.Role = mapped
		}
	}
	text := func(key Name) string {
		if v, ok := s.r.resolve(d[key]).(String); ok {
			return decodeTextString(v)
		}
		return ""
	}
	n.Title, n.Alt = text("T"), text("Alt")
	n.ActualText = text("ActualText")
	if l, ok := s.r.resolve(d["Lang"]).(String); ok {
		n.Lang = decodeTextString(l)
	}
	if pg, ok := d["Pg"].(Ref); ok {
		n.Page = s.r.pageIndexOf(pg)
	}
	n.Children = s.children(d["K"], depth+1)
	// An element with no page of its own sits on the page its content
	// does, which is what a reader means by "where is this heading".
	if n.Page < 0 {
		for _, c := range n.Children {
			if c.Page >= 0 {
				n.Page = c.Page
				break
			}
		}
	}
	return n
}

// pageIndexOf turns a page object reference into a page number.
func (r *Reader) pageIndexOf(ref Ref) int {
	for i, num := range r.pageRefs {
		if num == ref.Num {
			return i
		}
	}
	return -1
}

// StructText walks the tree and returns what the document says, in the
// order the structure puts it rather than the order the operators drew
// it.
//
// Where an element declares its actual text, that is used in place of
// whatever the glyphs spell; where a figure has alternate text, that
// stands in for the picture. An untagged document has no structure to
// walk and gives back nothing, which is the honest answer — PageText is
// what to use there.
func (r *Reader) StructText() string {
	tree := r.Structure()
	if len(tree) == 0 {
		return ""
	}
	var b []string
	var walk func(nodes []*StructNode, depth int)
	walk = func(nodes []*StructNode, depth int) {
		if depth > 64 {
			return
		}
		for _, n := range nodes {
			switch {
			case n.ActualText != "":
				b = append(b, n.ActualText)
				continue // the element says what it says; the glyphs below do not
			case n.Alt != "":
				b = append(b, n.Alt)
			}
			walk(n.Children, depth+1)
		}
	}
	walk(tree, 0)
	return joinLines(b)
}

func joinLines(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "\n"
		}
		out += p
	}
	return out
}

// StructOutline returns the headings a tagged document declares, in
// order, with their level: H1 is 1, H2 is 2. It is the table of contents
// a document has whether or not it also has bookmarks.
func (r *Reader) StructOutline() []StructHeading {
	var out []StructHeading
	var walk func(nodes []*StructNode, depth int)
	walk = func(nodes []*StructNode, depth int) {
		if depth > 64 {
			return
		}
		for _, n := range nodes {
			if level := headingLevel(n.Role); level > 0 {
				out = append(out, StructHeading{
					Level: level, Text: headingText(n), Page: n.Page,
				})
			}
			walk(n.Children, depth+1)
		}
	}
	walk(r.Structure(), 0)
	return out
}

// StructHeading is one heading of a tagged document.
type StructHeading struct {
	Level int
	Text  string
	Page  int
}

// headingLevel reads the number off H1..H6, and treats a bare H as the
// top level, which is what a document using the untitled form means.
func headingLevel(role string) int {
	if role == "H" {
		return 1
	}
	if len(role) == 2 && role[0] == 'H' && role[1] >= '1' && role[1] <= '6' {
		return int(role[1] - '0')
	}
	return 0
}

// headingText is the best text a heading offers about itself.
func headingText(n *StructNode) string {
	switch {
	case n.ActualText != "":
		return n.ActualText
	case n.Title != "":
		return n.Title
	case n.Alt != "":
		return n.Alt
	}
	for _, c := range n.Children {
		if t := headingText(c); t != "" {
			return t
		}
	}
	return ""
}
