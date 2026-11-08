//go:build !windows && !(darwin && cgo)

package appwindow

// Linux, and Mac builds made without cgo (cross-compiled), have no window: the launcher uses the browser.
func run(Options) error { return ErrUnavailable }

func alert(string, string) bool { return false }
