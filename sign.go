package gopdf

import (
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"fmt"
	"math/big"
	"sort"
	"time"
)

// Digital signatures. A signed PDF carries a detached CMS blob covering
// every byte of the file except the blob itself, so signing is inherently
// an incremental-update operation: the bytes already signed must not move.

// Signature describes a signature found in a document.
type Signature struct {
	// Field is the form field the signature occupies.
	Field string
	// Name, Reason, Location and ContactInfo are what the signer
	// declared, where they declared anything.
	Name, Reason, Location, ContactInfo string
	// When is the claimed signing time.
	When time.Time
	// Signer is the common name on the signing certificate.
	Signer string
	// Certificate is the signing certificate, when it could be read out
	// of the signature blob.
	Certificate *x509.Certificate
	// ByteRange is the span of the file the signature covers, as pairs of
	// offset and length.
	ByteRange []int
	// CoversWholeFile reports whether the byte range reaches the end of
	// the file. When it does not, the document was changed after signing.
	CoversWholeFile bool
	// Certified marks a signature that also restricts what later changes
	// are permitted.
	Certified bool
	// Permissions is a certifying signature's /DocMDP level: 1 forbids
	// any change, 2 allows form filling, 3 also allows annotations.
	Permissions int
}

// HasSignatures reports whether the document carries any signature.
func (r *Reader) HasSignatures() bool {
	return len(r.Signatures()) > 0
}

// Signatures lists the document's signatures.
func (r *Reader) Signatures() []Signature {
	var out []Signature
	for _, w := range r.formWidgets() {
		if w.field.Type != FieldSignature {
			continue
		}
		sig, ok := r.resolve(w.fieldD["V"]).(Dict)
		if !ok {
			continue // an empty signature field, waiting to be signed
		}
		s := Signature{Field: w.field.Name}
		get := func(key Name) string {
			v, _ := r.resolve(sig[key]).(String)
			return decodeTextString(v)
		}
		s.Name, s.Reason = get("Name"), get("Reason")
		s.Location, s.ContactInfo = get("Location"), get("ContactInfo")
		if m, ok := r.resolve(sig["M"]).(String); ok {
			s.When = parsePDFDate(string(m))
		}
		if arr, ok := r.resolve(sig["ByteRange"]).(Array); ok {
			for _, e := range arr {
				if v, ok := toInt(r.resolve(e)); ok {
					s.ByteRange = append(s.ByteRange, v)
				}
			}
		}
		if len(s.ByteRange) >= 4 {
			end := s.ByteRange[len(s.ByteRange)-2] + s.ByteRange[len(s.ByteRange)-1]
			s.CoversWholeFile = end >= len(r.data)-2
		}
		if cert := signerCertificate(r.signatureBlob(sig, s.ByteRange)); cert != nil {
			s.Certificate = cert
			s.Signer = cert.Subject.CommonName
		}
		// A certifying signature restricts later changes.
		if refs, ok := r.resolve(sig["Reference"]).(Array); ok {
			for _, e := range refs {
				ref, ok := r.resolve(e).(Dict)
				if !ok || r.resolve(ref["TransformMethod"]) != Name("DocMDP") {
					continue
				}
				s.Certified = true
				s.Permissions = 2
				if params, ok := r.resolve(ref["TransformParams"]).(Dict); ok {
					if p, ok := toInt(r.resolve(params["P"])); ok {
						s.Permissions = p
					}
				}
			}
		}
		out = append(out, s)
	}
	return out
}

// signatureBlob returns a signature's raw CMS bytes. It reads them out of
// the gap the byte range leaves in the file rather than out of the parsed
// string, because a signature's /Contents is exempt from the document's
// encryption: in a protected file the decrypted object is nonsense.
func (r *Reader) signatureBlob(sig Dict, byteRange []int) []byte {
	if len(byteRange) >= 4 {
		open, end := byteRange[1], byteRange[2]
		if 0 <= open && open < end && end <= len(r.data) &&
			r.data[open] == '<' && r.data[end-1] == '>' {
			if blob, ok := decodeHexString(r.data[open+1 : end-1]); ok {
				return blob
			}
		}
	}
	// A file whose byte range is unusable is not one gopdf can check
	// anyway; fall back to the string, correct when nothing is encrypted.
	if blob, ok := r.resolve(sig["Contents"]).(String); ok {
		return []byte(blob)
	}
	return nil
}

