// Package password hashes and verifies the student install/uninstall
// password using a salted iterated SHA-256 key derivation.
package password

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
)

// DefaultIterations is the KDF iteration count.
const DefaultIterations = 120000

// Stored is the persisted password material.
type Stored struct {
	Salt       string `json:"salt"`
	Hash       string `json:"hash"`
	Iterations int    `json:"iterations"`
}

// Hash derives a stored credential for pass.
func Hash(pass string) (Stored, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return Stored{}, err
	}
	return Stored{
		Salt:       hex.EncodeToString(salt),
		Hash:       hex.EncodeToString(derive(pass, salt, DefaultIterations)),
		Iterations: DefaultIterations,
	}, nil
}

// Verify reports whether pass matches the stored credential.
func Verify(pass string, s Stored) bool {
	salt, err := hex.DecodeString(s.Salt)
	if err != nil {
		return false
	}
	want, err := hex.DecodeString(s.Hash)
	if err != nil {
		return false
	}
	iters := s.Iterations
	if iters <= 0 {
		iters = DefaultIterations
	}
	got := derive(pass, salt, iters)
	return subtle.ConstantTimeCompare(got, want) == 1
}

func derive(pass string, salt []byte, iters int) []byte {
	sum := sha256.Sum256(append(append([]byte{}, salt...), []byte(pass)...))
	out := sum[:]
	for i := 1; i < iters; i++ {
		sum = sha256.Sum256(append(append([]byte{}, out...), []byte(pass)...))
		out = sum[:]
	}
	return out
}
