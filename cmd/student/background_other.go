//go:build !windows

package main

import (
	"os"
	"os/exec"
)

// spawnHidden starts the student client detached (non-Windows platforms
// have no console window to hide; Unix autostart uses init scripts).
func spawnHidden(args []string) error {
	cmd := exec.Command(os.Args[0], args...)
	return cmd.Start()
}
