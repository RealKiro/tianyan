//go:build windows

package tray

import (
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// One tray icon per process, letting the wndProc callback find the active
// icon without per-callback context plumbing — same as internal/display
// and internal/dock.
var (
	regMu      sync.Mutex
	curSession *Icon

	cbOnce  sync.Once
	cbWndPr uintptr

	buttonClassName = func() *uint16 { // unused here; kept for symmetry with dock
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
	procDestroyWindow      = user32.NewProc("DestroyWindow")
	procRegisterWindowMsgW = user32.NewProc("RegisterWindowMessageW")
	procShellNotifyIconW   = user32.NewProc("Shell_NotifyIconW")
	procGetKeyState        = user32.NewProc("GetKeyState")
	procLoadCursorW        = user32.NewProc("LoadCursorW")
	procCreateIcon         = user32.NewProc("CreateIcon")
	procGetClientRect      = user32.NewProc("GetClientRect")
	procGetWindowTextW     = user32.NewProc("GetWindowTextW")
	procSetWindowTextW     = user32.NewProc("SetWindowTextW")
	procSetFocus           = user32.NewProc("SetFocus")
	procCreateSolidBrush   = gdi32.NewProc("CreateSolidBrush")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procFillRect           = user32.NewProc("FillRect")
)

const (
	wsChild   = 0x40000000
	wsVisible = 0x10000000
	wsCaption = 0x00C00000
	wsSysmenu = 0x00080000
	wsBorder  = 0x00800000
	wsTabstop = 0x00010000
	wsGroup   = 0x00020000

	esPassword    = 0x0020
	esAutohscroll = 0x0080

	wmDestroy    = 0x0002
	wmErasebkgnd = 0x0014
	wmCommand    = 0x0111

	wmTrayCB    = 0x8500 // NOTIFYICONDATA.uCallbackMessage (WM_APP range)
	wmLBtnDblCl = 0x0203
	wmAppClose  = 0x8001

	nimAdd    = 0
	nimModify = 1
	nimDelete = 2

	nifMessage = 0x01
	nifIcon    = 0x02
	nifTip     = 0x04

	vkShift = 0x10

	idcArrow = 32512

	idEdit   = 101
	idOk     = 102
	idCancel = 103

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

// notifyIconData mirrors NOTIFYICONDATAW up to szTip (the legacy layout),
// which is all a plain icon + tooltip + callback needs.
type notifyIconData struct {
	cbSize           uint32
	_                uint32
	hwnd             uintptr
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	_                uint32
	hIcon            uintptr
	szTip            [128]uint16
}

// Icon is a tray icon on Windows.
type Icon struct {
	opts           Options
	hwnd           uintptr // message-only window
	dlgHwnd        uintptr // unlock password dialog (while open)
	editHwnd       uintptr
	normalIcon     uintptr
	grayIcon       uintptr
	taskbarCreated uint32
	gray           atomic.Bool
	uid            uint32

	mu        sync.Mutex
	nid       notifyIconData
	doneCh    chan struct{}
	closeOnce sync.Once
	doneOnce  sync.Once
}

// Supported reports whether the platform can show tray icons.
func Supported() bool { return true }

// New adds the tray icon and starts its message pump. Only one icon per
// process; Close removes it (normally the icon lives with the process).
func New(opts Options) (*Icon, error) {
	regMu.Lock()
	if curSession != nil {
		regMu.Unlock()
		return nil, syscall.EBUSY
	}
	t := &Icon{opts: opts, uid: 1}
	cb := callbacks()
	curSession = t
	regMu.Unlock()

	errCh := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := t.init(cb); err != nil {
			regMu.Lock()
			if curSession == t {
				curSession = nil
			}
			regMu.Unlock()
			errCh <- err
			return
		}
		errCh <- nil
		t.pumpLoop()
	}()
	if err := <-errCh; err != nil {
		return nil, err
	}
	return t, nil
}

// SetGray switches between the normal (connected) and gray (disconnected)
// icon. Thread-safe.
func (t *Icon) SetGray(on bool) {
	if t == nil || t.gray.Load() == on {
		return
	}
	t.gray.Store(on)
	t.mu.Lock()
	nid := t.nid
	t.mu.Unlock()
	if nid.hwnd == 0 {
		return
	}
	nid.hIcon = t.pickIcon()
	nid.uFlags = nifIcon
	procShellNotifyIconW.Call(uintptr(nimModify), uintptr(unsafe.Pointer(&nid)))
}

// Close removes the tray icon. Thread-safe and idempotent.
func (t *Icon) Close() {
	if t == nil {
		return
	}
	t.closeOnce.Do(func() {
		if t.hwnd != 0 {
			procPostMessageW.Call(t.hwnd, wmAppClose, 0, 0)
		} else {
			t.finish()
		}
	})
	<-t.doneCh
}

func (t *Icon) pickIcon() uintptr {
	if t.gray.Load() {
		return t.grayIcon
	}
	return t.normalIcon
}

func (t *Icon) init(cb uintptr) error {
	hInstance, _, _ := procGetModuleHandleW.Call(0)
	hCursor, _, _ := procLoadCursorW.Call(0, uintptr(idcArrow))

	msgClass := wndclassex{
		cbSize:        uint32(unsafe.Sizeof(wndclassex{})),
		lpfnWndProc:   cb,
		hInstance:     hInstance,
		hCursor:       hCursor,
		lpszClassName: utf16Ptr("TianYanTrayMsg"),
	}
	if atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&msgClass))); atom == 0 && err != errClassAlreadyExists {
		return err
	}
	dlgClass := wndclassex{
		cbSize:        uint32(unsafe.Sizeof(wndclassex{})),
		lpfnWndProc:   cb,
		hInstance:     hInstance,
		hCursor:       hCursor,
		hbrBackground: 6, // COLOR_WINDOW+1
		lpszClassName: utf16Ptr("TianYanTrayDlg"),
	}
	if atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&dlgClass))); atom == 0 && err != errClassAlreadyExists {
		return err
	}

	t.normalIcon = makeIcon(0x2F, 0x6F, 0xB2) // console blue: connected
	t.grayIcon = makeIcon(0x96, 0x96, 0x96)   // gray: disconnected

	// Message-only window (parent HWND_MESSAGE == (HWND)-3) receives the
	// tray callback without ever appearing on screen.
	hwnd, _, _ := procCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(msgClass.lpszClassName)), 0, 0,
		0, 0, 0, 0, ^uintptr(2), 0, hInstance, 0)
	if hwnd == 0 {
		return syscall.EINVAL
	}
	t.hwnd = hwnd

	name, _ := syscall.UTF16PtrFromString("TaskbarCreated")
	if id, _, _ := procRegisterWindowMsgW.Call(uintptr(unsafe.Pointer(name))); id != 0 {
		t.taskbarCreated = uint32(id)
	}

	nid := notifyIconData{
		cbSize:           uint32(unsafe.Sizeof(notifyIconData{})),
		hwnd:             hwnd,
		uID:              t.uid,
		uFlags:           nifMessage | nifIcon | nifTip,
		uCallbackMessage: wmTrayCB,
		hIcon:            t.normalIcon,
	}
	copy(nid.szTip[:], utf16z(t.opts.Tooltip, 128))
	t.mu.Lock()
	t.nid = nid
	t.mu.Unlock()
	if ret, _, _ := procShellNotifyIconW.Call(uintptr(nimAdd), uintptr(unsafe.Pointer(&nid))); ret == 0 {
		return syscall.EINVAL
	}
	return nil
}

