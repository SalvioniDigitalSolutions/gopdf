package gopdf

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rc4"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
)

// ErrPasswordRequired is returned when a file is encrypted and the
// supplied password (the empty string, for Open and NewReader) does not
// open it. Retry with OpenPassword or NewReaderPassword.
var ErrPasswordRequired = errors.New("gopdf: encrypted PDF requires a password")

// Permissions is a set of operations an encrypted document allows. The
// PDF specification treats these as advisory: conforming viewers honor
// them, but they are not a security boundary — anyone able to open the
// document can extract its contents.
type Permissions uint32

// Permission bits, numbered as in the PDF specification's /P entry.
const (
	AllowPrint        Permissions = 1 << 2  // print at reduced resolution
	AllowModify       Permissions = 1 << 3  // change the document
	AllowCopy         Permissions = 1 << 4  // copy text and graphics
	AllowAnnotate     Permissions = 1 << 5  // add or modify annotations
	AllowFillForms    Permissions = 1 << 8  // fill in form fields
	AllowAccessible   Permissions = 1 << 9  // extract for accessibility
	AllowAssemble     Permissions = 1 << 10 // insert, rotate, delete pages
	AllowHighResPrint Permissions = 1 << 11 // print at full resolution

	// AllowAll grants every permission.
	AllowAll = AllowPrint | AllowModify | AllowCopy | AllowAnnotate |
		AllowFillForms | AllowAccessible | AllowAssemble | AllowHighResPrint
	// AllowNone grants nothing beyond opening the document.
	AllowNone Permissions = 0
)

// permissionBits converts a permission set to the /P integer, setting the
// reserved bits the specification requires.
func permissionBits(p Permissions) int32 {
	v := ^uint32(0)           // bits 13-32 and all others start set
	v &^= 0x3                 // bits 1-2 are reserved and must be 0
	v &^= uint32(AllowAll)    // clear every grantable bit...
	v |= uint32(p & AllowAll) // ...then set the granted ones
	return int32(v)
}

// cryptMethod is the algorithm a crypt filter applies.
type cryptMethod int

const (
	cryptNone cryptMethod = iota // /Identity: no encryption
	cryptRC4
	cryptAESV2 // AES-128-CBC
	cryptAESV3 // AES-256-CBC
)

// stdCrypt implements the PDF standard security handler.
type stdCrypt struct {
	key     []byte // file encryption key
	r       int    // revision
	stmF    cryptMethod
	strF    cryptMethod
	encDict Dict // the /Encrypt dictionary (written verbatim, never encrypted)
	id      []byte
}

// padBytes is the standard 32-byte password padding string.
var padBytes = []byte{
	0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56,
	0xFF, 0xFA, 0x01, 0x08, 0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80,
	0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
}

func padPassword(pw []byte) []byte {
	out := make([]byte, 32)
	n := copy(out, pw)
	copy(out[n:], padBytes)
	return out
}

// --- reading ---

