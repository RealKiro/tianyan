//go:build windows

package dock

import (
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// One docked bar per process (either the teacher bar or the student bar),
// letting the wndProc callback find the active bar without per-callback
// context plumbing — same approach as internal/display.
var (
	regMu      sync.Mutex
	curSession *Bar

	cbOnce  sync.Once
	cbWndPr uintptr

	buttonClassName = func() *uint16 {
		p, _ := syscall.UTF16PtrFromString("BUTTON")
		return p
	}()
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procGetModuleHandleW   = kernel32.NewProc("GetModuleHandleW")
	procRegisterClassExW   = user32.NewProc("RegisterClassExW")
	procCreateWindowExW    = user32.NewProc("CreateWindowExW")
	procDefWindowProcW     = user32.NewProc("DefWindowProcW")
	procGetMessageW        = user32.NewProc("GetMessageW")
	procTranslateMessage   = user32.NewProc("TranslateMessage")
	procDispatchMessageW   = user32.NewProc("DispatchMessageW")
	procPostQuitMessage    = user32.NewProc("PostQuitMessage")
	procPostMessageW       = user32.NewProc("PostMessageW")
	procSendMessageW       = user32.NewProc("SendMessageW")
	procDestroyWindow      = user32.NewProc("DestroyWindow")
	procShowWindow         = user32.NewProc("ShowWindow")
	procSetWindowPos       = user32.NewProc("SetWindowPos")
	procMoveWindow         = user32.NewProc("MoveWindow")
	procInvalidateRect     = user32.NewProc("InvalidateRect")
	procBeginPaint         = user32.NewProc("BeginPaint")
	procEndPaint           = user32.NewProc("EndPaint")
	procGetClientRect      = user32.NewProc("GetClientRect")
	procGetSystemMetrics   = user32.NewProc("GetSystemMetrics")
	procGetCursorPos       = user32.NewProc("GetCursorPos")
	procSetTimer           = user32.NewProc("SetTimer")
	procKillTimer          = user32.NewProc("KillTimer")
	procCreateRoundRectRgn = gdi32.NewProc("CreateRoundRectRgn")
	procSetWindowRgn       = user32.NewProc("SetWindowRgn")
	procLoadCursorW        = user32.NewProc("LoadCursorW")
	procMessageBoxW        = user32.NewProc("MessageBoxW")
	procCreateSolidBrush   = gdi32.NewProc("CreateSolidBrush")
	procCreateFontW        = gdi32.NewProc("CreateFontW")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procSelectObject       = gdi32.NewProc("SelectObject")
	procSetBkMode          = gdi32.NewProc("SetBkMode")
	procSetTextColor       = gdi32.NewProc("SetTextColor")
	procDrawTextW          = gdi32.NewProc("DrawTextW")
	procFillRect           = user32.NewProc("FillRect")
)

const (
	wsPopup        = 0x80000000
	wsChild        = 0x40000000
	wsVisible      = 0x10000000
	wsExTopmost    = 0x00000008
	wsExToolWindow = 0x00000080

	hwndTopmost = ^uintptr(0) // (HWND)-1

	smCxscreen = 0
	smCyscreen = 1

	swHide = 0
	swShow = 5

	wmDestroy    = 0x0002
	wmSize       = 0x0005
	wmEnable     = 0x000A
	wmPaint      = 0x000F
	wmClose      = 0x0010
	wmErasebkgnd = 0x0014
	wmMouseMove  = 0x0200
	wmTimer      = 0x0113
	wmCommand    = 0x0111
	wmSetFont    = 0x0030

	wmAppClose   = 0x8001 // posted by Close(); always destroys
	wmAppSetText = 0x8002 // posted by SetLabel(); thread-safe label update
	wmAppShow    = 0x8003 // posted by Hide/Show

	timerCheck = 1 // hover-away poll timer id

	swpNozorder = 0x0004

	dtSingleline  = 0x0020
	dtVcenter     = 0x0004
	dtEndEllipsis = 0x8000

	defaultCharset  = 1
	cleartypeQualit = 5

	idcArrow = 32512

	// MessageBoxW flags
	mbYesno       = 0x04
	mbIconwarning = 0x30
	idYes         = 6

	errClassAlreadyExists = syscall.Errno(1410)
)

type wndclassex struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type msg struct {
	hwnd    uintptr
	message uint32
	_       uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	_       uint32
	pt      point
}

type point struct {
	x, y int32
}

type rect struct {
	left, top, right, bottom int32
}

type paintstruct struct {
	hdc         uintptr
	fErase      int32
	_           int32
	rcPaint     rect
	fRestore    int32
	_           int32
	fIncUpdate  int32
	_           int32
	rgbReserved [32]byte
}

type btnCtl struct {
	id   int
	w    int
	hwnd uintptr
}

// Bar is the Dynamic-Island-style floating toolbar on Windows: a rounded
// pill docked flush to the top edge of the screen that expands into the
// full toolbar when the mouse hovers over it and retracts automatically
// shortly after the mouse leaves.
type Bar struct {
	opts     Options
	hwnd     uintptr
	font     uintptr
	buttons  []btnCtl
	expanded bool
	barX     int
	barW     int
	barH     int
	outTicks int

	mu           sync.Mutex
	curLabel     string
	pendingLabel string
	labelX       int32 // label area start (after buttons)
	labelEnd     int32 // label area end (client width minus margin)

	doneCh    chan struct{}
	closeOnce sync.Once
	doneOnce  sync.Once
	closed    atomic.Bool
}

// Supported reports whether the platform can show docked toolbars.
func Supported() bool { return true }

// NewBar creates and shows the island toolbar. Only one bar may be active
// per process; it lives with the process until Close is called.
func NewBar(opts Options) (*Bar, error) {
	regMu.Lock()
	if curSession != nil {
		regMu.Unlock()
		return nil, syscall.EBUSY
	}
	b := &Bar{opts: opts, doneCh: make(chan struct{})}
	cb := callbacks()
	curSession = b
	regMu.Unlock()

	// Win32 windows must be created and pumped on the same OS thread.
	errCh := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := b.openWindow(cb); err != nil {
			regMu.Lock()
			if curSession == b {
				curSession = nil
			}
			regMu.Unlock()
			errCh <- err
			return
		}
		errCh <- nil
		b.pumpLoop()
	}()
	if err := <-errCh; err != nil {
		return nil, err
	}
	return b, nil
}

