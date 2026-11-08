//go:build !windows

package secret

const available = false

func seal([]byte) ([]byte, error) { return nil, ErrUnavailable }
func open([]byte) ([]byte, error) { return nil, ErrUnavailable }
