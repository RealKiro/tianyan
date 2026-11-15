// Package block enforces application and website blacklist/whitelist
// policies on the student machine.
package block

import (
	"strings"
	"sync"
	"time"
)

// Policy is the application/website blocking policy.
type Policy struct {
	Mode          string   `json:"mode"` // black | white | off
	Apps          []string `json:"apps"`
	Sites         []string `json:"sites"`
	BlockUSBGames bool     `json:"block_usb_games"` // detect games run from removable drives
}

// Enforcer applies the policy. Application enforcement is polled on Windows
// (appblock_windows.go); other platforms are currently no-ops. Website
// enforcement uses the system hosts file (requires admin/root).
type Enforcer struct {
	mu     sync.Mutex
	policy Policy

	// OnViolation reports a blocked item (kind: "app" | "usb_game") to the
	// student client so it can notify the teacher.
	OnViolation  func(kind, target string)
	lastUSBCheck time.Time
}

// New returns an Enforcer.
func New() *Enforcer { return &Enforcer{} }

// SetPolicy stores the policy and applies website rules immediately.
func (e *Enforcer) SetPolicy(p Policy) error {
	p.Apps = normalize(p.Apps)
	p.Sites = normalize(p.Sites)
	e.mu.Lock()
	e.policy = p
	e.mu.Unlock()

	if p.Mode == "black" {
		return applyHosts(p.Sites)
	}
	// Whitelist mode is not yet implemented for websites; clear any block.
	return applyHosts(nil)
}

// Policy returns the current policy.
func (e *Enforcer) Policy() Policy {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.policy
}

// AppLoop periodically terminates blacklisted applications until stop closes.
func (e *Enforcer) AppLoop(stop <-chan struct{}, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			e.enforceAppsOnce()
		}
	}
}

func normalize(items []string) []string {
	out := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, it := range items {
		it = strings.ToLower(strings.TrimSpace(it))
		if it == "" || seen[it] {
			continue
		}
		seen[it] = true
		out = append(out, it)
	}
	return out
}
