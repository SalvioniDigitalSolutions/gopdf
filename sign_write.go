package gopdf

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"strings"
)

// Writing a signature. A signature covers the whole file except its own
// blob, so the file has to be laid out first with fixed-width
// placeholders, then the byte range measured and the blob patched in
// without moving a single byte.

func sha256Sum(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

const (
	// The placeholder text the signature dictionary is written with. Both
	// spans are fixed width, so patching them cannot shift any offset.
	sigByteRangeKey         = "/ByteRange "
	sigByteRangePlaceholder = "[0          0          0          0         ]"
	sigContentsKey          = "/Contents "
	byteRangeSlotWidth      = 10
	defaultSigReserve       = 8192
)

// Sign adds a digital signature to the document. The signature covers
// every byte of the resulting file except its own blob, and is written as
// an incremental update, so any signature already on the document keeps
// covering the bytes it signed.
//
// The signature is computed when the document is written, once the file's
// layout is known.
func (u *Updater) Sign(opts SignOptions) error {
	if opts.Certificate == nil || opts.Key == nil {
		return fmt.Errorf("gopdf: signing needs a certificate and a private key")
	}
	if opts.FieldName == "" {
		opts.FieldName = "Signature1"
	}
	if opts.ReservedBytes <= 0 {
		opts.ReservedBytes = defaultSigReserve
	}
	// A certifying signature may forbid the very change being made.
	for _, s := range u.r.Signatures() {
		if s.Certified && s.Permissions == 1 {
			return fmt.Errorf("gopdf: %q certifies the document and forbids any change",
				s.Field)
		}
	}
	u.signing = &opts
	return nil
}

// prepareSignature creates the signature dictionary and its field, and
// hangs the field off the document's form and first page.
func (u *Updater) prepareSignature() error {
	opts := u.signing
	sig := Dict{
		"Type":      Name("Sig"),
		"Filter":    Name("Adobe.PPKLite"),
		"SubFilter": Name("adbe.pkcs7.detached"),
		"M":         String(strings.Trim(pdfDate(opts.signingTime()), "()")),
	}
	for key, value := range map[Name]string{
		"Name": opts.Name, "Reason": opts.Reason,
		"Location": opts.Location, "ContactInfo": opts.ContactInfo,
	} {
		if value != "" {
			sig[key] = String(textStringBytes(value))
		}
	}
	u.sigValueNum = u.add(sig)

	// An invisible widget carries the field: hidden from view, printed.
	u.sigFieldNum = u.add(Dict{
		"FT": Name("Sig"), "T": String(textStringBytes(opts.FieldName)),
		"V":    Ref{Num: u.sigValueNum},
		"Type": Name("Annot"), "Subtype": Name("Widget"),
		"Rect": Array{float64(0), float64(0), float64(0), float64(0)},
		"F":    int64(132),
	})

	rootRef, ok := u.r.trailer["Root"].(Ref)
	if !ok {
		return fmt.Errorf("gopdf: the catalog is not an indirect object")
	}
	root, ok := u.r.resolve(rootRef).(Dict)
	if !ok {
		return fmt.Errorf("gopdf: document has no catalog")
	}
	acro, _ := u.r.resolve(root["AcroForm"]).(Dict)
	newAcro := cloneDict(acro)
	fields, _ := u.r.resolve(newAcro["Fields"]).(Array)
	newAcro["Fields"] = append(append(Array{}, fields...), Ref{Num: u.sigFieldNum})
	// SigFlags 3 tells a viewer the form holds signatures and must be
	// saved incrementally.
	newAcro["SigFlags"] = int64(3)

	newRoot := cloneDict(root)
	newRoot["AcroForm"] = newAcro
	u.set(rootRef.Num, newRoot)

	pageNum, ok := u.r.pageObjectNumber(0)
	if !ok {
		return fmt.Errorf("gopdf: the first page is not an indirect object")
	}
	pageDict := cloneDict(u.pageDict(pageNum, 0))
	annots, _ := u.r.resolve(pageDict["Annots"]).(Array)
	pageDict["Annots"] = append(append(Array{}, annots...), Ref{Num: u.sigFieldNum})
	u.set(pageNum, pageDict)
	return nil
}

// writeSignatureValue emits the signature dictionary with placeholders in
// place of the byte range and the blob.
func (u *Updater) writeSignatureValue(ow *offsetWriter, ctx *writeCtx, sig Dict) {
	ow.str("<<")
	for _, k := range sortedKeys(sig) {
		ow.str(" ")
		writeName(ow, k)
		ow.str(" ")
		writeValue(ow, sig[k], ctx)
	}
	ow.printf(" %s%s", sigByteRangeKey, sigByteRangePlaceholder)
	ow.printf(" %s<%s> >>\n", sigContentsKey, strings.Repeat("0", u.signing.ReservedBytes*2))
}

// signBuffer measures the span around the placeholder, computes the
// signature and patches both in. The file's length never changes, so
// every offset the cross-reference records stays valid.
func (u *Updater) signBuffer(file []byte, updateStart int) ([]byte, error) {
	opts := u.signing
	reserved := opts.ReservedBytes * 2

	// Only the appended region is searched. An earlier signature in the
	// original bytes has placeholders of its own, and patching those
	// would corrupt the very bytes that signature covers.
	region := file[updateStart:]
	marker := []byte(sigContentsKey + "<" + strings.Repeat("0", 32))
	start := bytes.Index(region, marker)
	if start < 0 {
		return nil, fmt.Errorf("gopdf: the signature placeholder was not written")
	}
	start += updateStart
	open := start + len(sigContentsKey) // the '<'
	contentsEnd := open + 1 + reserved + 1
	if contentsEnd > len(file) || file[contentsEnd-1] != '>' {
		return nil, fmt.Errorf("gopdf: the signature placeholder is malformed")
	}

	// The signature covers everything except the blob between the
	// brackets, brackets included.
	ranges := []int{0, open, contentsEnd, len(file) - contentsEnd}

	at := bytes.Index(region, []byte(sigByteRangeKey+"["))
	if at < 0 {
		return nil, fmt.Errorf("gopdf: the byte range placeholder was not written")
	}
	at += updateStart
	var text strings.Builder
	text.WriteString("[")
	for i, v := range ranges {
		if i > 0 {
			text.WriteString(" ")
		}
		fmt.Fprintf(&text, "%-*d", byteRangeSlotWidth, v)
	}
	text.WriteString("]")
	if text.Len() != len(sigByteRangePlaceholder) {
		return nil, fmt.Errorf("gopdf: a byte range value is too large for its placeholder")
	}
	copy(file[at+len(sigByteRangeKey):], text.String())

	digest := sha256.New()
	digest.Write(file[:open])
	digest.Write(file[contentsEnd:])

	blob, err := buildCMS(*opts, digest.Sum(nil))
	if err != nil {
		return nil, err
	}
	if len(blob)*2 > reserved {
		return nil, fmt.Errorf("gopdf: the signature needs %d bytes but only %d were "+
			"reserved; raise SignOptions.ReservedBytes", len(blob), opts.ReservedBytes)
	}
	const digits = "0123456789ABCDEF"
	out := file[open+1 : open+1+reserved]
	for i := range out {
		out[i] = '0'
	}
	for i, b := range blob {
		out[i*2] = digits[b>>4]
		out[i*2+1] = digits[b&0x0F]
	}
	return file, nil
}
