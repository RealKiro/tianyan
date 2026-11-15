//go:build !windows

package display

import (
	"fmt"
	"runtime"
)

// Session is a broadcast display window (placeholder on non-Windows).
type Session struct {
	// OnClosed mirrors the Windows field so callers compile everywhere;
	// it is never invoked on this platform.
	OnClosed func()
}

// Supported reports whether the platform can show broadcast windows.
func Supported() bool { return false }

// NewSession is a placeholder on non-Windows platforms.
func NewSession(title string, fullscreen bool) (*Session, error) {
	return nil, fmt.Errorf("display: broadcast window not implemented on %s", runtime.GOOS)
}

// Update is a no-op placeholder.
func (s *Session) Update(jpeg []byte) {}

// Close is a no-op placeholder.
func (s *Session) Close() {}
