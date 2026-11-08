// Package folderpick asks the player to pick a folder with the system's own dialog: the Explorer folder picker on
// Windows, Finder's on macOS, zenity or kdialog on Linux.
package folderpick

import "errors"

var (
	// ErrCancelled means the player closed the dialog without picking a folder.
	ErrCancelled = errors.New("no folder picked")
	// ErrUnavailable means there's no folder dialog here; the player types the path instead.
	ErrUnavailable = errors.New("no folder dialog on this system")
)

// Pick shows the dialog, opened at start when that folder exists, and returns the folder picked. It blocks until
// the dialog closes.
func Pick(title, start string) (string, error) {
	return pick(title, start)
}
