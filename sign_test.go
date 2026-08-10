package gopdf

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"math/big"
	"strings"
	"testing"
	"time"
)

// testSigner builds a self-signed certificate and its key.
func testSigner(t *testing.T, commonName string) (*x509.Certificate, crypto.Signer) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(20260806),
		Subject: pkix.Name{
			CommonName:   commonName,
			Organization: []string{"gopdf tests"},
		},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    time.Now().Add(24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

func signedFixture(t *testing.T, opts SignOptions) ([]byte, []byte) {
	t.Helper()
	doc := New()
	doc.Compress = false
	page := doc.AddPage()
	page.SetFont(Helvetica, 12)
	page.Text(60, 80, "This document is signed.")
	src := docBytes(t, doc)

	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	u := Update(r)
	if err := u.Sign(opts); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return src, buf.Bytes()
}

func TestSignByteRangeCoversEverything(t *testing.T) {
	cert, key := testSigner(t, "Ada Lovelace")
	src, out := signedFixture(t, SignOptions{
		Certificate: cert, Key: key,
		Name: "Ada Lovelace", Reason: "Approval", Location: "London",
	})

	if !bytes.HasPrefix(out, src) {
		t.Fatal("signing did not preserve the original bytes")
	}
	r, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	sigs := r.Signatures()
	if len(sigs) != 1 {
		t.Fatalf("found %d signatures, want 1", len(sigs))
	}
	s := sigs[0]
	if s.Field != "Signature1" {
		t.Errorf("field = %q", s.Field)
	}
	if s.Name != "Ada Lovelace" || s.Reason != "Approval" || s.Location != "London" {
		t.Errorf("declared details = %+v", s)
	}
	if s.Signer != "Ada Lovelace" {
		t.Errorf("signer from the certificate = %q", s.Signer)
	}
	if !s.CoversWholeFile {
		t.Errorf("byte range %v does not reach the end of the %d-byte file",
			s.ByteRange, len(out))
	}
	if len(s.ByteRange) != 4 {
		t.Fatalf("byte range = %v, want four numbers", s.ByteRange)
	}
	// The two covered spans must meet exactly around the blob, leaving no
	// byte of the file unsigned except the blob itself.
	gapStart := s.ByteRange[0] + s.ByteRange[1]
	gapEnd := s.ByteRange[2]
	if out[gapStart] != '<' || out[gapEnd-1] != '>' {
		t.Errorf("the unsigned gap is not the signature blob: %q...%q",
			out[gapStart], out[gapEnd-1])
	}
	if s.ByteRange[2]+s.ByteRange[3] != len(out) {
		t.Errorf("the second span ends at %d, but the file is %d bytes",
			s.ByteRange[2]+s.ByteRange[3], len(out))
	}
}

// TestSignatureVerifies is the substance of the feature: the signature
// must actually verify against the digest of the bytes it claims to
// cover, using the certificate it embeds.
func TestSignatureVerifies(t *testing.T) {
	cert, key := testSigner(t, "Grace Hopper")
	_, out := signedFixture(t, SignOptions{Certificate: cert, Key: key})

	r, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	sigs := r.Signatures()
	if len(sigs) != 1 {
		t.Fatalf("found %d signatures, want 1", len(sigs))
	}
	verifySignature(t, out, sigs[0].ByteRange, cert)
}

// verifySignature recomputes the digest over the bytes a signature claims
// to cover and checks the embedded signature against it, independently of
// the code that produced either.
func verifySignature(t *testing.T, out []byte, br []int, cert *x509.Certificate) {
	t.Helper()

	// Recompute the digest over exactly the covered spans.
	digest := sha256.New()
	digest.Write(out[br[0] : br[0]+br[1]])
	digest.Write(out[br[2] : br[2]+br[3]])
	want := digest.Sum(nil)

	blob := signatureBlob(t, out)
	var ci contentInfo
	if _, err := asn1.Unmarshal(blob, &ci); err != nil {
		t.Fatalf("the signature blob is not well-formed CMS: %v", err)
	}
	if !ci.ContentType.Equal(oidSignedData) {
		t.Errorf("content type = %v, want signedData", ci.ContentType)
	}
	var sd signedData
	if _, err := asn1.Unmarshal(ci.Content.Bytes, &sd); err != nil {
		t.Fatalf("SignedData is not well-formed: %v", err)
	}
	if len(sd.SignerInfos) != 1 {
		t.Fatalf("%d signer infos, want 1", len(sd.SignerInfos))
	}
	si := sd.SignerInfos[0]

	// The signature is computed over the attributes tagged as a SET; Go's
	// asn1 needs a SEQUENCE to unmarshal them into a slice, so the two
	// forms differ only in that first tag byte.
	signedForm := append([]byte(nil), si.SignedAttrs.FullBytes...)
	signedForm[0] = 0x31
	seqForm := append([]byte(nil), si.SignedAttrs.FullBytes...)
	seqForm[0] = 0x30

	var attrs []attribute
	if _, err := asn1.Unmarshal(seqForm, &attrs); err != nil {
		t.Fatalf("signed attributes are not well-formed: %v", err)
	}
	if len(attrs) != 3 {
		t.Errorf("%d signed attributes, want content type, digest and time", len(attrs))
	}
	var gotDigest []byte
	for _, a := range attrs {
		if a.Type.Equal(oidMessageDigest) {
			if _, err := asn1.Unmarshal(a.Values.Bytes, &gotDigest); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !bytes.Equal(gotDigest, want) {
		t.Fatalf("the signed digest does not match the covered bytes\n got %x\nwant %x",
			gotDigest, want)
	}

	// And the signature itself must verify over those attributes.
	sum := sha256.Sum256(signedForm)
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		t.Fatal("expected an RSA certificate")
	}
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], si.Signature); err != nil {
		t.Fatalf("the signature does not verify: %v", err)
	}
}

