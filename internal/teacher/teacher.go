// Package teacher implements the teacher-side server: student registry via
// multicast discovery, screen-frame routing to the web console, remote
// command dispatch and file transfer.
package teacher

import (
	"bytes"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gorilla/websocket"

	"tianyan/internal/auth"
	"tianyan/internal/capture"
	"tianyan/internal/discovery"
	"tianyan/internal/protocol"
	"tianyan/internal/transfer"
	"tianyan/web"
)

// DefaultConfig returns the teacher defaults.
func DefaultConfig() Config {
	return Config{
		Name:        "教师机",
		ControlPort: 5555,
		HTTPAddr:    ":8080",
		DataDir:     filepath.Join(os.TempDir(), "tianyan-teacher"),
		DemoFPS:     8,
		DemoWidth:   1600,
		DemoQuality: 60,
	}
}

// Config configures the teacher server.
type Config struct {
	Name        string
	ControlPort int
	HTTPAddr    string
	DataDir     string
	AuthKey     string // shared auth key (empty = no auth)
	DemoFPS     int    // broadcast frame rate (teacher screen source)
	DemoWidth   int    // broadcast frame max width, 0 = native resolution
	DemoQuality int    // broadcast JPEG quality 1..100
}

// Server is the teacher server.
type Server struct {
	cfg Config

	mu       sync.Mutex
	students map[string]*Student
	hub      *hub
	recv     *transfer.Receiver

	demo    demoState
	pending []string // student ids with an unanswered broadcast request

	lastFullscreen bool // remembered demo mode for the docked-bar broadcast toggle
}

// demoState tracks the current screen broadcast (Veyon's demo feature):
// source "" = the teacher's own screen, or a student id being relayed.
type demoState struct {
	mu         sync.Mutex
	active     bool
	source     string
	fullscreen bool
	stopCh     chan struct{}
}

// Student is a registered student machine.
type Student struct {
	id          string
	info        protocol.Hello
	conn        net.Conn
	writeMu     sync.Mutex
	fullViewers int
	lastSeen    time.Time
	muted       bool
	volume      int
	blacked     bool
	locked      bool
	raising     bool
	demoing     bool
	exempt      bool // rule enforcement paused (reward for finished work)
}

type hub struct {
	mu       sync.RWMutex
	clients  map[*uiClient]bool
	fullSubs map[string]map[*uiClient]bool
}

type uiClient struct {
	conn   *websocket.Conn
	send   chan []byte
	fullOf string
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool { return true },
}

// New creates a teacher server.
func New(cfg Config) (*Server, error) {
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return nil, err
	}
	return &Server{
		cfg:      cfg,
		students: map[string]*Student{},
		hub: &hub{
			clients:  map[*uiClient]bool{},
			fullSubs: map[string]map[*uiClient]bool{},
		},
		recv: transfer.NewReceiver(),
	}, nil
}

// Run starts discovery, the control listener and the HTTP console.
func (s *Server) Run() error {
	if _, err := discovery.ListenAnnounce(s.onAnnounce); err != nil {
		return fmt.Errorf("teacher: announce listener: %w", err)
	}
	go s.presenceLoop()

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", s.cfg.ControlPort))
	if err != nil {
		return fmt.Errorf("teacher: control listener: %w", err)
	}
	go s.acceptLoop(ln)
	log.Printf("teacher: control listening on :%d", s.cfg.ControlPort)

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(web.FS)))
	mux.HandleFunc("/ws", s.serveWS)
	mux.HandleFunc("/api/roster", s.handleRoster)
	mux.HandleFunc("/api/export", s.handleExport)
	mux.HandleFunc("/api/import", s.handleImport)
	mux.HandleFunc("/api/sendfile", s.handleSendFile)
	mux.HandleFunc("/api/wol", s.handleWOL)

	log.Printf("teacher: console listening on http://%s", s.cfg.HTTPAddr)
	return http.ListenAndServe(s.cfg.HTTPAddr, mux)
}

// ---- discovery & student connections ----

func (s *Server) presenceLoop() {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	ip := localIP()
	for range t.C {
		if err := discovery.BroadcastPresence(protocol.TeacherPresence{
			Name: s.cfg.Name,
			IP:   ip,
			Port: s.cfg.ControlPort,
		}); err != nil {
			log.Printf("teacher: presence: %v", err)
		}
	}
}

func (s *Server) onAnnounce(a protocol.Announce, _ *net.UDPAddr) {
	h := protocol.Hello{
		Name:      a.Name,
		Hostname:  a.Hostname,
		IP:        a.IP,
		MAC:       a.MAC,
		OS:        a.OS,
		User:      a.User,
		StudentID: a.StudentID,
		Group:     a.Group,
	}
	id := studentKey(h)
	s.mu.Lock()
	st := s.students[id]
	if st == nil {
		st = &Student{id: id, info: h, lastSeen: time.Now()}
		s.students[id] = st
	} else {
		if h.Name != "" {
			st.info.Name = h.Name
		}
		if h.IP != "" {
			st.info.IP = h.IP
		}
		if h.MAC != "" {
			st.info.MAC = h.MAC
		}
		if h.User != "" {
			st.info.User = h.User
		}
		st.lastSeen = time.Now()
	}
	s.mu.Unlock()
	s.pushRoster()
}

