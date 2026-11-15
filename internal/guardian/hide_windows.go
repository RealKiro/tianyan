//go:build windows

package guardian

import (
	"os/exec"
	"syscall"
)

// hideConsole keeps respawned student processes from flashing a console
// window in the user's desktop session.
func hideConsole(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= 0x08000000 // CREATE_NO_WINDOW
}
