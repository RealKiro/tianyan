package block

import "testing"

func TestExemptToggle(t *testing.T) {
	e := New()
	if e.Exempt() {
		t.Fatal("fresh enforcer must not be exempt")
	}
	// The hosts file update may fail without admin rights; the state flip is
	// what matters here and happens before any error return.
	_ = e.SetExempt(true)
	if !e.Exempt() {
		t.Fatal("SetExempt(true) did not enable exemption")
	}
	_ = e.SetExempt(false)
	if e.Exempt() {
		t.Fatal("SetExempt(false) did not disable exemption")
	}
}

func TestExemptKeepsPolicy(t *testing.T) {
	e := New()
	p := Policy{Mode: "black", Apps: []string{"Game.exe"}, Sites: []string{"games.com"}, BlockUSBGames: true}
	_ = e.SetPolicy(p)
	_ = e.SetExempt(true)
	got := e.Policy()
	if got.Mode != "black" || len(got.Apps) != 1 || !got.BlockUSBGames {
		t.Fatalf("policy lost while exempt: %+v", got)
	}
}
