// Package rubikameow implements a bounded native Rubika personal-account client.
// Protocol observations are pinned in docs/providers/sources.md. This package
// does not import HTTP framework, SQL, or webhook implementations.
package rubikameow

import (
	"bytes"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"strconv"
	"unicode/utf8"
)

const maxPayload = 8 << 20

type object map[string]any

func (o object) str(k string) string {
	switch v := o[k].(type) {
	case string:
		return v
	case json.Number:
		return string(v)
	}
	return ""
}
func (o object) num(k string) int64 {
	n, _ := strconv.ParseInt(o.str(k), 10, 64)
	switch v := o[k].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	}
	return n
}
func asObject(v any) object {
	if o, ok := v.(object); ok {
		return o
	}
	if o, ok := v.(map[string]any); ok {
		return object(o)
	}
	return nil
}
func asObjects(v any) []object {
	values, _ := v.([]any)
	out := make([]object, 0, len(values))
	for _, value := range values {
		if o := asObject(value); o != nil {
			out = append(out, o)
		}
	}
	return out
}
func jsonObject(raw []byte) (object, error) {
	if len(raw) > maxPayload || !utf8.Valid(raw) {
		return nil, errors.New("invalid JSON payload")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	budget := 40000
	v, e := jsonValue(d, 0, &budget)
	if e != nil {
		return nil, e
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, errors.New("trailing JSON")
	}
	o := asObject(v)
	if o == nil {
		return nil, errors.New("JSON object required")
	}
	return o, nil
}
func jsonValue(d *json.Decoder, depth int, budget *int) (any, error) {
	if depth > 48 || *budget <= 0 {
		return nil, errors.New("JSON complexity limit")
	}
	*budget--
	t, e := d.Token()
	if e != nil {
		return nil, e
	}
	switch v := t.(type) {
	case json.Delim:
		switch v {
		case '{':
			o := object{}
			for d.More() {
				key, e := d.Token()
				if e != nil {
					return nil, e
				}
				name, ok := key.(string)
				if !ok {
					return nil, errors.New("invalid JSON key")
				}
				if _, ok = o[name]; ok {
					return nil, errors.New("duplicate JSON key")
				}
				item, e := jsonValue(d, depth+1, budget)
				if e != nil {
					return nil, e
				}
				o[name] = item
			}
			end, e := d.Token()
			if e != nil || end != json.Delim('}') {
				return nil, errors.New("invalid JSON object")
			}
			return o, nil
		case '[':
			items := []any{}
			for d.More() {
				if len(items) >= 10000 {
					return nil, errors.New("JSON array limit")
				}
				item, e := jsonValue(d, depth+1, budget)
				if e != nil {
					return nil, e
				}
				items = append(items, item)
			}
			end, e := d.Token()
			if e != nil || end != json.Delim(']') {
				return nil, errors.New("invalid JSON array")
			}
			return items, nil
		}
		return nil, errors.New("invalid JSON delimiter")
	}
	return t, nil
}
func transformAuth(s string) string {
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z':
			b[i] = byte((32-int(c-'a'))%26) + 'a'
		case c >= 'A' && c <= 'Z':
			b[i] = byte((29-int(c-'A'))%26) + 'A'
		case c >= '0' && c <= '9':
			b[i] = byte((13-int(c-'0'))%10) + '0'
		}
	}
	return string(b)
}
func authKey(auth string) ([]byte, error) {
	if len(auth) != 32 {
		return nil, errors.New("invalid authentication key")
	}
	for _, c := range auth {
		if c < 'a' || c > 'z' {
			return nil, errors.New("invalid authentication key")
		}
	}
	s := auth[16:24] + auth[:8] + auth[24:] + auth[8:16]
	key := []byte(s)
	for i, c := range key {
		key[i] = (c-'a'+9)%26 + 'a'
	}
	return key, nil
}
func randomAuth() (string, error) {
	out := make([]byte, 32)
	for i := range out {
		for {
			var b [1]byte
			if _, err := rand.Read(b[:]); err != nil {
				return "", err
			}
			if b[0] < 234 {
				out[i] = 'a' + b[0]%26
				break
			}
		}
	}
	return string(out), nil
}
func encrypt(data object, key []byte) (string, error) {
	raw, e := json.Marshal(data)
	if e != nil {
		return "", e
	}
	if len(raw) > maxPayload {
		return "", errors.New("payload too large")
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return "", e
	}
	pad := aes.BlockSize - len(raw)%aes.BlockSize
	raw = append(raw, bytes.Repeat([]byte{byte(pad)}, pad)...)
	cipher.NewCBCEncrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(raw, raw)
	return base64.StdEncoding.EncodeToString(raw), nil
}
func decrypt(encoded string, key []byte) (object, error) {
	bad := errors.New("invalid encrypted provider response")
	if len(encoded) > (maxPayload+aes.BlockSize)*4/3+8 {
		return nil, bad
	}
	raw, e := base64.StdEncoding.DecodeString(encoded)
	if e != nil || len(raw) == 0 || len(raw)%aes.BlockSize != 0 {
		return nil, bad
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, bad
	}
	cipher.NewCBCDecrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(raw, raw)
	pad := int(raw[len(raw)-1])
	if pad < 1 || pad > aes.BlockSize {
		return nil, bad
	}
	valid := 1
	for _, b := range raw[len(raw)-pad:] {
		valid &= subtle.ConstantTimeByteEq(b, byte(pad))
	}
	if valid != 1 {
		return nil, bad
	}
	return jsonObject(raw[:len(raw)-pad])
}
func parsePrivateKey(raw []byte) (*rsa.PrivateKey, error) {
	if len(raw) > 4096 {
		return nil, errors.New("invalid private key")
	}
	block, rest := pem.Decode(raw)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 || block.Type != "RSA PRIVATE KEY" {
		return nil, errors.New("invalid private key")
	}
	key, e := x509.ParsePKCS1PrivateKey(block.Bytes)
	if e != nil {
		return nil, e
	}
	if key.N.BitLen() != 1024 {
		return nil, errors.New("unsupported provider RSA key size")
	}
	return key, key.Validate()
}
func createKeys() (string, []byte, error) {
	key, e := rsa.GenerateKey(rand.Reader, 1024)
	if e != nil {
		return "", nil, e
	}
	public, e := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if e != nil {
		return "", nil, e
	}
	wire := transformAuth(base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: public})))
	private := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return wire, private, nil
}
func sign(key *rsa.PrivateKey, encoded string) (string, error) {
	h := sha256.Sum256([]byte(encoded))
	signature, e := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
	if e != nil {
		return "", e
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}
func decryptAuth(key *rsa.PrivateKey, encoded string) (string, error) {
	raw, e := base64.StdEncoding.DecodeString(encoded)
	if e != nil || len(raw) != key.Size() {
		return "", errors.New("invalid encrypted authentication")
	}
	plain, e := rsa.DecryptOAEP(sha1.New(), rand.Reader, key, raw, nil)
	if e != nil {
		return "", errors.New("invalid encrypted authentication")
	}
	auth := string(plain)
	if _, e = authKey(auth); e != nil {
		return "", e
	}
	return auth, nil
}
