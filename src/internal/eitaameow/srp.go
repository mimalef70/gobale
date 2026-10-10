package eitaameow

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"errors"
	"math/big"

	"golang.org/x/crypto/pbkdf2"
)

// srpProof implements the explicitly named TL KDF using the SRP-6a equations
// documented at https://core.telegram.org/api/srp. Eitaa interoperability is a
// separate live acceptance gate; unknown algorithms never downgrade to plaintext.
func srpProof(password string, response object) (object, error) {
	bad := errors.New("invalid SRP parameters")
	a := asObject(response["current_algo"])
	if a.str("_") != "passwordKdfAlgoSHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow" {
		return nil, bad
	}
	pBytes, ok := a["p"].([]byte)
	if !ok || len(pBytes) != 256 {
		return nil, bad
	}
	p := new(big.Int).SetBytes(pBytes)
	g := a.num("g")
	if g < 2 || g > 7 || p.BitLen() != 2048 {
		return nil, bad
	}
	mod := func(n int64) int64 { return new(big.Int).Mod(p, big.NewInt(n)).Int64() }
	validG := false
	switch g {
	case 2:
		validG = mod(8) == 7
	case 3:
		validG = mod(3) == 2
	case 4:
		validG = true
	case 5:
		validG = mod(5) == 1 || mod(5) == 4
	case 6:
		validG = mod(24) == 19 || mod(24) == 23
	case 7:
		validG = mod(7) == 3 || mod(7) == 5 || mod(7) == 6
	}
	if !validG {
		return nil, bad
	}
	q := new(big.Int).Sub(p, big.NewInt(1))
	q.Rsh(q, 1)
	if !p.ProbablyPrime(32) || !q.ProbablyPrime(32) {
		return nil, bad
	}
	salt1, ok1 := a["salt1"].([]byte)
	salt2, ok2 := a["salt2"].([]byte)
	if !ok1 || !ok2 || len(salt1) == 0 || len(salt2) == 0 || len(salt1) > 64 || len(salt2) > 64 {
		return nil, bad
	}
	bBytes, ok := response["srp_B"].([]byte)
	if !ok || len(bBytes) > 256 {
		return nil, bad
	}
	B := new(big.Int).SetBytes(bBytes)
	if B.Sign() <= 0 || B.Cmp(p) >= 0 {
		return nil, bad
	}
	pad := func(n *big.Int) []byte { out := make([]byte, 256); return n.FillBytes(out) }
	hash := func(parts ...[]byte) []byte {
		h := sha256.New()
		for _, v := range parts {
			h.Write(v)
		}
		return h.Sum(nil)
	}
	inner := hash(salt1, []byte(password), salt1)
	ph1 := hash(salt2, inner, salt2)
	derived := pbkdf2.Key(ph1, salt1, 100000, 64, sha512.New)
	x := new(big.Int).SetBytes(hash(salt2, derived, salt2))
	G := big.NewInt(g)
	gBytes := pad(G)
	k := new(big.Int).SetBytes(hash(pBytes, gBytes))
	v := new(big.Int).Exp(G, x, p)
	kv := new(big.Int).Mul(k, v)
	kv.Mod(kv, p)
	base := new(big.Int).Sub(B, kv)
	base.Mod(base, p)
	minimum := new(big.Int).Lsh(big.NewInt(1), 1984)
	remaining := new(big.Int).Sub(p, base)
	if base.Cmp(minimum) < 0 || remaining.Cmp(minimum) < 0 {
		return nil, bad
	}
	secret := make([]byte, 256)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	private := new(big.Int).SetBytes(secret)
	A := new(big.Int).Exp(G, private, p)
	if A.Cmp(minimum) < 0 || new(big.Int).Sub(p, A).Cmp(minimum) < 0 {
		return nil, bad
	}
	aBytes := pad(A)
	bBytes = pad(B)
	u := new(big.Int).SetBytes(hash(aBytes, bBytes))
	if u.Sign() == 0 {
		return nil, bad
	}
	exponent := new(big.Int).Mul(u, x)
	exponent.Add(exponent, private)
	shared := new(big.Int).Exp(base, exponent, p)
	key := hash(pad(shared))
	hp, hg := hash(pBytes), hash(gBytes)
	for i := range hp {
		hp[i] ^= hg[i]
	}
	m1 := hash(hp, hash(salt1), hash(salt2), aBytes, bBytes, key)
	return object{"_": "inputCheckPasswordSRP", "srp_id": response.num("srp_id"), "A": aBytes, "M1": m1}, nil
}
