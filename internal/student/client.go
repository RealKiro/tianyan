// Package student implements the student-side client: auto-discovery,
// screen streaming, remote command execution, blocking policies and a small
// local toolbar served over HTTP.
package student

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"tianyan/internal/block"
	"tianyan/internal/capture"
	"tianyan/internal/control"
	"tianyan/internal/discovery"
	"tianyan/internal/protocol"
	"tianyan/internal/transfer"
)

// Version is the student client version.
const Version = "0.1.0"

const toolbarAddr = "127.0.0.1:5600"

var errNotConnected = errors.New("student: not connected to teacher")

// Config configures a student client.
type Config struct {
	Name            string
	StudentID       string
	Group           string
	TeacherIP       string // empty = auto-discover
	ServerPort      int    // teacher control TCP port
	DataDir         string
	AutoMute        bool          // mute when the teacher comes online
	RebootOnOffline bool          // reboot when teacher heartbeat is lost
	OfflineTimeout  time.Duration // how long to wait before declaring offline
}

// DefaultConfig returns a config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Name:            hostname(),
		ServerPort:      5555,
		DataDir:         filepath.Join(os.TempDir(), "tianyan"),
		AutoMute:        true,
		RebootOnOffline: false,
		OfflineTimeout:  30 * time.Second,
	}
}

// Client is a student client.
type Client struct {
	cfg      Config
	ctrl     control.Controller
	cap      *capture.Capturer
	enforcer *block.Enforcer
	recv     *transfer.Receiver

	writeMu sync.Mutex
	mu      sync.Mutex
	conn    net.Conn

	mac, ip, hostname string
	teacherIP         string

	fullWanted bool
	muted      bool
	volume     int
	blacked    bool
	locked     bool
	raising    bool

	lastRecv      time.Time
	pendingReboot bool

	stopCh chan struct{}

	msgMu          sync.Mutex
	lastTeacherMsg string
}

// New creates a student client.
func New(cfg Config) (*Client, error) {
	ctrl, err := control.New()
	if err != nil {
		return nil, fmt.Errorf("student: control init: %w", err)
	}
	c := &Client{
		cfg:      cfg,
		ctrl:     ctrl,
		cap:      capture.New(),
		enforcer: block.New(),
		recv:     transfer.NewReceiver(),
		mac:      macAddress(),
		ip:       localIPv4(),
		hostname: hostname(),
		stopCh:   make(chan struct{}),
	}
	if c.hostname == "" {
		c.hostname = "student"
	}
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return nil, err
	}
	// Apply the built-in default blocking policy (games/video sites). This
	// requires administrator privileges for the hosts file; failure is
	// non-fatal for the rest of the client.
	if err := c.enforcer.SetPolicy(block.DefaultPolicy()); err != nil {
		log.Printf("student: default block policy not applied: %v", err)
	}
	c.enforcer.OnViolation = func(kind, target string) {
		if kind == "usb_game" {
			c.sendChat("【管控】检测到从U盘运行游戏 " + target + "，已强制关闭")
			return
		}
		c.sendChat("【管控】已阻止运行：" + target)
	}
	return c, nil
}

// Run starts all background loops and blocks until Close is called.
func (c *Client) Run() {
	defer c.ctrl.Close()

	go c.announceLoop()
	go c.listenPresence()
	go c.connectLoop()
	go c.offlineMonitor()
	go c.enforcer.AppLoop(c.stopCh, 2*time.Second)
	go c.startToolbar()
	<-c.stopCh
}

// Close stops the client.
func (c *Client) Close() {
	select {
	case <-c.stopCh:
	default:
		close(c.stopCh)
	}
	c.mu.Lock()
	if c.conn != nil {
		_ = c.conn.Close()
	}
	c.mu.Unlock()
}

