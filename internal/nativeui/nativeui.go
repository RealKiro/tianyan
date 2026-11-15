// Package nativeui provides small native dialogs so the student client
// needs no browser at all: a text input box (send a message to the
// teacher), a file picker (submit homework) and a status window (opened
// from the tray icon after the admin password is verified). Every dialog
// runs its own Win32 message pump on a locked OS thread.
package nativeui
