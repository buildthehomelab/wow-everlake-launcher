package folderpick

import (
	"os"
	"os/exec"
	"strings"
)

// pick asks Finder's folder dialog through AppleScript, which works in the app window and the browser alike (an
// NSOpenPanel of our own would need the Cocoa main thread, which only the window has).
func pick(title, start string) (string, error) {
	script := `on run argv
	set p to item 1 of argv
	set t to item 2 of argv
	activate
	if p is not "" then
		return POSIX path of (choose folder with prompt t default location (POSIX file p))
	end if
	return POSIX path of (choose folder with prompt t)
end run`
	if fi, err := os.Stat(start); err != nil || !fi.IsDir() {
		start = ""
	}
	out, err := exec.Command("osascript", "-e", script, start, title).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && strings.Contains(string(ee.Stderr), "-128") {
			return "", ErrCancelled // "User canceled. (-128)"
		}
		if _, ok := err.(*exec.ExitError); ok {
			return "", err
		}
		return "", ErrUnavailable
	}
	p := strings.TrimRight(strings.TrimSpace(string(out)), "/")
	if p == "" {
		return "/", nil
	}
	return p, nil
}
