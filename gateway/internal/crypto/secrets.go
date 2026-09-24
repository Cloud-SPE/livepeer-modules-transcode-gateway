package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
)

// SecretBox encrypts paid-operation recovery material. The operation identity
// is authenticated as associated data so ciphertext cannot be moved to a
// different customer's operation. Keep this key outside database backups.
type SecretBox struct{ aead cipher.AEAD }

func NewSecretBox(encodedKey string) (*SecretBox, error) {
	key, err := base64.StdEncoding.Strict().DecodeString(encodedKey)
	if err != nil || len(key) != 32 {
		return nil, errors.New("OPERATION_SECRETS_KEY must be canonical base64 of 32 bytes")
	}
	if base64.StdEncoding.EncodeToString(key) != encodedKey {
		return nil, errors.New("OPERATION_SECRETS_KEY must be canonical base64")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &SecretBox{aead: aead}, nil
}
func (b *SecretBox) Seal(identity string, plain []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plain, []byte(identity)), nil
}
func (b *SecretBox) Open(identity string, sealed []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("invalid operation ciphertext")
	}
	return b.aead.Open(nil, sealed[:n], sealed[n:], []byte(identity))
}
