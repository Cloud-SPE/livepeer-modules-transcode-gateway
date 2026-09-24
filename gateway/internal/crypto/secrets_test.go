package crypto

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestOperationSecretsAuthenticatedAndBoundToIdentity(t *testing.T) {
	box, err := NewSecretBox(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("scoped-broker-credential")
	a, err := box.Seal("operation-a", plain)
	if err != nil {
		t.Fatal(err)
	}
	b, err := box.Seal("operation-a", plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) || bytes.Contains(a, plain) {
		t.Fatal("ciphertext must use a fresh nonce and conceal plaintext")
	}
	got, err := box.Open("operation-a", a)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("roundtrip: %q %v", got, err)
	}
	if _, err = box.Open("operation-b", a); err == nil {
		t.Fatal("cross-operation ciphertext accepted")
	}
	a[len(a)-1] ^= 1
	if _, err = box.Open("operation-a", a); err == nil {
		t.Fatal("tampering accepted")
	}
	if _, err = box.Open("operation-a", []byte("short")); err == nil {
		t.Fatal("truncated ciphertext accepted")
	}
	wrong, _ := NewSecretBox(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)))
	if _, err = wrong.Open("operation-a", b); err == nil {
		t.Fatal("wrong key accepted")
	}
}
