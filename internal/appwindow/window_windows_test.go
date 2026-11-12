//go:build windows

package appwindow

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/webui"
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

func TestWindowExistingClientControls(t *testing.T) {
	if os.Getenv("EVERLAKE_WINDOW_TEST") != "1" {
		t.Skip("set EVERLAKE_WINDOW_TEST=1 to open a real window")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	stop := make(chan struct{})
	var once sync.Once
	result := make(chan string, 1)
	finish := func(value string) {
		once.Do(func() { result <- value; close(stop) })
	}
	install := t.TempDir()
	ui := webui.New(install, "test", webui.Actions{
		Client: func(_ context.Context, dir, action string, in webui.PlayInput, _ func(string)) error {
			if action != "client-adopt" || in.Source != `C:\Existing Client` || in.Product != "wow" {
				finish("incorrect client action payload")
				return nil
			}
			finish(dir)
			return nil
		},
	}).Handler()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/era-test-failed" {
			finish(r.URL.Query().Get("reason"))
			return
		}
		ui.ServeHTTP(w, r)
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(`<script>
const eraTest = setInterval(() => {
  const button = document.getElementById("adoptClient");
  if (button.disabled) return;
  clearInterval(eraTest);
  document.getElementById("importSource").value = "C:\\Existing Client";
  document.getElementById("importProduct").value = "wow";
  button.closest("details").open = true;
  button.click();
}, 100);
</script>`))
		}
	}))
	defer ts.Close()
	timer := time.AfterFunc(45*time.Second, func() { finish("Existing client action timed out") })
	defer timer.Stop()
	if err := Run(Options{Title: "Everlake existing client test", URL: ts.URL + "/", Width: 600, Height: 600,
		DataDir: t.TempDir(), Stop: stop}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if got != install {
			t.Fatal(got)
		}
	default:
		t.Fatal("window closed before the Existing client action ran")
	}
}
