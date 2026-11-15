//go:build !windows

package tray

import (
	"fmt"
	"runtime"
)

// Icon is a tray icon handle (placeholder on non-Windows).
type Icon struct{}

// Supported reports whether the platform can show tray icons.
func Supported() bool { return false }

// New is a placeholder on non-Windows platforms.
func New(opts Options) (*Icon, error) {
	return nil, fmt.Errorf("tray: tray icon not implemented on %s", runtime.GOOS)
}

// SetGray is a no-op placeholder.
func (i *Icon) SetGray(on bool) {}

// Close is a no-op placeholder.
func (i *Icon) Close() {}
