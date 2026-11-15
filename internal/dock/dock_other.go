//go:build !windows

package dock

import (
	"fmt"
	"runtime"
)

// Bar is a docked toolbar (placeholder on non-Windows).
type Bar struct{}

// Supported reports whether the platform can show docked toolbars.
func Supported() bool { return false }

// NewBar is a placeholder on non-Windows platforms.
func NewBar(opts Options) (*Bar, error) {
	return nil, fmt.Errorf("dock: docked toolbar not implemented on %s", runtime.GOOS)
}

// SetLabel is a no-op placeholder.
func (b *Bar) SetLabel(text string) {}

// Hide is a no-op placeholder.
func (b *Bar) Hide() {}

// SetEnabled is a no-op placeholder.
func (b *Bar) SetEnabled(id int, enabled bool) {}

// Show is a no-op placeholder.
func (b *Bar) Show() {}

// Close is a no-op placeholder.
func (b *Bar) Close() {}

// MessageBox is a placeholder on non-Windows platforms.
func MessageBox(title, text string) bool { return false }