// decodeHexString decodes the body of a PDF hexadecimal string.
func decodeHexString(src []byte) ([]byte, bool) {
	out := make([]byte, 0, len(src)/2)
	var half byte
	var have bool
	for _, c := range src {
		if isWS(c) {
			continue
		}
		v, err := hexVal(c)
		if err != nil {
			return nil, false
		}
		if have {
			out = append(out, half<<4|v)
			have = false
		} else {
			half, have = v, true
		}
	}
	if have { // an odd digit count pads with a trailing zero
		out = append(out, half<<4)
	}
	return out, true
}

// parsePDFDate reads a PDF date string such as D:20260806120000+02'00'.
func parsePDFDate(s string) time.Time {
	if len(s) < 10 || s[:2] != "D:" {
		return time.Time{}
	}
	body := s[2:]
	if len(body) > 14 {
		body = body[:14]
	}
	for _, layout := range []string{"20060102150405", "200601021504", "2006010215", "20060102"} {
		if len(body) == len(layout) {
			if t, err := time.Parse(layout, body); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}

// signerCertificate pulls the signing certificate out of a CMS blob.
func signerCertificate(blob []byte) *x509.Certificate {
	// The blob sits in a fixed reserve and is padded with zeros to fill
	// it. Those are not trimmed: a DER object carries its own length, so
	// the parser stops at the end of the structure and hands back the
	// padding as the remainder. Trimming would be a guess, and a wrong
	// one whenever the signature's own last byte is zero — about one
	// signature in 256, which is exactly often enough to look like
	// something else.
	var ci contentInfo
	if _, err := asn1.Unmarshal(blob, &ci); err != nil {
		return nil
	}
	var sd signedData
	if _, err := asn1.Unmarshal(ci.Content.Bytes, &sd); err != nil {
		return nil
	}
	certs, err := x509.ParseCertificates(sd.Certificates.Bytes)
	if err != nil || len(certs) == 0 {
		return nil
	}
	// The end-entity certificate is the one that signed; prefer a
	// certificate that is not a CA.
	for _, c := range certs {
		if !c.IsCA {
			return c
		}
	}
	return certs[0]
}

// --- CMS SignedData ---

var (
	oidSignedData    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidData          = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
	oidContentType   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 3}
	oidMessageDigest = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}
	oidSigningTime   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 5}
	oidSHA256        = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	oidRSA           = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}
	oidECDSA         = asn1.ObjectIdentifier{1, 2, 840, 10045, 2, 1}
)

type contentInfo struct {
	ContentType asn1.ObjectIdentifier
	// The content is wrapped in an explicit [0] built by hand: a
	// RawValue holding FullBytes is emitted verbatim, so a struct tag
	// asking for the wrapper would be ignored on the way out.
	Content asn1.RawValue
}

type encapContentInfo struct {
	EContentType asn1.ObjectIdentifier
}

type issuerAndSerial struct {
	Issuer       asn1.RawValue
	SerialNumber *big.Int
}

type attribute struct {
	Type   asn1.ObjectIdentifier
	Values asn1.RawValue `asn1:"set"`
}

type signerInfo struct {
	Version            int
	SID                issuerAndSerial
	DigestAlgorithm    pkix.AlgorithmIdentifier
	SignedAttrs        asn1.RawValue `asn1:"optional,tag:0"`
	SignatureAlgorithm pkix.AlgorithmIdentifier
	Signature          []byte
}

type signedData struct {
	Version          int
	DigestAlgorithms []pkix.AlgorithmIdentifier `asn1:"set"`
	EncapContentInfo encapContentInfo
	Certificates     asn1.RawValue `asn1:"optional,tag:0"`
	SignerInfos      []signerInfo  `asn1:"set"`
}

// derHeader writes an ASN.1 tag and definite length.
func derHeader(tag byte, length int) []byte {
	out := []byte{tag}
	switch {
	case length < 0x80:
		return append(out, byte(length))
	case length < 0x100:
		return append(out, 0x81, byte(length))
	case length < 0x10000:
		return append(out, 0x82, byte(length>>8), byte(length))
	default:
		return append(out, 0x83, byte(length>>16), byte(length>>8), byte(length))
	}
}

// derSet encodes the elements as a SET OF, sorted as DER requires.
func derSet(tag byte, elements [][]byte) []byte {
	sorted := append([][]byte(nil), elements...)
	sort.Slice(sorted, func(i, j int) bool {
		return string(sorted[i]) < string(sorted[j])
	})
	var body []byte
	for _, e := range sorted {
		body = append(body, e...)
	}
	return append(derHeader(tag, len(body)), body...)
}