// signatureBlob extracts the CMS bytes from the file's /Contents.
func signatureBlob(t *testing.T, file []byte) []byte {
	t.Helper()
	at := bytes.Index(file, []byte(sigContentsKey+"<"))
	if at < 0 {
		t.Fatal("no signature contents found")
	}
	start := at + len(sigContentsKey) + 1
	end := bytes.IndexByte(file[start:], '>')
	if end < 0 {
		t.Fatal("unterminated signature contents")
	}
	hexed := file[start : start+end]
	out := make([]byte, len(hexed)/2)
	for i := range out {
		hi, err1 := hexVal(hexed[i*2])
		lo, err2 := hexVal(hexed[i*2+1])
		if err1 != nil || err2 != nil {
			t.Fatal("the signature blob is not hexadecimal")
		}
		out[i] = hi<<4 | lo
	}
	for len(out) > 0 && out[len(out)-1] == 0 {
		out = out[:len(out)-1]
	}
	return out
}

func TestSignTamperingIsDetectable(t *testing.T) {
	cert, key := testSigner(t, "Ada Lovelace")
	_, out := signedFixture(t, SignOptions{Certificate: cert, Key: key})

	r, err := NewReader(out)
	if err != nil {
		t.Fatal(err)
	}
	br := r.Signatures()[0].ByteRange

	before := sha256.New()
	before.Write(out[br[0] : br[0]+br[1]])
	before.Write(out[br[2] : br[2]+br[3]])
	original := before.Sum(nil)

	// Change one byte of the signed content.
	tampered := append([]byte(nil), out...)
	idx := bytes.Index(tampered, []byte("This document is signed."))
	if idx < 0 {
		t.Fatal("could not find the page text to tamper with")
	}
	tampered[idx] = 'X'

	after := sha256.New()
	after.Write(tampered[br[0] : br[0]+br[1]])
	after.Write(tampered[br[2] : br[2]+br[3]])
	if bytes.Equal(after.Sum(nil), original) {
		t.Error("a change inside the signed range did not change the digest")
	}
}

