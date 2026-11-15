//go:build windows || darwin || (linux && !loong64 && !mips64le)

package capture

import (
	"bytes"
	"image"
	"image/jpeg"

	"github.com/kbinani/screenshot"
	xdraw "golang.org/x/image/draw"
)

// ScreenSize returns the primary display dimensions.
func (c *Capturer) ScreenSize() (int, int) {
	b := screenshot.GetDisplayBounds(0)
	return b.Dx(), b.Dy()
}

// Capture grabs the primary display and returns a JPEG frame, optionally
// downscaled so its width does not exceed maxWidth (0 = native resolution).
func (c *Capturer) Capture(maxWidth int, quality int) (*Frame, error) {
	if quality <= 0 || quality > 100 {
		quality = 60
	}
	img, err := screenshot.CaptureDisplay(0)
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	if maxWidth > 0 && w > maxWidth {
		nw := maxWidth
		nh := h * maxWidth / w
		if nh < 1 {
			nh = 1
		}
		dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Over, nil)
		img = dst
		w, h = nw, nh
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return &Frame{JPEG: buf.Bytes(), Width: w, Height: h}, nil
}

// CaptureRaw returns the raw RGBA primary display without scaling.
func (c *Capturer) CaptureRaw() (*image.RGBA, error) {
	return screenshot.CaptureDisplay(0)
}
