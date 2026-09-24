package livepeer

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"golang.org/x/crypto/sha3"
	"strings"
)

// CallerSigner holds a persistent delegated invocation key. It cannot mint
// tickets or spend outside the exact LOC-signed authorization scope.
type CallerSigner struct{ key *secp256k1.PrivateKey }

func NewCallerSigner(encoded string) (*CallerSigner, error) {
	raw, err := hex.DecodeString(strings.TrimPrefix(encoded, "0x"))
	if err != nil || len(raw) != 32 {
		return nil, fmt.Errorf("caller key must be 32-byte hexadecimal")
	}
	var scalar secp256k1.ModNScalar
	if scalar.SetByteSlice(raw) || scalar.IsZero() {
		return nil, fmt.Errorf("caller key outside secp256k1 scalar range")
	}
	return &CallerSigner{key: secp256k1.PrivKeyFromBytes(raw)}, nil
}
func (s *CallerSigner) PublicKey() string {
	return hex.EncodeToString(s.key.PubKey().SerializeCompressed())
}
func (s *CallerSigner) Proof(authorization string) (string, error) {
	raw, err := base64.StdEncoding.Strict().DecodeString(authorization)
	if err != nil || len(raw) == 0 {
		return "", fmt.Errorf("invalid spend authorization encoding")
	}
	digest := keccak(append([]byte("livepeer-invocation-proof/v1\x00"), raw...))
	digest = keccak(append([]byte("\x19Ethereum Signed Message:\n32"), digest...))
	signature := ecdsa.SignCompact(s.key, digest, false)
	wire := append(append([]byte{}, signature[1:]...), signature[0])
	return base64.StdEncoding.EncodeToString(wire), nil
}
func keccak(b []byte) []byte           { h := sha3.NewLegacyKeccak256(); _, _ = h.Write(b); return h.Sum(nil) }
func RequestDigest(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }
