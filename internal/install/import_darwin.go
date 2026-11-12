package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"syscall"
)

func importFreeSpace(path string) (uint64, error) {
	var s unix.Statfs_t
	err := unix.Statfs(path, &s)
	return s.Bavail * uint64(s.Bsize), err
}

type publicationReservation struct {
	Device int32
	Inode  uint64
}

func reservationPath(to string) string {
	return filepath.Join(to+".everlake-import", "publication.json")
}
func recoverImportPublication(to string) error {
	data, err := os.ReadFile(reservationPath(to))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var saved publicationReservation
	if err := json.Unmarshal(data, &saved); err != nil {
		return err
	}
	info, err := os.Lstat(to)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || st.Dev != saved.Device || st.Ino != saved.Inode {
		return fmt.Errorf("publication destination changed: %s", to)
	}
	// Remove only the empty directory this transaction reserved.
	if err := os.Remove(to); err != nil {
		return fmt.Errorf("publication reservation is no longer empty: %w", err)
	}
	return nil
}
func publishImport(from, to string) error {
	err := unix.RenamexNp(from, to, unix.RENAME_EXCL)
	if !errors.Is(err, unix.ENOTSUP) {
		return err
	}
	var fs unix.Statfs_t
	if statErr := unix.Statfs(filepath.Dir(to), &fs); statErr != nil {
		return statErr
	}
	name := make([]byte, 0, len(fs.Fstypename))
	for _, b := range fs.Fstypename {
		if b == 0 {
			break
		}
		name = append(name, byte(b))
	}
	if string(name) != "exfat" {
		return err
	}
	// macOS exFAT directory rename rejects an existing destination, including
	// an empty directory. This is exercised on an actual exFAT volume.
	return os.Rename(from, to)
}
