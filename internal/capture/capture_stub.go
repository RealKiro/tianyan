//go:build linux && (loong64 || mips64le)

package capture

import "image"

// ScreenSize is unavailable on architectures where the screen-capture
// dependency (gen2brain/shm) has no port yet (UOS LoongArch / old MIPS).
func (c *Capturer) ScreenSize() (int, int) { return 0, 0 }

// Capture is unavailable on these architectures; the student client keeps
// working without screen monitoring (chat, files, lock, policies).
func (c *Capturer) Capture(maxWidth int, quality int) (*Frame, error) {
	return nil, image.ErrFormat
}

// CaptureRaw is unavailable on these architectures.
func (c *Capturer) CaptureRaw() (*image.RGBA, error) {
	return nil, image.ErrFormat
}
