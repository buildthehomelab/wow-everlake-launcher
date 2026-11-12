//go:build darwin

package assets

import "golang.org/x/sys/unix"

func cloneFile(source, dest string) error { return unix.Clonefile(source, dest, 0) }

const supportsArchiveCloning = true
