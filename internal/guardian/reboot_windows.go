//go:build windows

package guardian

import "os/exec"

func rebootSystem() {
	_ = exec.Command("shutdown", "/r", "/t", "10").Run()
}
