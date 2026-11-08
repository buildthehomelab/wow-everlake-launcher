// Package appwindow shows the launcher's local page in a window of its own: WebView2 on Windows, WKWebView on
// macOS. The page is the same one a browser would show (internal/webui); the window only frames it, so the launcher
// looks like an app instead of a browser tab.
//
// Run must be called on the process's main thread: main locks its goroutine to that thread before anything else.
// Cocoa refuses to run anywhere else, and WebView2 needs its window and message loop on one thread.
package appwindow

import (
	"errors"
	"fmt"
	"os"
)

// ErrUnavailable means this build or this machine can't show a window (no WebView2 runtime, a Mac build without
// cgo, another platform). The caller falls back to the browser.
var ErrUnavailable = errors.New("no app window on this machine")

// Options describe the window.
type Options struct {
	Title  string
	URL    string
	Width  int // in points; scaled for the screen on Windows
	Height int
	// DataDir is where WebView2 keeps its profile (cache, cookies). Windows only; empty means the user's local app
	// data folder.
	DataDir string
	// Stop closes the window when it's closed (or receives a value).
	Stop <-chan struct{}
}

// Run opens the window and returns when it's closed (or Stop fires). It returns ErrUnavailable, without having
// shown anything, when no window can be made here.
func Run(o Options) error {
	if o.Width <= 0 {
		o.Width = 900
	}
	if o.Height <= 0 {
		o.Height = 760
	}
	return run(o)
}

// Alert tells the player something went wrong when there may be no terminal to print it to: a message box on
// Windows and macOS, standard error elsewhere.
func Alert(title, msg string) {
	if !alert(title, msg) {
		fmt.Fprintf(os.Stderr, "%s: %s\n", title, msg)
	}
}
