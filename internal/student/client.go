// Package student implements the student-side client: auto-discovery,
// screen streaming, remote command execution, blocking policies and fully
// native interaction (island toolbar + tray icon, no browser).
package student

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"log"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"tianyan/internal/auth"
	"tianyan/internal/block"
	"tianyan/internal/capture"
	"tianyan/internal/control"
	"tianyan/internal/discovery"
	"tianyan/internal/display"
	"tianyan/internal/protocol"
	"tianyan/internal/transfer"
	"tianyan/internal/vnc"
)

// Version is the student client version.
// Version is the student client version; CI injects the release tag via
// -ldflags "-X tianyan/internal/student.Version=...".
var Version = "0.1.0"

var errNotConnected = errors.New("student: not connected to teacher")

// Config configures a student client.
type Config struct {
	Name             string
	StudentID        string
	Group            string
	TeacherIP        string // empty = auto-discover
	ServerPort       int    // teacher control TCP port
	DataDir          string
	AuthKey          string        // shared auth key (empty = no auth)
	VNCPort          int           // VNC (RFB) listener port, 0 = disabled
	DenyFeatures     string        // comma-separated feature groups to disable: audio,screen,program,power,rules,control,message
	AutoMute         bool          // mute when the teacher comes online
	RebootOnOffline  bool          // reboot when teacher heartbeat is lost
	OfflineTimeout   time.Duration // how long to wait before declaring offline
	NetDownReboot    bool          // lock screen + reboot when the network link goes down
	NetDownTimeout   time.Duration // link-down duration before triggering NetDownReboot
	ReconnectRestart time.Duration // restart the client process after failing to reconnect this long (0 = off)
}

// DefaultConfig returns a config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Name:             hostname(),
		ServerPort:       5555,
		DataDir:          filepath.Join(os.TempDir(), "tianyan"),
		VNCPort:          5900,
		AutoMute:         true,
		RebootOnOffline:  false,
		OfflineTimeout:   30 * time.Second,
		NetDownReboot:    true,
		NetDownTimeout:   3 * time.Second,
		ReconnectRestart: 5 * time.Minute,
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
	exempt     bool // rule enforcement paused by the teacher (reward)

	demoSess       *display.Session // active broadcast display (nil = none)
	demoFullscreen bool
	demoing        bool

	authNonce    string
	authVerified bool
	deny         map[string]bool

	lastRecv      time.Time
	pendingReboot bool

	// reconnect tracking: only a client that has been connected before may
	// restart itself after losing the teacher (prevents boot-time restart
	// loops when the teacher machine is simply switched off).
	everConnected  bool
	lastDisconnect time.Time

	// Own-screen broadcast request state machine: "" (none) → "pending" →
	// "approved" (teacher started the relay) / "denied" (teacher refused or
	// stopped the broadcast).
	broadcastState string
	broadcastMsg   string

	stopCh chan struct{}

	msgMu          sync.Mutex
	lastTeacherMsg string

	// Hooks used by the docked toolbar (see cmd/student/main.go).
	OnUpdate        func() // any user-visible state changed; refresh the bar text
	OnDemoing       func(active bool)
	OnOverlay       func(on bool) // lock screen (black screen / input lock) active
	OnConnState     func(online bool)
	OnRestartClient func() // reconnect watchdog: rebuild the client process
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
	c.deny = parseDeny(cfg.DenyFeatures)
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
	go c.netWatch()
	go c.reconnectWatch()
	go c.enforcer.AppLoop(c.stopCh, 2*time.Second)
	go c.startVNC()
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
				User:      currentUser(),
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

	// 共享密钥认证：向教师端发起挑战，验证教师端身份。
	c.mu.Lock()
	c.authNonce = ""
	c.authVerified = c.cfg.AuthKey == ""
	c.mu.Unlock()
	if c.cfg.AuthKey != "" {
		nonce := auth.Nonce()
		c.mu.Lock()
		c.authNonce = nonce
		c.mu.Unlock()
		c.sendJSON(protocol.NewMessage("auth_challenge", protocol.Auth{Nonce: nonce}))
		go func(conn net.Conn) {
			time.Sleep(5 * time.Second)
			c.mu.Lock()
			pending := c.authNonce != ""
			c.mu.Unlock()
			if pending {
				_ = conn.Close()
			}
		}(conn)
	}

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

	c.notifyConnState(true)
	c.readLoop(conn) // blocks until the connection closes

	close(done)
	c.closeDemo() // teacher gone = broadcast over
	c.setBroadcastState("", "与教师断开连接")
	c.mu.Lock()
	c.conn = nil
	c.everConnected = true
	c.lastDisconnect = time.Now()
	c.fullWanted = false
	c.mu.Unlock()
	_ = conn.Close()
	c.notifyConnState(false)
}

