//go:build !windows && !darwin

package gamepatch

// Launch is supported on Windows and macOS.
func Launch(o Options) (*Game, error) { return nil, ErrUnsupported }
