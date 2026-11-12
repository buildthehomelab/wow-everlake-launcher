package assets

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
)

// ShareArchive creates independent copy-on-write files where supported. Both
// the immutable store and the client's mutable archive retain their own inode.
func (s Store) ShareArchive(path string) (bool, error) {
	if s.Root == "" || !supportsArchiveCloning {
		return false, nil
	}
	hashFile := func(path string) (string, error) {
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return "", err
		}
		return hex.EncodeToString(h.Sum(nil)), nil
	}
	key, err := hashFile(path)
	if err != nil {
		return false, err
	}
	root := filepath.Join(s.Root, "archives")
	if err := os.MkdirAll(root, 0700); err != nil {
		return false, err
	}
	stored := filepath.Join(root, key)
	got, err := hashFile(stored)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if got != key {
		// Publish a fresh clone atomically, including when an old cache copy is corrupt.
		f, err := os.CreateTemp(root, ".archive-repair-*")
		if err != nil {
			return false, err
		}
		staged := f.Name()
		f.Close()
		if err := os.Remove(staged); err != nil {
			return false, err
		}
		defer os.Remove(staged)
		if err := cloneFile(path, staged); err != nil {
			return false, nil
		}
		if err := os.Chmod(staged, 0444); err != nil {
			return false, err
		}
		if err := os.Rename(staged, stored); err != nil {
			return false, err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".shared-archive-*")
	if err != nil {
		return false, err
	}
	name := tmp.Name()
	tmp.Close()
	if err := os.Remove(name); err != nil {
		return false, err
	}
	defer os.Remove(name)
	if err := cloneFile(stored, name); err != nil {
		return false, nil
	}
	if err := os.Chmod(name, 0644); err != nil {
		return false, err
	}
	if err := os.Rename(name, path); err != nil {
		return false, err
	}
	return true, nil
}
