//go:build !windows

package nativeui

// Supported reports whether native dialogs are available.
func Supported() bool { return false }

// InputBox is a placeholder on non-Windows platforms.
func InputBox(title, label string, cb func(text string)) {}

// PickFile is a placeholder on non-Windows platforms.
func PickFile(title string, cb func(path string)) {}

// StatusBox is a placeholder on non-Windows platforms.
func StatusBox(title, text string) {}