func (c *Client) announceLoop() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case <-t.C:
			a := protocol.Announce{
				Name:      c.cfg.Name,
				Hostname:  c.hostname,
				IP:        c.ip,
				MAC:       c.mac,
				OS:        runtime.GOOS,
				StudentID: c.cfg.StudentID,
				Group:     c.cfg.Group,
			}
			if err := discovery.BroadcastAnnounce(a); err != nil {
				log.Printf("student: announce: %v", err)
			}
		}
	}
}

func (c *Client) listenPresence() {
	_, err := discovery.ListenPresence(func(p protocol.TeacherPresence, _ *net.UDPAddr) {
		c.mu.Lock()
		if c.teacherIP == "" || c.teacherIP == p.IP {
			c.teacherIP = p.IP
		}
		c.mu.Unlock()
	})
	if err != nil {
		log.Printf("student: presence listener: %v", err)
	}
}

func (c *Client) connectLoop() {
	backoff := time.Second
	for {
		select {
		case <-c.stopCh:
			return
		default:
		}

		ip := c.resolveTeacherIP()
		if ip == "" {
			time.Sleep(time.Second)
			continue
		}
		c.mu.Lock()
		connected := c.conn != nil
		c.mu.Unlock()
		if connected {
			time.Sleep(time.Second)
			continue
		}

		addr := net.JoinHostPort(ip, strconv.Itoa(c.cfg.ServerPort))
		conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
		if err != nil {
			time.Sleep(backoff)
			if backoff < 5*time.Second {
				backoff += time.Second
			}
			continue
		}
		backoff = time.Second
		log.Printf("student: connected to teacher %s", addr)
		c.runConnection(conn)
		log.Printf("student: disconnected from teacher")
	}
}

