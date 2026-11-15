// Package display renders a stream of JPEG frames in a top-level window.
// Student machines use it to show a screen broadcast (demo) coming from
// the teacher over the control channel.
//
// A session is created per broadcast: fullscreen sessions cover the whole
// screen and swallow keyboard input (blocking Alt+Tab/Alt+F4/Win key via a
// low-level keyboard hook, mirroring Veyon's LockWidget behavior);
// window sessions show a normal, minimizable window and do not lock input.
package display