// SetLabel updates the status text shown on the bar. Thread-safe.
func (b *Bar) SetLabel(text string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.pendingLabel = text
	b.mu.Unlock()
	if b.hwnd != 0 {
		procPostMessageW.Call(b.hwnd, wmAppSetText, 0, 0)
	}
}

// SetEnabled enables/disables a toolbar button by id (used to gray out
// student actions while disconnected from the teacher). Thread-safe.
func (b *Bar) SetEnabled(id int, enabled bool) {
	if b == nil {
		return
	}
	flag := uintptr(0)
	if enabled {
		flag = 1
	}
	for _, btn := range b.buttons {
		if btn.id == id && btn.hwnd != 0 {
			procPostMessageW.Call(btn.hwnd, wmEnable, flag, 0)
		}
	}
}

// Hide conceals the bar (used on student machines while a broadcast or
// lock screen covers the desktop). Thread-safe.
func (b *Bar) Hide() { b.postShow(false) }

// Show re-reveals the bar. Thread-safe.
func (b *Bar) Show() { b.postShow(true) }

func (b *Bar) postShow(show bool) {
	if b == nil || b.hwnd == 0 {
		return
	}
	flag := uintptr(0)
	if show {
		flag = 1
	}
	procPostMessageW.Call(b.hwnd, wmAppShow, flag, 0)
}

// Close tears down the bar. Thread-safe and idempotent.
func (b *Bar) Close() {
	if b == nil {
		return
	}
	b.closeOnce.Do(func() {
		if b.hwnd != 0 {
			procPostMessageW.Call(b.hwnd, wmAppClose, 0, 0)
		} else {
			b.finish()
		}
	})
	<-b.doneCh
}