func (s *Server) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go s.handleStudent(conn)
	}
}

func (s *Server) handleStudent(conn net.Conn) {
	defer conn.Close()
	var id string
	var expectedNonce string
	var authDeadline *time.Timer
	authenticated := s.cfg.AuthKey == ""
	defer func() {
		if authDeadline != nil {
			authDeadline.Stop()
		}
		if id != "" {
			s.markOffline(id)
		}
	}()

	for {
		typ, payload, err := protocol.ReadFrame(conn)
		if err != nil {
			return
		}
		switch typ {
		case protocol.FrameJSON:
			var msg protocol.Message
			if json.Unmarshal(payload, &msg) != nil {
				continue
			}
			switch msg.Type {
			case "hello":
				var h protocol.Hello
				_ = json.Unmarshal(msg.Data, &h)
				id = s.registerStudent(h, conn)
				if s.cfg.AuthKey != "" && !authenticated {
					expectedNonce = auth.Nonce()
					s.sendToStudent(id, protocol.NewMessage("auth_challenge", protocol.Auth{Nonce: expectedNonce}))
					authDeadline = time.AfterFunc(5*time.Second, func() { _ = conn.Close() })
				}

			case "auth_challenge":
				var a protocol.Auth
				_ = json.Unmarshal(msg.Data, &a)
				if id != "" {
					s.sendToStudent(id, protocol.NewMessage("auth_response", protocol.Auth{Token: auth.Token(s.cfg.AuthKey, a.Nonce)}))
				}

			case "auth_response":
				var a protocol.Auth
				_ = json.Unmarshal(msg.Data, &a)
				if s.cfg.AuthKey != "" && !authenticated && auth.Verify(s.cfg.AuthKey, expectedNonce, a.Token) {
					authenticated = true
					if authDeadline != nil {
						authDeadline.Stop()
						authDeadline = nil
					}
					s.sendPendingDemo(id) // catch up with an ongoing broadcast
				}
			case "status":
				if id != "" {
					s.updateStatus(id, msg.Data)
				}
			case "ack":
				var a protocol.Ack
				if id != "" && json.Unmarshal(msg.Data, &a) == nil && !a.OK && a.Error != "" {
					s.hub.broadcast(eventMsg("error", id, s.studentName(id), a.Error))
				}
			case "raise_hand":
				if id != "" {
					var rh protocol.RaiseHand
					_ = json.Unmarshal(msg.Data, &rh)
					s.mu.Lock()
					if st := s.students[id]; st != nil {
						st.raising = rh.On
					}
					s.mu.Unlock()
					s.pushRoster()
				}
			case "broadcast_request":
				if id != "" {
					s.handleBroadcastRequest(id)
				}
			case "chat":
				if id != "" {
					var ch protocol.Chat
					_ = json.Unmarshal(msg.Data, &ch)
					s.hub.broadcast(eventMsg("chat", id, ch.From, ch.Text))
				}
			}

		case protocol.FrameImage:
			if id == "" || len(payload) < 2 {
				continue
			}
			mode := payload[0]
			jpg := payload[1:]
			s.mu.Lock()
			if st := s.students[id]; st != nil {
				st.lastSeen = time.Now()
			}
			s.mu.Unlock()
			if mode == protocol.ImageThumb {
				s.hub.broadcast(frameMsg(id, "thumb", jpg))
			} else if mode == protocol.ImageFull {
				s.hub.broadcastFull(id, frameMsg(id, "full", jpg))
				// Student relay: while this student is the demo source,
				// re-broadcast its full frames to everyone else.
				s.demo.mu.Lock()
				relay := s.demo.active && s.demo.source == id
				s.demo.mu.Unlock()
				if relay {
					s.fanoutDemo(jpg, id)
				}
			} else if mode == protocol.ImageShot {
				s.saveScreenshot(id, jpg)
			}

		case protocol.FrameFile:
			if id == "" {
				continue
			}
			if info, done, err := s.recv.Handle(payload, s.cfg.DataDir); err != nil {
				log.Printf("teacher: file receive: %v", err)
			} else if done {
				s.hub.broadcast(eventMsg("chat", id, s.studentName(id), "提交了作业："+info.Name))
			}
		}
	}
}

func (s *Server) registerStudent(h protocol.Hello, conn net.Conn) string {
	id := studentKey(h)
	s.mu.Lock()
	st := s.students[id]
	if st == nil {
		st = &Student{id: id}
		s.students[id] = st
	}
	st.info = h
	st.conn = conn
	st.lastSeen = time.Now()
	s.mu.Unlock()
	if s.cfg.AuthKey == "" {
		s.sendPendingDemo(id) // catch up with an ongoing broadcast
	}
	s.pushRoster()
	return id
}