func utf16Ptr(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}

func utf16z(s string, max int) []uint16 {
	out := make([]uint16, max)
	n := 0
	for _, r := range s {
		if n >= max-1 {
			break
		}
		if r < 0x10000 {
			out[n] = uint16(r)
			n++
		}
	}
	out[n] = 0
	return out
}

func (t *Icon) pumpLoop() {
	var m msg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		switch int64(ret) {
		case 0, -1:
			t.finish()
			return
		default:
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
	}
}

func (t *Icon) finish() {
	t.doneOnce.Do(func() {
		t.mu.Lock()
		nid := t.nid
		t.mu.Unlock()
		if nid.hwnd != 0 {
			procShellNotifyIconW.Call(uintptr(nimDelete), uintptr(unsafe.Pointer(&nid)))
		}
		regMu.Lock()
		if curSession == t {
			curSession = nil
		}
		regMu.Unlock()
		close(t.doneCh)
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
	t := curSession
	regMu.Unlock()
	if t == nil {
		return defProc(hwnd, umsg, wparam, lparam)
	}
	if hwnd == t.dlgHwnd {
		return t.dialogProc(hwnd, umsg, wparam, lparam)
	}
	switch umsg {
	case wmTrayCB:
		t.trayEvent(lparam)
		return 0
	case uintptr(t.taskbarCreated):
		// Explorer restarted: re-add the icon.
		t.mu.Lock()
		nid := t.nid
		t.mu.Unlock()
		nid.hIcon = t.pickIcon()
		procShellNotifyIconW.Call(uintptr(nimAdd), uintptr(unsafe.Pointer(&nid)))
		return 0
	case wmAppClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	return defProc(hwnd, umsg, wparam, lparam)
}

// trayEvent handles mouse activity on the tray icon. Everything is
// swallowed except Shift+double-click, which opens the password dialog —
// students must not be able to operate or exit the client from the icon.
func (t *Icon) trayEvent(lparam uintptr) {
	if uint32(lparam) != wmLBtnDblCl {
		return
	}
	state, _, _ := procGetKeyState.Call(uintptr(vkShift))
	if state&0x8000 == 0 {
		return // plain double click: ignored
	}
	t.openUnlockDialog()
}

// openUnlockDialog shows the admin password prompt (pump thread only).
func (t *Icon) openUnlockDialog() {
	if t.dlgHwnd != 0 {
		procSetFocus.Call(t.dlgHwnd)
		return
	}
	hInstance, _, _ := procGetModuleHandleW.Call(0)
	title := utf16Ptr("天眼 · 客户端管理")
	cls := utf16Ptr("TianYanTrayDlg")
	dlg, _, _ := procCreateWindowExW.Call(
		uintptr(0x00000001), // WS_EX_DLGMODALFRAME
		uintptr(unsafe.Pointer(cls)),
		uintptr(unsafe.Pointer(title)),
		uintptr(wsCaption|wsSysmenu|wsVisible),
		380, 260, 320, 140, 0, 0, hInstance, 0)
	if dlg == 0 {
		return
	}
	t.dlgHwnd = dlg
	mkChild := func(class, text string, style, x, y, w, h, id uintptr) uintptr {
		cp := utf16Ptr(class)
		tp := utf16Ptr(text)
		child, _, _ := procCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(cp)), uintptr(unsafe.Pointer(tp)),
			uintptr(wsChild|wsVisible|style),
			x, y, w, h, dlg, id, hInstance, 0)
		return child
	}
	mkChild("STATIC", "请输入管理密码（Shift+双击托盘图标进入）：", 0, 12, 12, 284, 20, 0)
	t.editHwnd = mkChild("EDIT", "", uintptr(esPassword|esAutohscroll|wsBorder|wsTabstop), 12, 38, 288, 24, idEdit)
	mkChild("BUTTON", "确定", uintptr(wsTabstop|wsGroup), 100, 74, 60, 26, idOk)
	mkChild("BUTTON", "取消", uintptr(wsTabstop), 170, 74, 60, 26, idCancel)
	procSetFocus.Call(t.editHwnd)
}

func (t *Icon) dialogProc(hwnd, umsg, wparam, lparam uintptr) uintptr {
	switch umsg {
	case wmCommand:
		if wparam>>16 != 0 { // not BN_CLICKED
			return 0
		}
		switch uint32(wparam) & 0xffff {
		case idOk:
			t.tryUnlock()
			return 0
		case idCancel:
			t.closeDialog(hwnd)
			return 0
		}
	case wmErasebkgnd:
		var rc rect
		procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
		brush, _, _ := procCreateSolidBrush.Call(0x00F2ECE7)
		procFillRect.Call(wparam, uintptr(unsafe.Pointer(&rc)), brush)
		procDeleteObject.Call(brush)
		return 1
	case wmDestroy:
		t.dlgHwnd = 0
		t.editHwnd = 0
		return 0
	}
	return defProc(hwnd, umsg, wparam, lparam)
}

func (t *Icon) tryUnlock() {
	buf := make([]uint16, 256)
	n, _, _ := procGetWindowTextW.Call(t.editHwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	pass := syscall.UTF16ToString(buf[:n])
	if t.opts.Verify != nil && t.opts.Verify(pass) {
		t.closeDialog(t.dlgHwnd)
		if t.opts.OnUnlock != nil {
			go t.opts.OnUnlock()
		}
		return
	}
	procSetWindowTextW.Call(t.editHwnd, uintptr(unsafe.Pointer(utf16Ptr(""))))
}

func (t *Icon) closeDialog(hwnd uintptr) {
	t.dlgHwnd = 0
	t.editHwnd = 0
	procDestroyWindow.Call(hwnd)
}

// makeIcon renders a 32×32 32bpp circle icon (BGRA pixels) as an HICON: a
// colored disc with a lighter ring — blue when connected, gray when not.
func makeIcon(r, g, b byte) uintptr {
	const size = 32
	xor := make([]byte, size*size*4)
	and := make([]byte, size*size/8) // all zero: per-pixel alpha decides
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)-15.5, float64(y)-15.5
			d2 := dx*dx + dy*dy
			i := (y*size + x) * 4
			switch {
			case d2 <= 9*9:
				xor[i], xor[i+1], xor[i+2], xor[i+3] = b, g, r, 255
			case d2 <= 12*12: // ring, slightly lighter
				xor[i], xor[i+1], xor[i+2], xor[i+3] = lighter(b), lighter(g), lighter(r), 255
			}
		}
	}
	h, _, _ := procCreateIcon.Call(0, uintptr(size), uintptr(size), 1, 32,
		uintptr(unsafe.Pointer(&and[0])), uintptr(unsafe.Pointer(&xor[0])))
	return h
}

func lighter(v byte) byte {
	d := int(v) + 40
	if d > 255 {
		d = 255
	}
	return byte(d)
}

func defProc(hwnd, umsg, wparam, lparam uintptr) uintptr {
	ret, _, _ := procDefWindowProcW.Call(hwnd, umsg, wparam, lparam)
	return ret
}