func (c *Client) resolveTeacherIP() string {
	if c.cfg.TeacherIP != "" {
		return c.cfg.TeacherIP
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.teacherIP
}

func (c *Client) runConnection(conn net.Conn) {
	c.mu.Lock()
	c.conn = conn
	c.lastRecv = time.Now()
	c.fullWanted = false
	c.mu.Unlock()

	c.sendJSON(protocol.NewMessage("hello", c.buildHello()))

	if c.cfg.AutoMute {
		if err := c.ctrl.SetMute(true); err == nil {
			c.mu.Lock()
			c.muted = true
			c.mu.Unlock()
			c.sendStatus()
		}
	}

	done := make(chan struct{})
	go c.thumbLoop(done)
	go c.fullLoop(done)
	go c.statusLoop(done)

	c.readLoop(conn) // blocks until the connection closes

	close(done)
	c.mu.Lock()
	c.conn = nil
	c.fullWanted = false
	c.mu.Unlock()
	_ = conn.Close()
}

func (c *Client) buildHello() protocol.Hello {
	w, h := c.cap.ScreenSize()
	return protocol.Hello{
		Name:      c.cfg.Name,
		Hostname:  c.hostname,
		IP:        c.ip,
		MAC:       c.mac,
		OS:        runtime.GOOS,
		StudentID: c.cfg.StudentID,
		Group:     c.cfg.Group,
		Width:     w,
		Height:    h,
		Version:   Version,
	}
}

func (c *Client) readLoop(conn net.Conn) {
	for {
		typ, payload, err := protocol.ReadFrame(conn)
		if err != nil {
			return
		}
		c.mu.Lock()
		c.lastRecv = time.Now()
		c.mu.Unlock()
		switch typ {
		case protocol.FrameJSON:
			var msg protocol.Message
			if json.Unmarshal(payload, &msg) == nil {
				c.handleMessage(msg)
			}
		case protocol.FrameFile:
			if info, done, err := c.recv.Handle(payload, c.cfg.DataDir); err != nil {
				log.Printf("student: file receive: %v", err)
			} else if done {
				log.Printf("student: received file %s", info.Name)
				c.sendChat("已接收文件：" + info.Name)
			}
		}
	}
}

func (c *Client) handleMessage(msg protocol.Message) {
	switch msg.Type {
	case "view":
		var v protocol.View
		_ = json.Unmarshal(msg.Data, &v)
		c.mu.Lock()
		c.fullWanted = v.Mode == "full"
		c.mu.Unlock()

	case "mute":
		var m protocol.Mute
		_ = json.Unmarshal(msg.Data, &m)
		if err := c.ctrl.SetMute(m.On); err == nil {
			c.mu.Lock()
			c.muted = m.On
			c.mu.Unlock()
			c.sendStatus()
		} else {
			c.sendAck(msg.ID, err)
		}

	case "volume":
		var v protocol.Volume
		_ = json.Unmarshal(msg.Data, &v)
		if err := c.ctrl.SetVolume(v.Level); err == nil {
			c.mu.Lock()
			c.volume = v.Level
			c.mu.Unlock()
			c.sendStatus()
		} else {
			c.sendAck(msg.ID, err)
		}

	case "black":
		var b protocol.Black
		_ = json.Unmarshal(msg.Data, &b)
		if err := c.ctrl.BlackScreen(b.On, b.Text); err == nil {
			c.mu.Lock()
			c.blacked = b.On
			c.mu.Unlock()
			c.sendStatus()
		} else {
			c.sendAck(msg.ID, err)
		}

	case "lock_input":
		var l protocol.LockInput
		_ = json.Unmarshal(msg.Data, &l)
		if err := c.ctrl.LockInput(l.On); err == nil {
			c.mu.Lock()
			c.locked = l.On
			c.mu.Unlock()
			c.sendStatus()
		}

	case "open":
		var o protocol.Open
		_ = json.Unmarshal(msg.Data, &o)
		c.sendAck(msg.ID, c.ctrl.Open(o.Target))

	case "close":
		var cl protocol.Close
		_ = json.Unmarshal(msg.Data, &cl)
		var err error
		if cl.Kind == "app" {
			err = c.ctrl.CloseApp(cl.Target)
		} else {
			err = c.ctrl.CloseWindow(cl.Target)
		}
		c.sendAck(msg.ID, err)

	case "power":
		var p protocol.Power
		_ = json.Unmarshal(msg.Data, &p)
		c.sendAck(msg.ID, c.ctrl.Power(p.Action, p.Delay, p.Abort))

	case "blocklist":
		var bl protocol.BlockList
		_ = json.Unmarshal(msg.Data, &bl)
		err := c.enforcer.SetPolicy(block.Policy{Mode: bl.Mode, Apps: bl.Apps, Sites: bl.Sites, BlockUSBGames: bl.BlockUSBGames})
		if err != nil {
			log.Printf("student: blocklist: %v", err)
			c.sendAck(msg.ID, err)
		}

	case "chat":
		var ch protocol.Chat
		_ = json.Unmarshal(msg.Data, &ch)
		c.msgMu.Lock()
		c.lastTeacherMsg = ch.Text
		c.msgMu.Unlock()

	case "input":
		var in protocol.Input
		_ = json.Unmarshal(msg.Data, &in)
		c.applyInput(in)
	}
}

func (c *Client) applyInput(in protocol.Input) {
	switch in.Kind {
	case "key":
		_ = c.ctrl.Key(in.Key, in.Down)
	case "mouse":
		if in.Btn == "" {
			_ = c.ctrl.MouseMove(in.X, in.Y)
		} else {
			_ = c.ctrl.MouseButton(in.Btn, in.Down)
		}
	case "wheel":
		_ = c.ctrl.MouseScroll(in.DY)
	}
}

func (c *Client) thumbLoop(done <-chan struct{}) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
		}
		c.mu.Lock()
		conn := c.conn
		c.mu.Unlock()
		if conn == nil {
			continue
		}
		fr, err := c.cap.Capture(480, 40)
		if err != nil {
			continue
		}
		payload := append([]byte{protocol.ImageThumb}, fr.JPEG...)
		if err := c.writeFrame(protocol.FrameImage, payload); err != nil {
			return
		}
	}
}

