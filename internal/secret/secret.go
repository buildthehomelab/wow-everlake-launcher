// Package secret encrypts small secrets (a remembered password) so only this user on this computer can read them
// back: DPAPI on Windows, where the launcher signs in to realms. Elsewhere there's nothing to keep them with and
// Available is false.
package secret

import "errors"

// ErrUnavailable means secrets can't be kept on this system.
var ErrUnavailable = errors.New("passwords can't be remembered on this system")

// entropy is the launcher's own extra secret for its DPAPI blobs, so another program asking DPAPI for "any blob this
// user owns" can't open them without it.
var entropy = []byte("Everlake launcher remembered password v1")

// Available reports whether Seal and Open work here.
func Available() bool { return available }

// Seal encrypts plain for this user.
func Seal(plain []byte) ([]byte, error) { return seal(plain) }

// Open decrypts what Seal made.
func Open(sealed []byte) ([]byte, error) { return open(sealed) }