// newStdCrypt builds a handler from a file's /Encrypt dictionary and
// authenticates the password (user first, then owner).
func (r *Reader) newStdCrypt(enc Dict, password string) (*stdCrypt, error) {
	if filter, _ := r.resolve(enc["Filter"]).(Name); filter != "Standard" {
		return nil, fmt.Errorf("gopdf: unsupported security handler /%s", filter)
	}
	v, _ := toInt(r.resolve(enc["V"]))
	rev, _ := toInt(r.resolve(enc["R"]))
	length, ok := toInt(r.resolve(enc["Length"]))
	if !ok || length <= 0 {
		length = 40
	}
	o, _ := r.resolve(enc["O"]).(String)
	u, _ := r.resolve(enc["U"]).(String)
	pVal, _ := toInt(r.resolve(enc["P"]))
	encryptMetadata := true
	if b, ok := r.resolve(enc["EncryptMetadata"]).(bool); ok {
		encryptMetadata = b
	}

	c := &stdCrypt{r: rev, encDict: enc}
	if idArr, ok := r.resolve(r.trailer["ID"]).(Array); ok && len(idArr) > 0 {
		if s, ok := r.resolve(idArr[0]).(String); ok {
			c.id = []byte(s)
		}
	}

	// V4 and V5 select algorithms through named crypt filters.
	switch v {
	case 1:
		c.stmF, c.strF = cryptRC4, cryptRC4
		length = 40
	case 2:
		c.stmF, c.strF = cryptRC4, cryptRC4
	case 4, 5:
		cf, _ := r.resolve(enc["CF"]).(Dict)
		lookup := func(key Name) (cryptMethod, int) {
			name, _ := r.resolve(enc[key]).(Name)
			if name == "" || name == "Identity" {
				return cryptNone, length
			}
			f, _ := r.resolve(cf[name]).(Dict)
			bits := length
			if l, ok := toInt(r.resolve(f["Length"])); ok && l > 0 {
				// /Length here may be bytes or bits.
				if l <= 32 {
					bits = l * 8
				} else {
					bits = l
				}
			}
			switch m, _ := r.resolve(f["CFM"]).(Name); m {
			case "V2":
				return cryptRC4, bits
			case "AESV2":
				return cryptAESV2, 128
			case "AESV3":
				return cryptAESV3, 256
			case "None":
				return cryptNone, bits
			default:
				return cryptNone, bits
			}
		}
		c.stmF, length = lookup("StmF")
		c.strF, _ = lookup("StrF")
	default:
		return nil, fmt.Errorf("gopdf: unsupported encryption version V=%d", v)
	}

	pw := []byte(password)
	if rev >= 5 {
		key, err := authenticateV5(pw, []byte(o), []byte(u),
			bytesOf(r.resolve(enc["OE"])), bytesOf(r.resolve(enc["UE"])), rev)
		if err != nil {
			return nil, err
		}
		c.key = key
		return c, nil
	}

	n := length / 8
	if n < 5 {
		n = 5
	}
	if n > 16 {
		n = 16
	}
	if len(o) < 32 || len(u) < 16 {
		return nil, errors.New("gopdf: malformed /Encrypt dictionary")
	}

	// Try the password as the user password, then as the owner password
	// (which recovers the user password first).
	key := legacyFileKey(pw, []byte(o), int32(pVal), c.id, rev, n, encryptMetadata)
	if legacyAuthUser(key, []byte(u), c.id, rev) {
		c.key = key
		return c, nil
	}
	if userPw, ok := legacyRecoverUserPassword(pw, []byte(o), rev, n); ok {
		key = legacyFileKey(userPw, []byte(o), int32(pVal), c.id, rev, n, encryptMetadata)
		if legacyAuthUser(key, []byte(u), c.id, rev) {
			c.key = key
			return c, nil
		}
	}
	return nil, ErrPasswordRequired
}

func bytesOf(v any) []byte {
	s, _ := v.(String)
	return []byte(s)
}

// legacyFileKey implements Algorithm 2: the RC4/AES-128 file key.
func legacyFileKey(pw, o []byte, p int32, id []byte, rev, n int, encryptMetadata bool) []byte {
	h := md5.New()
	h.Write(padPassword(pw))
	h.Write(o[:32])
	var pb [4]byte
	binary.LittleEndian.PutUint32(pb[:], uint32(p))
	h.Write(pb[:])
	h.Write(id)
	if rev >= 4 && !encryptMetadata {
		h.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF})
	}
	key := h.Sum(nil)
	if rev >= 3 {
		for i := 0; i < 50; i++ {
			sum := md5.Sum(key[:n])
			key = sum[:]
		}
	}
	return key[:n]
}

// legacyAuthUser implements Algorithms 4 and 6: verify the /U entry.
func legacyAuthUser(key, u, id []byte, rev int) bool {
	if rev == 2 {
		want, err := rc4Apply(key, padBytes)
		return err == nil && len(u) >= 32 && bytes.Equal(want, u[:32])
	}
	h := md5.New()
	h.Write(padBytes)
	h.Write(id)
	data := h.Sum(nil)
	data, err := rc4Apply(key, data)
	if err != nil {
		return false
	}
	tmp := make([]byte, len(key))
	for i := 1; i <= 19; i++ {
		for j := range key {
			tmp[j] = key[j] ^ byte(i)
		}
		if data, err = rc4Apply(tmp, data); err != nil {
			return false
		}
	}
	return len(u) >= 16 && bytes.Equal(data[:16], u[:16])
}