func (c *Client) fullLoop(done <-chan struct{}) {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
		}
		c.mu.Lock()
		wanted := c.fullWanted
		conn := c.conn
		c.mu.Unlock()
		if !wanted || conn == nil {
			continue
		}
		fr, err := c.cap.Capture(0, 70)
		if err != nil {
			continue
		}
		payload := append([]byte{protocol.ImageFull}, fr.JPEG...)
		if err := c.writeFrame(protocol.FrameImage, payload); err != nil {
			return
		}
	}
}

func (c *Client) statusLoop(done <-chan struct{}) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			c.sendStatus()
		}
	}
}

func (c *Client) sendStatus() {
	c.mu.Lock()
	st := protocol.Status{Muted: c.muted, Volume: c.volume, Blacked: c.blacked, Locked: c.locked}
	c.mu.Unlock()
	c.sendJSON(protocol.NewMessage("status", st))
}

func (c *Client) sendAck(id string, err error) {
	ack := protocol.Ack{OK: err == nil}
	if err != nil {
		ack.Error = err.Error()
	}
	msg := protocol.NewMessage("ack", ack)
	msg.ID = id
	c.sendJSON(msg)
}

// RaiseHand sends a raise-hand event to the teacher.
func (c *Client) RaiseHand(on bool) {
	c.mu.Lock()
	c.raising = on
	c.mu.Unlock()
	c.sendJSON(protocol.NewMessage("raise_hand", protocol.RaiseHand{On: on}))
}

func (c *Client) sendChat(text string) {
	c.sendJSON(protocol.NewMessage("chat", protocol.Chat{From: c.cfg.Name, Text: text}))
}

func (c *Client) sendFileToTeacher(path, name string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	info := protocol.FileInfo{ID: transfer.NewID(), Name: name, Size: st.Size()}
	c.writeMu.Lock()
	err = transfer.SendFile(c.writeFrameLocked, info, f)
	c.writeMu.Unlock()
	if err != nil {
		log.Printf("student: send file %s: %v", name, err)
	}
}

func (c *Client) offlineMonitor() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case <-t.C:
		}
		if !c.cfg.RebootOnOffline {
			continue
		}
		c.mu.Lock()
		connected := c.conn != nil
		offline := connected && time.Since(c.lastRecv) > c.cfg.OfflineTimeout
		rebooting := c.pendingReboot
		c.mu.Unlock()

		if offline && !rebooting {
			c.mu.Lock()
			c.pendingReboot = true
			c.mu.Unlock()
			// Windows shows a system countdown dialog for this reboot; other
			// platforms reboot immediately.
			_ = c.ctrl.Power("reboot", 60, false)
		} else if !offline && rebooting {
			c.mu.Lock()
			c.pendingReboot = false
			c.mu.Unlock()
			_ = c.ctrl.Power("reboot", 0, true)
		}
	}
}

// ---- wire helpers ----

func (c *Client) writeFrame(typ byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.writeFrameLocked(typ, payload)
}

func (c *Client) writeFrameLocked(typ byte, payload []byte) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return errNotConnected
	}
	return protocol.WriteFrame(conn, typ, payload)
}

func (c *Client) sendJSON(msg protocol.Message) {
	raw, _ := json.Marshal(msg)
	_ = c.writeFrame(protocol.FrameJSON, raw)
}

// ---- local toolbar ----

