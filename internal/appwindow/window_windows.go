//go:build windows

package appwindow

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/webviewloader"
)

var (
	user32                            = syscall.NewLazyDLL("user32.dll")
	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware            = user32.NewProc("SetProcessDPIAware")
	procGetDpiForSystem               = user32.NewProc("GetDpiForSystem")
	procMessageBoxW                   = user32.NewProc("MessageBoxW")
)

func run(o Options) error {
	// WebView2 ships with Windows 11 and reached Windows 10 through Windows Update; a PC without it gets the
	// browser instead. Checked before anything is shown, so a failure leaves no empty window behind.
	if v, err := webviewloader.GetInstalledVersion(); err != nil || v == "" {
		if err == nil {
			err = fmt.Errorf("the WebView2 runtime isn't installed")
		}
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	scale := dpiAware()
	data := o.DataDir
	if data == "" {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			base = os.TempDir()
		}
		data = filepath.Join(base, "Everlake", "WebView2")
	}
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		DataPath:  data,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title: o.Title, Width: uint(o.Width * scale / 96), Height: uint(o.Height * scale / 96), Center: true,
			IconId: 1, // the .exe's icon (internal/winres.IconID); without one Windows shows its default
		},
	})
	if w == nil {
		return fmt.Errorf("%w: WebView2 didn't start", ErrUnavailable)
	}
	done := make(chan struct{})
	defer close(done)
	if o.Stop != nil {
		go func() {
			select {
			case <-o.Stop:
				w.Destroy() // posts WM_CLOSE: safe from any thread
			case <-done:
			}
		}()
	}
	w.SetSize(480*scale/96, 420*scale/96, webview2.HintMin)
	w.Navigate(o.URL)
	w.Run()
	return nil
}

// dpiAware makes the window sharp on scaled screens (otherwise Windows stretches it as a bitmap) and returns the
// screen's DPI, 96 at 100 %.
func dpiAware() int {
	const perMonitorV2 = ^uintptr(3) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 (-4), Windows 10 1703+
	if procSetProcessDpiAwarenessContext.Find() == nil {
		procSetProcessDpiAwarenessContext.Call(perMonitorV2)
	} else if procSetProcessDPIAware.Find() == nil {
		procSetProcessDPIAware.Call()
	}
	if procGetDpiForSystem.Find() == nil {
		if dpi, _, _ := procGetDpiForSystem.Call(); dpi >= 96 {
			return int(dpi)
		}
	}
	return 96
}

func alert(title, msg string) bool {
	t, err1 := syscall.UTF16PtrFromString(title)
	m, err2 := syscall.UTF16PtrFromString(msg)
	if err1 != nil || err2 != nil || procMessageBoxW.Find() != nil {
		return false
	}
	const mbIconError = 0x10
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), mbIconError)
	return true
}
