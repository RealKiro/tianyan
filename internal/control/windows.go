//go:build windows

package control

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/go-ole/go-ole"
	"github.com/moutend/go-wca/pkg/wca"
	"golang.org/x/sys/windows"
)

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	procKeybdEvent       = user32.NewProc("keybd_event")
	procMouseEvent       = user32.NewProc("mouse_event")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
)

const (
	keyEventfKeyUp       = 0x0002
	keyEventfExtendedKey = 0x0001

	mouseEventfMove       = 0x0001
	mouseEventfLeftDown   = 0x0002
	mouseEventfLeftUp     = 0x0004
	mouseEventfRightDown  = 0x0008
	mouseEventfRightUp    = 0x0010
	mouseEventfMiddleDown = 0x0020
	mouseEventfMiddleUp   = 0x0040
	mouseEventfWheel      = 0x0800
	mouseEventfAbsolute   = 0x8000
)

type vkInfo struct {
	vk  uint16
	ext bool
}

// keyVK maps a browser KeyboardEvent.code name to a Windows virtual key.
func keyVK(name string) (vkInfo, bool) {
	switch name {
	case "Enter":
		return vkInfo{0x0D, false}, true
	case "Escape":
		return vkInfo{0x1B, false}, true
	case "Backspace":
		return vkInfo{0x08, false}, true
	case "Tab":
		return vkInfo{0x09, false}, true
	case "Space":
		return vkInfo{0x20, false}, true
	case "CapsLock":
		return vkInfo{0x14, false}, true
	case "ArrowLeft":
		return vkInfo{0x25, true}, true
	case "ArrowUp":
		return vkInfo{0x26, true}, true
	case "ArrowRight":
		return vkInfo{0x27, true}, true
	case "ArrowDown":
		return vkInfo{0x28, true}, true
	case "Insert":
		return vkInfo{0x2D, true}, true
	case "Delete":
		return vkInfo{0x2E, true}, true
	case "Home":
		return vkInfo{0x24, true}, true
	case "End":
		return vkInfo{0x23, true}, true
	case "PageUp":
		return vkInfo{0x21, true}, true
	case "PageDown":
		return vkInfo{0x22, true}, true
	case "NumLock":
		return vkInfo{0x90, true}, true
	case "ScrollLock":
		return vkInfo{0x91, true}, true
	case "ShiftLeft", "ShiftRight":
		return vkInfo{0x10, false}, true
	case "ControlLeft", "ControlRight":
		return vkInfo{0x11, false}, true
	case "AltLeft", "AltRight":
		return vkInfo{0x12, false}, true
	case "MetaLeft", "MetaRight":
		return vkInfo{0x5B, true}, true
	case "Minus":
		return vkInfo{0xBD, false}, true
	case "Equal":
		return vkInfo{0xBB, false}, true
	case "BracketLeft":
		return vkInfo{0xDB, false}, true
	case "BracketRight":
		return vkInfo{0xDD, false}, true
	case "Backslash":
		return vkInfo{0xDC, false}, true
	case "Semicolon":
		return vkInfo{0xBA, false}, true
	case "Quote":
		return vkInfo{0xDE, false}, true
	case "Comma":
		return vkInfo{0xBC, false}, true
	case "Period":
		return vkInfo{0xBE, false}, true
	case "Slash":
		return vkInfo{0xBF, false}, true
	case "Backquote":
		return vkInfo{0xC0, false}, true
	case "PrintScreen":
		return vkInfo{0x2C, true}, true
	case "Pause":
		return vkInfo{0x13, false}, true
	}
	if len(name) == 4 && name[0] == 'K' && name[1] == 'e' && name[2] == 'y' && name[3] >= 'A' && name[3] <= 'Z' {
		return vkInfo{uint16(name[3]), false}, true
	}
	if len(name) == 6 && name[:5] == "Digit" && name[5] >= '0' && name[5] <= '9' {
		return vkInfo{uint16(name[5]), false}, true
	}
	if len(name) == 7 && name[:6] == "Numpad" && name[6] >= '0' && name[6] <= '9' {
		return vkInfo{uint16(name[6]) + 0x60, false}, true
	}
	if len(name) >= 2 && name[0] == 'F' {
		if n, err := strconv.Atoi(name[1:]); err == nil && n >= 1 && n <= 12 {
			return vkInfo{uint16(0x70 + n - 1), false}, true
		}
	}
	return vkInfo{}, false
}

func keybd(vk uint16, flags uint32) {
	procKeybdEvent.Call(uintptr(vk), 0, uintptr(flags), 0)
}

func mouse(flags uint32, dx, dy int, data uint32) {
	procMouseEvent.Call(uintptr(flags), uintptr(dx), uintptr(dy), uintptr(data), 0)
}

type windowsAudio struct {
	aev *wca.IAudioEndpointVolume
}

