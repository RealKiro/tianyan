// Package capture captures the local screen and encodes JPEG frames.
package capture

// Frame is a compressed screen image.
type Frame struct {
	JPEG   []byte
	Width  int
	Height int
}

// Capturer captures the primary display. Screen access is unavailable on
// some architectures (see capture_stub.go); Capture then returns an error
// and the rest of the client keeps working.
type Capturer struct{}

// New returns a Capturer.
func New() *Capturer { return &Capturer{} }
