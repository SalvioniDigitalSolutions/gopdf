package gopdf

import (
	"fmt"
	"strings"
	"time"
)

// XMP metadata.
//
// A document says who wrote it twice: once in the information
// dictionary, which is a handful of strings, and once in an XMP packet,
// which is RDF/XML and is what an archive or an asset manager reads.
// The two are supposed to agree and routinely do not, because a tool
// updates one and leaves the other.
//
// Both directions are here. Reading pulls the fields that have an
// equivalent in the information dictionary out of the packet, and hands
// back the raw packet for anything else. Writing generates a packet that
// agrees with the information dictionary, because writing one that
// contradicts it would be worse than writing none.

// XMP is a document's metadata packet.
type XMP struct {
	// Title, Author, Subject, Keywords, Creator and Producer are the
	// fields the information dictionary also carries.
	Title, Author, Subject, Keywords string
	Creator, Producer                string
	// Created and Modified are the packet's own timestamps.
	Created, Modified time.Time
	// Raw is the packet as it stands, for anything this does not model.
	// It is empty for a document that has no packet.
	Raw []byte
}

// XMP returns the document's metadata packet.
//
// A document with no packet gives back a zero XMP, which is not an
// error: most documents have none.
func (r *Reader) XMP() XMP {
	var out XMP
	stm, ok := r.resolve(r.Catalog()["Metadata"]).(*rawStream)
	if !ok {
		return out
	}
	data, err := r.decodeStream(stm.dict, stm.data)
	if err != nil {
		return out
	}
	out.Raw = data
	text := string(data)
	out.Title = xmpFirst(text, "dc:title")
	out.Author = xmpFirst(text, "dc:creator")
	out.Subject = xmpFirst(text, "dc:description")
	out.Creator = xmpValue(text, "xmp:CreatorTool")
	out.Producer = xmpValue(text, "pdf:Producer")
	out.Keywords = xmpValue(text, "pdf:Keywords")
	out.Created = parseXMPDate(xmpValue(text, "xmp:CreateDate"))
	out.Modified = parseXMPDate(xmpValue(text, "xmp:ModifyDate"))
	return out
}

// xmpValue reads a simple property, written either as an element or as
// an attribute of the description — RDF allows both and producers use
// both, sometimes in the same packet.
func xmpValue(text, name string) string {
	if v, ok := betweenTags(text, "<"+name+">", "</"+name+">"); ok {
		return unescapeXML(strings.TrimSpace(v))
	}
	// The attribute form: name="value", quoted either way.
	for _, q := range []string{`"`, `'`} {
		open := name + "=" + q
		if i := strings.Index(text, open); i >= 0 {
			rest := text[i+len(open):]
			if j := strings.Index(rest, q); j >= 0 {
				return unescapeXML(rest[:j])
			}
		}
	}
	return ""
}

// xmpFirst reads the first entry of a language alternative or a
// sequence, which is how dc:title and dc:creator are written.
func xmpFirst(text, name string) string {
	body, ok := betweenTags(text, "<"+name+">", "</"+name+">")
	if !ok {
		return xmpValue(text, name)
	}
	if v, ok := betweenTags(body, "<rdf:li", "</rdf:li>"); ok {
		// The opening tag may carry an xml:lang attribute.
		if i := strings.IndexByte(v, '>'); i >= 0 {
			return unescapeXML(strings.TrimSpace(v[i+1:]))
		}
	}
	return unescapeXML(strings.TrimSpace(body))
}

func betweenTags(text, open, close string) (string, bool) {
	i := strings.Index(text, open)
	if i < 0 {
		return "", false
	}
	rest := text[i+len(open):]
	j := strings.Index(rest, close)
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}

func unescapeXML(s string) string {
	r := strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`,
		"&apos;", "'", "&amp;", "&")
	return r.Replace(s)
}

func escapeXML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;",
		`"`, "&quot;")
	return r.Replace(s)
}

