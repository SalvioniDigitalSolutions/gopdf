package gopdf

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// twoPageDoc builds a small document to walk around in.
func twoPageDoc(t *testing.T) []byte {
	t.Helper()
	doc := New()
	doc.Compress = false
	for _, s := range []string{"First page", "Second page"} {
		p := doc.AddPage()
		p.SetFont(Helvetica, 12)
		p.Text(72, 700, s)
	}
	return docBytes(t, doc)
}

func TestObjectGraphIsReachable(t *testing.T) {
	r := NewReaderOrFail(t, twoPageDoc(t))

	cat := r.Catalog()
	if cat == nil {
		t.Fatal("no catalog")
	}
	if cat["Type"] != Name("Catalog") {
		t.Errorf("catalog /Type = %v", cat["Type"])
	}
	pages, ok := r.Resolve(cat["Pages"]).(Dict)
	if !ok {
		t.Fatal("the catalog does not lead to a page tree")
	}
	if n, _ := toInt(r.Resolve(pages["Count"])); n != 2 {
		t.Errorf("page tree /Count = %v, want 2", pages["Count"])
	}

	// The trailer must lead back to the same catalog.
	if _, ok := r.Trailer()["Root"].(Ref); !ok {
		t.Errorf("trailer /Root = %T, want a reference", r.Trailer()["Root"])
	}

	// And a page reached by number must be the same page reached by walking.
	pd := r.PageDict(1)
	if pd == nil {
		t.Fatal("no second page")
	}
	ref, ok := r.PageRef(1)
	if !ok {
		t.Fatal("the second page is not an indirect object")
	}
	viaRef, ok := r.Object(ref).(Dict)
	if !ok {
		t.Fatalf("object %d is %T, want a page dictionary", ref.Num, r.Object(ref))
	}
	if viaRef["Type"] != Name("Page") {
		t.Errorf("object %d /Type = %v", ref.Num, viaRef["Type"])
	}
	if len(viaRef) != len(pd) {
		t.Errorf("the page by number and the page by reference differ")
	}
}

func TestResolveHandsBackAUsableStream(t *testing.T) {
	doc := New()
	doc.Compress = true // so the content stream is actually encoded
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(72, 700, "Compressed content")
	r := NewReaderOrFail(t, docBytes(t, doc))

	stm, ok := r.Resolve(r.PageDict(0)["Contents"]).(*Stream)
	if !ok {
		t.Fatalf("/Contents resolved to %T, want a stream", r.Resolve(r.PageDict(0)["Contents"]))
	}
	if stm.Dict["Filter"] != Name("FlateDecode") {
		t.Fatalf("the fixture is not compressed: /Filter = %v", stm.Dict["Filter"])
	}
	data, err := stm.Data()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("Compressed content")) {
		t.Errorf("the decoded stream does not hold the page's text: %q", data)
	}
	// Raw is the bytes as stored, which for a compressed stream is not
	// the same thing at all.
	if raw := stm.Raw(); bytes.Equal(raw, data) {
		t.Error("Raw returned the decoded bytes")
	} else if len(raw) == 0 {
		t.Error("Raw returned nothing")
	}
}

// TestResolveDecodesInsideAnEncryptedFile is the case a naive escape
// hatch gets wrong: the bytes on disk are encrypted, so handing them
// back undecrypted looks like a stream and reads as noise.
func TestResolveDecodesInsideAnEncryptedFile(t *testing.T) {
	doc := New()
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(72, 700, "Secret content")
	doc.Encrypt("", "owner", AllowPrint, AES256)
	r := NewReaderOrFail(t, docBytes(t, doc))

	if !r.IsEncrypted() {
		t.Fatal("the fixture is not encrypted")
	}
	stm, ok := r.Resolve(r.PageDict(0)["Contents"]).(*Stream)
	if !ok {
		t.Fatal("/Contents is not a stream")
	}
	data, err := stm.Data()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("Secret content")) {
		t.Errorf("the decoded stream is not the page content: %q", firstBytes(data))
	}
}