// TestSignAfterSigningKeepsTheFirst checks that a second signature is
// appended without disturbing the bytes the first one covers.
func TestSignAfterSigningKeepsTheFirst(t *testing.T) {
	cert1, key1 := testSigner(t, "First Signer")
	_, once := signedFixture(t, SignOptions{
		Certificate: cert1, Key: key1, FieldName: "Signature1",
	})

	r, err := NewReader(once)
	if err != nil {
		t.Fatal(err)
	}
	cert2, key2 := testSigner(t, "Second Signer")
	u := Update(r)
	if err := u.Sign(SignOptions{
		Certificate: cert2, Key: key2, FieldName: "Signature2",
	}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	twice := buf.Bytes()

	if !bytes.HasPrefix(twice, once) {
		t.Fatal("the second signature disturbed the bytes the first covers")
	}
	r2, err := NewReader(twice)
	if err != nil {
		t.Fatal(err)
	}
	sigs := r2.Signatures()
	if len(sigs) != 2 {
		t.Fatalf("found %d signatures, want 2", len(sigs))
	}
	names := map[string]bool{}
	for _, s := range sigs {
		names[s.Signer] = true
	}
	if !names["First Signer"] || !names["Second Signer"] {
		t.Errorf("signers = %v", names)
	}
	// The first signature no longer covers the whole file, which is
	// exactly how a reader detects a later revision.
	for _, s := range sigs {
		if s.Field == "Signature1" && s.CoversWholeFile {
			t.Error("the first signature should no longer reach the end of the file")
		}
		if s.Field == "Signature2" && !s.CoversWholeFile {
			t.Error("the second signature should cover the whole file")
		}
	}
}

func TestSignValidation(t *testing.T) {
	cert, key := testSigner(t, "Ada")
	src := invoiceFixture(t)
	r, _ := NewReader(src)

	if err := Update(r).Sign(SignOptions{Key: key}); err == nil {
		t.Error("expected an error without a certificate")
	}
	if err := Update(r).Sign(SignOptions{Certificate: cert}); err == nil {
		t.Error("expected an error without a key")
	}

	// Too little room for the blob must be reported, not truncated.
	u := Update(r)
	if err := u.Sign(SignOptions{Certificate: cert, Key: key, ReservedBytes: 64}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	_, err := u.WriteTo(&buf)
	if err == nil {
		t.Fatal("expected an error when the reservation is too small")
	}
	if !strings.Contains(err.Error(), "ReservedBytes") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestSignaturesOnUnsignedDocument(t *testing.T) {
	r, err := NewReader(invoiceFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if r.HasSignatures() {
		t.Error("an unsigned document reports signatures")
	}
	if got := r.Signatures(); len(got) != 0 {
		t.Errorf("got %d signatures", len(got))
	}
}

func TestParsePDFDate(t *testing.T) {
	got := parsePDFDate("D:20260806120000+02'00'")
	if got.Year() != 2026 || got.Month() != 8 || got.Day() != 6 || got.Hour() != 12 {
		t.Errorf("parsed %v", got)
	}
	if !parsePDFDate("nonsense").IsZero() {
		t.Error("a malformed date should parse to the zero time")
	}
}

// TestSignEncryptedDoc covers a signature on a protected document. A
// signature's /Contents is exempt from the document's encryption, so both
// writing and reading it have to step around the cipher.
func TestSignEncryptedDoc(t *testing.T) {
	cert, key := testSigner(t, "Ada Lovelace")
	doc := New()
	p := doc.AddPage()
	p.SetFont(Helvetica, 12)
	p.Text(60, 70, "Confidential and signed")
	doc.Encrypt("", "owner", AllowAll, AES128)
	src := docBytes(t, doc)

	r, err := NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	u := Update(r)
	if err := u.Sign(SignOptions{Certificate: cert, Key: key, Name: "Ada Lovelace"}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := u.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	out, err := NewReader(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	sigs := out.Signatures()
	if len(sigs) != 1 {
		t.Fatalf("got %d signatures, want 1", len(sigs))
	}
	if !sigs[0].CoversWholeFile {
		t.Error("signature does not cover the whole file")
	}
	if sigs[0].Signer != "Ada Lovelace" {
		t.Errorf("signer = %q", sigs[0].Signer)
	}
	// The digest must match the bytes on disk, which only holds if
	// /Contents was left out of the encryption.
	verifySignature(t, buf.Bytes(), sigs[0].ByteRange, cert)
}
