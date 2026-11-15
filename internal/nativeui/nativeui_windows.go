//go:build windows

package nativeui

import (
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// One native dialog per process; the wndProc callback finds the active
// dialog through the package-level pointer — same approach as
// internal/display, internal/dock and internal/tray.
var (
	regMu      sync.Mutex
	curDialog  *dialog
	onceCb     sync.Once
	cbWndProc  uintptr
	utf16Blank = utf16Ptr("")
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	comdlg32 = windows.NewLazySystemDLL("comdlg32.dll")

	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procGetMessageW      = user32.NewProc("GetMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procGetWindowTextW   = user32.NewProc("GetWindowTextW")
	procSetWindowTextW   = user32.NewProc("SetWindowTextW")
	procSetFocus         = user32.NewProc("SetFocus")
	procLoadCursorW      = user32.NewProc("LoadCursorW")
	procGetClientRect    = user32.NewProc("GetClientRect")
	procSendMessageW     = user32.NewProc("SendMessageW")
	procGetOpenFileNameW = comdlg32.NewProc("GetOpenFileNameW")

	procCreateSolidBrush = gdi32.NewProc("CreateSolidBrush")
	procFillRect         = user32.NewProc("FillRect")
	procDeleteObject     = gdi32.NewProc("DeleteObject")
)

const (
	wsChild   = 0x40000000
	wsVisible = 0x10000000
	wsCaption = 0x00C00000
	wsSysmenu = 0x00080000
	wsBorder  = 0x00800000
	wsTabstop = 0x00010000

	esAutohscroll = 0x0080
	esMultiline   = 0x0004
	esReadonly    = 0x0800

	wmDestroy    = 0x0002
	wmErasebkgnd = 0x0014
	wmCommand    = 0x0111
	wmSetFont    = 0x0030

	idcArrow = 32512

	idEdit   = 101
	idOk     = 102
	idCancel = 103
	idText   = 104

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

// Supported reports whether native dialogs are available.
func Supported() bool { return true }

// InputBox shows a small dialog with a text field and delivers the entered
// text ("" on cancel) via cb. Non-blocking: the dialog lives on its own
// message-pump thread.
func InputBox(title, label string, cb func(text string)) {
	spawn(&dialog{
		kind:  kindInput,
		title: title,
		label: label,
		cb:    cb,
	})
}

// PickFile shows a native file-open dialog and delivers the selected path
// ("" on cancel) via cb. Blocking: intended to be called from a toolbar
// button handler, where a modal dialog is the natural behavior.
func PickFile(title string, cb func(path string)) {
	filter := utf16Ptr("所有文件 (*.*)\x00*.*\x00\x00")
	ofn := &openfilename{
		lStructSize: uint32(unsafe.Sizeof(openfilename{})),
		lpstrFilter: uintptr(unsafe.Pointer(filter)),
		nMaxFile:    32768,
	}
	buf := make([]uint16, 32768)
	ofn.lpstrFile = uintptr(unsafe.Pointer(&buf[0]))
	t, _ := syscall.UTF16PtrFromString(title)
	ofn.lpstrTitle = uintptr(unsafe.Pointer(t))
	ofn.Flags = 0x00000804 // OFN_PATHMUSTEXIST | OFN_FILEMUSTEXIST | OFN_HIDEREADONLY
	if ret, _, _ := procGetOpenFileNameW.Call(uintptr(unsafe.Pointer(ofn))); ret != 0 {
		cb(syscall.UTF16ToString(buf))
		return
	}
	cb("")
}

// StatusBox shows a read-only, multi-line information window.
func StatusBox(title, text string) {
	spawn(&dialog{
		kind:  kindStatus,
		title: title,
		label: text,
	})
}

const (
	kindInput  = 1
	kindStatus = 2
)

// dialog is the active native dialog (input box or status window).
type dialog struct {
	kind      int
	title     string
	label     string
	hwnd      uintptr
	editHwnd  uintptr
	cb        func(text string)
	doneCh    chan struct{}
	closeOnce sync.Once
}

func spawn(d *dialog) {
	regMu.Lock()
	if curDialog != nil {
		regMu.Unlock()
		return // one dialog at a time; a second request is ignored
	}
	curDialog = d
	regMu.Unlock()

	d.doneCh = make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := d.open(callbacks()); err != nil {
			regMu.Lock()
			if curDialog == d {
				curDialog = nil
			}
			regMu.Unlock()
			close(d.doneCh)
			return
		}
		d.pumpLoop()
	}()
}

func (d *dialog) open(cb uintptr) error {
	hInstance, _, _ := procGetModuleHandleW.Call(0)
	hCursor, _, _ := procLoadCursorW.Call(0, uintptr(idcArrow))
	wc := wndclassex{
		cbSize:        uint32(unsafe.Sizeof(wndclassex{})),
		lpfnWndProc:   cb,
		hInstance:     hInstance,
		hCursor:       hCursor,
		hbrBackground: 6, // COLOR_WINDOW+1
		lpszClassName: utf16Ptr("TianYanNativeDlg"),
	}
	if atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 && err != errClassAlreadyExists {
		return err
	}
	title := utf16Ptr(d.title)
	h, _, _ := procCreateWindowExW.Call(
		uintptr(0x00000001), // WS_EX_DLGMODALFRAME
		uintptr(unsafe.Pointer(wc.lpszClassName)),
		uintptr(unsafe.Pointer(title)),
		uintptr(wsCaption|wsSysmenu|wsVisible),
		420, 240, 360, 170, 0, 0, hInstance, 0)
	if h == 0 {
		return syscall.EINVAL
	}
	d.hwnd = h
	mkChild := func(class, text string, style, x, y, w, hgt, id uintptr) uintptr {
		cp := utf16Ptr(class)
		tp := utf16Ptr(text)
		child, _, _ := procCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(cp)), uintptr(unsafe.Pointer(tp)),
			uintptr(wsChild|wsVisible|style),
			x, y, w, hgt, h, id, hInstance, 0)
		return child
	}
	switch d.kind {
	case kindInput:
		mkChild("STATIC", d.label, 0, 14, 14, 320, 20, 0)
		d.editHwnd = mkChild("EDIT", "", uintptr(esAutohscroll|wsBorder|wsTabstop), 14, 40, 320, 24, idEdit)
		mkChild("BUTTON", "确定", uintptr(wsTabstop), 96, 82, 72, 28, idOk)
		mkChild("BUTTON", "取消", uintptr(wsTabstop), 180, 82, 72, 28, idCancel)
		procSetFocus.Call(d.editHwnd)
	case kindStatus:
		d.editHwnd = mkChild("EDIT", d.label,
			uintptr(esMultiline|esReadonly|wsBorder|wsTabstop), 14, 14, 320, 96, idEdit)
		mkChild("BUTTON", "关闭", uintptr(wsTabstop), 132, 118, 84, 28, idOk)
	}
	return nil
}