// TestObjectReadsThroughAnObjectStream checks the other storage form:
// an object packed inside an /ObjStm has no offset of its own.
func TestObjectReadsThroughAnObjectStream(t *testing.T) {
	r := NewReaderOrFail(t, packedFixture(t, true))

	var packed int
	for _, ref := range r.Objects() {
		if !r.xref[ref.Num].inObjStm {
			continue
		}
		packed++
		if r.Object(ref) == nil {
			t.Errorf("object %d lives in an object stream and read as nothing",
				ref.Num)
		}
	}
	if packed == 0 {
		t.Fatal("the fixture packed nothing into an object stream")
	}
	// And a walk from the trailer must find the pages inside them.
	var pages int
	r.Walk(func(_ Ref, obj any) bool {
		if d, ok := obj.(Dict); ok && d["Type"] == Name("Page") {
			pages++
		}
		return true
	})
	if pages != 12 {
		t.Errorf("walked past %d pages, want 12", pages)
	}
}

func TestWalkVisitsEveryObjectOnce(t *testing.T) {
	r := NewReaderOrFail(t, twoPageDoc(t))

	counts := map[int]int{}
	var pages int
	r.Walk(func(ref Ref, obj any) bool {
		if ref.Num != 0 {
			counts[ref.Num]++
		}
		if d, ok := obj.(Dict); ok && d["Type"] == Name("Page") {
			pages++
		}
		return true
	})
	if pages != 2 {
		t.Errorf("walked past %d pages, want 2", pages)
	}
	for num, n := range counts {
		if n != 1 {
			t.Errorf("object %d was visited %d times", num, n)
		}
	}
	if len(counts) < 5 {
		t.Errorf("the walk only reached %d objects", len(counts))
	}

	// Returning false stops the walk where it stands.
	var seen int
	r.Walk(func(Ref, any) bool { seen++; return seen < 3 })
	if seen != 3 {
		t.Errorf("the walk continued past the stop, to %d objects", seen)
	}
}

// TestWalkSurvivesACycle: a page points at its parent, which points back
// at the page. A walk that does not remember where it has been never
// returns.
func TestWalkSurvivesACycle(t *testing.T) {
	r := NewReaderOrFail(t, twoPageDoc(t))
	done := make(chan int, 1)
	go func() {
		n := 0
		r.Walk(func(Ref, any) bool { n++; return n < 100000 })
		done <- n
	}()
	select {
	case n := <-done:
		if n >= 100000 {
			t.Error("the walk did not terminate on its own")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the walk did not finish")
	}
}

func TestAddAndReplaceObjects(t *testing.T) {
	src := twoPageDoc(t)
	r := NewReaderOrFail(t, src)
	u := Update(r)

	// A brand-new stream, and a dictionary pointing at it.
	body := []byte("q 1 0 0 RG 4 w 100 100 m 400 400 l S Q")
	stm := u.AddObject(NewStream(Dict{}, body))
	holder := u.AddObject(Dict{"Type": Name("Nothing"), "Points": stm})

	// And a change to something that already exists.
	if err := u.SetCatalogEntry("Lang", String("en-GB")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}

	out := NewReaderOrFail(t, buf.Bytes())
	if got := out.Catalog()["Lang"]; !bytes.Equal([]byte(got.(String)), []byte("en-GB")) {
		t.Errorf("catalog /Lang = %v", got)
	}
	back, ok := out.Object(holder).(Dict)
	if !ok {
		t.Fatalf("the new dictionary read back as %T", out.Object(holder))
	}
	if back["Type"] != Name("Nothing") {
		t.Errorf("the new dictionary lost its /Type: %v", back)
	}
	gotStream, ok := out.Resolve(back["Points"]).(*Stream)
	if !ok {
		t.Fatalf("the new stream read back as %T", out.Resolve(back["Points"]))
	}
	data, err := gotStream.Data()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, body) {
		t.Errorf("the new stream read back as %q", data)
	}
	// The writer owns /Length; a caller must not have to compute it.
	if n, _ := toInt(out.Resolve(gotStream.Dict["Length"])); n != len(body) {
		t.Errorf("/Length = %v, want %d", gotStream.Dict["Length"], len(body))
	}
	_ = stm
}

