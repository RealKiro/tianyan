// Package auth implements the shared-key challenge/response authentication
// used between the teacher and students (inspired by Veyon's key-based
// authentication model).
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

// Nonce returns a random hex nonce.
func Nonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Token derives the HMAC-SHA256 response for a given key and nonce.
func Token(key, nonce string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte("tianyan-auth:" + nonce))
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify reports whether token matches the expected response.
func Verify(key, nonce, token string) bool {
	return hmac.Equal([]byte(token), []byte(Token(key, nonce)))
}
