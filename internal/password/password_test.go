package password

import "testing"

func TestHashVerify(t *testing.T) {
	s, err := Hash("secret123")
	if err != nil {
		t.Fatal(err)
	}
	if !Verify("secret123", s) {
		t.Fatal("expected correct password to verify")
	}
	if Verify("wrong", s) {
		t.Fatal("expected wrong password to fail")
	}
}