func TestSetObjectRefusesAnObjectThatIsNotThere(t *testing.T) {
	r := NewReaderOrFail(t, twoPageDoc(t))
	u := Update(r)

	if err := u.SetObject(Ref{Num: 99999}, Dict{}); err == nil {
		t.Error("replacing an object the document does not have was allowed")
	}
	if err := u.SetObject(Ref{Num: 0}, Dict{}); err == nil {
		t.Error("object 0 was allowed")
	}
	// But an object AddObject just made is fair game.
	ref := u.AddObject(Dict{"A": int64(1)})
	if err := u.SetObject(ref, Dict{"A": int64(2)}); err != nil {
		t.Errorf("replacing a freshly added object: %v", err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	out := NewReaderOrFail(t, buf.Bytes())
	d, _ := out.Object(ref).(Dict)
	if n, _ := toInt(out.Resolve(d["A"])); n != 2 {
		t.Errorf("the replacement did not take: %v", d)
	}
}

func TestSetPageEntry(t *testing.T) {
	r := NewReaderOrFail(t, twoPageDoc(t))
	u := Update(r)
	if err := u.SetPageEntry(1, "UserUnit", int64(2)); err != nil {
		t.Fatal(err)
	}
	if err := u.SetPageEntry(9, "UserUnit", int64(2)); err == nil {
		t.Error("a page that does not exist was accepted")
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	out := NewReaderOrFail(t, buf.Bytes())
	if n, _ := toInt(out.Resolve(out.PageDict(1)["UserUnit"])); n != 2 {
		t.Errorf("page 1 /UserUnit = %v", out.PageDict(1)["UserUnit"])
	}
	if out.PageDict(0)["UserUnit"] != nil {
		t.Error("the other page was changed too")
	}
	// The page must still be a page.
	if out.NumPages() != 2 {
		t.Errorf("%d pages after the edit", out.NumPages())
	}
	if _, err := out.PageText(1); err != nil {
		t.Errorf("the edited page no longer reads: %v", err)
	}
}

// TestClonesAreIndependent: the reader hands back its own dictionaries,
// so the documented way to change one is to clone it first.
func TestClonesAreIndependent(t *testing.T) {
	r := NewReaderOrFail(t, twoPageDoc(t))
	orig := r.PageDict(0)
	c := orig.Clone()
	c["Rotate"] = int64(90)
	if orig["Rotate"] != nil {
		t.Error("changing the clone changed the original")
	}
	if Dict(nil).Clone() != nil {
		t.Error("cloning a nil dictionary should give nil")
	}

	arr := Array{int64(1), int64(2)}
	ac := arr.Clone()
	ac[0] = int64(9)
	if arr[0] != int64(1) {
		t.Error("changing the array clone changed the original")
	}
	if Array(nil).Clone() != nil {
		t.Error("cloning a nil array should give nil")
	}
}

func TestInheritedPageValue(t *testing.T) {
	// A page tree that puts /Resources and /MediaBox on the root node
	// and nothing on the page, which the spec allows and real writers do.
	src := twoPageDoc(t)
	r := NewReaderOrFail(t, src)
	u := Update(r)
	pageRef, _ := r.PageRef(0)
	page := r.PageDict(0).Clone()
	rootRef, _ := page["Parent"].(Ref)
	if rootRef.Num == 0 {
		t.Skip("this writer does not give the page a parent")
	}
	root := r.Object(rootRef).(Dict).Clone()
	root["MediaBox"] = page["MediaBox"]
	root["Resources"] = page["Resources"]
	delete(page, "MediaBox")
	delete(page, "Resources")
	if err := u.SetObject(pageRef, page); err != nil {
		t.Fatal(err)
	}
	if err := u.SetObject(rootRef, root); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}

	out := NewReaderOrFail(t, buf.Bytes())
	if out.PageDict(0)["MediaBox"] != nil {
		t.Fatal("the fixture still has a /MediaBox on the page")
	}
	box, ok := out.InheritedPageValue(0, "MediaBox").(Array)
	if !ok || len(box) != 4 {
		t.Fatalf("inherited /MediaBox = %v", out.InheritedPageValue(0, "MediaBox"))
	}
	if out.InheritedPageValue(0, "Resources") == nil {
		t.Error("inherited /Resources came back empty")
	}
	if out.InheritedPageValue(0, "NoSuchKey") != nil {
		t.Error("a key nothing defines came back with something")
	}
	// And the rest of the library must agree, since inheritance is not
	// optional: the page still has a size and still reads.
	if size, err := out.PageSize(0); err != nil || size.W == 0 {
		t.Errorf("page size after moving /MediaBox up the tree: %v %v", size, err)
	}
	if txt, err := out.PageText(0); err != nil || !strings.Contains(txt, "First page") {
		t.Errorf("page text after moving /Resources up the tree: %q %v", txt, err)
	}
}

func TestObjectsListsWhatTheFileDefines(t *testing.T) {
	r := NewReaderOrFail(t, twoPageDoc(t))
	objs := r.Objects()
	if len(objs) < 5 {
		t.Fatalf("the file defines %d objects", len(objs))
	}
	for i := 1; i < len(objs); i++ {
		if objs[i].Num <= objs[i-1].Num {
			t.Fatalf("objects are not in order: %v", objs)
		}
	}
	// Every one of them must read.
	for _, ref := range objs {
		if r.Object(ref) == nil {
			t.Errorf("object %d is listed but reads as nothing", ref.Num)
		}
	}
	if r.Object(Ref{Num: 1 << 20}) != nil {
		t.Error("an object number the file never used read as something")
	}
}

func firstBytes(b []byte) []byte {
	if len(b) > 80 {
		return b[:80]
	}
	return b
}

// TestWriteIntegerKinds: an integer written into the object graph has to
// arrive as a number.
//
// The writer knew int64 and float64, and everything else fell through to
// null — so a caller building a dictionary the way anyone would, with an
// untyped constant, produced a file that parses cleanly and says nothing
// where the number was meant. Nothing reported it. The /W array of a CID
// font written that way came back [null [null]] and every glyph took the
// default width.
func TestWriteIntegerKinds(t *testing.T) {
	doc := New()
	doc.Compress = false
	doc.AddPage()
	r := NewReaderOrFail(t, docBytes(t, doc))

	u := Update(r)
	ref := u.AddObject(Dict{
		"Plain":   7,
		"Int8":    int8(-8),
		"Int16":   int16(1600),
		"Int32":   int32(-32000),
		"Int64":   int64(64),
		"Uint":    uint(11),
		"Uint8":   uint8(255),
		"Uint16":  uint16(65535),
		"Uint32":  uint32(4000000),
		"Uint64":  uint64(9),
		"Float32": float32(1.5),
		"Nested":  Array{1, 2, Array{3}},
	})
	if err := u.SetCatalogEntry("TestNumbers", ref); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}

	out := NewReaderOrFail(t, buf.Bytes())
	got, ok := out.Resolve(out.Catalog()["TestNumbers"]).(Dict)
	if !ok {
		t.Fatal("the dictionary did not come back")
	}
	for _, c := range []struct {
		key  Name
		want float64
	}{
		{"Plain", 7}, {"Int8", -8}, {"Int16", 1600}, {"Int32", -32000},
		{"Int64", 64}, {"Uint", 11}, {"Uint8", 255}, {"Uint16", 65535},
		{"Uint32", 4000000}, {"Uint64", 9}, {"Float32", 1.5},
	} {
		v := out.Resolve(got[c.key])
		if v == nil {
			t.Errorf("/%s came back null; it was written as nothing", c.key)
			continue
		}
		f, ok := toFloat(v)
		if !ok || f != c.want {
			t.Errorf("/%s = %v (%T), want %v", c.key, v, v, c.want)
		}
	}
	arr, ok := out.Resolve(got["Nested"]).(Array)
	if !ok || len(arr) != 3 {
		t.Fatalf("/Nested = %v", out.Resolve(got["Nested"]))
	}
	if f, ok := toFloat(out.Resolve(arr[0])); !ok || f != 1 {
		t.Errorf("integers inside an array were not written: %v", arr)
	}
	if inner, ok := out.Resolve(arr[2]).(Array); !ok || len(inner) != 1 {
		t.Errorf("a nested array did not survive: %v", arr[2])
	} else if f, ok := toFloat(out.Resolve(inner[0])); !ok || f != 3 {
		t.Errorf("an integer nested two deep was not written: %v", inner)
	}
}
