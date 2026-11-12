//go:build !windows && (!darwin || !cgo)

package realm

import "errors"

// WriteLaunchOptions needs a supported native platform build.
func WriteLaunchOptions(portal string, l *Login) error {
	return errors.New("account handoff requires Windows or a native macOS launcher build")
}
