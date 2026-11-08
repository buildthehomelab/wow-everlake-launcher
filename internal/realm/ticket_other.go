//go:build !windows

package realm

import "errors"

// WriteLaunchOptions is Windows-only: on macOS the login goes into the net.battle preferences instead
// (wow-project-everlake's client/macos/login.py).
func WriteLaunchOptions(portal string, l *Login) error {
	return errors.New("storing the login for the game is written for Windows only")
}
