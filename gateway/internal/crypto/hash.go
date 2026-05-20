package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
)

// HashWithPepper returns the lowercase hex-encoded SHA-256 of value+pepper.
func HashWithPepper(value, pepper string) string {
	h := sha256.Sum256([]byte(value + pepper))
	return hex.EncodeToString(h[:])
}

// ConstantTimeEqual reports whether a and b are equal in constant time.
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// RandomToken returns a base64url-encoded random token of the requested
// byte length (default 32).
func RandomToken(numBytes int) (string, error) {
	if numBytes <= 0 {
		numBytes = 32
	}
	buf := make([]byte, numBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// NewAPIKey returns ("sk-<32-bytes-b64url>", prefix, hash).
//
// `prefix` is the first 12 chars of the visible key — enough to show
// in dashboards without leaking entropy.
// `hash` is SHA-256(key + pepper).
func NewAPIKey(pepper string) (key, prefix, hash string, err error) {
	tok, err := RandomToken(32)
	if err != nil {
		return "", "", "", err
	}
	key = "sk-" + tok
	if len(key) >= 12 {
		prefix = key[:12]
	} else {
		prefix = key
	}
	hash = HashWithPepper(key, pepper)
	return key, prefix, hash, nil
}
