//go:build windows

package display

import (
	"bytes"
	"image"
	"image/draw"
	"image/jpeg"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// One broadcast window at a time per process (a student machine only ever
// watches one broadcast), which lets the wndProc/hook callbacks find the
// active session without per-callback context plumbing.
var (
	regMu      sync.Mutex
	curSession *Session
	blockKeys  int32 // 1 = fullscreen: swallow all keyboard input in the hook

	cbOnce    sync.Once
	cbWndProc uintptr
	cbHook    uintptr
)

func callbacks() (uintptr, uintptr) {
	cbOnce.Do(func() {
		cbWndProc = syscall.NewCallback(wndProc)
		cbHook = syscall.NewCallback(keyboardHook)
	})
	return cbWndProc, cbHook
}

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procGetMessageW      = user32.NewProc("GetMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
	procPostMessageW     = user32.NewProc("PostMessageW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procShowWindow       = user32.NewProc("ShowWindow")
	procSetWindowPos     = user32.NewProc("SetWindowPos")
	procInvalidateRect   = user32.NewProc("InvalidateRect")
	procBeginPaint       = user32.NewProc("BeginPaint")
	procEndPaint         = user32.NewProc("EndPaint")
	procGetClientRect    = user32.NewProc("GetClientRect")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
	procLoadCursorW      = user32.NewProc("LoadCursorW")
	procSetCursor        = user32.NewProc("SetCursor")
	procSetWindowsHookEx = user32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHk  = user32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx   = user32.NewProc("CallNextHookEx")
	procFillRect         = user32.NewProc("FillRect")

	procStretchDIBits    = gdi32.NewProc("StretchDIBits")
	procCreateSolidBrush = gdi32.NewProc("CreateSolidBrush")
	procDeleteObject     = gdi32.NewProc("DeleteObject")
)

const (
	wsPopup            = 0x80000000
	wsVisible          = 0x10000000
	wsOverlappedWindow = 0x00CF0000
	wsExTopmost        = 0x00000008
	wsExToolWindow     = 0x00000080

	hwndTopmost   = ^uintptr(0) // (HWND)-1
	swpNosize     = 0x0001
	swpNomove     = 0x0002
	swpShowwindow = 0x0040

	smCxscreen = 0
	smCyscreen = 1
	swShow     = 5

	wmDestroy    = 0x0002
	wmPaint      = 0x000F
	wmClose      = 0x0010
	wmErasebkgnd = 0x0014
	wmSetcursor  = 0x0020
	wmAppClose   = 0x8001 // posted by Close(); always destroys

	whKeyboardLl = 13

	errClassAlreadyExists = syscall.Errno(1410)

	idcArrow    = 32512
	srccopy     = 0x00CC0020
	dibRgbColor = 0
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

type bitmapinfoheader struct {
	biSize          uint32
	biWidth         int32
	biHeight        int32 // negative = top-down rows
	biPlanes        uint16
	biBitCount      uint16
	biCompression   uint32
	biSizeImage     uint32
	biXPelsPerMeter int32
	biYPelsPerMeter int32
	biClrUsed       uint32
	biClrImportant  uint32
}

type bitmapinfo struct {
	header bitmapinfoheader
	colors [1]uint32 // unused for 32bpp BI_RGB
}

// Session is a broadcast display window on Windows.
type Session struct {
	title      string
	fullscreen bool
	hwnd       uintptr
	rawCh      chan []byte // JPEG frames in, drained by decoderLoop
	doneCh     chan struct{}
	closeOnce  sync.Once
	doneOnce   sync.Once
	closed     atomic.Bool
	explicit   atomic.Bool // true when torn down via Close(), not user-close
	OnClosed   func()      // invoked when the window exits without an explicit Close

	mu    sync.Mutex
	frame []byte // latest BGRA frame (painted while mu held)
	fw    int
	fh    int
}

// Supported reports whether the platform can show broadcast windows.
func Supported() bool { return true }

// NewSession opens a broadcast window. Only one session may be active at a
// time; close the previous one first. OnClosed (optional) fires when the
// window goes away on its own, e.g. the student closed a window-mode demo.
func NewSession(title string, fullscreen bool) (*Session, error) {
	regMu.Lock()
	if curSession != nil {
		regMu.Unlock()
		return nil, syscall.EBUSY
	}
	s := &Session{
		title:      title,
		fullscreen: fullscreen,
		rawCh:      make(chan []byte, 2),
		doneCh:     make(chan struct{}),
	}
	wndProcCb, hookCb := callbacks()
	curSession = s
	regMu.Unlock()

	// Win32 windows must be created and pumped on the same OS thread: do
	// both on one dedicated, locked thread. NewSession returns as soon as
	// the window exists (or reports why it could not be created).
	errCh := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := s.openWindow(wndProcCb); err != nil {
			regMu.Lock()
			if curSession == s {
				curSession = nil
			}
			regMu.Unlock()
			errCh <- err
			return
		}
		errCh <- nil
		s.pumpLoop(hookCb)
	}()
	if err := <-errCh; err != nil {
		return nil, err
	}
	if s.fullscreen {
		atomic.StoreInt32(&blockKeys, 1)
	}
	go s.decoderLoop()
	return s, nil
}

// Update queues a JPEG frame for display, dropping the frame if the decoder
// cannot keep up (always shows the newest complete frame it can).
func (s *Session) Update(jpeg []byte) {
	if len(jpeg) == 0 || s.closed.Load() {
		return
	}
	select {
	case s.rawCh <- jpeg:
	default:
	}
}

// Close tears down the window, the keyboard hook and the decoder goroutine.
// It blocks until the window goroutine has exited. Safe to call multiple
// times and after the window already closed itself.
func (s *Session) Close() {
	s.closeOnce.Do(func() {
		s.explicit.Store(true)
		atomic.StoreInt32(&blockKeys, 0)
		if s.hwnd != 0 {
			procPostMessageW.Call(s.hwnd, wmAppClose, 0, 0)
		} else {
			s.finish()
		}
	})
	<-s.doneCh
}

// openWindow registers the class and creates the window. The message loop
// continues on its own locked OS thread afterwards.
func (s *Session) openWindow(wndProcCb uintptr) error {
	className, err := syscall.UTF16PtrFromString("TianYanDemoWnd")
	if err != nil {
		return err
	}
	hInstance, _, _ := procGetModuleHandleW.Call(0)
	hCursor, _, _ := procLoadCursorW.Call(0, uintptr(idcArrow))
	wc := wndclassex{
		cbSize:      uint32(unsafe.Sizeof(wndclassex{})),
		lpfnWndProc: wndProcCb,
		hInstance:   hInstance,
		hCursor:     hCursor,
		// No background brush: WM_ERASEBKGND fills black explicitly, so the
		// window never flashes white before the first frame arrives.
		hbrBackground: 0,
		lpszClassName: className,
	}
	if atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
		// The class persists for the process lifetime, so a repeat session
		// after a previous broadcast hits ERROR_CLASS_ALREADY_EXISTS.
		if err != errClassAlreadyExists {
			return err
		}
	}
	hwnd := s.createWindow(className, hInstance)
	if hwnd == 0 {
		return syscall.EINVAL
	}
	s.hwnd = hwnd
	procShowWindow.Call(hwnd, uintptr(swShow))
	if s.fullscreen {
		procSetWindowPos.Call(hwnd, hwndTopmost, 0, 0, 0, 0,
			uintptr(swpNomove|swpNosize|swpShowwindow))
	}
	return nil
}

func (s *Session) createWindow(className *uint16, hInstance uintptr) uintptr {
	title, err := syscall.UTF16PtrFromString(s.title)
	if err != nil {
		title = className
	}
	scrW, _, _ := procGetSystemMetrics.Call(uintptr(smCxscreen))
	scrH, _, _ := procGetSystemMetrics.Call(uintptr(smCyscreen))
	if s.fullscreen {
		ex := uintptr(wsExTopmost | wsExToolWindow)
		style := uintptr(wsPopup | wsVisible)
		h, _, _ := procCreateWindowExW.Call(ex, uintptr(unsafe.Pointer(className)),
			uintptr(unsafe.Pointer(title)), style, 0, 0, scrW, scrH,
			0, 0, hInstance, 0)
		return h
	}
	// Window mode: centered, 85% of the screen, normal title bar (the
	// student may move/minimize/close it — mirrors Veyon's window demo).
	w, h := scrW*85/100, scrH*85/100
	x, y := (scrW-w)/2, (scrH-h)/2
	hwnd, _, _ := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)), uintptr(wsOverlappedWindow|wsVisible),
		x, y, w, h, 0, 0, hInstance, 0)
	return hwnd
}

