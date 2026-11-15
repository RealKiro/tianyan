package protocol

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte("hello world")
	if err := WriteFrame(&buf, FrameJSON, payload); err != nil {
		t.Fatal(err)
	}
	typ, got, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if typ != FrameJSON {
		t.Fatalf("type = %#x", typ)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload = %q", got)
	}
}

func TestMessageJSON(t *testing.T) {
	var buf bytes.Buffer
	msg := NewMessage("hello", Hello{Name: "a", MAC: "00:11:22:33:44:55"})
	if err := WriteJSON(&buf, msg); err != nil {
		t.Fatal(err)
	}
	typ, raw, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if typ != FrameJSON {
		t.Fatalf("type = %#x", typ)
	}
	var got Message
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "hello" {
		t.Fatalf("type = %q", got.Type)
	}
	var h Hello
	if err := json.Unmarshal(got.Data, &h); err != nil {
		t.Fatal(err)
	}
	if h.MAC != "00:11:22:33:44:55" {
		t.Fatalf("mac = %q", h.MAC)
	}
}

func TestDemoJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, NewMessage("demo", Demo{On: true, Fullscreen: true})); err != nil {
		t.Fatal(err)
	}
	_, raw, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	var got Message
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "demo" {
		t.Fatalf("type = %q", got.Type)
	}
	var d Demo
	if err := json.Unmarshal(got.Data, &d); err != nil {
		t.Fatal(err)
	}
	if !d.On || !d.Fullscreen {
		t.Fatalf("demo = %+v", d)
	}
}

func TestImageModeBytes(t *testing.T) {
	// The first byte of a FrameImage payload selects the stream mode; the
	// demo/shot modes must not collide with the monitoring modes.
	modes := map[byte]string{
		ImageThumb: "thumb", ImageFull: "full",
		ImageDemo: "demo", ImageShot: "shot",
	}
	seen := map[byte]bool{}
	for b, name := range modes {
		if seen[b] {
			t.Fatalf("mode %s repeats byte %#x", name, b)
		}
		seen[b] = true
	}
	if modes[ImageDemo] == modes[ImageThumb] || modes[ImageDemo] == modes[ImageFull] {
		t.Fatal("demo mode collides with monitoring modes")
	}
}

func TestBroadcastResultJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, NewMessage("broadcast_result", BroadcastResult{Approved: false, Reason: "教师已拒绝广播请求"})); err != nil {
		t.Fatal(err)
	}
	_, raw, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	var got Message
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "broadcast_result" {
		t.Fatalf("type = %q", got.Type)
	}
	var r BroadcastResult
	if err := json.Unmarshal(got.Data, &r); err != nil {
		t.Fatal(err)
	}
	if r.Approved || r.Reason != "教师已拒绝广播请求" {
		t.Fatalf("result = %+v", r)
	}
}
