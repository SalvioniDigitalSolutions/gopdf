package gopdf

import (
	"bytes"
	"strings"
	"testing"
)

// attachedDoc builds a document carrying files.
func attachedDoc(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	doc := New()
	doc.Compress = false
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(72, 100, "A document with something inside it.")
	r := NewReaderOrFail(t, docBytes(t, doc))

	u := Update(r)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	// A map iterates in no order; attaching in a fixed one keeps the test
	// telling the same story every run.
	for _, name := range sortedStrings(names) {
		if err := u.Attach(name, files[name]); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sortedStrings(s []string) []string {
	out := append([]string(nil), s...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func TestAttachAndRead(t *testing.T) {
	src := attachedDoc(t, map[string][]byte{
		"figures.csv": []byte("year,total\n2026,41\n"),
		"notes.txt":   []byte("the original wording"),
	})
	r := NewReaderOrFail(t, src)

	list := r.Attachments()
	if len(list) != 2 {
		t.Fatalf("found %d attachments, want 2: %+v", len(list), list)
	}
	if list[0].Name != "figures.csv" || list[1].Name != "notes.txt" {
		t.Errorf("names = %q, %q", list[0].Name, list[1].Name)
	}
	data, err := list[0].Data()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "year,total\n2026,41\n" {
		t.Errorf("the first attachment reads back as %q", data)
	}
	if list[0].Size != 19 {
		t.Errorf("size = %d, want 19", list[0].Size)
	}
	if list[0].Page != -1 {
		t.Errorf("a document-level attachment reports page %d, want -1", list[0].Page)
	}
	// The document still works as a document.
	if txt, err := r.PageText(0); err != nil || !strings.Contains(txt, "inside it") {
		t.Errorf("page text after attaching: %q %v", txt, err)
	}
}

// TestAttachTwiceKeepsBoth: the collection is a name tree, and adding to
// it has to carry what is already there rather than replace it.
func TestAttachTwiceKeepsBoth(t *testing.T) {
	doc := New()
	doc.Compress = false
	doc.AddPage()
	r := NewReaderOrFail(t, docBytes(t, doc))

	u := Update(r)
	if err := u.Attach("one.txt", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := u.Attach("two.txt", []byte("second")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	out := NewReaderOrFail(t, buf.Bytes())
	list := out.Attachments()
	if len(list) != 2 {
		t.Fatalf("found %d attachments, want 2", len(list))
	}

	// And a second update on the result keeps them both again.
	u2 := Update(out)
	if err := u2.Attach("three.txt", []byte("third")); err != nil {
		t.Fatal(err)
	}
	var buf2 bytes.Buffer
	if _, err := u2.WriteTo(&buf2); err != nil {
		t.Fatal(err)
	}
	final := NewReaderOrFail(t, buf2.Bytes()).Attachments()
	if len(final) != 3 {
		t.Fatalf("after a second update there are %d attachments, want 3", len(final))
	}
	for _, want := range []string{"one.txt", "three.txt", "two.txt"} {
		found := false
		for _, a := range final {
			if a.Name == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is gone", want)
		}
	}
}

func TestRemoveAttachments(t *testing.T) {
	src := attachedDoc(t, map[string][]byte{
		"keep.txt":   []byte("this one stays"),
		"secret.csv": []byte("this one must not survive"),
	})
	r := NewReaderOrFail(t, src)
	u := Update(r)

	n, err := u.RemoveAttachments(func(a Attachment) bool {
		return strings.HasSuffix(a.Name, ".csv")
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("removed %d attachments, want 1", n)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.Bytes()

	list := NewReaderOrFail(t, out).Attachments()
	if len(list) != 1 || list[0].Name != "keep.txt" {
		t.Fatalf("attachments after removal = %+v", list)
	}
	if data, err := list[0].Data(); err != nil || string(data) != "this one stays" {
		t.Errorf("the kept attachment reads as %q (%v)", data, err)
	}
}

// TestRemovedAttachmentIsNotJustUnlinked is the part that matters. An
// incremental update appends, so an object it stops pointing at is still
// in the file and still findable. The bytes have to go.
func TestRemovedAttachmentIsNotJustUnlinked(t *testing.T) {
	const secret = "the-unredacted-original-9f3a"
	src := attachedDoc(t, map[string][]byte{"original.txt": []byte(secret)})
	r := NewReaderOrFail(t, src)
	u := Update(r)
	if n, err := u.RemoveAttachments(nil); err != nil || n != 1 {
		t.Fatalf("removed %d (%v)", n, err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	// An update appends, so the original bytes are still in this file:
	// what the caller gets is a document that no longer offers them.
	// Rewriting is what drops them, which is what redaction does.
	if len(NewReaderOrFail(t, buf.Bytes()).Attachments()) != 0 {
		t.Error("the attachment is still on offer after removal")
	}

	// Through a redaction, which rewrites rather than appends, the bytes
	// must be gone from the file entirely.
	rd := Redact(NewReaderOrFail(t, buf.Bytes()))
	var redacted bytes.Buffer
	if _, err := rd.WriteTo(&redacted); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(redacted.Bytes(), []byte(secret)) {
		t.Error("the attachment's bytes survived into the rewritten file")
	}
}

// TestAttachmentsOnAPage covers the other place a file lives: a
// paperclip annotation rather than the document's collection.
func TestAttachmentsOnAPage(t *testing.T) {
	doc := New()
	doc.Compress = false
	doc.AddPage()
	r := NewReaderOrFail(t, docBytes(t, doc))

	u := Update(r)
	stream := u.AddObject(NewStream(Dict{
		"Type": Name("EmbeddedFile"), "Subtype": Name("text#2Fplain"),
		"Params": Dict{"Size": int64(5)},
	}, []byte("clip!")))
	spec := u.AddObject(Dict{
		"Type": Name("Filespec"), "F": String("clipped.txt"),
		"Desc": String("a note about it"),
		"EF":   Dict{"F": stream},
	})
	annot := u.AddObject(Dict{
		"Type": Name("Annot"), "Subtype": Name("FileAttachment"),
		"Rect": Array{100.0, 600.0, 120.0, 620.0}, "FS": spec,
	})
	if err := u.SetPageEntry(0, "Annots", Array{annot}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}

	out := NewReaderOrFail(t, buf.Bytes())
	list := out.Attachments()
	if len(list) != 1 {
		t.Fatalf("found %d attachments, want the one on the page", len(list))
	}
	a := list[0]
	if a.Name != "clipped.txt" || a.Page != 0 {
		t.Errorf("attachment = %+v", a)
	}
	if a.Description != "a note about it" {
		t.Errorf("description = %q", a.Description)
	}
	// The MIME type is a name, so its slash arrives escaped.
	if a.MIMEType != "text/plain" {
		t.Errorf("MIME type = %q, want text/plain", a.MIMEType)
	}
	if data, err := a.Data(); err != nil || string(data) != "clip!" {
		t.Errorf("data = %q (%v)", data, err)
	}

	// And it can be taken off again, annotation and all.
	u2 := Update(out)
	if n, err := u2.RemoveAttachments(nil); err != nil || n != 1 {
		t.Fatalf("removed %d (%v)", n, err)
	}
	var buf2 bytes.Buffer
	if _, err := u2.WriteTo(&buf2); err != nil {
		t.Fatal(err)
	}
	final := NewReaderOrFail(t, buf2.Bytes())
	if len(final.Attachments()) != 0 {
		t.Error("the page attachment survived removal")
	}
	if annots, err := final.Annotations(0); err != nil || len(annots) != 0 {
		t.Errorf("the paperclip annotation is still on the page: %v %v", annots, err)
	}
}

// TestNameTreeWithKids: a document with many attachments splits its name
// tree into branches, and reading only the root finds none of them.
func TestNameTreeWithKids(t *testing.T) {
	doc := New()
	doc.Compress = false
	doc.AddPage()
	r := NewReaderOrFail(t, docBytes(t, doc))
	u := Update(r)

	// Build the tree by hand, since gopdf writes a flat one.
	leaf := func(names ...string) Ref {
		var flat Array
		for _, n := range names {
			stm := u.AddObject(NewStream(Dict{"Type": Name("EmbeddedFile")},
				[]byte("contents of "+n)))
			spec := u.AddObject(Dict{
				"Type": Name("Filespec"), "F": String(n),
				"EF": Dict{"F": stm},
			})
			flat = append(flat, String(n), spec)
		}
		return u.AddObject(Dict{"Names": flat, "Limits": Array{
			String(names[0]), String(names[len(names)-1])}})
	}
	root := u.AddObject(Dict{"Kids": Array{
		leaf("a.txt", "b.txt"),
		u.AddObject(Dict{"Kids": Array{leaf("c.txt", "d.txt")}}),
	}})
	if err := u.SetCatalogEntry("Names", Dict{"EmbeddedFiles": root}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}

	list := NewReaderOrFail(t, buf.Bytes()).Attachments()
	if len(list) != 4 {
		t.Fatalf("found %d attachments through the tree, want 4: %+v", len(list), list)
	}
	for i, want := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		if list[i].Name != want {
			t.Errorf("attachment %d is %q, want %q", i, list[i].Name, want)
		}
		if data, err := list[i].Data(); err != nil || string(data) != "contents of "+want {
			t.Errorf("%s reads as %q (%v)", want, data, err)
		}
	}
}

func TestAttachmentsOfADocumentWithNone(t *testing.T) {
	doc := New()
	doc.AddPage()
	r := NewReaderOrFail(t, docBytes(t, doc))
	if list := r.Attachments(); len(list) != 0 {
		t.Errorf("a document with no attachments reports %d", len(list))
	}
	u := Update(r)
	if n, err := u.RemoveAttachments(nil); err != nil || n != 0 {
		t.Errorf("removing from a document with none: %d, %v", n, err)
	}
	if err := u.Attach("", []byte("x")); err == nil {
		t.Error("an attachment with no name was accepted")
	}
}

// TestRedactionRefusesALeakInAKeptAttachment is the reason any of this
// is here. A report is redacted, the caller asks to keep the spreadsheet
// it came from, and the spreadsheet still holds the same words. The page
// looks clean and the document is not, so the output is withheld.
func TestRedactionRefusesALeakInAKeptAttachment(t *testing.T) {
	doc := New()
	doc.Compress = false
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(72, 100, "Contact Ada Lovelace about the audit.")
	r := NewReaderOrFail(t, docBytes(t, doc))

	u := Update(r)
	if err := u.Attach("contacts.csv", []byte("name,role\nAda Lovelace,analyst\n")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	withFile := buf.Bytes()

	rd := Redact(NewReaderOrFail(t, withFile))
	rd.Text("Ada Lovelace")
	rd.KeepAttachments(true)
	var out bytes.Buffer
	_, err := rd.WriteTo(&out)
	if err == nil {
		t.Fatal("the redaction succeeded with the name still in the attachment")
	}
	if !strings.Contains(err.Error(), "contacts.csv") {
		t.Errorf("the error does not name the attachment: %v", err)
	}
	if out.Len() != 0 {
		t.Error("a document that failed its own check was written anyway")
	}

	// Letting the attachment go, which is the default, makes the same
	// redaction succeed and takes the words out of the file entirely.
	rd2 := Redact(NewReaderOrFail(t, withFile))
	rd2.Text("Ada Lovelace")
	var clean bytes.Buffer
	if _, err := rd2.WriteTo(&clean); err != nil {
		t.Fatalf("with the attachment removed: %v", err)
	}
	if bytes.Contains(clean.Bytes(), []byte("Ada Lovelace")) {
		t.Error("the name survived into the redacted bytes")
	}
	if bytes.Contains(clean.Bytes(), []byte("analyst")) {
		t.Error("the attachment's contents survived into the redacted bytes")
	}
	final := NewReaderOrFail(t, clean.Bytes())
	if len(final.Attachments()) != 0 {
		t.Error("the attachment is still listed")
	}
	if txt, err := final.PageText(0); err != nil || strings.Contains(txt, "Lovelace") {
		t.Errorf("page text = %q (%v)", txt, err)
	}
	// The rest of the sentence is still there: removal is not deletion
	// of the page.
	if txt, _ := final.PageText(0); !strings.Contains(txt, "audit") {
		t.Errorf("the rest of the line went too: %q", txt)
	}
}

// TestRedactorListsAttachments: a reviewer should be able to see what
// the document is carrying before deciding.
func TestRedactorListsAttachments(t *testing.T) {
	src := attachedDoc(t, map[string][]byte{"sheet.csv": []byte("a,b\n")})
	rd := Redact(NewReaderOrFail(t, src))
	list := rd.Attachments()
	if len(list) != 1 || list[0].Name != "sheet.csv" {
		t.Fatalf("the redactor reports %+v", list)
	}
}

// TestRedactionRemovesAttachmentsByDefault: no rule here reaches into an
// attachment, so leaving one in place is the likeliest way for a
// redacted document to give up what it was redacted for.
func TestRedactionRemovesAttachmentsByDefault(t *testing.T) {
	src := attachedDoc(t, map[string][]byte{"unrelated.txt": []byte("nothing to do with it")})
	rd := Redact(NewReaderOrFail(t, src))
	rd.Text("A document")
	var out bytes.Buffer
	if _, err := rd.WriteTo(&out); err != nil {
		t.Fatal(err)
	}
	if list := NewReaderOrFail(t, out.Bytes()).Attachments(); len(list) != 0 {
		t.Fatalf("the attachment survived a redaction: %+v", list)
	}
	if bytes.Contains(out.Bytes(), []byte("nothing to do with it")) {
		t.Error("the attachment's bytes are still in the file")
	}

	// And KeepAttachments says otherwise.
	rd2 := Redact(NewReaderOrFail(t, src))
	rd2.KeepAttachments(true)
	rd2.Text("A document")
	var kept bytes.Buffer
	if _, err := rd2.WriteTo(&kept); err != nil {
		t.Fatal(err)
	}
	list := NewReaderOrFail(t, kept.Bytes()).Attachments()
	if len(list) != 1 {
		t.Fatalf("KeepAttachments dropped it anyway: %+v", list)
	}
	if data, err := list[0].Data(); err != nil || string(data) != "nothing to do with it" {
		t.Errorf("the kept attachment reads as %q (%v)", data, err)
	}
}

// TestRedactionKeepsNamedDestinations: the catalog's /Names holds more
// than embedded files. Deleting it wholesale to be rid of them takes the
// named destinations too, and every internal link in the document stops
// working.
func TestRedactionKeepsNamedDestinations(t *testing.T) {
	doc := New()
	doc.Compress = false
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(72, 100, "Chapter one, and a word to remove.")
	r := NewReaderOrFail(t, docBytes(t, doc))

	u := Update(r)
	pageRef, _ := r.PageRef(0)
	dests := u.AddObject(Dict{"Names": Array{
		String("chapter1"), Array{pageRef, Name("Fit")},
	}})
	if err := u.SetCatalogEntry("Names", Dict{
		"Dests":      dests,
		"JavaScript": u.AddObject(Dict{"Names": Array{String("s"), Dict{}}}),
	}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}

	rd := Redact(NewReaderOrFail(t, buf.Bytes()))
	rd.Text("remove")
	var out bytes.Buffer
	if _, err := rd.WriteTo(&out); err != nil {
		t.Fatal(err)
	}
	final := NewReaderOrFail(t, out.Bytes())
	names, ok := final.Resolve(final.Catalog()["Names"]).(Dict)
	if !ok {
		t.Fatal("the catalog lost its /Names entirely")
	}
	if names["Dests"] == nil {
		t.Error("the named destinations were thrown away with the metadata")
	}
	if names["JavaScript"] != nil {
		t.Error("the document scripts survived the metadata strip")
	}
	if txt, _ := final.PageText(0); strings.Contains(txt, "remove") {
		t.Errorf("the redaction itself did not happen: %q", txt)
	}
}

// TestDocumentAttach covers the other half of the pair: a file put into
// a document being built, rather than into one that already exists.
func TestDocumentAttach(t *testing.T) {
	doc := New()
	doc.Compress = false
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(72, 100, "A report with its figures attached.")
	if err := doc.Attach("figures.csv", []byte("year,total\n2026,41\n")); err != nil {
		t.Fatal(err)
	}
	if err := doc.AttachWithDescription("notes.txt", "how it was compiled",
		[]byte("the working")); err != nil {
		t.Fatal(err)
	}
	if err := doc.Attach("", []byte("x")); err == nil {
		t.Error("an attachment with no name was accepted")
	}

	r := NewReaderOrFail(t, docBytes(t, doc))
	list := r.Attachments()
	if len(list) != 2 {
		t.Fatalf("found %d attachments, want 2: %+v", len(list), list)
	}
	if list[0].Name != "figures.csv" || list[1].Name != "notes.txt" {
		t.Fatalf("names = %q, %q", list[0].Name, list[1].Name)
	}
	if list[1].Description != "how it was compiled" {
		t.Errorf("description = %q", list[1].Description)
	}
	if data, err := list[0].Data(); err != nil || string(data) != "year,total\n2026,41\n" {
		t.Errorf("the first attachment reads as %q (%v)", data, err)
	}
	if data, err := list[1].Data(); err != nil || string(data) != "the working" {
		t.Errorf("the second attachment reads as %q (%v)", data, err)
	}
	if list[0].Size != 19 {
		t.Errorf("size = %d, want 19", list[0].Size)
	}
	// And the document is still a document.
	if txt, err := r.PageText(0); err != nil || !strings.Contains(txt, "figures attached") {
		t.Errorf("page text: %q %v", txt, err)
	}
	verifyXref(t, docBytes(t, doc))
}

// TestDocumentAttachThenUpdate: a file added at build time and another
// added by a later update have to end up in the same collection.
func TestDocumentAttachThenUpdate(t *testing.T) {
	doc := New()
	doc.Compress = false
	doc.AddPage()
	if err := doc.Attach("first.txt", []byte("one")); err != nil {
		t.Fatal(err)
	}
	r := NewReaderOrFail(t, docBytes(t, doc))
	u := Update(r)
	if err := u.Attach("second.txt", []byte("two")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	list := NewReaderOrFail(t, buf.Bytes()).Attachments()
	if len(list) != 2 {
		t.Fatalf("found %d attachments, want both: %+v", len(list), list)
	}
}
