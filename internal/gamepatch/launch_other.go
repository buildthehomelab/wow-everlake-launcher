//go:build !windows

package gamepatch

// Launch is Windows-only.
func Launch(o Options) (*Game, error) { return nil, ErrUnsupported }
