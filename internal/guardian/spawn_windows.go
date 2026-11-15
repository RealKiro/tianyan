//go:build windows

package guardian

import (
	"os/exec"
	"strconv"
	"syscall"
)

// Spawn launches a detached guardian process watching pid.
func Spawn(exe string, pid int) error {
	cmd := exec.Command(exe, "-guard", "-pid", strconv.Itoa(pid))
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000008} // DETACHED_PROCESS
	return cmd.Start()
}