// parseXMPDate reads an ISO 8601 timestamp, which is what XMP uses where
// the information dictionary uses its own format.
func parseXMPDate(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04",
		"2006-01-02", "2006-01", "2006",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// --- writing ---

// SetXMP writes a metadata packet describing the document.
//
// Only the fields the information dictionary also carries are written,
// and they are taken from it: a packet that contradicts the dictionary
// is worse than none, since the two are read by different tools and
// disagreement is how a document ends up with two authors.
func (d *Document) SetXMP(on bool) { d.wantXMP = on }

// buildXMP returns the metadata stream, or nil if none was asked for.
func (d *Document) buildXMP() any {
	if !d.wantXMP {
		return nil
	}
	info := d.info
	if info.Producer == "" {
		info.Producer = "gopdf"
	}
	packet := xmpPacket(info, d.CreationDate)
	ref := rawRef(len(d.raw))
	d.raw = append(d.raw, &rawStream{
		dict: Dict{"Type": Name("Metadata"), "Subtype": Name("XML")},
		data: packet,
	})
	return ref
}

// SetXMP writes a metadata packet into an existing document, describing
// it as its information dictionary does.
func (u *Updater) SetXMP(info Info) error {
	if info.Producer == "" {
		info.Producer = "gopdf"
	}
	u.SetInfo(info)
	stream := u.AddObject(NewStream(
		Dict{"Type": Name("Metadata"), "Subtype": Name("XML")},
		xmpPacket(info, time.Now()),
	))
	return u.SetCatalogEntry("Metadata", stream)
}

// xmpPacket renders the RDF for a document's metadata.
//
// It is written out by hand rather than through an XML library because
// the shape is fixed and the escaping is the only part that varies —
// and because a packet is padded with whitespace so a tool can rewrite
// it in place without moving the rest of the file.
func xmpPacket(info Info, created time.Time) []byte {
	var b strings.Builder
	// The packet opens with a byte order mark, written as its bytes so
	// the source file itself stays plain ASCII.
	b.WriteString("<?xpacket begin=\"\xef\xbb\xbf\" id=\"W5M0MpCehiHzreSzNTczkc9d\"?>\n")
	b.WriteString(`<x:xmpmeta xmlns:x="adobe:ns:meta/" x:xmptk="gopdf">` + "\n")
	b.WriteString(` <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` + "\n")
	b.WriteString(`  <rdf:Description rdf:about=""` + "\n")
	b.WriteString(`    xmlns:dc="http://purl.org/dc/elements/1.1/"` + "\n")
	b.WriteString(`    xmlns:xmp="http://ns.adobe.com/xap/1.0/"` + "\n")
	b.WriteString(`    xmlns:pdf="http://ns.adobe.com/pdf/1.3/">` + "\n")

	alt := func(tag, value string) {
		if value == "" {
			return
		}
		fmt.Fprintf(&b, "   <%s><rdf:Alt><rdf:li xml:lang=\"x-default\">%s"+
			"</rdf:li></rdf:Alt></%s>\n", tag, escapeXML(value), tag)
	}
	seq := func(tag, value string) {
		if value == "" {
			return
		}
		fmt.Fprintf(&b, "   <%s><rdf:Seq><rdf:li>%s</rdf:li></rdf:Seq></%s>\n",
			tag, escapeXML(value), tag)
	}
	simple := func(tag, value string) {
		if value == "" {
			return
		}
		fmt.Fprintf(&b, "   <%s>%s</%s>\n", tag, escapeXML(value), tag)
	}
	alt("dc:title", info.Title)
	seq("dc:creator", info.Author)
	alt("dc:description", info.Subject)
	simple("pdf:Keywords", info.Keywords)
	simple("xmp:CreatorTool", info.Creator)
	simple("pdf:Producer", info.Producer)
	if !created.IsZero() {
		stamp := created.UTC().Format(time.RFC3339)
		simple("xmp:CreateDate", stamp)
		simple("xmp:ModifyDate", stamp)
	}
	b.WriteString("  </rdf:Description>\n </rdf:RDF>\n</x:xmpmeta>\n")
	// The padding is what lets a later tool rewrite the packet without
	// moving anything after it, and the specification asks for it.
	for i := 0; i < 20; i++ {
		b.WriteString(strings.Repeat(" ", 79) + "\n")
	}
	b.WriteString(`<?xpacket end="w"?>`)
	return []byte(b.String())
}