func (s *Server) markOffline(id string) {
	wasSource := false
	s.mu.Lock()
	if st := s.students[id]; st != nil {
		st.conn = nil
		st.fullViewers = 0
		st.demoing = false
	}
	s.demo.mu.Lock() // lock order: s.mu -> demo.mu everywhere
	if s.demo.active && s.demo.source == id {
		wasSource = true
	}
	s.demo.mu.Unlock()
	s.mu.Unlock()
	if wasSource {
		log.Printf("teacher: demo source %s went offline; stopping broadcast", id)
		s.stopDemo()
		return
	}
	s.dropPending(id)
	s.pushRoster()
}

// sendPendingDemo brings a freshly connected (and, with auth, just
// verified) student into an ongoing broadcast.
func (s *Server) sendPendingDemo(id string) {
	s.demo.mu.Lock()
	active, fullscreen, source := s.demo.active, s.demo.fullscreen, s.demo.source
	s.demo.mu.Unlock()
	if !active || id == source || id == "" {
		return
	}
	s.sendToStudent(id, protocol.NewMessage("demo", protocol.Demo{On: true, Fullscreen: fullscreen}))
	s.sendToStudent(id, protocol.NewMessage("view", protocol.View{Mode: "off"}))
	s.mu.Lock()
	if st := s.students[id]; st != nil {
		st.demoing = true
	}
	s.mu.Unlock()
	s.pushRoster()
}

func (s *Server) updateStatus(id string, payload []byte) {
	var st protocol.Status
	if json.Unmarshal(payload, &st) != nil {
		return
	}
	s.mu.Lock()
	if x := s.students[id]; x != nil {
		x.muted = st.Muted
		x.volume = st.Volume
		x.blacked = st.Blacked
		x.locked = st.Locked
		x.exempt = st.Exempt
		// NOTE: demoing is intentionally NOT taken from the student here —
		// the teacher is the single source of truth for broadcast state, and
		// stale periodic status reports would overwrite a just-started demo.
		x.lastSeen = time.Now()
	}
	s.mu.Unlock()
	s.pushRoster()
}

func (s *Server) studentName(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.students[id]; st != nil && st.info.Name != "" {
		return st.info.Name
	}
	return id
}

func studentKey(h protocol.Hello) string {
	if h.MAC != "" {
		return h.MAC
	}
	return h.IP
}

// ---- command dispatch ----

type command struct {
	Mute      *protocol.Mute      `json:"mute,omitempty"`
	Volume    *protocol.Volume    `json:"volume,omitempty"`
	Black     *protocol.Black     `json:"black,omitempty"`
	LockInput *protocol.LockInput `json:"lock_input,omitempty"`
	Demo      *struct {
		On         bool   `json:"on"`
		Fullscreen bool   `json:"fullscreen"`
		Source     string `json:"source"` // "" / "teacher" = teacher screen, else student id
	} `json:"demo,omitempty"`
	Screenshot  *struct{}             `json:"screenshot,omitempty"`
	LockScreen  *protocol.LockScreen  `json:"lockscreen,omitempty"`
	Open        *protocol.Open        `json:"open,omitempty"`
	Close       *protocol.Close       `json:"close,omitempty"`
	Power       *protocol.Power       `json:"power,omitempty"`
	BlockList   *protocol.BlockList   `json:"blocklist,omitempty"`
	BlockExempt *protocol.BlockExempt `json:"block_exempt,omitempty"`
	Chat        *struct {
		Text string `json:"text"`
	} `json:"chat,omitempty"`
}

func (s *Server) dispatchCommand(ids []string, raw json.RawMessage) {
	var cmd command
	if json.Unmarshal(raw, &cmd) != nil {
		return
	}
	// The broadcast is a global mode, not a per-student command: it targets
	// every online student regardless of the selection.
	if cmd.Demo != nil {
		if cmd.Demo.On {
			source := cmd.Demo.Source
			if source == "teacher" {
				source = ""
			}
			if source != "" {
				s.mu.Lock()
				st := s.students[source]
				s.mu.Unlock()
				if st == nil || st.conn == nil {
					s.hub.broadcast(eventMsg("error", "", "教师", "广播源学生不在线，无法开始转播"))
					return
				}
			}
			s.startDemo(source, cmd.Demo.Fullscreen)
		} else {
			s.stopDemo()
		}
		return
	}
	var msg protocol.Message
	switch {
	case cmd.Mute != nil:
		msg = protocol.NewMessage("mute", *cmd.Mute)
	case cmd.Volume != nil:
		msg = protocol.NewMessage("volume", *cmd.Volume)
	case cmd.Black != nil:
		msg = protocol.NewMessage("black", *cmd.Black)
	case cmd.LockInput != nil:
		msg = protocol.NewMessage("lock_input", *cmd.LockInput)
	case cmd.Screenshot != nil:
		msg = protocol.NewMessage("screenshot", struct{}{})
	case cmd.Open != nil:
		msg = protocol.NewMessage("open", *cmd.Open)
	case cmd.Close != nil:
		msg = protocol.NewMessage("close", *cmd.Close)
	case cmd.Power != nil:
		msg = protocol.NewMessage("power", *cmd.Power)
	case cmd.BlockList != nil:
		msg = protocol.NewMessage("blocklist", *cmd.BlockList)
	case cmd.BlockExempt != nil:
		msg = protocol.NewMessage("block_exempt", *cmd.BlockExempt)
	case cmd.LockScreen != nil:
		msg = protocol.NewMessage("lockscreen", *cmd.LockScreen)
	case cmd.Chat != nil:
		msg = protocol.NewMessage("chat", protocol.Chat{From: "教师", Text: cmd.Chat.Text})
	default:
		return
	}
	if len(ids) == 0 {
		s.broadcastToAll(msg)
		return
	}
	for _, id := range ids {
		s.sendToStudent(id, msg)
	}
}