func newWindowsAudio() (*windowsAudio, error) {
	_ = ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED)
	var de *wca.IMMDeviceEnumerator
	if err := wca.CoCreateInstance(wca.CLSID_MMDeviceEnumerator, 0, wca.CLSCTX_ALL, wca.IID_IMMDeviceEnumerator, &de); err != nil {
		return nil, err
	}
	defer de.Release()
	var mmd *wca.IMMDevice
	if err := de.GetDefaultAudioEndpoint(wca.ERender, wca.EConsole, &mmd); err != nil {
		return nil, err
	}
	defer mmd.Release()
	var aev *wca.IAudioEndpointVolume
	if err := mmd.Activate(wca.IID_IAudioEndpointVolume, wca.CLSCTX_ALL, nil, &aev); err != nil {
		return nil, err
	}
	return &windowsAudio{aev: aev}, nil
}

func (a *windowsAudio) SetMute(on bool) error  { return a.aev.SetMute(on, nil) }
func (a *windowsAudio) GetMute() (bool, error) { var m bool; err := a.aev.GetMute(&m); return m, err }
func (a *windowsAudio) SetVolume(level int) error {
	if level < 0 {
		level = 0
	}
	if level > 100 {
		level = 100
	}
	return a.aev.SetMasterVolumeLevelScalar(float32(level)/100, nil)
}
func (a *windowsAudio) GetVolume() (int, error) {
	var v float32
	if err := a.aev.GetMasterVolumeLevelScalar(&v); err != nil {
		return 0, err
	}
	return int(v*100 + 0.5), nil
}
func (a *windowsAudio) Close() {
	if a != nil && a.aev != nil {
		a.aev.Release()
		a.aev = nil
	}
}

const overlayScript = `
param([string]$Text = '', [string]$Image = '', [string]$Color = '')
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
$form = New-Object System.Windows.Forms.Form
$form.Text = 'TianYan'
$form.FormBorderStyle = 'None'
$form.WindowState = 'Maximized'
$form.TopMost = $true
$form.StartPosition = 'CenterScreen'
$form.BackColor = [System.Drawing.Color]::Black
$form.ForeColor = [System.Drawing.Color]::White
$form.ControlBox = $false
$form.ShowInTaskbar = $false
$form.KeyPreview = $true
if ($Image -ne '' -and (Test-Path $Image)) {
  $form.BackgroundImage = [System.Drawing.Image]::FromFile($Image)
  $form.BackgroundImageLayout = 'Stretch'
}
$label = New-Object System.Windows.Forms.Label
$label.Text = $Text
if ($Color -ne '') {
  $label.ForeColor = [System.Drawing.Color]::FromName($Color)
} else {
  $label.ForeColor = [System.Drawing.Color]::White
}
$label.BackColor = [System.Drawing.Color]::Transparent
$label.Font = New-Object System.Drawing.Font('Microsoft YaHei', 30, [System.Drawing.FontStyle]::Bold)
$label.Dock = 'Fill'
$label.TextAlign = 'MiddleCenter'
$form.Controls.Add($label)
$form.Add_KeyDown({ $_.SuppressKeyPress = $true })
$form.Add_KeyPress({ $_.Handled = $true })
[System.Windows.Forms.Application]::Run($form)
`

type winController struct {
	audio    *windowsAudio
	audioErr error
	scrW     int
	scrH     int

	mu        sync.Mutex
	overlay   *exec.Cmd
	lockOn    bool
	lockText  string
	lockImage string
	lockColor string
}

// New returns the Windows controller.
func New() (Controller, error) {
	w, h := getScreenSize()
	c := &winController{scrW: w, scrH: h}
	if a, err := newWindowsAudio(); err == nil {
		c.audio = a
	} else {
		c.audioErr = err
	}
	return c, nil
}

func getScreenSize() (int, int) {
	w, _, _ := procGetSystemMetrics.Call(0) // SM_CXSCREEN
	h, _, _ := procGetSystemMetrics.Call(1) // SM_CYSCREEN
	return int(w), int(h)
}

func (c *winController) ScreenSize() (int, int) { return c.scrW, c.scrH }

func (c *winController) MouseMove(x, y int) error {
	if c.scrW <= 0 || c.scrH <= 0 {
		return errors.New("control: screen size unknown")
	}
	dx := x * 65535 / c.scrW
	dy := y * 65535 / c.scrH
	mouse(mouseEventfMove|mouseEventfAbsolute, dx, dy, 0)
	return nil
}

func (c *winController) MouseButton(button string, down bool) error {
	var up, dn uint32
	switch button {
	case "left":
		up, dn = mouseEventfLeftUp, mouseEventfLeftDown
	case "right":
		up, dn = mouseEventfRightUp, mouseEventfRightDown
	case "middle":
		up, dn = mouseEventfMiddleUp, mouseEventfMiddleDown
	default:
		return fmt.Errorf("control: unknown mouse button %q", button)
	}
	f := up
	if down {
		f = dn
	}
	mouse(f, 0, 0, 0)
	return nil
}

