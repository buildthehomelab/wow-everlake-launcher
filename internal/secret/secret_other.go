//go:build !windows && (!darwin || !cgo)

package secret

const available = false

func seal([]byte) ([]byte, error) { return nil, ErrUnavailable }
func open([]byte) ([]byte, error) { return nil, ErrUnavailable }

func forget([]byte) error { return nil }