func (s *Server) broadcastToAll(msg protocol.Message) {
	raw, _ := json.Marshal(msg)
	s.mu.Lock()
	students := make([]*Student, 0, len(s.students))
	for _, st := range s.students {
		students = append(students, st)
	}
	s.mu.Unlock()
	for _, st := range students {
		st.writeMu.Lock()
		s.mu.Lock()
		conn := st.conn
		s.mu.Unlock()
		if conn != nil {
			_ = protocol.WriteFrame(conn, protocol.FrameJSON, raw)
		}
		st.writeMu.Unlock()
	}
}

func (s *Server) sendToStudent(id string, msg protocol.Message) {
	raw, _ := json.Marshal(msg)
	s.mu.Lock()
	st := s.students[id]
	if st == nil {
		s.mu.Unlock()
		return
	}
	st.writeMu.Lock()
	conn := st.conn
	s.mu.Unlock()
	if conn != nil {
		_ = protocol.WriteFrame(conn, protocol.FrameJSON, raw)
	}
	st.writeMu.Unlock()
}

func (s *Server) sendInput(id string, in protocol.Input) {
	s.sendToStudent(id, protocol.NewMessage("input", in))
}

// ---- screen broadcast (demo) ----

// startDemo begins a broadcast. source "" streams the teacher's own screen;
// a student id relays that student's screen to everyone else. Fullscreen
// mode also locks student input on the receiving machines.
func (s *Server) startDemo(source string, fullscreen bool) {
	s.stopDemo() // switching source/mode: tear down any current broadcast
	s.lastFullscreen = fullscreen

	s.demo.mu.Lock()
	s.demo.active = true
	s.demo.source = source
	s.demo.fullscreen = fullscreen
	stopCh := make(chan struct{})
	s.demo.stopCh = stopCh
	s.demo.mu.Unlock()

	ids := s.onlineStudentIDs()
	for _, id := range ids {
		if id != source {
			s.sendToStudent(id, protocol.NewMessage("demo", protocol.Demo{On: true, Fullscreen: fullscreen}))
		}
	}
	// Pause thumbnail streaming while broadcasting (Veyon's
	// SlowDownThumbnailUpdates) to keep LAN bandwidth for demo frames.
	for _, id := range ids {
		s.sendToStudent(id, protocol.NewMessage("view", protocol.View{Mode: "off"}))
	}
	if source != "" {
		// Student relay: make the source push full frames, which are then
		// fanned out to the rest of the class from the ImageFull handler.
		s.sendToStudent(source, protocol.NewMessage("view", protocol.View{Mode: "full"}))
	} else {
		go s.teacherCaptureLoop(stopCh)
	}
	s.mu.Lock()
	for id, st := range s.students {
		st.demoing = st.conn != nil && id != source
	}
	s.mu.Unlock()
	s.pushRoster()
	s.pushDemoStatus()
}

// stopDemo tears down the current broadcast, if any, and restores the
// normal thumbnail monitoring mode.
func (s *Server) stopDemo() {
	s.demo.mu.Lock()
	wasActive := s.demo.active
	stopCh := s.demo.stopCh
	source := s.demo.source
	s.demo.active = false
	s.demo.source = ""
	s.demo.stopCh = nil
	s.demo.mu.Unlock()
	if stopCh != nil {
		close(stopCh)
	}
	if !wasActive {
		return
	}
	if source != "" {
		// The student who was sharing its screen resets its toolbar state.
		s.sendToStudent(source, protocol.NewMessage("broadcast_result",
			protocol.BroadcastResult{Approved: false, Reason: "教师已结束广播"}))
	}
	for _, id := range s.onlineStudentIDs() {
		s.sendToStudent(id, protocol.NewMessage("demo", protocol.Demo{On: false}))
		s.sendToStudent(id, protocol.NewMessage("view", protocol.View{Mode: "thumb"}))
	}
	s.mu.Lock()
	for _, st := range s.students {
		st.demoing = false
	}
	s.mu.Unlock()
	s.pushRoster()
	s.pushDemoStatus()
}

