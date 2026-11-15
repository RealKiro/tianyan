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
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

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
	}
}

// Config configures the teacher server.
type Config struct {
	Name        string
	ControlPort int
	HTTPAddr    string
	DataDir     string
}

// Server is the teacher server.
type Server struct {
	cfg Config

	mu       sync.Mutex
	students map[string]*Student
	hub      *hub
	recv     *transfer.Receiver
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
	defer func() {
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
			case "status":
				if id != "" {
					s.updateStatus(id, payload)
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
			} else {
				s.hub.broadcastFull(id, frameMsg(id, "full", jpg))
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
	s.pushRoster()
	return id
}

func (s *Server) markOffline(id string) {
	s.mu.Lock()
	if st := s.students[id]; st != nil {
		st.conn = nil
		st.fullViewers = 0
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
	Open      *protocol.Open      `json:"open,omitempty"`
	Close     *protocol.Close     `json:"close,omitempty"`
	Power     *protocol.Power     `json:"power,omitempty"`
	BlockList *protocol.BlockList `json:"blocklist,omitempty"`
	Chat      *struct {
		Text string `json:"text"`
	} `json:"chat,omitempty"`
}

func (s *Server) dispatchCommand(ids []string, raw json.RawMessage) {
	var cmd command
	if json.Unmarshal(raw, &cmd) != nil {
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
	case cmd.Open != nil:
		msg = protocol.NewMessage("open", *cmd.Open)
	case cmd.Close != nil:
		msg = protocol.NewMessage("close", *cmd.Close)
	case cmd.Power != nil:
		msg = protocol.NewMessage("power", *cmd.Power)
	case cmd.BlockList != nil:
		msg = protocol.NewMessage("blocklist", *cmd.BlockList)
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
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Mode  string          `json:"mode"`
			IDs   []string        `json:"ids"`
			Cmd   json.RawMessage `json:"cmd"`
			Input protocol.Input  `json:"input"`
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
	Group     string `json:"group,omitempty"`
	StudentID string `json:"student_id,omitempty"`
	Online    bool   `json:"online"`
	Muted     bool   `json:"muted"`
	Volume    int    `json:"volume"`
	Blacked   bool   `json:"blacked"`
	Locked    bool   `json:"locked"`
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
			Group:     st.info.Group,
			StudentID: st.info.StudentID,
			Online:    st.conn != nil,
			Muted:     st.muted,
			Volume:    st.volume,
			Blacked:   st.blacked,
			Locked:    st.locked,
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
