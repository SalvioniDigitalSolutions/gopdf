package gopdf

import (
	"bytes"
	"fmt"
	"testing"
)

// A signature value's binary blob is left alone, but its signer name,
// reason, location and contact are strings like any other.
func TestPseudonymize_SignatureStrings(t *testing.T) {
	const name = "Marialuisa Vanetti"
	raw := authored(t, "Firmato da "+name+".")
	raw = appendUpdate(t, raw, " /AcroForm 500 0 R", " /Annots [501 0 R]", func(n int) map[int]string {
		return map[int]string{
			500: "<< /Fields [501 0 R] /SigFlags 3 >>",
			501: fmt.Sprintf("<< /Type /Annot /Subtype /Widget /FT /Sig /T (Signature1) /Rect [56 100 256 140] /F 4 /V 502 0 R >>"),
			502: fmt.Sprintf("<< /Type /Sig /Filter /Adobe.PPKLite /SubFilter /adbe.pkcs7.detached /Name (%s) /Reason (Approvato da %s) /Location (Lugano) /ContactInfo (%s@example.com) /ByteRange [0 100 200 300] /Contents <00> >>", name, name, "marialuisa.vanetti"),
		}
	})
	out := pseudonymized(t, raw, name)
	if bytes.Contains(out, []byte(name)) {
		t.Fatal("signer name survives in the signature dictionary")
	}
	if !bytes.Contains(out, []byte("/ByteRange")) {
		t.Fatal("signature dictionary lost")
	}
}

// An image's own XMP (camera, GPS, captions) is dropped with the
// thumbnails and alternates.
func TestPseudonymize_DropsImageMetadata(t *testing.T) {
	const name = "Marialuisa Vanetti"
	raw := authored(t, "Foto di "+name+".")
	xmp := "<x:xmpmeta><rdf:Description dc:creator=\"" + name + "\"/></x:xmpmeta>"
	raw = appendUpdate(t, raw, "", " /Resources << /Font << /F1 4 0 R >> /XObject << /Im1 600 0 R >> >>", func(n int) map[int]string {
		return map[int]string{
			600: fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8 /Metadata 601 0 R /Length 1 >>\nstream\n\x80\nendstream"),
			601: fmt.Sprintf("<< /Type /Metadata /Subtype /XML /Length %d >>\nstream\n%s\nendstream", len(xmp), xmp),
		}
	})
	out := pseudonymized(t, raw, name)
	if bytes.Contains(out, []byte("xmpmeta")) || bytes.Contains(out, []byte(name)) {
		t.Fatal("image metadata survived")
	}
}