func (c *winController) MouseScroll(dy int) error {
	mouse(mouseEventfWheel, 0, 0, uint32(dy*120))
	return nil
}

func (c *winController) Key(key string, down bool) error {
	info, ok := keyVK(key)
	if !ok {
		return fmt.Errorf("control: unsupported key %q", key)
	}
	flags := uint32(0)
	if info.ext {
		flags |= keyEventfExtendedKey
	}
	if !down {
		flags |= keyEventfKeyUp
	}
	keybd(info.vk, flags)
	return nil
}

func (c *winController) SetMute(on bool) error {
	if c.audio == nil {
		return c.audioErr
	}
	return c.audio.SetMute(on)
}

func (c *winController) Muted() (bool, error) {
	if c.audio == nil {
		return false, c.audioErr
	}
	return c.audio.GetMute()
}

func (c *winController) SetVolume(level int) error {
	if c.audio == nil {
		return c.audioErr
	}
	return c.audio.SetVolume(level)
}

func (c *winController) Volume() (int, error) {
	if c.audio == nil {
		return 0, c.audioErr
	}
	return c.audio.GetVolume()
}

// LockScreen shows/hides the merged lock overlay: a full-screen topmost
// window with a custom message and/or wallpaper image that swallows
// keyboard input and covers the desktop (input lock). Each "on" command
// carries the full intent: an empty text falls back to the default message
// (white), an empty image means a plain black background, and an empty
// color means the default white message text.
func (c *winController) LockScreen(on bool, text, image, color string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lockOn = on
	if on {
		c.lockText = text
		c.lockImage = image
		c.lockColor = color
	}
	return c.syncOverlayLocked()
}

// BlackScreen is the legacy plain-black overlay (kept for compatibility).
func (c *winController) BlackScreen(on bool, text string) error {
	return c.LockScreen(on, text, "", "")
}

// LockInput is the legacy input-only lock (kept for compatibility).
func (c *winController) LockInput(on bool) error {
	return c.LockScreen(on, "输入已锁定，请保持安静", "", "")
}

func (c *winController) syncOverlayLocked() error {
	if c.lockOn {
		text := c.lockText
		if text == "" {
			text = "请保持安静"
		}
		return c.ensureOverlay(text, c.lockImage, c.lockColor)
	}
	c.stopOverlay()
	return nil
}

func (c *winController) ensureOverlay(text, image, color string) error {
	c.stopOverlay()
	f, err := os.CreateTemp("", "tianyan_overlay_*.ps1")
	if err != nil {
		return err
	}
	if _, err := f.WriteString(overlayScript); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	f.Close()
	scriptPath := f.Name()
	args := []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-File", scriptPath, "-Text", text}
	if image != "" {
		args = append(args, "-Image", image)
	}
	if color != "" {
		args = append(args, "-Color", color)
	}
	cmd := exec.Command("powershell", args...)
	if err := cmd.Start(); err != nil {
		os.Remove(scriptPath)
		return err
	}
	c.overlay = cmd
	go func() {
		_ = cmd.Wait()
		os.Remove(scriptPath)
	}()
	return nil
}

func (c *winController) stopOverlay() {
	if c.overlay != nil && c.overlay.Process != nil {
		_ = c.overlay.Process.Kill()
	}
	c.overlay = nil
}

func (c *winController) Open(target string) error {
	return exec.Command("cmd", "/C", "start", "", target).Start()
}

func (c *winController) CloseApp(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("control: empty process name")
	}
	if !strings.Contains(name, ".") {
		name += ".exe"
	}
	return exec.Command("taskkill", "/F", "/IM", name).Run()
}

const closeWindowScript = `
param([string]$Title)
Get-Process | Where-Object { $_.MainWindowTitle -like "*$Title*" } | ForEach-Object { $_.CloseMainWindow() | Out-Null }
`

func (c *winController) CloseWindow(title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return errors.New("control: empty window title")
	}
	f, err := os.CreateTemp("", "tianyan_close_*.ps1")
	if err != nil {
		return err
	}
	if _, err := f.WriteString(closeWindowScript); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	f.Close()
	path := f.Name()
	defer os.Remove(path)
	return exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", path, "-Title", title).Run()
}

func (c *winController) Power(action string, delaySec int, abort bool) error {
	if abort {
		return exec.Command("shutdown", "/a").Start()
	}
	var args []string
	switch action {
	case "shutdown":
		args = []string{"/s", "/t", strconv.Itoa(delaySec)}
	case "reboot":
		args = []string{"/r", "/t", strconv.Itoa(delaySec)}
	case "logout":
		args = []string{"/l"}
	default:
		return fmt.Errorf("control: unknown power action %q", action)
	}
	return exec.Command("shutdown", args...).Start()
}

func (c *winController) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopOverlay()
	if c.audio != nil {
		c.audio.Close()
	}
	return nil
}