// teacherCaptureLoop grabs the teacher's screen and fans frames out until
// the broadcast is stopped.
func (s *Server) teacherCaptureLoop(stopCh <-chan struct{}) {
	cap := capture.New()
	fps := s.cfg.DemoFPS
	if fps <= 0 || fps > 30 {
		fps = 8
	}
	t := time.NewTicker(time.Second / time.Duration(fps))
	defer t.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-t.C:
		}
		if len(s.onlineStudentIDs()) == 0 {
			continue // nobody watching: skip the capture work
		}
		fr, err := cap.Capture(s.cfg.DemoWidth, s.cfg.DemoQuality)
		if err != nil {
			log.Printf("teacher: demo capture: %v", err)
			continue
		}
		s.fanoutDemo(fr.JPEG, "")
	}
}

// fanoutDemo sends one demo frame to every online student except exclude
// (the relay source). Each write runs on its own goroutine with a deadline
// so a slow student cannot stall the rest of the class.
func (s *Server) fanoutDemo(jpg []byte, exclude string) {
	payload := make([]byte, 1, len(jpg)+1)
	payload[0] = protocol.ImageDemo
	payload = append(payload, jpg...)
	for _, st := range s.demoTargets(exclude) {
		go s.writeDemoFrame(st, payload)
	}
}

// demoTargets snapshots the online students eligible for demo frames.
func (s *Server) demoTargets(exclude string) []*Student {
	s.mu.Lock()
	defer s.mu.Unlock()
	targets := make([]*Student, 0, len(s.students))
	for id, st := range s.students {
		if id != exclude && st.conn != nil {
			targets = append(targets, st)
		}
	}
	return targets
}

func (s *Server) writeDemoFrame(st *Student, payload []byte) {
	st.writeMu.Lock()
	defer st.writeMu.Unlock()
	s.mu.Lock()
	conn := st.conn
	s.mu.Unlock()
	if conn == nil {
		return
	}
	_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	_ = protocol.WriteFrame(conn, protocol.FrameImage, payload)
}

// saveScreenshot stores a full-quality frame from a student on disk and
// reports the path to the console.
func (s *Server) saveScreenshot(id string, jpg []byte) {
	dir := filepath.Join(s.cfg.DataDir, "screenshots")
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("teacher: screenshots dir: %v", err)
		return
	}
	name := s.studentName(id)
	path := filepath.Join(dir, sanitizeFilename(name)+"-"+time.Now().Format("20060102-150405")+".jpg")
	if err := os.WriteFile(path, jpg, 0644); err != nil {
		log.Printf("teacher: screenshot save: %v", err)
		s.hub.broadcast(eventMsg("error", id, name, "截图保存失败："+err.Error()))
		return
	}
	s.hub.broadcast(eventMsg("screenshot", id, name, path))
}

// sanitizeFilename maps a student name to a safe file name, replacing
// separators with dashes (same spirit as Veyon's screenshot naming).
func sanitizeFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "student"
	}
	return out
}

func (s *Server) onlineStudentIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.students))
	for id, st := range s.students {
		if st.conn != nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// pushDemoStatus reports the broadcast mode to the console.
func (s *Server) pushDemoStatus() {
	s.demo.mu.Lock()
	active, source, fullscreen := s.demo.active, s.demo.source, s.demo.fullscreen
	s.demo.mu.Unlock()
	name := "教师屏幕"
	if source != "" {
		name = s.studentName(source)
	}
	raw, _ := json.Marshal(map[string]any{
		"type": "demo", "active": active,
		"fullscreen": fullscreen, "source": source, "source_name": name,
	})
	s.hub.broadcast(raw)
}

// ---- student broadcast approval (学生请求广播自己的屏幕) ----

// handleBroadcastRequest queues a student's request to share its screen and
// notifies the console. The teacher must approve before any broadcast of a
// student screen starts.
func (s *Server) handleBroadcastRequest(id string) {
	s.demo.mu.Lock()
	busy := s.demo.active
	s.demo.mu.Unlock()
	if busy {
		s.sendToStudent(id, protocol.NewMessage("broadcast_result",
			protocol.BroadcastResult{Approved: false, Reason: "已有广播进行中，请稍后再试"}))
		return
	}
	name := s.studentName(id)
	changed := false
	s.mu.Lock()
	for _, p := range s.pending {
		if p == id {
			changed = true // already queued; re-notify the console anyway
			break
		}
	}
	if !changed {
		s.pending = append(s.pending, id)
	}
	s.mu.Unlock()
	s.hub.broadcast(eventMsg("broadcast", id, name, "请求广播自己的屏幕，等待教师批准"))
	s.pushBroadcastReqs()
}

// replyBroadcast applies the teacher's decision from the console/dock bar.
func (s *Server) replyBroadcast(id string, approved bool, fullscreen bool) {
	s.mu.Lock()
	out := s.pending[:0]
	for _, p := range s.pending {
		if p != id {
			out = append(out, p)
		}
	}
	s.pending = out
	s.mu.Unlock()
	s.pushBroadcastReqs()
	if !approved {
		s.sendToStudent(id, protocol.NewMessage("broadcast_result",
			protocol.BroadcastResult{Approved: false, Reason: "教师已拒绝广播请求"}))
		return
	}
	if st := s.studentConn(id); st == nil {
		s.sendToStudent(id, protocol.NewMessage("broadcast_result",
			protocol.BroadcastResult{Approved: false, Reason: "学生已离线"}))
		s.hub.broadcast(eventMsg("error", "", "教师", "广播审批目标已离线"))
		return
	}
	s.sendToStudent(id, protocol.NewMessage("broadcast_result",
		protocol.BroadcastResult{Approved: true}))
	s.startDemo(id, fullscreen)
}

// pushBroadcastReqs sends the current pending list to the console.
func (s *Server) pushBroadcastReqs() {
	s.mu.Lock()
	ids := append([]string(nil), s.pending...)
	s.mu.Unlock()
	type reqEntry struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		IP   string `json:"ip"`
	}
	reqs := make([]reqEntry, 0, len(ids))
	for _, id := range ids {
		s.mu.Lock()
		st := s.students[id]
		var name, ip string
		if st != nil {
			name, ip = st.info.Name, st.info.IP
		}
		s.mu.Unlock()
		if st == nil || st.conn == nil {
			continue // vanished student drops out of the list
		}
		if name == "" {
			name = id
		}
		reqs = append(reqs, reqEntry{ID: id, Name: name, IP: ip})
	}
	raw, _ := json.Marshal(map[string]any{"type": "broadcast_req", "reqs": reqs})
	s.hub.broadcast(raw)
}