// MessageBox asks a yes/no question and reports whether the user answered
// yes.
func MessageBox(title, text string) bool {
	mb, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		return false
	}
	mt, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return false
	}
	ret, _, _ := procMessageBoxW.Call(0,
		uintptr(unsafe.Pointer(mb)), uintptr(unsafe.Pointer(mt)),
		uintptr(mbYesno|mbIconwarning))
	return ret == idYes
}

func (b *Bar) barHeight() int {
	if b.opts.Teacher {
		return 40
	}
	return 32
}

// expandedWidth/collapsedWidth define the two island sizes. The expanded
// bar floats centered near the top; the collapsed pill hugs the edge.
func (b *Bar) expandedWidth() int {
	scrW := int(b.screenW())
	if b.opts.Teacher {
		w := 900
		if scrW-40 < w {
			w = scrW - 40
		}
		if w < 320 {
			w = scrW
		}
		return w
	}
	w := 480
	if scrW-40 < w {
		w = scrW - 40
	}
	if w < 240 {
		w = scrW
	}
	return w
}

func (b *Bar) collapsedWidth() int {
	if b.opts.Teacher {
		return 210
	}
	return 150
}

func (b *Bar) screenW() uintptr {
	w, _, _ := procGetSystemMetrics.Call(uintptr(smCxscreen))
	return w
}

// openWindow registers the class and creates the island window plus its
// child buttons. Must run on the locked pump thread.
func (b *Bar) openWindow(cb uintptr) error {
	className, err := syscall.UTF16PtrFromString("TianYanDockBar")
	if err != nil {
		return err
	}
	hInstance, _, _ := procGetModuleHandleW.Call(0)
	hCursor, _, _ := procLoadCursorW.Call(0, uintptr(idcArrow))
	wc := wndclassex{
		cbSize:      uint32(unsafe.Sizeof(wndclassex{})),
		lpfnWndProc: cb,
		hInstance:   hInstance,
		hCursor:     hCursor,
		// No background brush: WM_ERASEBKGND fills the island color.
		hbrBackground: 0,
		lpszClassName: className,
	}
	if atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
		if err != errClassAlreadyExists {
			return err
		}
	}
	if hFont := b.createFont(); hFont != 0 {
		b.font = hFont
	}

	b.barH = b.barHeight()
	b.expanded = true
	title, err := syscall.UTF16PtrFromString(b.opts.Title)
	if err != nil {
		title = className
	}
	ex := uintptr(wsExTopmost | wsExToolWindow)
	style := uintptr(wsPopup | wsVisible)
	b.barX = (int(b.screenW()) - b.expandedWidth()) / 2
	hwnd, _, _ := procCreateWindowExW.Call(ex, uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)), style,
		uintptr(b.barX), 0, uintptr(b.expandedWidth()), uintptr(b.barH),
		0, 0, hInstance, 0)
	if hwnd == 0 {
		return syscall.EINVAL
	}
	b.hwnd = hwnd
	b.createChildren(hInstance)
	b.applyRegion()
	b.relayout()
	return nil
}

func (b *Bar) createFont() uintptr {
	face, _ := syscall.UTF16PtrFromString("Microsoft YaHei UI")
	height := int32(-18) // negative = character height
	h, _, _ := procCreateFontW.Call(
		uintptr(height), 0, 0, 0, 400, 0, 0, 0,
		uintptr(defaultCharset), 0, 0, uintptr(cleartypeQualit), 0,
		uintptr(unsafe.Pointer(face)))
	return h
}

// createChildren makes the child BUTTON controls.
func (b *Bar) createChildren(hInstance uintptr) {
	add := func(id int, label string) {
		lp, err := syscall.UTF16PtrFromString(label)
		if err != nil {
			return
		}
		h, _, _ := procCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(buttonClassName)),
			uintptr(unsafe.Pointer(lp)),
			uintptr(wsChild|wsVisible), // BS_PUSHBUTTON == 0
			0, 0, 10, 10,               // position fixed by relayout
			b.hwnd, uintptr(int32(id)), hInstance, 0)
		if h == 0 {
			return
		}
		if b.font != 0 {
			procSendMessageW.Call(h, uintptr(wmSetFont), b.font, 1)
		}
		b.buttons = append(b.buttons, btnCtl{id: id, w: btnWidth(label), hwnd: h})
	}
	for _, btn := range b.opts.Buttons {
		add(btn.ID, btn.Label)
	}
}

