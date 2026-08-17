package gopdf

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestXMPRoundTrip(t *testing.T) {
	doc := New()
	doc.Compress = false
	doc.CreationDate = time.Date(2026, 8, 17, 9, 30, 0, 0, time.UTC)
	doc.SetInfo(Info{
		Title:    "A Report & Its <Findings>",
		Author:   "Ada Lovelace",
		Subject:  "The analytical engine",
		Keywords: "engine, analysis",
		Creator:  "gopdf tests",
	})
	doc.SetXMP(true)
	doc.AddPage()
	src := docBytes(t, doc)

	r := NewReaderOrFail(t, src)
	x := r.XMP()
	if len(x.Raw) == 0 {
		t.Fatal("no packet was written")
	}
	if x.Title != "A Report & Its <Findings>" {
		t.Errorf("title = %q", x.Title)
	}
	if x.Author != "Ada Lovelace" {
		t.Errorf("author = %q", x.Author)
	}
	if x.Subject != "The analytical engine" {
		t.Errorf("subject = %q", x.Subject)
	}
	if x.Keywords != "engine, analysis" {
		t.Errorf("keywords = %q", x.Keywords)
	}
	if x.Creator != "gopdf tests" {
		t.Errorf("creator = %q", x.Creator)
	}
	if x.Producer != "gopdf" {
		t.Errorf("producer = %q", x.Producer)
	}
	if !x.Created.Equal(doc.CreationDate) {
		t.Errorf("created = %v, want %v", x.Created, doc.CreationDate)
	}

	// The packet must agree with the information dictionary, since the
	// two are read by different tools.
	info := r.Info()
	if info.Title != x.Title || info.Author != x.Author {
		t.Errorf("the packet and the dictionary disagree: %+v against %+v", info, x)
	}
	// And the special characters must have survived as characters, not
	// as markup.
	if bytes.Contains(x.Raw, []byte("Its <Findings>")) {
		t.Error("a title containing markup was written unescaped")
	}
	if !bytes.Contains(x.Raw, []byte("&lt;Findings&gt;")) {
		t.Error("the title was not escaped as XML")
	}
	verifyXref(t, src)
}

func TestXMPAbsentIsNotAnError(t *testing.T) {
	doc := New()
	doc.AddPage()
	r := NewReaderOrFail(t, docBytes(t, doc))
	x := r.XMP()
	if len(x.Raw) != 0 || x.Title != "" {
		t.Errorf("a document with no packet reported %+v", x)
	}
}

