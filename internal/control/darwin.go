//go:build darwin

package control

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// darwinController is a best-effort macOS implementation. Input injection is
// intentionally deferred (it requires CGEvent via cgo and Accessibility
// permission); audio, launch and power use osascript / open.
type darwinController struct{}

// New returns the macOS controller.
func New() (Controller, error) { return &darwinController{}, nil }

func (c *darwinController) ScreenSize() (int, int) { return 0, 0 }

func (c *darwinController) MouseMove(x, y int) error {
	return fmt.Errorf("control: input injection not yet implemented on macOS")
}

func (c *darwinController) MouseButton(button string, down bool) error {
	return fmt.Errorf("control: input injection not yet implemented on macOS")
}

func (c *darwinController) MouseScroll(dy int) error {
	return fmt.Errorf("control: input injection not yet implemented on macOS")
}

func (c *darwinController) Key(key string, down bool) error {
	return fmt.Errorf("control: input injection not yet implemented on macOS")
}

func (c *darwinController) SetMute(on bool) error {
	v := "false"
	if on {
		v = "true"
	}
	return exec.Command("osascript", "-e", fmt.Sprintf("set volume output muted %s", v)).Run()
}

func (c *darwinController) Muted() (bool, error) {
	out, err := exec.Command("osascript", "-e", "output muted of (get volume settings)").Output()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

func (c *darwinController) SetVolume(level int) error {
	return exec.Command("osascript", "-e", fmt.Sprintf("set volume output volume %d", level)).Run()
}

func (c *darwinController) Volume() (int, error) {
	out, err := exec.Command("osascript", "-e", "output volume of (get volume settings)").Output()
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

func (c *darwinController) LockScreen(on bool, text, image, color string) error {
	return fmt.Errorf("control: lock screen not yet implemented on macOS")
}

func (c *darwinController) BlackScreen(on bool, text string) error {
	return c.LockScreen(on, text, "", "")
}

func (c *darwinController) LockInput(on bool) error { return nil }

func (c *darwinController) Open(target string) error {
	return exec.Command("open", target).Start()
}

func (c *darwinController) CloseApp(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("control: empty process name")
	}
	return exec.Command("killall", name).Run()
}

func (c *darwinController) CloseWindow(title string) error {
	return fmt.Errorf("control: close window not yet implemented on macOS")
}

func (c *darwinController) Power(action string, delaySec int, abort bool) error {
	var verb string
	switch action {
	case "shutdown":
		verb = "shut down"
	case "reboot":
		verb = "restart"
	case "logout":
		verb = "log out"
	default:
		return fmt.Errorf("control: unknown power action %q", action)
	}
	return exec.Command("osascript", "-e", fmt.Sprintf("tell application \"System Events\" to %s", verb)).Run()
}

func (c *darwinController) Close() error { return nil }