// legacyRecoverUserPassword implements Algorithm 7: derive the user
// password from the owner password and the /O entry.
func legacyRecoverUserPassword(ownerPw, o []byte, rev, n int) ([]byte, bool) {
	sum := md5.Sum(padPassword(ownerPw))
	key := sum[:]
	if rev >= 3 {
		for i := 0; i < 50; i++ {
			s := md5.Sum(key)
			key = s[:]
		}
	}
	key = key[:n]
	data := append([]byte(nil), o[:32]...)
	if rev == 2 {
		out, err := rc4Apply(key, data)
		return out, err == nil
	}
	tmp := make([]byte, len(key))
	for i := 19; i >= 0; i-- {
		for j := range key {
			tmp[j] = key[j] ^ byte(i)
		}
		out, err := rc4Apply(tmp, data)
		if err != nil {
			return nil, false
		}
		data = out
	}
	return data, true
}

// authenticateV5 implements the AES-256 handler (revisions 5 and 6).
func authenticateV5(pw, o, u, oe, ue []byte, rev int) ([]byte, error) {
	if len(u) < 48 {
		return nil, errors.New("gopdf: malformed /U entry")
	}
	uValidation, uKeySalt := u[32:40], u[40:48]
	if bytes.Equal(hash2B(pw, uValidation, nil, rev), u[:32]) {
		ikey := hash2B(pw, uKeySalt, nil, rev)
		return aesNoPadDecrypt(ikey, make([]byte, 16), ue)
	}
	if len(o) >= 48 && len(oe) > 0 {
		oValidation, oKeySalt := o[32:40], o[40:48]
		if bytes.Equal(hash2B(pw, oValidation, u[:48], rev), o[:32]) {
			ikey := hash2B(pw, oKeySalt, u[:48], rev)
			return aesNoPadDecrypt(ikey, make([]byte, 16), oe)
		}
	}
	return nil, ErrPasswordRequired
}

// hash2B is the password hash for revision 6 (Algorithm 2.B); revision 5
// uses a single SHA-256.
func hash2B(pw, salt, udata []byte, rev int) []byte {
	h := sha256.New()
	h.Write(pw)
	h.Write(salt)
	h.Write(udata)
	k := h.Sum(nil)
	if rev < 6 {
		return k
	}
	var e []byte
	for round := 0; ; round++ {
		var k1 []byte
		for i := 0; i < 64; i++ {
			k1 = append(k1, pw...)
			k1 = append(k1, k...)
			k1 = append(k1, udata...)
		}
		block, err := aes.NewCipher(k[:16])
		if err != nil {
			return k
		}
		e = make([]byte, len(k1)-len(k1)%16)
		cipher.NewCBCEncrypter(block, k[16:32]).CryptBlocks(e, k1[:len(e)])
		sum := 0
		for _, b := range e[:16] {
			sum += int(b)
		}
		var hh hash.Hash
		switch sum % 3 {
		case 0:
			hh = sha256.New()
		case 1:
			hh = sha512.New384()
		default:
			hh = sha512.New()
		}
		hh.Write(e)
		k = hh.Sum(nil)
		if round >= 63 && int(e[len(e)-1]) <= round-31 {
			break
		}
	}
	return k[:32]
}

// --- per-object keys and transforms ---

// objectKey derives the key for one object; AES-256 uses the file key
// directly.
func (c *stdCrypt) objectKey(num, gen int, method cryptMethod) []byte {
	if method == cryptAESV3 || c.r >= 5 {
		return c.key
	}
	h := md5.New()
	h.Write(c.key)
	h.Write([]byte{byte(num), byte(num >> 8), byte(num >> 16), byte(gen), byte(gen >> 8)})
	if method == cryptAESV2 {
		h.Write([]byte{0x73, 0x41, 0x6C, 0x54}) // "sAlT"
	}
	key := h.Sum(nil)
	n := len(c.key) + 5
	if n > 16 {
		n = 16
	}
	return key[:n]
}

