package transfer

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"tianyan/internal/protocol"
)

func TestSendReceive(t *testing.T) {
	content := []byte("homework data: 你好，作业")
	info := protocol.FileInfo{ID: "test-1", Name: "作业.txt", Size: int64(len(content))}

	var buf bytes.Buffer
	write := func(typ byte, payload []byte) error {
		return protocol.WriteFrame(&buf, typ, payload)
	}
	if err := SendFile(write, info, bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	recv := NewReceiver()
	var done bool
	r := bytes.NewReader(buf.Bytes())
	for {
		typ, payload, err := protocol.ReadFrame(r)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if typ != protocol.FrameFile {
			t.Fatalf("typ = %#x", typ)
		}
		if _, done, err = recv.Handle(payload, dir); err != nil {
			t.Fatal(err)
		}
	}
	if !done {
		t.Fatal("receiver did not finish")
	}
	data, err := os.ReadFile(filepath.Join(dir, "作业.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, content) {
		t.Fatalf("content mismatch: %q", data)
	}
}
