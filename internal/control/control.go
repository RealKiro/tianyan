// Package control provides OS-level control operations (input injection,
// audio, black screen, power, launching) used by the student client.
package control

// Controller is the platform-specific system controller.
type Controller interface {
	ScreenSize() (int, int)
	MouseMove(x, y int) error
	MouseButton(button string, down bool) error
	MouseScroll(dy int) error
	Key(key string, down bool) error
	SetMute(on bool) error
	Muted() (bool, error)
	SetVolume(level int) error
	Volume() (int, error)
	BlackScreen(on bool, text string) error
	LockInput(on bool) error
	Open(target string) error
	CloseApp(name string) error
	CloseWindow(title string) error
	Power(action string, delaySec int, abort bool) error
	Close() error
}
