//go:build !windows

package guardian

import (
	"os/exec"
	"strconv"
)

// Spawn launches a guardian process watching pid.
func Spawn(exe string, pid int) error {
	return exec.Command(exe, "-guard", "-pid", strconv.Itoa(pid)).Start()
}
