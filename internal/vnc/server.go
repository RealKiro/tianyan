// Package vnc implements a minimal RFB 3.8 VNC server on the student side so
// that standard VNC clients (including Veyon) can view and control the
// student screen. Inspired by Veyon's VNC-based screen sharing.
package vnc

import (
	"encoding/binary"
	"errors"
	"image"
	"io"
	"log"
	"net"
)

// Handler supplies the screen and input injection for a VNC session.
type Handler interface {
	ScreenSize() (int, int)
	CaptureRaw() (*image.RGBA, error)
	MouseMove(x, y int)
	MouseButton(button string, down bool)
	MouseScroll(dy int)
	Key(key string, down bool)
}

// ServeConn handles one VNC client connection.
func ServeConn(conn net.Conn, h Handler) {
	defer conn.Close()
	if err := handshake(conn, h); err != nil {
		return
	}
	st := &state{}
	if err := messageLoop(conn, h, st); err != nil {
		log.Printf("vnc: session ended: %v", err)
	}
}

type state struct {
	lastMask byte
}

func handshake(conn net.Conn, h Handler) error {
	if _, err := io.WriteString(conn, "RFB 003.008\n"); err != nil {
		return err
	}
	ver := make([]byte, 12)
	if _, err := io.ReadFull(conn, ver); err != nil {
		return err
	}
	// Security type: None (1).
	if _, err := conn.Write([]byte{1}); err != nil {
		return err
	}
	sec := make([]byte, 1)
	if _, err := io.ReadFull(conn, sec); err != nil {
		return err
	}
	if sec[0] != 1 {
		return errors.New("vnc: unsupported security type")
	}
	// ClientInit: shared flag (ignored).
	if _, err := io.ReadFull(conn, make([]byte, 1)); err != nil {
		return err
	}

	w, ht := h.ScreenSize()
	buf := make([]byte, 24)
	binary.BigEndian.PutUint16(buf[0:], uint16(w))
	binary.BigEndian.PutUint16(buf[2:], uint16(ht))
	buf[4] = 32                               // bits per pixel
	buf[5] = 24                               // depth
	buf[6] = 0                                // big endian flag
	buf[7] = 1                                // true colour flag
	binary.BigEndian.PutUint16(buf[8:], 255)  // red max
	binary.BigEndian.PutUint16(buf[10:], 255) // green max
	binary.BigEndian.PutUint16(buf[12:], 255) // blue max
	buf[14] = 16                              // red shift
	buf[15] = 8                               // green shift
	buf[16] = 0                               // blue shift
	// bytes 17-19 are padding
	name := "TianYan"
	binary.BigEndian.PutUint32(buf[20:], uint32(len(name)))
	buf = append(buf, []byte(name)...)
	_, err := conn.Write(buf)
	return err
}

func messageLoop(conn net.Conn, h Handler, st *state) error {
	var msg [1]byte
	for {
		if _, err := io.ReadFull(conn, msg[:]); err != nil {
			return err
		}
		switch msg[0] {
		case 0: // SetPixelFormat
			if err := discard(conn, 19); err != nil {
				return err
			}
		case 2: // SetEncodings
			if err := discard(conn, 1); err != nil {
				return err
			}
			var n uint16
			if err := binary.Read(conn, binary.BigEndian, &n); err != nil {
				return err
			}
			if err := discard(conn, int(n)*4); err != nil {
				return err
			}
		case 3: // FramebufferUpdateRequest
			var p [9]byte
			if _, err := io.ReadFull(conn, p[:]); err != nil {
				return err
			}
			x := int(binary.BigEndian.Uint16(p[1:]))
			y := int(binary.BigEndian.Uint16(p[3:]))
			w := int(binary.BigEndian.Uint16(p[5:]))
			ht := int(binary.BigEndian.Uint16(p[7:]))
			if err := sendUpdate(conn, h, x, y, w, ht); err != nil {
				return err
			}
		case 4: // KeyEvent
			var p [7]byte
			if _, err := io.ReadFull(conn, p[:]); err != nil {
				return err
			}
			down := p[0] != 0
			keysym := binary.BigEndian.Uint32(p[3:])
			if key, ok := keysymToKey(keysym); ok {
				h.Key(key, down)
			}
		case 5: // PointerEvent
			var p [5]byte
			if _, err := io.ReadFull(conn, p[:]); err != nil {
				return err
			}
			mask := p[0]
			x := int(binary.BigEndian.Uint16(p[1:]))
			y := int(binary.BigEndian.Uint16(p[3:]))
			h.MouseMove(x, y)
			applyButtons(h, st, mask)
		case 6: // ClientCutText
			if err := discard(conn, 3); err != nil {
				return err
			}
			var n uint32
			if err := binary.Read(conn, binary.BigEndian, &n); err != nil {
				return err
			}
			if err := discard(conn, int(n)); err != nil {
				return err
			}
		default:
			return errors.New("vnc: unknown message type")
		}
	}
}

func applyButtons(h Handler, st *state, mask byte) {
	for _, b := range []struct {
		bit  byte
		name string
	}{{1, "left"}, {2, "middle"}, {4, "right"}} {
		was := st.lastMask & b.bit
		is := mask & b.bit
		if was != is {
			h.MouseButton(b.name, is != 0)
		}
	}
	st.lastMask = mask
}

func sendUpdate(conn net.Conn, h Handler, x, y, w, ht int) error {
	img, err := h.CaptureRaw()
	if err != nil {
		return err
	}
	sw := img.Bounds().Dx()
	sh := img.Bounds().Dy()
	if w <= 0 || ht <= 0 {
		x, y, w, ht = 0, 0, sw, sh
	}
	if x+w > sw {
		w = sw - x
	}
	if y+ht > sh {
		ht = sh - y
	}
	if w <= 0 || ht <= 0 {
		x, y, w, ht = 0, 0, sw, sh
	}

	hdr := make([]byte, 16)
	hdr[0] = 0 // FramebufferUpdate
	hdr[1] = 0
	binary.BigEndian.PutUint16(hdr[2:], 1) // one rectangle
	binary.BigEndian.PutUint16(hdr[4:], uint16(x))
	binary.BigEndian.PutUint16(hdr[6:], uint16(y))
	binary.BigEndian.PutUint16(hdr[8:], uint16(w))
	binary.BigEndian.PutUint16(hdr[10:], uint16(ht))
	binary.BigEndian.PutUint32(hdr[12:], 0) // RAW encoding
	if _, err := conn.Write(hdr); err != nil {
		return err
	}

	rgba := img
	row := make([]byte, w*4)
	for j := 0; j < ht; j++ {
		off := ((y+j)*sw + x) * 4
		src := rgba.Pix[off : off+w*4]
		for i := 0; i < w; i++ {
			row[i*4+0] = src[i*4+2] // B
			row[i*4+1] = src[i*4+1] // G
			row[i*4+2] = src[i*4+0] // R
			row[i*4+3] = 0
		}
		if _, err := conn.Write(row); err != nil {
			return err
		}
	}
	return nil
}

func discard(conn net.Conn, n int) error {
	buf := make([]byte, 4096)
	for n > 0 {
		k := n
		if k > len(buf) {
			k = len(buf)
		}
		m, err := conn.Read(buf[:k])
		if err != nil {
			return err
		}
		n -= m
	}
	return nil
}