// pumpLoop runs the message pump for the window. It must be called on the
// OS thread that created the window (see NewSession).
func (s *Session) pumpLoop(hookCb uintptr) {
	if s.fullscreen {
		// LL keyboard hooks fire on the installing thread, which must pump
		// messages — this loop does exactly that.
		hook, _, _ := procSetWindowsHookEx.Call(uintptr(whKeyboardLl), hookCb, 0, 0)
		defer procUnhookWindowsHk.Call(hook)
	}

	var m msg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		switch int64(ret) {
		case 0: // WM_QUIT
			s.finish()
			return
		case -1:
			s.finish()
			return
		default:
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
	}
}

// finish marks the session as gone and notifies the owner if the window was
// closed by the user rather than by an explicit Close().
func (s *Session) finish() {
	s.doneOnce.Do(func() {
		s.closed.Store(true)
		regMu.Lock()
		if curSession == s {
			curSession = nil
		}
		regMu.Unlock()
		if !s.explicit.Load() && s.OnClosed != nil {
			s.OnClosed()
		}
		close(s.doneCh)
	})
}

func keyboardHook(ncode, wparam, lparam uintptr) uintptr {
	if int32(ncode) >= 0 && atomic.LoadInt32(&blockKeys) == 1 {
		return 1 // swallow every keystroke while fullscreen demo is up
	}
	ret, _, _ := procCallNextHookEx.Call(0, ncode, wparam, lparam)
	return ret
}