// dropPending removes a student from the pending list (on disconnect).
func (s *Server) dropPending(id string) {
	s.mu.Lock()
	out := s.pending[:0]
	for _, p := range s.pending {
		if p != id {
			out = append(out, p)
		}
	}
	changed := len(out) != len(s.pending)
	s.pending = out
	s.mu.Unlock()
	if changed {
		s.pushBroadcastReqs()
	}
}

// ---- docked-bar quick actions ----

// QuickStatus returns the one-line status shown on the teacher dock bar.
func (s *Server) QuickStatus() string {
	online, total, raising, blacked := 0, 0, 0, 0
	s.mu.Lock()
	for _, st := range s.students {
		total++
		if st.conn != nil {
			online++
		}
		if st.raising {
			raising++
		}
		if st.blacked {
			blacked++
		}
	}
	nPending := len(s.pending)
	s.mu.Unlock()
	line := fmt.Sprintf("在线 %d/%d", online, total)
	if raising > 0 {
		line += fmt.Sprintf(" · %d 人举手", raising)
	}
	if blacked > 0 {
		line += fmt.Sprintf(" · %d 台锁屏", blacked)
	}
	if nPending > 0 {
		line += fmt.Sprintf(" · %d 个广播请求待审批", nPending)
	}
	return line
}

// QuickCommand performs a docked-bar action. It applies to all online
// students; toggles read the current state first.
func (s *Server) QuickCommand(action string) {
	switch action {
	case "console":
		url := s.cfg.HTTPAddr
		if url == "" || url[0] == ':' {
			url = "http://localhost" + url
		}
		_ = exec.Command("cmd", "/C", "start", "", url).Start()
	case "broadcast":
		s.demo.mu.Lock()
		active := s.demo.active
		s.demo.mu.Unlock()
		if active {
			s.stopDemo()
			return
		}
		s.startDemo("", s.lastFullscreen)
	case "black", "lock", "lockscreen":
		// Merged lock-screen toggle: if any online student is locked up,
		// lift every lock; otherwise lock all screens with the default text.
		if s.anyOnline(func(st *Student) bool { return st.blacked || st.locked }) {
			s.broadcastToAll(protocol.NewMessage("lockscreen", protocol.LockScreen{On: false}))
		} else {
			s.broadcastToAll(protocol.NewMessage("lockscreen", protocol.LockScreen{On: true, Text: "请保持安静"}))
		}
	case "mute":
		if s.anyOnline(func(st *Student) bool { return st.muted }) {
			s.broadcastToAll(protocol.NewMessage("mute", protocol.Mute{On: false}))
		} else {
			s.broadcastToAll(protocol.NewMessage("mute", protocol.Mute{On: true}))
		}
	case "shot":
		for _, id := range s.onlineStudentIDs() {
			s.sendToStudent(id, protocol.NewMessage("screenshot", struct{}{}))
		}
	case "shutdown", "reboot":
		msg := protocol.NewMessage("power", protocol.Power{Action: action, Delay: 0})
		s.broadcastToAll(msg)
	case "stop":
		s.stopDemo()
	}
}

// anyOnline reports whether the predicate holds for any online student.
func (s *Server) anyOnline(pred func(*Student) bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.students {
		if st.conn != nil && pred(st) {
			return true
		}
	}
	return false
}

// studentConn returns a snapshot of the student record when online.
func (s *Server) studentConn(id string) *Student {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.students[id]
	if st == nil || st.conn == nil {
		return nil
	}
	return st
}

// ---- file transfer ----

