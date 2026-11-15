// Package transfer implements chunked file transfer over the control
// connection using protocol.FrameFile frames.
package transfer

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"time"

	"tianyan/internal/protocol"
)

const chunkSize = 256 * 1024

const (
	opBegin = 0x01
	opChunk = 0x02
	opEnd   = 0x03
	opAbort = 0x04
)

// NewID returns a unique transfer id.
func NewID() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), rand.Uint32())
}

// SendFile streams a file to the peer using the provided writer callback.
func SendFile(write func(typ byte, payload []byte) error, info protocol.FileInfo, data io.Reader) error {
	raw, _ := json.Marshal(info)
	if err := write(protocol.FrameFile, frame(opBegin, info.ID, raw)); err != nil {
		return err
	}
	buf := make([]byte, chunkSize)
	for {
		n, err := data.Read(buf)
		if n > 0 {
			if werr := write(protocol.FrameFile, frame(opChunk, info.ID, buf[:n])); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	return write(protocol.FrameFile, frame(opEnd, info.ID, nil))
}

// frame builds [op][string id][0 terminator][data].
func frame(op byte, id string, data []byte) []byte {
	rawID := []byte(id)
	out := make([]byte, 0, 1+len(rawID)+1+len(data))
	out = append(out, op)
	out = append(out, rawID...)
	out = append(out, 0) // id terminator
	out = append(out, data...)
	return out
}

func parseFrame(payload []byte) (op byte, id string, data []byte, err error) {
	if len(payload) < 2 {
		return 0, "", nil, fmt.Errorf("transfer: short frame")
	}
	op = payload[0]
	rest := payload[1:]
	i := 0
	for i < len(rest) && rest[i] != 0 {
		i++
	}
	if i >= len(rest) {
		return 0, "", nil, fmt.Errorf("transfer: missing id terminator")
	}
	id = string(rest[:i])
	data = rest[i+1:]
	return op, id, data, nil
}

type incoming struct {
	info protocol.FileInfo
	f    *os.File
}

// Receiver reassembles incoming files.
type Receiver struct {
	mu    sync.Mutex
	files map[string]*incoming
}

// NewReceiver returns a Receiver.
func NewReceiver() *Receiver {
	return &Receiver{files: map[string]*incoming{}}
}

// Handle processes one FrameFile payload. dir must already exist. It returns
// the FileInfo and done=true when the file is complete.
func (r *Receiver) Handle(payload []byte, dir string) (protocol.FileInfo, bool, error) {
	op, id, data, err := parseFrame(payload)
	if err != nil {
		return protocol.FileInfo{}, false, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	switch op {
	case opBegin:
		var info protocol.FileInfo
		if err := json.Unmarshal(data, &info); err != nil {
			return info, false, err
		}
		f, err := os.CreateTemp(dir, ".tianyan_*")
		if err != nil {
			return info, false, err
		}
		r.files[id] = &incoming{info: info, f: f}
		return info, false, nil

	case opChunk:
		in, ok := r.files[id]
		if !ok {
			return protocol.FileInfo{}, false, nil
		}
		if _, err := in.f.Write(data); err != nil {
			return protocol.FileInfo{}, false, err
		}
		return in.info, false, nil

	case opEnd:
		in, ok := r.files[id]
		if !ok {
			return protocol.FileInfo{}, false, nil
		}
		tmp := in.f.Name()
		_ = in.f.Close()
		delete(r.files, id)
		dest := filepath.Join(dir, filepath.Base(in.info.Name))
		if err := os.Rename(tmp, dest); err != nil {
			return in.info, false, err
		}
		return in.info, true, nil

	case opAbort:
		if in, ok := r.files[id]; ok {
			_ = in.f.Close()
			_ = os.Remove(in.f.Name())
			delete(r.files, id)
		}
		return protocol.FileInfo{}, false, nil
	}
	return protocol.FileInfo{}, false, nil
}
