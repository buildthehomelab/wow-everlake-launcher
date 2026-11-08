//go:build windows

package appwindow

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// The window keeps Windows' own frame (snap, drag, the three buttons and their keyboard shortcuts all stay
// native), but the title bar is painted in the page's field colour and draws no title or icon, so it reads as part
// of the page instead of a white strip across the top. The taskbar still shows the name and icon.
//
// The page tells the window its colour through the everlakeFrame binding (page.html calls it on load and when
// the player picks another colour). Until then the bar uses frameDefault, which is close to every school's field.

const frameDefault = 0x0e0d18 // RRGGBB

var (
	dwmapi                      = syscall.NewLazyDLL("dwmapi.dll")
	procDwmSetWindowAttribute   = dwmapi.NewProc("DwmSetWindowAttribute")
	uxtheme                     = syscall.NewLazyDLL("uxtheme.dll")
	procSetWindowThemeAttribute = uxtheme.NewProc("SetWindowThemeAttribute")
	gdi32                       = syscall.NewLazyDLL("gdi32.dll")
	procCreateSolidBrush        = gdi32.NewProc("CreateSolidBrush")
	procSetWindowsHookExW       = user32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx     = user32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx          = user32.NewProc("CallNextHookEx")
	procGetClassNameW           = user32.NewProc("GetClassNameW")
	procGetWindowLongPtrW       = user32.NewProc("GetWindowLongPtrW")
	procSetClassLongPtrW        = user32.NewProc("SetClassLongPtrW")
	procRedrawWindow            = user32.NewProc("RedrawWindow")
	kernel32                    = syscall.NewLazyDLL("kernel32.dll")
	procGetCurrentThreadId      = kernel32.NewProc("GetCurrentThreadId")
	framingHook                 uintptr
	framingCallback             = syscall.NewCallback(cbtHook)
	framedWindow                uintptr
)

const (
	dwmwaUseImmersiveDarkModeOld = 19 // Windows 10 1809 to 1909
	dwmwaUseImmersiveDarkMode    = 20 // Windows 10 2004 and later
	dwmwaCaptionColor            = 35 // Windows 11
	dwmwaTextColor               = 36 // Windows 11

	wtaNonClient       = 1
	wtncaNoDrawCaption = 0x1
	wtncaNoDrawIcon    = 0x2

	whCBT          = 5
	hcbtCreateWnd  = 3
	gwlStyle       = ^uintptr(15) // GWL_STYLE (-16)
	gclpHbrBackgnd = ^uintptr(9)  // GCLP_HBRBACKGROUND (-10)
	wsChild        = 0x40000000
	rdwFrame       = 0x0400
	rdwInvalidate  = 0x0001
	rdwUpdateNow   = 0x0100
)

// frameBefore starts watching this thread for the launcher's window being made, so its title bar is dark before
// it's first shown: go-webview2 creates and shows the window inside NewWithOptions, with no way in between. It
// also sets the WebView2 background, which shows until the page's first paint, to the same colour. frameAfter
// undoes the watch and frames the window again in case the early attempt didn't take.
func frameBefore() {
	if os.Getenv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR") == "" {
		os.Setenv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR", fmt.Sprintf("FF%06X", frameDefault))
	}
	if procSetWindowsHookExW.Find() != nil {
		return
	}
	tid, _, _ := procGetCurrentThreadId.Call()
	framingHook, _, _ = procSetWindowsHookExW.Call(whCBT, framingCallback, 0, tid)
}

func frameAfter(hwnd uintptr) {
	if framingHook != 0 {
		procUnhookWindowsHookEx.Call(framingHook)
		framingHook = 0
	}
	if hwnd != 0 {
		frame(hwnd, frameDefault)
		framedWindow = hwnd
	}
}

// cbtHook sees each window this thread creates before it gets its first message.
func cbtHook(code, wparam, lparam uintptr) uintptr {
	if int32(code) == hcbtCreateWnd && framedWindow == 0 && isLauncherWindow(wparam) {
		framedWindow = wparam
		brush, _, _ := procCreateSolidBrush.Call(uintptr(colorref(frameDefault)))
		if brush != 0 {
			procSetClassLongPtrW.Call(wparam, gclpHbrBackgnd, brush) // the client area before WebView2 paints
		}
		frame(wparam, frameDefault)
	}
	r, _, _ := procCallNextHookEx.Call(0, code, wparam, lparam)
	return r
}

// isLauncherWindow: go-webview2's top-level window (class "webview"), not one of WebView2's own child windows.
func isLauncherWindow(hwnd uintptr) bool {
	style, _, _ := procGetWindowLongPtrW.Call(hwnd, gwlStyle)
	if style&wsChild != 0 {
		return false
	}
	buf := make([]uint16, 32)
	n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return n > 0 && syscall.UTF16ToString(buf[:n]) == "webview"
}

// frame paints the title bar in rgb (0xRRGGBB) and hides its title and icon. Each step is best effort: Windows 10
// has no caption colour and gets the dark title bar instead; very old builds keep the light one.
func frame(hwnd uintptr, rgb uint32) {
	if procDwmSetWindowAttribute.Find() == nil {
		on := int32(1)
		if r, _, _ := procDwmSetWindowAttribute.Call(hwnd, dwmwaUseImmersiveDarkMode, uintptr(unsafe.Pointer(&on)), 4); r != 0 {
			procDwmSetWindowAttribute.Call(hwnd, dwmwaUseImmersiveDarkModeOld, uintptr(unsafe.Pointer(&on)), 4)
		}
		caption, text := colorref(rgb), colorref(0xf2f0f7)
		procDwmSetWindowAttribute.Call(hwnd, dwmwaCaptionColor, uintptr(unsafe.Pointer(&caption)), 4)
		procDwmSetWindowAttribute.Call(hwnd, dwmwaTextColor, uintptr(unsafe.Pointer(&text)), 4)
	}
	if procSetWindowThemeAttribute.Find() == nil {
		opts := struct{ flags, mask uint32 }{wtncaNoDrawCaption | wtncaNoDrawIcon, wtncaNoDrawCaption | wtncaNoDrawIcon}
		procSetWindowThemeAttribute.Call(hwnd, wtaNonClient, uintptr(unsafe.Pointer(&opts)), unsafe.Sizeof(opts))
	}
	procRedrawWindow.Call(hwnd, 0, 0, rdwFrame|rdwInvalidate|rdwUpdateNow)
}
