//go:build windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// spawnHidden starts the student client with a hidden console window so the
// scheduled-task autostart does not flash a console on the user's desktop.
func spawnHidden(args []string) error {
	cmd := exec.Command(os.Args[0], args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	return cmd.Start()
}
