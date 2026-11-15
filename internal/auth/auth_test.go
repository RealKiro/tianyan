package auth

import "testing"

func TestTokenVerify(t *testing.T) {
	if !Verify("secret", "nonce-1", Token("secret", "nonce-1")) {
		t.Fatal("expected token to verify")
	}
	if Verify("secret", "nonce-1", Token("other", "nonce-1")) {
		t.Fatal("expected wrong-key token to fail")
	}
	if Verify("secret", "nonce-1", Token("secret", "nonce-2")) {
		t.Fatal("expected wrong-nonce token to fail")
	}
}
