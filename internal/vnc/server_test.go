package vnc

import (
	"encoding/binary"
	"image"
	"io"
	"net"
	"testing"
)

type fakeHandler struct {
	img     *image.RGBA
	keys    []string
	buttons []string
}

func (f *fakeHandler) ScreenSize() (int, int)           { return f.img.Bounds().Dx(), f.img.Bounds().Dy() }
func (f *fakeHandler) CaptureRaw() (*image.RGBA, error) { return f.img, nil }
func (f *fakeHandler) MouseMove(x, y int)               {}
func (f *fakeHandler) MouseButton(b string, d bool)     { f.buttons = append(f.buttons, b) }
func (f *fakeHandler) MouseScroll(dy int)               {}
func (f *fakeHandler) Key(k string, d bool)             { f.keys = append(f.keys, k) }

func TestHandshakeAndUpdate(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	// pixel0 red, pixel1 green, pixel2 blue, pixel3 black
	img.Pix[0], img.Pix[1], img.Pix[2], img.Pix[3] = 255, 0, 0, 255
	img.Pix[4], img.Pix[5], img.Pix[6], img.Pix[7] = 0, 255, 0, 255
	img.Pix[8], img.Pix[9], img.Pix[10], img.Pix[11] = 0, 0, 255, 255
	img.Pix[12], img.Pix[13], img.Pix[14], img.Pix[15] = 0, 0, 0, 255

	h := &fakeHandler{img: img}
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	go ServeConn(server, h)

	ver := make([]byte, 12)
	if _, err := io.ReadFull(client, ver); err != nil {
		t.Fatal(err)
	}
	if string(ver) != "RFB 003.008\n" {
		t.Fatalf("version = %q", ver)
	}
	if _, err := client.Write([]byte("RFB 003.008\n")); err != nil {
		t.Fatal(err)
	}
	sec := make([]byte, 1)
	if _, err := io.ReadFull(client, sec); err != nil {
		t.Fatal(err)
	}
	if sec[0] != 1 {
		t.Fatalf("security = %d", sec[0])
	}
	client.Write([]byte{1}) // choose None
	client.Write([]byte{0}) // ClientInit shared flag

	init := make([]byte, 24)
	if _, err := io.ReadFull(client, init); err != nil {
		t.Fatal(err)
	}
	w := binary.BigEndian.Uint16(init[0:2])
	ht := binary.BigEndian.Uint16(init[2:4])
	if w != 2 || ht != 2 {
		t.Fatalf("size = %dx%d", w, ht)
	}
	nlen := binary.BigEndian.Uint32(init[20:24])
	name := make([]byte, nlen)
	if _, err := io.ReadFull(client, name); err != nil {
		t.Fatal(err)
	}
	if string(name) != "TianYan" {
		t.Fatalf("name = %q", name)
	}

	// SetEncodings (raw only)
	client.Write([]byte{2, 0, 0, 1, 0, 0, 0, 0})
	// FramebufferUpdateRequest: full screen
	client.Write([]byte{3, 0, 0, 0, 0, 0, 0, 2, 0, 2})

	upd := make([]byte, 16)
	if _, err := io.ReadFull(client, upd); err != nil {
		t.Fatal(err)
	}
	if upd[0] != 0 || binary.BigEndian.Uint16(upd[2:4]) != 1 {
		t.Fatalf("bad update header %v", upd[:4])
	}
	if enc := binary.BigEndian.Uint32(upd[12:16]); enc != 0 {
		t.Fatalf("encoding = %d", enc)
	}
	pix := make([]byte, 16)
	if _, err := io.ReadFull(client, pix); err != nil {
		t.Fatal(err)
	}
	// pixel0 = red -> BGRA = 0,0,255,0
	if pix[0] != 0 || pix[1] != 0 || pix[2] != 255 || pix[3] != 0 {
		t.Fatalf("pixel0 = %v", pix[:4])
	}
	// pixel2 = blue -> BGRA = 255,0,0,0
	if pix[8] != 255 || pix[9] != 0 || pix[10] != 0 || pix[11] != 0 {
		t.Fatalf("pixel2 = %v", pix[8:12])
	}
}
