//go:build windows

package guardian

import (
	"os/exec"
	"strconv"
	"syscall"
)

// Spawn launches a detached guardian process watching pid and returns
// its PID for mutual-watchdog monitoring.
func Spawn(exe string, pid int) (int, error) {
	cmd := exec.Command(exe, "-guard", "-pid", strconv.Itoa(pid))
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000008} // DETACHED_PROCESS
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}
