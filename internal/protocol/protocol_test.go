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