// buildSignedAttributes assembles the attributes a signature covers, and
// returns both the DER a signature is computed over (tagged as a SET) and
// the implicitly tagged form the signer info carries.
func buildSignedAttributes(digest []byte, when time.Time) (signedForm, carriedForm []byte, err error) {
	rawValue := func(v any) (asn1.RawValue, error) {
		b, err := asn1.Marshal(v)
		if err != nil {
			return asn1.RawValue{}, err
		}
		return asn1.RawValue{FullBytes: append(derHeader(0x31, len(b)), b...)}, nil
	}
	ctVal, err := rawValue(oidData)
	if err != nil {
		return nil, nil, err
	}
	mdVal, err := rawValue(digest)
	if err != nil {
		return nil, nil, err
	}
	stVal, err := rawValue(when.UTC())
	if err != nil {
		return nil, nil, err
	}

	var encoded [][]byte
	for _, a := range []attribute{
		{Type: oidContentType, Values: ctVal},
		{Type: oidMessageDigest, Values: mdVal},
		{Type: oidSigningTime, Values: stVal},
	} {
		b, err := asn1.Marshal(a)
		if err != nil {
			return nil, nil, err
		}
		encoded = append(encoded, b)
	}
	// The signature covers the attributes tagged as a SET; the signer
	// info carries the identical content under an implicit [0].
	return derSet(0x31, encoded), derSet(0xA0, encoded), nil
}

// SignOptions describes a signature to apply.
type SignOptions struct {
	// Certificate is the signing certificate, and Key the private key
	// matching it. Both are required.
	Certificate *x509.Certificate
	Key         crypto.Signer
	// Chain holds any intermediate certificates to embed alongside.
	Chain []*x509.Certificate
	// Name, Reason, Location and ContactInfo are recorded in the
	// signature dictionary for a reader to display.
	Name, Reason, Location, ContactInfo string
	// When is the claimed signing time; the zero value means now.
	When time.Time
	// FieldName is the form field to create. It defaults to "Signature1".
	FieldName string
	// ReservedBytes is how much room to leave for the signature blob.
	// The default suits an ordinary certificate chain.
	ReservedBytes int
}

// signingTime is the claimed signing time, defaulting to now.
func (o SignOptions) signingTime() time.Time {
	if o.When.IsZero() {
		return time.Now()
	}
	return o.When
}

// buildCMS produces the detached signature blob covering digest.
func buildCMS(opts SignOptions, digest []byte) ([]byte, error) {
	when := opts.When
	if when.IsZero() {
		when = time.Now()
	}
	signedForm, carriedForm, err := buildSignedAttributes(digest, when)
	if err != nil {
		return nil, err
	}

	signature, err := opts.Key.Sign(rand.Reader, sha256Sum(signedForm), crypto.SHA256)
	if err != nil {
		return nil, fmt.Errorf("gopdf: signing failed: %w", err)
	}

	sigAlg := pkix.AlgorithmIdentifier{Algorithm: oidRSA, Parameters: asn1.NullRawValue}
	switch opts.Certificate.PublicKeyAlgorithm {
	case x509.ECDSA:
		sigAlg = pkix.AlgorithmIdentifier{Algorithm: oidECDSA}
	}

	var certBytes []byte
	certBytes = append(certBytes, opts.Certificate.Raw...)
	for _, c := range opts.Chain {
		certBytes = append(certBytes, c.Raw...)
	}

	sd := signedData{
		Version: 1,
		DigestAlgorithms: []pkix.AlgorithmIdentifier{
			{Algorithm: oidSHA256, Parameters: asn1.NullRawValue},
		},
		EncapContentInfo: encapContentInfo{EContentType: oidData},
		Certificates: asn1.RawValue{
			Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true,
			Bytes:     certBytes,
			FullBytes: append(derHeader(0xA0, len(certBytes)), certBytes...),
		},
		SignerInfos: []signerInfo{{
			Version: 1,
			SID: issuerAndSerial{
				Issuer:       asn1.RawValue{FullBytes: opts.Certificate.RawIssuer},
				SerialNumber: opts.Certificate.SerialNumber,
			},
			DigestAlgorithm:    pkix.AlgorithmIdentifier{Algorithm: oidSHA256, Parameters: asn1.NullRawValue},
			SignedAttrs:        asn1.RawValue{FullBytes: carriedForm},
			SignatureAlgorithm: sigAlg,
			Signature:          signature,
		}},
	}
	sdBytes, err := asn1.Marshal(sd)
	if err != nil {
		return nil, err
	}
	return asn1.Marshal(contentInfo{
		ContentType: oidSignedData,
		Content: asn1.RawValue{
			FullBytes: append(derHeader(0xA0, len(sdBytes)), sdBytes...),
		},
	})
}
