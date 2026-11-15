//go:build !windows

package guardian

import "os/exec"

func rebootSystem() {
	_ = exec.Command("systemctl", "reboot").Run()
}