func (s *Server) sendFileTo(id, name string, data []byte) error {
	s.mu.Lock()
	st := s.students[id]
	if st == nil {
		s.mu.Unlock()
		return fmt.Errorf("unknown student %s", id)
	}
	st.writeMu.Lock()
	conn := st.conn
	s.mu.Unlock()
	if conn == nil {
		st.writeMu.Unlock()
		return fmt.Errorf("student %s offline", id)
	}
	defer st.writeMu.Unlock()
	info := protocol.FileInfo{ID: transfer.NewID(), Name: name, Size: int64(len(data))}
	return transfer.SendFile(func(typ byte, payload []byte) error {
		return protocol.WriteFrame(conn, typ, payload)
	}, info, bytes.NewReader(data))
}

// ---- WebSocket console ----

func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &uiClient{conn: conn, send: make(chan []byte, 256)}
	s.hub.register(c)
	defer func() {
		s.setFullView(c, "", "thumb")
		s.hub.unregister(c)
		_ = conn.Close()
	}()
	go s.uiWriter(c)
	s.pushRosterTo(c)
	s.uiReader(c)
}

func (s *Server) uiWriter(c *uiClient) {
	for msg := range c.send {
		_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			return
		}
	}
}

func (s *Server) uiReader(c *uiClient) {
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var m struct {
			Type       string          `json:"type"`
			ID         string          `json:"id"`
			Mode       string          `json:"mode"`
			IDs        []string        `json:"ids"`
			Cmd        json.RawMessage `json:"cmd"`
			Input      protocol.Input  `json:"input"`
			Approved   bool            `json:"approved"`
			Fullscreen bool            `json:"fullscreen"`
		}
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.Type {
		case "view":
			s.setFullView(c, m.ID, m.Mode)
		case "cmd":
			s.dispatchCommand(m.IDs, m.Cmd)
		case "input":
			s.sendInput(m.ID, m.Input)
		case "broadcast_reply":
			if m.ID != "" {
				s.replyBroadcast(m.ID, m.Approved, m.Fullscreen)
			}
		}
	}
}

func (s *Server) setFullView(c *uiClient, id, mode string) {
	// Release previous subscription.
	if c.fullOf != "" {
		prev := c.fullOf
		c.fullOf = ""
		s.hub.unsubscribeFullFrom(c, prev)
		s.mu.Lock()
		if st := s.students[prev]; st != nil {
			st.fullViewers--
			if st.fullViewers <= 0 {
				st.fullViewers = 0
			}
		}
		s.mu.Unlock()
		s.sendToStudent(prev, protocol.NewMessage("view", protocol.View{Mode: "thumb"}))
	}
	if mode != "full" || id == "" {
		return
	}
	c.fullOf = id
	s.hub.subscribeFull(c, id)
	s.mu.Lock()
	if st := s.students[id]; st != nil {
		st.fullViewers++
	}
	s.mu.Unlock()
	s.sendToStudent(id, protocol.NewMessage("view", protocol.View{Mode: "full"}))
}

// ---- roster / events ----

