//go:build windows

package install

import (
	"os"
	"syscall"
	"unsafe"
)

var procLockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

// lockFile takes an exclusive lock on f's first byte without waiting (LOCKFILE_EXCLUSIVE_LOCK |
// LOCKFILE_FAIL_IMMEDIATELY). Closing the file releases it.
func lockFile(f *os.File) error {
	var ol syscall.Overlapped
	r, _, err := procLockFileEx.Call(f.Fd(), 3, 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
	if r == 0 {
		return err
	}
	return nil
}