func wndProc(hwnd, umsg, wparam, lparam uintptr) uintptr {
	regMu.Lock()
	s := curSession
	regMu.Unlock()
	if s == nil {
		return defWindowProc(hwnd, umsg, wparam, lparam)
	}
	switch umsg {
	case wmPaint:
		var ps paintstruct
		hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		s.paint(hdc)
		procEndPaint.Call(hdc, uintptr(unsafe.Pointer(&ps)))
		return 0
	case wmErasebkgnd:
		s.eraseBackground(wparam) // wparam is the erase HDC
		return 1
	case wmSetcursor:
		if s.fullscreen {
			procSetCursor.Call(0) // blank cursor during fullscreen demo
			return 1
		}
	case wmClose:
		if s.fullscreen {
			return 0 // fullscreen demo ignores Alt+F4 (hook blocks it anyway)
		}
		procDestroyWindow.Call(hwnd)
		return 0
	case wmAppClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	return defWindowProc(hwnd, umsg, wparam, lparam)
}

func defWindowProc(hwnd, umsg, wparam, lparam uintptr) uintptr {
	ret, _, _ := procDefWindowProcW.Call(hwnd, umsg, wparam, lparam)
	return ret
}

func (s *Session) paint(hdc uintptr) {
	var rc rect
	procGetClientRect.Call(s.hwnd, uintptr(unsafe.Pointer(&rc)))
	dstW, dstH := rc.right-rc.left, rc.bottom-rc.top
	if dstW <= 0 || dstH <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.frame == nil {
		return
	}
	bmi := bitmapinfo{header: bitmapinfoheader{
		biSize:     uint32(unsafe.Sizeof(bitmapinfoheader{})),
		biWidth:    int32(s.fw),
		biHeight:   -int32(s.fh),
		biPlanes:   1,
		biBitCount: 32,
	}}
	procStretchDIBits.Call(hdc,
		0, 0, uintptr(dstW), uintptr(dstH),
		0, 0, uintptr(s.fw), uintptr(s.fh),
		uintptr(unsafe.Pointer(&s.frame[0])),
		uintptr(unsafe.Pointer(&bmi)),
		uintptr(dibRgbColor), uintptr(srccopy))
}

func (s *Session) eraseBackground(hdc uintptr) {
	var rc rect
	procGetClientRect.Call(s.hwnd, uintptr(unsafe.Pointer(&rc)))
	brush, _, _ := procCreateSolidBrush.Call(0) // black
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), brush)
	procDeleteObject.Call(brush)
}

// decoderLoop decodes queued JPEG frames into BGRA buffers and triggers a
// repaint. The double-buffer swap keeps allocation bounded: the buffer just
// painted becomes the scratch for the next decode.
func (s *Session) decoderLoop() {
	var scratch []byte
	for {
		var jpg []byte
		select {
		case <-s.doneCh:
			return
		case jpg = <-s.rawCh:
		}
		img, err := jpeg.Decode(bytes.NewReader(jpg))
		if err != nil {
			continue
		}
		b := img.Bounds()
		w, h := b.Dx(), b.Dy()
		if w <= 0 || h <= 0 || s.closed.Load() {
			continue
		}
		need := w * h * 4
		if len(scratch) < need {
			scratch = make([]byte, need)
		}
		pix := scratch[:need]
		dst := &image.RGBA{Pix: pix, Stride: w * 4, Rect: image.Rect(0, 0, w, h)}
		draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
		for i := 0; i+3 < len(pix); i += 4 { // RGBA -> BGRA for GDI
			pix[i], pix[i+2] = pix[i+2], pix[i]
		}
		s.mu.Lock()
		s.frame, scratch = pix, s.frame
		s.fw, s.fh = w, h
		s.mu.Unlock()
		if !s.closed.Load() {
			procInvalidateRect.Call(s.hwnd, 0, 0)
		}
	}
}