type rosterEntry struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IP        string `json:"ip"`
	MAC       string `json:"mac"`
	OS        string `json:"os"`
	User      string `json:"user,omitempty"`
	Group     string `json:"group,omitempty"`
	StudentID string `json:"student_id,omitempty"`
	Online    bool   `json:"online"`
	Muted     bool   `json:"muted"`
	Volume    int    `json:"volume"`
	Blacked   bool   `json:"blacked"`
	Locked    bool   `json:"locked"`
	Demoing   bool   `json:"demoing"`
	Exempt    bool   `json:"exempt"`
	Raising   bool   `json:"raising"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	LastSeen  int64  `json:"last_seen"`
}

func (s *Server) buildRoster() []rosterEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]rosterEntry, 0, len(s.students))
	for id, st := range s.students {
		out = append(out, rosterEntry{
			ID:        id,
			Name:      st.info.Name,
			IP:        st.info.IP,
			MAC:       st.info.MAC,
			OS:        st.info.OS,
			User:      st.info.User,
			Group:     st.info.Group,
			StudentID: st.info.StudentID,
			Online:    st.conn != nil,
			Muted:     st.muted,
			Volume:    st.volume,
			Blacked:   st.blacked,
			Locked:    st.locked,
			Demoing:   st.demoing,
			Exempt:    st.exempt,
			Raising:   st.raising,
			Width:     st.info.Width,
			Height:    st.info.Height,
			LastSeen:  st.lastSeen.Unix(),
		})
	}
	return out
}

func (s *Server) rosterMsg() []byte {
	raw, _ := json.Marshal(map[string]any{"type": "roster", "students": s.buildRoster()})
	return raw
}

func (s *Server) pushRoster() {
	s.hub.broadcast(s.rosterMsg())
}

func (s *Server) pushRosterTo(c *uiClient) {
	select {
	case c.send <- s.rosterMsg():
	default:
	}
}

func frameMsg(id, mode string, jpg []byte) []byte {
	b64 := base64.StdEncoding.EncodeToString(jpg)
	raw, _ := json.Marshal(map[string]any{"type": "frame", "id": id, "mode": mode, "jpg": b64})
	return raw
}

func eventMsg(kind, id, from, text string) []byte {
	raw, _ := json.Marshal(map[string]any{"type": "event", "kind": kind, "id": id, "from": from, "text": text})
	return raw
}

// ---- hub ----

func (h *hub) register(c *uiClient) {
	h.mu.Lock()
	h.clients[c] = true
	h.mu.Unlock()
}

func (h *hub) unregister(c *uiClient) {
	h.mu.Lock()
	delete(h.clients, c)
	for id, m := range h.fullSubs {
		delete(m, c)
		if len(m) == 0 {
			delete(h.fullSubs, id)
		}
	}
	h.mu.Unlock()
}

func (h *hub) subscribeFull(c *uiClient, id string) {
	h.mu.Lock()
	if h.fullSubs[id] == nil {
		h.fullSubs[id] = map[*uiClient]bool{}
	}
	h.fullSubs[id][c] = true
	h.mu.Unlock()
}

func (h *hub) unsubscribeFullFrom(c *uiClient, id string) {
	h.mu.Lock()
	if m := h.fullSubs[id]; m != nil {
		delete(m, c)
		if len(m) == 0 {
			delete(h.fullSubs, id)
		}
	}
	h.mu.Unlock()
}

func (h *hub) broadcast(msg []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		select {
		case c.send <- msg:
		default:
		}
	}
}

func (h *hub) broadcastFull(id string, msg []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.fullSubs[id] {
		select {
		case c.send <- msg:
		default:
		}
	}
}

// ---- REST API ----

func (s *Server) handleRoster(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.buildRoster())
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=students.csv")
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"name", "ip", "mac", "group", "student_id", "online"})
		for _, e := range s.buildRoster() {
			_ = cw.Write([]string{e.Name, e.IP, e.MAC, e.Group, e.StudentID, boolStr(e.Online)})
		}
		cw.Flush()
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=students.json")
	_ = json.NewEncoder(w).Encode(s.buildRoster())
}

type importRow struct {
	Name      string `json:"name"`
	IP        string `json:"ip"`
	MAC       string `json:"mac"`
	Group     string `json:"group"`
	StudentID string `json:"student_id"`
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var rows []importRow
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if len(body) > 0 && body[0] == '[' {
		if json.Unmarshal(body, &rows) != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
	} else {
		cr := csv.NewReader(bytes.NewReader(body))
		records, err := cr.ReadAll()
		if err != nil || len(records) == 0 {
			http.Error(w, "bad csv", http.StatusBadRequest)
			return
		}
		// Assume header row: name,ip,mac,group,student_id
		for _, rec := range records[1:] {
			row := importRow{}
			for i, v := range rec {
				switch i {
				case 0:
					row.Name = v
				case 1:
					row.IP = v
				case 2:
					row.MAC = v
				case 3:
					row.Group = v
				case 4:
					row.StudentID = v
				}
			}
			rows = append(rows, row)
		}
	}
	s.applyImport(rows)
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"ok":true}`)
}

func (s *Server) applyImport(rows []importRow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range rows {
		var st *Student
		if r.MAC != "" {
			st = s.students[r.MAC]
		}
		if st == nil && r.IP != "" {
			for _, x := range s.students {
				if x.info.IP == r.IP {
					st = x
					break
				}
			}
		}
		if st == nil {
			continue
		}
		if r.Name != "" {
			st.info.Name = r.Name
		}
		if r.Group != "" {
			st.info.Group = r.Group
		}
		if r.StudentID != "" {
			st.info.StudentID = r.StudentID
		}
	}
	s.pushRoster()
}

func (s *Server) handleSendFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	ids := splitIDs(r.FormValue("ids"))
	file, hdr, err := r.FormFile("file")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 512<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(ids) == 0 {
		s.mu.Lock()
		for id := range s.students {
			ids = append(ids, id)
		}
		s.mu.Unlock()
	}
	var failed []string
	for _, id := range ids {
		if err := s.sendFileTo(id, hdr.Filename, data); err != nil {
			failed = append(failed, id)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "failed": failed})
}

func (s *Server) handleWOL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var m struct {
		MAC string `json:"mac"`
	}
	_ = json.NewDecoder(r.Body).Decode(&m)
	err := wakeOnLAN(m.MAC)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// ---- helpers ----

func splitIDs(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func localIP() string {
	addrs, _ := net.InterfaceAddrs()
	var fallback string
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		ip4 := ipnet.IP.To4()
		if ip4 == nil {
			continue
		}
		if isPrivateIPv4(ip4) {
			return ip4.String()
		}
		if fallback == "" && !ip4.IsLinkLocalUnicast() {
			fallback = ip4.String()
		}
	}
	return fallback
}

func isPrivateIPv4(ip net.IP) bool {
	return len(ip) == 4 &&
		(ip[0] == 10 ||
			(ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31) ||
			(ip[0] == 192 && ip[1] == 168))
}
