// Package protocol defines the wire protocol between the teacher (server)
// and student (client) processes.
package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
)

// Frame types carried over the TCP control channel.
const (
	FrameJSON  byte = 0x01 // JSON control message
	FrameImage byte = 0x02 // JPEG screen frame (student -> teacher)
	FrameFile  byte = 0x03 // file transfer chunk (both directions)
)

// Image stream modes (first byte of a FrameImage payload).
const (
	ImageThumb byte = 0x01
	ImageFull  byte = 0x02
	ImageDemo  byte = 0x03 // broadcast frame (teacher -> student)
	ImageShot  byte = 0x04 // screenshot reply (student -> teacher)
)

// MaxFrameSize bounds a single frame to avoid unbounded allocations.
const MaxFrameSize = 128 << 20 // 128 MiB

// Message is the envelope for JSON control messages.
type Message struct {
	ID   string          `json:"id,omitempty"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// NewMessage builds a Message from a typed payload.
func NewMessage(typ string, data any) Message {
	raw, _ := json.Marshal(data)
	return Message{Type: typ, Data: raw}
}

// WriteFrame writes a length-prefixed frame.
func WriteFrame(w io.Writer, typ byte, payload []byte) error {
	hdr := [5]byte{typ}
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// ReadFrame reads one frame, returning its type and payload.
func ReadFrame(r io.Reader) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > MaxFrameSize {
		return 0, nil, errors.New("protocol: frame too large")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, nil, err
	}
	return hdr[0], buf, nil
}

// WriteJSON marshals and writes a JSON control message.
func WriteJSON(w io.Writer, msg Message) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return WriteFrame(w, FrameJSON, raw)
}

// ---- Payloads ----

// Hello is sent by a student immediately after connecting.
type Hello struct {
	Name      string `json:"name"`
	Hostname  string `json:"hostname"`
	IP        string `json:"ip"`
	MAC       string `json:"mac"`
	OS        string `json:"os"`
	User      string `json:"user,omitempty"` // logged-in user name
	StudentID string `json:"student_id,omitempty"`
	Group     string `json:"group,omitempty"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Version   string `json:"version,omitempty"`
}

// Announce is the UDP broadcast sent periodically by students.
type Announce struct {
	Name      string `json:"name"`
	Hostname  string `json:"hostname"`
	IP        string `json:"ip"`
	MAC       string `json:"mac"`
	OS        string `json:"os"`
	User      string `json:"user,omitempty"` // logged-in user name
	StudentID string `json:"student_id,omitempty"`
	Group     string `json:"group,omitempty"`
}

// TeacherPresence is the UDP broadcast sent periodically by the teacher so
// students can discover it without manual configuration.
type TeacherPresence struct {
	Name string `json:"name"`
	IP   string `json:"ip"`
	Port int    `json:"port"`
}

// View controls the student's capture stream.
type View struct {
	Mode    string `json:"mode"` // thumb | full | off
	FPS     int    `json:"fps"`
	Width   int    `json:"width"`   // 0 = native resolution
	Quality int    `json:"quality"` // JPEG quality 1..100
}

// Input is a remote-control event injected on the student machine.
type Input struct {
	Kind string `json:"kind"` // key | mouse | wheel
	Key  string `json:"key,omitempty"`
	Down bool   `json:"down,omitempty"`
	X    int    `json:"x,omitempty"`
	Y    int    `json:"y,omitempty"`
	Btn  string `json:"btn,omitempty"` // left | right | middle
	DY   int    `json:"dy,omitempty"`
}

// Mute is an audio mute command.
type Mute struct {
	On bool `json:"on"`
}

// Volume sets absolute volume 0..100.
type Volume struct {
	Level int `json:"level"`
}

// Black toggles the black screen with an optional custom message.
type Black struct {
	On   bool   `json:"on"`
	Text string `json:"text,omitempty"`
}

// LockScreen is the merged black-screen + input-lock tool: an overlay with
// a custom message (default "请保持安静") or a custom wallpaper image
// (file name previously pushed to the student's data directory) that
// swallows keyboard and mouse input.
type LockScreen struct {
	On        bool   `json:"on"`
	Text      string `json:"text,omitempty"`
	Wallpaper string `json:"wallpaper,omitempty"`
}

// LockInput toggles input blocking on the student machine.
type LockInput struct {
	On bool `json:"on"`
}

// Demo starts/stops receiving a screen broadcast on a student machine.
// Fullscreen mode displays on top and swallows student input; window mode
// shows a normal window the student may minimize.
type Demo struct {
	On         bool `json:"on"`
	Fullscreen bool `json:"fullscreen"`
}

// Open tells a student to open an application or URL.
type Open struct {
	Kind   string `json:"kind"` // app | url
	Target string `json:"target"`
}

// Close tells a student to close an application (by process name) or a
// window (by title substring).
type Close struct {
	Kind   string `json:"kind"` // app | window
	Target string `json:"target"`
}

// Auth carries an authentication challenge or response.
type Auth struct {
	Nonce string `json:"nonce,omitempty"`
	Token string `json:"token,omitempty"`
}

// Power commands shutdown / reboot / logout with an optional delay.
type Power struct {
	Action string `json:"action"` // shutdown | reboot | logout
	Delay  int    `json:"delay"`  // seconds
	Abort  bool   `json:"abort"`
}

// Chat is a text message in either direction.
type Chat struct {
	From string `json:"from,omitempty"`
	Text string `json:"text"`
}

// RaiseHand is sent by a student to request attention.
type RaiseHand struct {
	On bool `json:"on"`
}

// BroadcastResult is the teacher's reply to a student's request to share
// its own screen with the class: approval (broadcast starts) or rejection.
type BroadcastResult struct {
	Approved bool   `json:"approved"`
	Reason   string `json:"reason,omitempty"`
}

// BlockList is the application/website blacklist or whitelist policy.
type BlockList struct {
	Mode          string   `json:"mode"` // black | white
	Apps          []string `json:"apps"`
	Sites         []string `json:"sites"`
	BlockUSBGames bool     `json:"block_usb_games,omitempty"`
}

// BlockExempt pauses/resumes rule enforcement for individual students (e.g.
// reward those who finished their work with free app/web access). The
// stored policy is kept and resumes when the exemption is lifted.
type BlockExempt struct {
	On bool `json:"on"`
}

// Status reports student state back to the teacher.
type Status struct {
	Muted   bool `json:"muted"`
	Volume  int  `json:"volume"`
	Blacked bool `json:"blacked"`
	Locked  bool `json:"locked"`
	Demoing bool `json:"demoing"`
	Exempt  bool `json:"exempt"`
}

// Ack acknowledges a command.
type Ack struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// FileInfo describes a file about to be transferred.
type FileInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}
