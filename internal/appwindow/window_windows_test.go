//go:build windows

package appwindow

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"
)

// TestWindowLoadsPage opens a real WebView2 window on a page whose script tints the title bar and calls back, and
// closes it once it has.
// It needs a desktop session and the WebView2 runtime, so it runs only when EVERLAKE_WINDOW_TEST=1 (CI's Windows
// job sets it in a step of its own).
func TestWindowLoadsPage(t *testing.T) {
	if os.Getenv("EVERLAKE_WINDOW_TEST") != "1" {
		t.Skip("set EVERLAKE_WINDOW_TEST=1 to open a real window")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	stop := make(chan struct{})
	var once sync.Once
	called := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<!doctype html><title>t</title><script>` +
			// The title-bar colour hook must be there and answer before the page reports back.
			`everlakeFrame("#43303a").then(() => fetch("ping?ua=" + encodeURIComponent(navigator.userAgent)))</script>`))
	})
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() {
			called <- r.URL.Query().Get("ua")
			close(stop)
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	timeout := time.AfterFunc(60*time.Second, func() { once.Do(func() { close(stop) }) })
	defer timeout.Stop()
	err := Run(Options{Title: "Everlake window test", URL: ts.URL + "/", Width: 400, Height: 300,
		DataDir: t.TempDir(), Stop: stop})
	if errors.Is(err, ErrUnavailable) {
		t.Skipf("no window here: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	select {
	case ua := <-called:
		t.Logf("page loaded in the window: %s", ua)
	default:
		t.Fatal("the window closed without the page calling back (timed out)")
	}
}