func TestXMPOnAnExistingDocument(t *testing.T) {
	doc := New()
	doc.Compress = false
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(72, 100, "body")
	r := NewReaderOrFail(t, docBytes(t, doc))

	u := Update(r)
	if err := u.SetXMP(Info{Title: "Added later", Author: "Grace Hopper"}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	out := NewReaderOrFail(t, buf.Bytes())
	x := out.XMP()
	if x.Title != "Added later" || x.Author != "Grace Hopper" {
		t.Errorf("packet = %+v", x)
	}
	// The information dictionary is updated to match, so the document
	// does not end up saying two things.
	if info := out.Info(); info.Title != "Added later" {
		t.Errorf("the dictionary still says %q", info.Title)
	}
	if txt, err := out.PageText(0); err != nil || !strings.Contains(txt, "body") {
		t.Errorf("page text: %q %v", txt, err)
	}
}

// TestXMPReadsAnotherProducersPacket: the fields turn up as elements in
// one packet and as attributes in the next, and both are ordinary.
func TestXMPReadsOtherShapes(t *testing.T) {
	packets := []struct {
		name, body, wantTitle, wantProducer string
	}{
		{
			name: "elements",
			body: `<rdf:Description rdf:about="">` +
				`<dc:title><rdf:Alt><rdf:li xml:lang="x-default">Elem Title</rdf:li>` +
				`</rdf:Alt></dc:title>` +
				`<pdf:Producer>SomeTool 3</pdf:Producer></rdf:Description>`,
			wantTitle: "Elem Title", wantProducer: "SomeTool 3",
		},
		{
			name: "attributes",
			body: `<rdf:Description rdf:about="" pdf:Producer="AttrTool 9">` +
				`<dc:title><rdf:Alt><rdf:li>Attr Title</rdf:li></rdf:Alt></dc:title>` +
				`</rdf:Description>`,
			wantTitle: "Attr Title", wantProducer: "AttrTool 9",
		},
		{
			name:      "a bare sequence",
			body:      `<dc:creator><rdf:Seq><rdf:li>Someone</rdf:li></rdf:Seq></dc:creator>`,
			wantTitle: "", wantProducer: "",
		},
	}
	for _, c := range packets {
		doc := New()
		doc.Compress = false
		doc.AddPage()
		r := NewReaderOrFail(t, docBytes(t, doc))
		u := Update(r)
		stm := u.AddObject(NewStream(Dict{"Type": Name("Metadata"), "Subtype": Name("XML")},
			[]byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta>`+
				c.body+`</x:xmpmeta><?xpacket end="w"?>`)))
		if err := u.SetCatalogEntry("Metadata", stm); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if _, err := u.WriteTo(&buf); err != nil {
			t.Fatal(err)
		}
		x := NewReaderOrFail(t, buf.Bytes()).XMP()
		if x.Title != c.wantTitle {
			t.Errorf("%s: title = %q, want %q", c.name, x.Title, c.wantTitle)
		}
		if x.Producer != c.wantProducer {
			t.Errorf("%s: producer = %q, want %q", c.name, x.Producer, c.wantProducer)
		}
		if len(x.Raw) == 0 {
			t.Errorf("%s: the raw packet was not handed back", c.name)
		}
	}
	// The creator sequence reads as its first entry.
	doc := New()
	doc.Compress = false
	doc.AddPage()
	r := NewReaderOrFail(t, docBytes(t, doc))
	u := Update(r)
	stm := u.AddObject(NewStream(Dict{},
		[]byte(`<dc:creator><rdf:Seq><rdf:li>Someone</rdf:li></rdf:Seq></dc:creator>`)))
	u.SetCatalogEntry("Metadata", stm)
	var buf bytes.Buffer
	u.WriteTo(&buf)
	if got := NewReaderOrFail(t, buf.Bytes()).XMP().Author; got != "Someone" {
		t.Errorf("author from a sequence = %q", got)
	}
}

func TestXMPDateParsing(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"2026-08-17T09:30:00Z", "2026-08-17 09:30:00 +0000 UTC"},
		{"2026-08-17", "2026-08-17 00:00:00 +0000 UTC"},
		{"2026", "2026-01-01 00:00:00 +0000 UTC"},
		{"not a date", "0001-01-01 00:00:00 +0000 UTC"},
		{"", "0001-01-01 00:00:00 +0000 UTC"},
	} {
		if got := parseXMPDate(c.in).String(); got != c.want {
			t.Errorf("parseXMPDate(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// A zone offset is an instant, not a name: Go labels it with the
	// local zone when the two agree, so the comparison is on the moment.
	got := parseXMPDate("2026-08-17T09:30:00+02:00")
	want := time.Date(2026, 8, 17, 7, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("an offset timestamp parsed to %v, want the instant %v", got, want)
	}
}

// TestXMPSurvivesRedaction: the packet is metadata and redaction throws
// it away, which is the documented behaviour and worth holding to.
func TestXMPIsStrippedByRedaction(t *testing.T) {
	doc := New()
	doc.Compress = false
	doc.SetInfo(Info{Title: "Confidential draft", Author: "Ada Lovelace"})
	doc.SetXMP(true)
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(72, 100, "Ada Lovelace wrote this.")
	src := docBytes(t, doc)

	if x := NewReaderOrFail(t, src).XMP(); x.Author != "Ada Lovelace" {
		t.Fatalf("the fixture has no packet: %+v", x)
	}
	rd := Redact(NewReaderOrFail(t, src))
	rd.Text("Ada Lovelace")
	var buf bytes.Buffer
	if _, err := rd.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	if x := NewReaderOrFail(t, buf.Bytes()).XMP(); len(x.Raw) != 0 {
		t.Errorf("the packet survived redaction: %q", x.Raw)
	}
	if bytes.Contains(buf.Bytes(), []byte("Lovelace")) {
		t.Error("the name survived in the metadata")
	}
}