func (c *Client) buildHello() protocol.Hello {
	w, h := c.cap.ScreenSize()
	return protocol.Hello{
		Name:      c.cfg.Name,
		Hostname:  c.hostname,
		IP:        c.ip,
		MAC:       c.mac,
		OS:        runtime.GOOS,
		User:      currentUser(),
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
		case protocol.FrameImage:
			// Broadcast frames from the teacher (ImageDemo); feed the
			// display session if one is open.
			if len(payload) >= 1 && payload[0] == protocol.ImageDemo {
				c.mu.Lock()
				sess := c.demoSess
				c.mu.Unlock()
				if sess != nil {
					sess.Update(payload[1:])
				}
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

var featureGroups = map[string]string{
	"mute":         "audio",
	"volume":       "audio",
	"black":        "screen",
	"lock_input":   "screen",
	"demo":         "broadcast",
	"screenshot":   "screen",
	"open":         "program",
	"close":        "program",
	"power":        "power",
	"blocklist":    "rules",
	"block_exempt": "rules",
	"input":        "control",
	"chat":         "message",
}

func parseDeny(s string) map[string]bool {
	m := map[string]bool{}
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f != "" {
			m[f] = true
		}
	}
	return m
}

// allowed reports whether a message type is permitted under the current
// feature access-control policy.
func (c *Client) allowed(msgType string) bool {
	f := featureGroups[msgType]
	if f == "" {
		return true
	}
	return !c.deny[f]
}

func (c *Client) handleMessage(msg protocol.Message) {
	if c.cfg.AuthKey != "" {
		c.mu.Lock()
		verified := c.authVerified
		c.mu.Unlock()
		if !verified && msg.Type != "auth_challenge" && msg.Type != "auth_response" {
			return
		}
	}
	if !c.allowed(msg.Type) {
		c.sendAck(msg.ID, errors.New("该功能已被管理员禁用"))
		return
	}
	switch msg.Type {
	case "auth_challenge":
		var a protocol.Auth
		_ = json.Unmarshal(msg.Data, &a)
		c.sendJSON(protocol.NewMessage("auth_response", protocol.Auth{Token: auth.Token(c.cfg.AuthKey, a.Nonce)}))

	case "auth_response":
		var a protocol.Auth
		_ = json.Unmarshal(msg.Data, &a)
		c.mu.Lock()
		nonce := c.authNonce
		c.mu.Unlock()
		if nonce != "" && auth.Verify(c.cfg.AuthKey, nonce, a.Token) {
			c.mu.Lock()
			c.authNonce = ""
			c.authVerified = true
			c.mu.Unlock()
		} else {
			log.Printf("student: teacher authentication failed")
			c.mu.Lock()
			conn := c.conn
			c.mu.Unlock()
			if conn != nil {
				_ = conn.Close()
			}
		}

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
		c.sendAck(msg.ID, c.applyLockScreen(b.On, b.Text, "", ""))

	case "lock_input":
		var l protocol.LockInput
		_ = json.Unmarshal(msg.Data, &l)
		c.sendAck(msg.ID, c.applyLockScreen(l.On, "输入已锁定，请保持安静", "", ""))

	case "lockscreen":
		var ls protocol.LockScreen
		_ = json.Unmarshal(msg.Data, &ls)
		c.sendAck(msg.ID, c.applyLockScreen(ls.On, ls.Text, ls.Wallpaper, ls.Color))

	case "demo":
		var d protocol.Demo
		_ = json.Unmarshal(msg.Data, &d)
		c.applyDemo(d, msg.ID)

	case "screenshot":
		fr, err := c.cap.Capture(0, 85)
		if err == nil {
			payload := append([]byte{protocol.ImageShot}, fr.JPEG...)
			err = c.writeFrame(protocol.FrameImage, payload)
		}
		c.sendAck(msg.ID, err)

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

	case "block_exempt":
		var be protocol.BlockExempt
		_ = json.Unmarshal(msg.Data, &be)
		if err := c.enforcer.SetExempt(be.On); err != nil {
			log.Printf("student: block exempt: %v", err)
		}
		c.mu.Lock()
		c.exempt = be.On
		c.mu.Unlock()
		c.sendStatus()
		c.sendAck(msg.ID, nil)

	case "chat":
		var ch protocol.Chat
		_ = json.Unmarshal(msg.Data, &ch)
		c.msgMu.Lock()
		c.lastTeacherMsg = ch.Text
		c.msgMu.Unlock()
		c.notifyUpdate()

	case "broadcast_result":
		var r protocol.BroadcastResult
		_ = json.Unmarshal(msg.Data, &r)
		c.mu.Lock()
		if r.Approved {
			c.broadcastState = "approved"
			c.broadcastMsg = "正在广播你的屏幕…"
		} else {
			c.broadcastState = ""
			c.broadcastMsg = r.Reason
		}
		c.mu.Unlock()
		c.notifyUpdate()

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

// RequestBroadcast asks the teacher for permission to share this screen
// with the class. The teacher must approve before the relay starts.
func (c *Client) RequestBroadcast() {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		c.setBroadcastState("", "未连接教师机，无法发起广播请求")
		return
	}
	c.mu.Lock()
	busy := c.broadcastState == "pending" || c.broadcastState == "approved"
	c.mu.Unlock()
	if busy {
		return
	}
	c.setBroadcastState("pending", "广播请求已发送，等待教师批准…")
	c.sendJSON(protocol.NewMessage("broadcast_request", struct{}{}))
}

// setBroadcastState updates the request state and notifies the toolbar hook.
func (c *Client) setBroadcastState(state, text string) {
	c.mu.Lock()
	c.broadcastState = state
	c.broadcastMsg = text
	c.mu.Unlock()
	c.notifyUpdate()
}

// notifyUpdate tells the toolbar hook to refresh its text immediately.
func (c *Client) notifyUpdate() {
	if c.OnUpdate != nil {
		go c.OnUpdate()
	}
}

// StatusLine returns the one-line status shown on the student dock bar.
func (c *Client) StatusLine() string {
	c.mu.Lock()
	online := c.conn != nil
	state := c.broadcastState
	raising := c.raising
	blacked := c.blacked
	teacher := c.teacherIP
	c.mu.Unlock()
	c.msgMu.Lock()
	lastMsg := c.lastTeacherMsg
	c.msgMu.Unlock()
	if !online {
		return "未连接教师机"
	}
	switch state {
	case "pending":
		return "广播请求等待教师批准…"
	case "approved":
		return "正在广播你的屏幕"
	}
	if raising {
		return "已举手，等待老师回应"
	}
	if blacked {
		return "屏幕已锁定（黑屏）"
	}
	if lastMsg != "" {
		return "老师：" + lastMsg
	}
	if teacher != "" {
		return "已连接教师机 " + teacher
	}
	return "已连接教师机"
}

// notifyOverlay reports whether a black-screen/input-lock overlay is active
// so the docked toolbar can hide itself beneath it.
func (c *Client) notifyOverlay() {
	if c.OnOverlay == nil {
		return
	}
	c.mu.Lock()
	on := c.blacked || c.locked
	c.mu.Unlock()
	go c.OnOverlay(on)
}

// notifyDemoing reports broadcast-receiving state so the toolbar can hide.
func (c *Client) notifyDemoing(active bool) {
	if c.OnDemoing == nil {
		return
	}
	go c.OnDemoing(active)
}

// applyLockScreen shows/hides the merged lock overlay and keeps the
// blacked/locked status in step (legacy status fields report both true
// while the lock screen is up). The wallpaper must be a bare file name;
// it is resolved inside the student data directory. Color names the
// message color ("" = white).
func (c *Client) applyLockScreen(on bool, text, image, color string) error {
	imageName := ""
	if image != "" {
		imageName = filepath.Base(image) // no path traversal
	}
	if err := c.ctrl.LockScreen(on, text, filepath.Join(c.cfg.DataDir, imageName), color); err != nil {
		return err
	}
	c.mu.Lock()
	c.blacked = on
	c.locked = on
	c.mu.Unlock()
	c.sendStatus()
	c.notifyOverlay()
	return nil
}

// applyDemo starts/stops the broadcast display session. Fullscreen sessions
// additionally swallow student input; window sessions may be closed by the
// student, which only stops the local display until the next demo command.
func (c *Client) applyDemo(d protocol.Demo, msgID string) {
	if !d.On {
		c.closeDemo()
		c.sendAck(msgID, nil)
		return
	}
	if !display.Supported() {
		c.sendAck(msgID, errors.New("该系统暂不支持屏幕广播显示"))
		return
	}
	c.mu.Lock()
	if c.demoSess != nil && c.demoFullscreen == d.Fullscreen {
		c.mu.Unlock()
		c.sendAck(msgID, nil) // already showing this mode; frames keep flowing
		return
	}
	c.mu.Unlock()
	// Lift a black-screen overlay so the broadcast window is visible.
	if c.blacked {
		_ = c.ctrl.BlackScreen(false, "")
		c.mu.Lock()
		c.blacked = false
		c.mu.Unlock()
		c.sendStatus()
	}
	sess, err := display.NewSession("天眼 · 屏幕广播", d.Fullscreen)
	if err != nil {
		log.Printf("student: demo session error: %v", err)
		c.sendAck(msgID, err)
		return
	}
	sess.OnClosed = func() {
		c.mu.Lock()
		if c.demoSess == sess {
			c.demoSess = nil
			c.demoing = false
		}
		c.mu.Unlock()
		c.sendStatus()
	}
	c.mu.Lock()
	old := c.demoSess
	c.demoSess = sess
	c.demoFullscreen = d.Fullscreen
	c.demoing = true
	c.mu.Unlock()
	if old != nil {
		old.Close()
	}
	c.notifyDemoing(true)
	c.sendStatus()
	c.sendAck(msgID, nil)
}

// closeDemo stops the display session and clears the demo state. It must
// not be called while holding c.mu (session Close may invoke OnClosed,
// which locks c.mu from the window goroutine).
func (c *Client) closeDemo() {
	c.mu.Lock()
	sess := c.demoSess
	c.mu.Unlock()
	if sess != nil {
		sess.Close()
	}
	c.mu.Lock()
	c.demoSess = nil
	c.demoing = false
	c.mu.Unlock()
	c.notifyDemoing(false)
	c.sendStatus()
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
		demoing := c.demoing
		conn := c.conn
		c.mu.Unlock()
		if conn == nil || demoing {
			// Thumbnails pause while a broadcast is displayed (Veyon's
			// SlowDownThumbnailUpdates behavior) to save bandwidth.
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
	st := protocol.Status{
		Muted:   c.muted,
		Volume:  c.volume,
		Blacked: c.blacked,
		Locked:  c.locked,
		Demoing: c.demoing,
		Exempt:  c.exempt,
	}
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
	c.notifyUpdate()
}

// ToggleRaiseHand flips the raise-hand state (docked-bar button).
func (c *Client) ToggleRaiseHand() {
	c.mu.Lock()
	on := !c.raising
	c.mu.Unlock()
	c.RaiseHand(on)
}

// SendChatMessage delivers a student-to-teacher chat message from the
// native input box.
func (c *Client) SendChatMessage(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	c.sendChat(text)
}

// SendHomework pushes a local file to the teacher as submitted homework
// (picked with the native file dialog).
func (c *Client) SendHomework(path string) {
	name := filepath.Base(path)
	go c.sendFileToTeacher(path, name)
	c.sendChat("已提交作业：" + name)
}

// AdminInfo renders the multi-line detail shown in the tray-unlock status
// window (name/IP/MAC/teacher/state).
func (c *Client) AdminInfo() string {
	c.mu.Lock()
	teacher := c.teacherIP
	online := c.conn != nil
	blacked, locked := c.blacked, c.locked
	c.mu.Unlock()
	exempt := c.enforcer.Exempt()
	host, _ := os.Hostname()
	lines := []string{
		"名称：" + c.cfg.Name,
		"主机名：" + host,
		"IP：" + c.ip,
		"MAC：" + c.mac,
		"教师机：" + teacher,
		"连接：" + map[bool]string{true: "已连接", false: "未连接"}[online],
		"锁屏：" + map[bool]string{true: "是", false: "否"}[blacked || locked],
		"策略限制：" + map[bool]string{true: "已解除（奖励）", false: "生效中"}[exempt],
		"版本：" + Version,
	}
	return strings.Join(lines, "\n")
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

// netWatch guards against students unplugging the network cable: while the
// physical link is down, the screen locks (input blocked) and the system
// reboots after a countdown; if the link recovers within the countdown the
// reboot is aborted and the lock screen lifts.
func (c *Client) netWatch() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	var downSince time.Time
	pending := false
	for {
		select {
		case <-c.stopCh:
			return
		case <-t.C:
		}
		if !c.cfg.NetDownReboot {
			return
		}
		if networkLinkUp() {
			if pending {
				// Link recovered: abort the scheduled reboot and unlock.
				_ = c.ctrl.Power("reboot", 0, true)
				_ = c.applyLockScreen(false, "", "", "")
				pending = false
			}
			downSince = time.Time{}
			continue
		}
		if downSince.IsZero() {
			downSince = time.Now()
		}
		c.mu.Lock()
		conn := c.conn
		c.mu.Unlock()
		if conn != nil {
			continue // teacher connection alive; nothing to protect against
		}
		if time.Since(downSince) < c.cfg.NetDownTimeout {
			continue
		}
		if !pending {
			pending = true
			_ = c.applyLockScreen(true, "网络已断开，系统将在 60 秒后自动重启", "", "Red")
			// The OS reboot countdown dialog doubles as the warning prompt.
			_ = c.ctrl.Power("reboot", 60, false)
		}
	}
}

// reconnectWatch handles losing the teacher while the network link is
// still fine (server closed, network glitch on the teacher side...): the
// connect loop keeps retrying, and if the client fails to reconnect within
// ReconnectRestart it rebuilds the client process (guardian restart or
// self-respawn) instead of staying broken forever. Only a client that has
// connected at least once may restart this way, so a boot before the
// teacher is switched on never causes a restart loop.
func (c *Client) reconnectWatch() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case <-t.C:
		}
		if c.cfg.ReconnectRestart <= 0 {
			return
		}
		c.mu.Lock()
		conn := c.conn
		ever := c.everConnected
		since := c.lastDisconnect
		c.mu.Unlock()
		if conn != nil || !ever {
			continue
		}
		if time.Since(since) < c.cfg.ReconnectRestart {
			continue
		}
		log.Printf("student: failed to reconnect for %v; restarting client process", c.cfg.ReconnectRestart)
		if c.OnRestartClient != nil {
			go c.OnRestartClient()
		}
		return
	}
}

// Shutdown stops the client and releases OS resources (lock overlay,
// audio, dialogs). Called before the process self-restarts.
func (c *Client) Shutdown() {
	c.Close()
	time.Sleep(300 * time.Millisecond) // let Run's ctrl cleanup finish
	_ = c.ctrl.Close()
}

// networkLinkUp reports whether any non-loopback interface is up with a
// usable IPv4 address.
func networkLinkUp() bool {
	ifs, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := i.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil && !ipnet.IP.IsLinkLocalUnicast() {
				return true
			}
		}
	}
	return false
}

