//go:build !windows

// Package service is a no-op on non-Windows platforms.
package service

import "errors"

// Name is the Windows service name.
const Name = "TianYanStudent"

// Install is not supported on this platform.
func Install(exePath string) error { return errors.New("service mode is only supported on Windows") }

// Uninstall is not supported on this platform.
func Uninstall() error { return errors.New("service mode is only supported on Windows") }
