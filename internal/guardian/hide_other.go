//go:build !windows

package guardian

import "os/exec"

// hideConsole is a no-op on platforms without a console window concept.
func hideConsole(cmd *exec.Cmd) {}