func (c *Client) startToolbar() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, toolbarHTML)
	})
	mux.HandleFunc("/api/info", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		raising := c.raising
		muted := c.muted
		volume := c.volume
		blacked := c.blacked
		locked := c.locked
		teacherIP := c.teacherIP
		c.mu.Unlock()
		c.msgMu.Lock()
		last := c.lastTeacherMsg
		c.msgMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": c.cfg.Name, "ip": c.ip, "mac": c.mac,
			"teacher": teacherIP, "raising": raising, "muted": muted,
			"volume": volume, "blacked": blacked, "locked": locked,
			"lastMessage": last,
		})
	})
	mux.HandleFunc("/api/raise", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		c.mu.Lock()
		raising := !c.raising
		c.mu.Unlock()
		c.RaiseHand(raising)
		_, _ = io.WriteString(w, "ok")
	})
	mux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		text := strings.TrimSpace(r.FormValue("text"))
		if text == "" {
			http.Error(w, "empty", http.StatusBadRequest)
			return
		}
		c.sendChat(text)
		_, _ = io.WriteString(w, "ok")
	})
	mux.HandleFunc("/api/homework", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		f, hdr, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer f.Close()
		dir := filepath.Join(c.cfg.DataDir, "homework")
		_ = os.MkdirAll(dir, 0755)
		dest := filepath.Join(dir, filepath.Base(hdr.Filename))
		out, err := os.Create(dest)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = io.Copy(out, f)
		_ = out.Close()
		go c.sendFileToTeacher(dest, hdr.Filename)
		c.sendChat("已提交作业：" + hdr.Filename)
		_, _ = io.WriteString(w, "ok")
	})

	srv := &http.Server{Addr: toolbarAddr, Handler: mux}
	log.Printf("student: toolbar listening on http://%s", toolbarAddr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("student: toolbar: %v", err)
	}
}

// ---- network identity helpers ----

func hostname() string {
	h, _ := os.Hostname()
	return h
}

func macAddress() string {
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		if len(i.HardwareAddr) >= 6 {
			return i.HardwareAddr.String()
		}
	}
	return ""
}

func localIPv4() string {
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

const toolbarHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>天眼 · 学生端</title>
<style>
body{font-family:"Microsoft YaHei",sans-serif;max-width:520px;margin:40px auto;padding:0 16px;color:#1f2937}
h1{font-size:20px} .card{background:#fff;border:1px solid #e5e7eb;border-radius:12px;padding:16px;margin:12px 0}
button{background:#2563eb;color:#fff;border:0;border-radius:8px;padding:10px 16px;font-size:14px;cursor:pointer}
button.secondary{background:#6b7280} button.warn{background:#dc2626}
input[type=text],input[type=file]{width:100%;box-sizing:border-box;padding:8px;margin:6px 0;border:1px solid #d1d5db;border-radius:6px}
.row{display:flex;gap:8px;align-items:center;margin:8px 0}
#status{color:#6b7280;font-size:13px;white-space:pre-line}
</style></head><body>
<h1>🖥️ 天眼学生端</h1>
<div class="card" id="status">加载中…</div>
<div class="card">
<div class="row"><button id="raise">✋ 举手</button><button id="chat" class="secondary">💬 发消息</button></div>
<input id="msg" type="text" placeholder="输入要发给老师的话">
<div class="row"><input type="file" id="file"><button id="submit" class="secondary">📤 提交作业</button></div>
</div>
<script>
const $=id=>document.getElementById(id);
async function refresh(){
  try{const r=await fetch('/api/info');const d=await r.json();
    $('status').textContent='姓名：'+d.name+'\nIP：'+d.ip+'  MAC：'+d.mac+'\n教师机：'+(d.teacher||'未发现')+'\n举手：'+(d.raising?'是':'否')+'  静音：'+(d.muted?'是':'否')+'  音量：'+d.volume+'\n黑屏：'+(d.blacked?'是':'否')+'  锁定：'+(d.locked?'是':'否')+'\n老师消息：'+(d.lastMessage||'无');
  }catch(e){$('status').textContent='连接本地服务失败';}
}
$('raise').onclick=async()=>{await fetch('/api/raise',{method:'POST'});refresh()};
$('chat').onclick=async()=>{const t=$('msg').value.trim();if(!t)return;await fetch('/api/chat',{method:'POST',body:new URLSearchParams({text:t})});$('msg').value='';refresh()};
$('submit').onclick=async()=>{const f=$('file').files[0];if(!f)return;const fd=new FormData();fd.append('file',f);await fetch('/api/homework',{method:'POST',body:fd});$('file').value='';refresh()};
setInterval(refresh,2000);refresh();
</script></body></html>`
