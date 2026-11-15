// Package tray shows a resident notification-area (system tray) icon for
// the student client, in the spirit of 冰点还原/Veyon: the icon is purely a
// status indicator — students cannot interact with it. The only way in is
// to hold Shift and double-click the icon, then enter the admin password;
// anything else (single click, right click, plain double click) is
// silently ignored.
//
// The icon turns gray while the client is disconnected from the teacher,
// mirroring the graying-out of the docked toolbar buttons.
package tray

// Options configures the tray icon.
type Options struct {
	Tooltip string
	// Verify checks the admin password typed into the unlock dialog.
	// Implementations should report their own failure feedback (e.g. a
	// MessageBox) and return false.
	Verify func(password string) bool
	// OnUnlock runs after a successful password entry (open the admin UI).
	OnUnlock func()
}