func (c *stdCrypt) decrypt(num, gen int, data []byte, method cryptMethod) ([]byte, error) {
	switch method {
	case cryptNone:
		return data, nil
	case cryptRC4:
		return rc4Apply(c.objectKey(num, gen, method), data)
	default:
		return aesCBCDecrypt(c.objectKey(num, gen, method), data)
	}
}

func (c *stdCrypt) encrypt(num, gen int, data []byte, method cryptMethod) ([]byte, error) {
	switch method {
	case cryptNone:
		return data, nil
	case cryptRC4:
		return rc4Apply(c.objectKey(num, gen, method), data)
	default:
		return aesCBCEncrypt(c.objectKey(num, gen, method), data)
	}
}

func rc4Apply(key, data []byte) ([]byte, error) {
	ci, err := rc4.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(data))
	ci.XORKeyStream(out, data)
	return out, nil
}

// aesCBCDecrypt decrypts data whose first 16 bytes are the IV, removing
// PKCS#7 padding.
func aesCBCDecrypt(key, data []byte) ([]byte, error) {
	if len(data) < aes.BlockSize {
		return nil, nil // empty or truncated payload decrypts to nothing
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	iv, body := data[:aes.BlockSize], data[aes.BlockSize:]
	body = body[:len(body)-len(body)%aes.BlockSize]
	if len(body) == 0 {
		return nil, nil
	}
	out := make([]byte, len(body))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, body)
	if pad := int(out[len(out)-1]); pad >= 1 && pad <= aes.BlockSize && pad <= len(out) {
		out = out[:len(out)-pad]
	}
	return out, nil
}

func aesCBCEncrypt(key, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	pad := aes.BlockSize - len(data)%aes.BlockSize
	padded := append(append([]byte(nil), data...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, aes.BlockSize+len(padded))
	if _, err := rand.Read(out[:aes.BlockSize]); err != nil {
		return nil, err
	}
	cipher.NewCBCEncrypter(block, out[:aes.BlockSize]).CryptBlocks(out[aes.BlockSize:], padded)
	return out, nil
}

// aesNoPadDecrypt decrypts with an explicit IV and no padding, as used for
// the /UE and /OE key-unwrapping entries.
func aesNoPadDecrypt(key, iv, data []byte) ([]byte, error) {
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("gopdf: malformed key-unwrapping entry")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, data)
	return out, nil
}

func aesNoPadEncrypt(key, iv, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(data))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, data)
	return out, nil
}

// --- writing ---

// EncryptionMethod selects the algorithm used to protect a document.
type EncryptionMethod int

const (
	// AES128 is AES-128 with revision 4, readable by essentially every
	// PDF viewer in use.
	AES128 EncryptionMethod = iota
	// AES256 is AES-256 with revision 6 (PDF 2.0), the strongest option;
	// it requires a reasonably modern viewer.
	AES256
)

// Encrypt turns on encryption for the document. The user password is
// required to open the file (an empty user password means anyone can open
// it, but the permissions still apply); the owner password grants full
// access. Passing an empty owner password reuses the user password.
//
// Encrypted output is not byte-deterministic: each save uses fresh random
// salts and initialization vectors.
//
// Permissions are advisory — conforming viewers honor them, but they do
// not protect against a determined reader who can open the document.
func (d *Document) Encrypt(userPassword, ownerPassword string, perms Permissions, method EncryptionMethod) {
	d.encryptSetup = &encryptSetup{
		user:   userPassword,
		owner:  ownerPassword,
		perms:  perms,
		method: method,
	}
}

// encryptSetup holds the requested encryption parameters until save time,
// when the document ID is known.
type encryptSetup struct {
	user, owner string
	perms       Permissions
	method      EncryptionMethod
}

// build produces the handler and the /Encrypt dictionary for a document
// with the given first ID element.
func (e *encryptSetup) build(id []byte) (*stdCrypt, error) {
	owner := e.owner
	if owner == "" {
		owner = e.user
	}
	p := permissionBits(e.perms)
	if e.method == AES256 {
		return buildAES256(e.user, owner, p)
	}
	return buildAES128(e.user, owner, p, id)
}