func (c *Client) notifyConnState(online bool) {
	if c.OnConnState != nil {
		go c.OnConnState(online)
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

// startVNC exposes the student screen via the RFB protocol on cfg.VNCPort.
func (c *Client) startVNC() {
	if c.cfg.VNCPort <= 0 {
		return
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", c.cfg.VNCPort))
	if err != nil {
		log.Printf("student: vnc: %v", err)
		return
	}
	log.Printf("student: VNC listening on :%d", c.cfg.VNCPort)
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go vnc.ServeConn(conn, &vncHandler{c: c})
	}
}

type vncHandler struct {
	c *Client
}

func (h *vncHandler) ScreenSize() (int, int)           { return h.c.cap.ScreenSize() }
func (h *vncHandler) CaptureRaw() (*image.RGBA, error) { return h.c.cap.CaptureRaw() }
func (h *vncHandler) MouseMove(x, y int)               { _ = h.c.ctrl.MouseMove(x, y) }
func (h *vncHandler) MouseButton(b string, d bool)     { _ = h.c.ctrl.MouseButton(b, d) }
func (h *vncHandler) MouseScroll(dy int)               { _ = h.c.ctrl.MouseScroll(dy) }
func (h *vncHandler) Key(k string, d bool)             { _ = h.c.ctrl.Key(k, d) }

// ---- network identity helpers ----

func hostname() string {
	h, _ := os.Hostname()
	return h
}

// currentUser returns the name of the user running the client (domain
// prefix stripped), mirroring Veyon's "logged in user" column.
func currentUser() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	name := u.Username
	if i := strings.LastIndexByte(name, '\\'); i >= 0 {
		name = name[i+1:]
	}
	return name
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