func (d *dialog) pumpLoop() {
	var m msg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		switch int64(ret) {
		case 0, -1:
			d.finish()
			return
		default:
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
	}
}

func (d *dialog) finish() {
	regMu.Lock()
	if curDialog == d {
		curDialog = nil
	}
	regMu.Unlock()
	close(d.doneCh)
}

func (d *dialog) close() {
	regMu.Lock()
	if curDialog == d {
		curDialog = nil
	}
	regMu.Unlock()
	if d.hwnd != 0 {
		procDestroyWindow.Call(d.hwnd)
	}
}

func callbacks() uintptr {
	onceCb.Do(func() {
		cbWndProc = syscall.NewCallback(wndProc)
	})
	return cbWndProc
}

func wndProc(hwnd, umsg, wparam, lparam uintptr) uintptr {
	regMu.Lock()
	d := curDialog
	regMu.Unlock()
	if d == nil || (hwnd != d.hwnd) {
		return defProc(hwnd, umsg, wparam, lparam)
	}
	switch umsg {
	case wmCommand:
		if wparam>>16 != 0 { // not BN_CLICKED
			return 0
		}
		switch uint32(wparam) & 0xffff {
		case idOk:
			if d.kind == kindInput {
				buf := make([]uint16, 1024)
				n, _, _ := procGetWindowTextW.Call(d.editHwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
				text := syscall.UTF16ToString(buf[:n])
				d.close()
				if d.cb != nil && text != "" {
					go d.cb(text)
				}
				return 0
			}
			d.close()
			return 0
		case idCancel:
			d.close()
			return 0
		}
	case wmErasebkgnd:
		var rc rect
		procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
		brush, _, _ := procCreateSolidBrush.Call(0x00F7F5F5) // Apple gray
		procFillRect.Call(wparam, uintptr(unsafe.Pointer(&rc)), brush)
		procDeleteObject.Call(brush)
		return 1
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	return defProc(hwnd, umsg, wparam, lparam)
}

func defProc(hwnd, umsg, wparam, lparam uintptr) uintptr {
	ret, _, _ := procDefWindowProcW.Call(hwnd, umsg, wparam, lparam)
	return ret
}

// openfilename mirrors OPENFILENAMEW for GetOpenFileNameW (x64 layout).
type openfilename struct {
	lStructSize     uint32
	_               uint32
	hwndOwner       uintptr
	hInstance       uintptr
	lpstrFilter     uintptr
	lpstrFile       uintptr
	nMaxFile        uint32
	_               uint32
	lpstrFileTitle  uintptr
	nMaxFileTitle   uint32
	_               uint32
	lpstrInitialDir uintptr
	lpstrTitle      uintptr
	Flags           uint32
	nFileOffset     uint16
	nFileExtension  uint16
	lpstrDefExt     uintptr
	lCustData       uintptr
	lpfnHook        uintptr
	lpTemplateName  uintptr
	pvReserved      uintptr
	dwReserved      uint32
	FlagsEx         uint32
}

func utf16Ptr(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}
