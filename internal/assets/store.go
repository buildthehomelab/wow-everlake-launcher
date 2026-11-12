// Package assets stores verified encoded objects independently of client manifests.
// Clients receive their own writable CASC archives.
package assets

import (
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/tact"
)

type Store struct{ Root string }

func (s Store) path(k tact.Key) string {
	key := k.String()
	return filepath.Join(s.Root, key[:2], key)
}

func (s Store) Read(k tact.Key) ([]byte, error) {
	if s.Root == "" {
		return nil, os.ErrNotExist
	}
	data, err := os.ReadFile(s.path(k))
	if err != nil {
		return nil, err
	}
	if err := tact.VerifyBLTE(data, k); err != nil {
		return nil, err
	}
	return data, nil
}

func (s Store) Put(k tact.Key, data []byte) error {
	if s.Root == "" {
		return nil
	}
	if err := tact.VerifyBLTE(data, k); err != nil {
		return err
	}
	if _, err := s.Read(k); err == nil {
		return nil
	}
	// Windows cannot replace a read-only destination, even by atomic rename.
	if runtime.GOOS == "windows" {
		if err := os.Chmod(s.path(k), 0600); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return writeCacheFile(s.path(k), data)
}

// Cache objects are disposable: a partial or lost file is rejected by Read.
// Atomic publication is sufficient; forcing an fsync for each of millions of
// cache entries stalls USB drives. The client's CASC storage still syncs normally.
func writeCacheFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".cache-part-*")
	if err != nil {
		return err
	}
	defer func() { f.Close(); os.Remove(f.Name()) }()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Chmod(0444); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	delay := 20 * time.Millisecond
	for attempt := 0; ; attempt++ {
		err = os.Rename(f.Name(), path)
		if err == nil || attempt == 8 {
			return err
		}
		time.Sleep(delay)
		delay *= 2
	}
}
