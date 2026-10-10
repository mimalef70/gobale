package eitaameow

import (
	"crypto/sha256"
	"errors"
	"unicode/utf8"
)

// The reviewed web login chooses this hash only for account.password2. It is
// SHA256(salt || UTF-8 password || salt), with the same phone/code challenge.
// SRP remains a separate explicit constructor; unknown variants fail closed.
func passwordCheck(password string, state object, challenge authChallenge) (string, object, error) {
	bad := errors.New("invalid password challenge")
	if password == "" || len(password) > 1024 || !utf8.ValidString(password) {
		return "", nil, bad
	}
	switch state.str("_") {
	case "account.password2":
		salt, ok := state["current_salt"].([]byte)
		if !ok || len(salt) == 0 || len(salt) > 256 || challenge.Phone == "" || challenge.Code == "" {
			return "", nil, bad
		}
		h := sha256.New()
		h.Write(salt)
		h.Write([]byte(password))
		h.Write(salt)
		return "auth.checkPassword2", object{"password_hash": h.Sum(nil), "phone_number": challenge.Phone, "phone_code": challenge.Code}, nil
	case "account.password":
		proof, err := srpProof(password, state)
		if err != nil {
			return "", nil, err
		}
		return "auth.checkPassword", object{"password": proof}, nil
	default:
		return "", nil, bad
	}
}
