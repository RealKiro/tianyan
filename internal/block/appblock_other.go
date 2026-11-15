//go:build !windows

package block

// enforceAppsOnce is a no-op on non-Windows platforms for now.
func (e *Enforcer) enforceAppsOnce() {}