func buildAES128(userPw, ownerPw string, p int32, id []byte) (*stdCrypt, error) {
	const n = 16 // 128-bit key
	// Algorithm 3: the /O entry.
	sum := md5.Sum(padPassword([]byte(ownerPw)))
	okey := sum[:]
	for i := 0; i < 50; i++ {
		s := md5.Sum(okey)
		okey = s[:]
	}
	okey = okey[:n]
	o, err := rc4Apply(okey, padPassword([]byte(userPw)))
	if err != nil {
		return nil, err
	}
	tmp := make([]byte, n)
	for i := 1; i <= 19; i++ {
		for j := 0; j < n; j++ {
			tmp[j] = okey[j] ^ byte(i)
		}
		if o, err = rc4Apply(tmp, o); err != nil {
			return nil, err
		}
	}

	key := legacyFileKey([]byte(userPw), o, p, id, 4, n, true)

	// Algorithm 5: the /U entry.
	h := md5.New()
	h.Write(padBytes)
	h.Write(id)
	u, err := rc4Apply(key, h.Sum(nil))
	if err != nil {
		return nil, err
	}
	for i := 1; i <= 19; i++ {
		for j := 0; j < n; j++ {
			tmp[j] = key[j] ^ byte(i)
		}
		if u, err = rc4Apply(tmp, u); err != nil {
			return nil, err
		}
	}
	u = append(u, make([]byte, 16)...) // pad to the required 32 bytes

	return &stdCrypt{
		key: key, r: 4, stmF: cryptAESV2, strF: cryptAESV2, id: id,
		encDict: Dict{
			"Filter": Name("Standard"), "V": int64(4), "R": int64(4),
			"Length": int64(128), "P": int64(p),
			"O": String(o), "U": String(u),
			"EncryptMetadata": true,
			"StmF":            Name("StdCF"), "StrF": Name("StdCF"),
			"CF": Dict{"StdCF": Dict{
				"CFM": Name("AESV2"), "AuthEvent": Name("DocOpen"),
				"Length": int64(16),
			}},
		},
	}, nil
}

func buildAES256(userPw, ownerPw string, p int32) (*stdCrypt, error) {
	key := make([]byte, 32)
	salts := make([]byte, 32) // user validation+key, owner validation+key
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if _, err := rand.Read(salts); err != nil {
		return nil, err
	}
	uVal, uKey := salts[0:8], salts[8:16]
	oVal, oKey := salts[16:24], salts[24:32]

	u := append(append(hash2B([]byte(userPw), uVal, nil, 6), uVal...), uKey...)
	ue, err := aesNoPadEncrypt(hash2B([]byte(userPw), uKey, nil, 6), make([]byte, 16), key)
	if err != nil {
		return nil, err
	}
	o := append(append(hash2B([]byte(ownerPw), oVal, u, 6), oVal...), oKey...)
	oe, err := aesNoPadEncrypt(hash2B([]byte(ownerPw), oKey, u, 6), make([]byte, 16), key)
	if err != nil {
		return nil, err
	}

	// The /Perms entry is the permission bits encrypted with the file key
	// in ECB mode, letting a viewer detect tampering with /P.
	permsBlock := make([]byte, 16)
	binary.LittleEndian.PutUint32(permsBlock[0:], uint32(p))
	copy(permsBlock[4:], []byte{0xFF, 0xFF, 0xFF, 0xFF, 'T', 'a', 'd', 'b'})
	if _, err := rand.Read(permsBlock[12:]); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	perms := make([]byte, 16)
	block.Encrypt(perms, permsBlock)

	return &stdCrypt{
		key: key, r: 6, stmF: cryptAESV3, strF: cryptAESV3,
		encDict: Dict{
			"Filter": Name("Standard"), "V": int64(5), "R": int64(6),
			"Length": int64(256), "P": int64(p),
			"O": String(o), "U": String(u),
			"OE": String(oe), "UE": String(ue), "Perms": String(perms),
			"EncryptMetadata": true,
			"StmF":            Name("StdCF"), "StrF": Name("StdCF"),
			"CF": Dict{"StdCF": Dict{
				"CFM": Name("AESV3"), "AuthEvent": Name("DocOpen"),
				"Length": int64(32),
			}},
		},
	}, nil
}