func btnWidth(label string) int {
	n := 0
	for _, r := range label {
		if r > 0x2E80 { // CJK and friends render wide
			n += 2
		} else {
			n++
		}
	}
	w := n*8 + 20
	if w < 30 {
		w = 30
	}
	return w
}

// applyRegion rounds the island corners (Dynamic-Island look).
func (b *Bar) applyRegion() {
	rgn, _, _ := procCreateRoundRectRgn.Call(0, 0,
		uintptr(b.barW+1), uintptr(b.barH+1), 22, 22)
	procSetWindowRgn.Call(b.hwnd, rgn, 1)
}

// applySize resizes/moves the island (centered horizontally) and refreshes
// layout + region. Must run on the pump thread.
func (b *Bar) applySize(w int) {
	scrW := int(b.screenW())
	if w > scrW {
		w = scrW
	}
	b.barX = (scrW - w) / 2
	b.barW = w
	procSetWindowPos.Call(b.hwnd, 0, uintptr(b.barX), 0, uintptr(w), uintptr(b.barH), uintptr(swpNozorder))
	b.applyRegion()
	b.relayout()
	procInvalidateRect.Call(b.hwnd, 0, 0)
}

// setExpanded switches between the full toolbar (hover) and the compact
// pill (auto-hidden at the edge).
func (b *Bar) setExpanded(on bool) {
	if b.expanded == on {
		return
	}
	b.expanded = on
	b.outTicks = 0
	if on {
		procSetTimer.Call(b.hwnd, uintptr(timerCheck), 300, 0)
		b.applySize(b.expandedWidth())
	} else {
		procKillTimer.Call(b.hwnd, uintptr(timerCheck))
		b.applySize(b.collapsedWidth())
	}
}

// checkCursor collapses the island once the cursor has stayed outside for
// a few polls (~1.2s), so a fleeting exit does not snap it shut.
func (b *Bar) checkCursor() {
	if !b.expanded {
		return
	}
	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	inside := pt.x >= int32(b.barX) && pt.x < int32(b.barX+b.barW) && pt.y >= -2 && pt.y < int32(b.barH)+4
	if inside {
		b.outTicks = 0
		return
	}
	b.outTicks++
	if b.outTicks >= 4 {
		b.setExpanded(false)
	}
}

// relayout repositions the child buttons and records where the label area
// starts/ends. Must run on the pump thread.
func (b *Bar) relayout() {
	cy := int32((b.barH - 28) / 2)
	if b.barH < 28 {
		cy = 2
	}
	x := int32(8)
	for _, btn := range b.buttons {
		if b.expanded {
			procMoveWindow.Call(btn.hwnd, uintptr(x), uintptr(cy), uintptr(btn.w), 28, 1)
			procShowWindow.Call(btn.hwnd, uintptr(swShow))
			x += int32(btn.w) + 4
		} else {
			procShowWindow.Call(btn.hwnd, uintptr(swHide))
		}
	}
	b.mu.Lock()
	if b.expanded {
		b.labelX = x + 6
		b.labelEnd = int32(b.barW) - 10
	} else {
		b.labelX = 12
		b.labelEnd = int32(b.barW) - 10
	}
	b.mu.Unlock()
}

func (b *Bar) pumpLoop() {
	var m msg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		switch int64(ret) {
		case 0, -1:
			b.finish()
			return
		default:
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
	}
}

func (b *Bar) finish() {
	b.doneOnce.Do(func() {
		b.closed.Store(true)
		regMu.Lock()
		if curSession == b {
			curSession = nil
		}
		regMu.Unlock()
		close(b.doneCh)
	})
}

func callbacks() uintptr {
	cbOnce.Do(func() {
		cbWndPr = syscall.NewCallback(wndProc)
	})
	return cbWndPr
}

