//go:build linux

package control

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// linuxController is a best-effort Linux implementation. Input injection uses
// xdotool and audio uses pactl; both must be installed on the student host.
type linuxController struct{}

// New returns the Linux controller.
func New() (Controller, error) { return &linuxController{}, nil }

func (c *linuxController) ScreenSize() (int, int) { return 0, 0 }

func (c *linuxController) MouseMove(x, y int) error {
	return exec.Command("xdotool", "mousemove", strconv.Itoa(x), strconv.Itoa(y)).Run()
}

func (c *linuxController) MouseButton(button string, down bool) error {
	btn := "1"
	switch button {
	case "left":
		btn = "1"
	case "middle":
		btn = "2"
	case "right":
		btn = "3"
	default:
		return fmt.Errorf("control: unknown mouse button %q", button)
	}
	action := "mouseup"
	if down {
		action = "mousedown"
	}
	return exec.Command("xdotool", action, btn).Run()
}

func (c *linuxController) MouseScroll(dy int) error {
	btn := "4"
	if dy < 0 {
		btn = "5"
	}
	return exec.Command("xdotool", "click", btn).Run()
}

func (c *linuxController) Key(key string, down bool) error {
	action := "keyup"
	if down {
		action = "keydown"
	}
	return exec.Command("xdotool", action, strings.ToLower(key)).Run()
}

func (c *linuxController) SetMute(on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return exec.Command("pactl", "set-sink-mute", "@DEFAULT_SINK@", v).Run()
}

func (c *linuxController) Muted() (bool, error) {
	return false, fmt.Errorf("control: mute query not implemented on Linux")
}

func (c *linuxController) SetVolume(level int) error {
	return exec.Command("pactl", "set-sink-volume", "@DEFAULT_SINK@", fmt.Sprintf("%d%%", level)).Run()
}

func (c *linuxController) Volume() (int, error) {
	return 0, fmt.Errorf("control: volume query not implemented on Linux")
}

func (c *linuxController) LockScreen(on bool, text, image, color string) error {
	state := "off"
	if on {
		state = "on"
	}
	return exec.Command("xset", "dpms", "force", state).Run()
}

func (c *linuxController) BlackScreen(on bool, text string) error {
	return c.LockScreen(on, text, "", "")
}

func (c *linuxController) LockInput(on bool) error { return nil }

func (c *linuxController) Open(target string) error {
	return exec.Command("xdg-open", target).Start()
}

func (c *linuxController) CloseApp(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("control: empty process name")
	}
	return exec.Command("pkill", "-x", name).Run()
}

func (c *linuxController) CloseWindow(title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("control: empty window title")
	}
	return exec.Command("xdotool", "search", "--name", title, "windowclose").Run()
}

func (c *linuxController) Power(action string, delaySec int, abort bool) error {
	if abort {
		return exec.Command("shutdown", "-c").Run()
	}
	switch action {
	case "shutdown":
		return exec.Command("systemctl", "poweroff").Run()
	case "reboot":
		return exec.Command("systemctl", "reboot").Run()
	case "logout":
		return exec.Command("loginctl", "terminate-session", "$XDG_SESSION_ID").Run()
	default:
		return fmt.Errorf("control: unknown power action %q", action)
	}
}

func (c *linuxController) Close() error { return nil }
