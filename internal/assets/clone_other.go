//go:build !darwin

package assets

import "errors"

func cloneFile(source, dest string) error {
	return errors.New("copy-on-write sharing unavailable on this platform")
}

const supportsArchiveCloning = false