func wndProc(hwnd, umsg, wparam, lparam uintptr) uintptr {
	regMu.Lock()
	b := curSession
	regMu.Unlock()
	if b == nil {
		return defProc(hwnd, umsg, wparam, lparam)
	}
	switch umsg {
	case wmCommand:
		if wparam>>16 != 0 { // notifications other than BN_CLICKED
			return 0
		}
		if b.opts.OnClick != nil {
			b.opts.OnClick(int(int32(uint32(wparam) & 0xffff)))
		}
		return 0
	case wmPaint:
		var ps paintstruct
		hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		b.paint(hdc)
		procEndPaint.Call(hdc, uintptr(unsafe.Pointer(&ps)))
		return 0
	case wmErasebkgnd:
		b.fillRectBG(wparam) // wparam is the erase HDC
		return 1
	case wmSize:
		b.barW = int(uint32(lparam) & 0xffff)
		b.barH = int((uint32(lparam) >> 16) & 0xffff)
		b.relayout()
		procInvalidateRect.Call(b.hwnd, 0, 0)
		return 0
	case wmMouseMove:
		// Hovering the pill expands the island; moving inside the expanded
		// bar keeps the away-timer reset.
		if !b.expanded {
			b.setExpanded(true)
		}
		b.outTicks = 0
		return 0
	case wmTimer:
		if wparam == uintptr(timerCheck) {
			b.checkCursor()
		}
		return 0
	case wmClose:
		// Alt+F4 on the borderless island must not kill it: retract to the
		// pill. The bar lives with the process.
		b.setExpanded(false)
		return 0
	case wmAppSetText:
		b.mu.Lock()
		b.curLabel = b.pendingLabel
		b.mu.Unlock()
		procInvalidateRect.Call(b.hwnd, 0, 0)
		return 0
	case wmAppShow:
		flag := uintptr(swShow)
		if wparam == 0 {
			flag = uintptr(swHide)
		}
		procShowWindow.Call(b.hwnd, flag)
		return 0
	case wmAppClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procKillTimer.Call(hwnd, uintptr(timerCheck))
		procPostQuitMessage.Call(0)
		return 0
	}
	return defProc(hwnd, umsg, wparam, lparam)
}

func defProc(hwnd, umsg, wparam, lparam uintptr) uintptr {
	ret, _, _ := procDefWindowProcW.Call(hwnd, umsg, wparam, lparam)
	return ret
}

func (b *Bar) fillRectBG(hdc uintptr) {
	var rc rect
	procGetClientRect.Call(b.hwnd, uintptr(unsafe.Pointer(&rc)))
	brush, _, _ := procCreateSolidBrush.Call(b.bgColor())
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), brush)
	procDeleteObject.Call(brush)
}

func (b *Bar) bgColor() uintptr {
	if b.opts.Teacher {
		return 0x00B26F2F // RGB(47,111,178) — console blue
	}
	return 0x00F2ECE7 // RGB(231,236,242)
}

func (b *Bar) textColor() uintptr {
	if b.opts.Teacher {
		return 0x00FFFFFF // white
	}
	return 0x0037291F // RGB(31,41,55)
}

func (b *Bar) paint(hdc uintptr) {
	var rc rect
	procGetClientRect.Call(b.hwnd, uintptr(unsafe.Pointer(&rc)))
	brush, _, _ := procCreateSolidBrush.Call(b.bgColor())
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), brush)
	procDeleteObject.Call(brush)

	procSetBkMode.Call(hdc, 1) // TRANSPARENT
	procSetTextColor.Call(hdc, b.textColor())
	procSelectObject.Call(hdc, b.font)

	b.mu.Lock()
	label := b.curLabel
	lx, lend := b.labelX, b.labelEnd
	b.mu.Unlock()
	if label == "" || lend <= lx {
		return
	}
	text, err := syscall.UTF16PtrFromString(label)
	if err != nil {
		return
	}
	area := rect{left: lx, top: 0, right: lend, bottom: int32(b.barH)}
	textLen := int32(-1) // -1: string is null-terminated
	procDrawTextW.Call(hdc, uintptr(unsafe.Pointer(text)), uintptr(textLen),
		uintptr(unsafe.Pointer(&area)),
		uintptr(dtSingleline|dtVcenter|dtEndEllipsis))
}
