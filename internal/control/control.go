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
	// LockScreen shows/hides the full-screen lock overlay (the merged
	// black-screen + input-lock tool): custom message text and/or a
	// wallpaper image (local path; empty = plain black background) and a
	// message color name (empty = white).
	LockScreen(on bool, text, image, color string) error
	BlackScreen(on bool, text string) error
	LockInput(on bool) error
	Open(target string) error
	CloseApp(name string) error
	CloseWindow(title string) error
	Power(action string, delaySec int, abort bool) error
	Close() error
}
