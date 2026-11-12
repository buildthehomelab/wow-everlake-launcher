package install

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/casc"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/tact"
)

// An old build is a content source, never an authority for the target build.
// Every reused blob must match the target manifest's full encoded key.
func (r *run) openReuse() (func(), error) {
	source, err := filepath.EvalSymlinks(r.ReuseFrom)
	if err != nil {
		return nil, err
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return nil, err
	}
	dest, err := filepath.EvalSymlinks(r.Dir)
	if err != nil {
		return nil, err
	}
	dest, err = filepath.Abs(dest)
	if err != nil {
		return nil, err
	}
	for _, pair := range [][2]string{{source, dest}, {dest, source}} {
		rel, err := filepath.Rel(pair[0], pair[1])
		if err != nil {
			return nil, err
		}
		if rel == "." || filepath.IsLocal(rel) {
			return nil, fmt.Errorf("reuse source and destination must be separate folders")
		}
	}
	if _, err := ActiveBuilds(source); err != nil {
		return nil, fmt.Errorf("reuse source: %w", err)
	}
	if err := checkRunningClient(source); err != nil {
		return nil, err
	}
	unlock, err := lockFolder(source, true)
	if err != nil {
		return nil, err
	}
	dataDir := filepath.Join(source, "Data", "data")
	if _, err := os.Stat(dataDir); err != nil {
		unlock()
		return nil, err
	}
	st, err := casc.OpenReadOnly(dataDir)
	if err != nil {
		unlock()
		return nil, err
	}
	r.ReuseFrom, r.reuse = source, st
	r.Log("Reusing verified content from %s (%d indexed objects); missing or damaged content will download", source, st.Len())
	return func() { st.Close(); unlock() }, nil
}

func (r *run) readReuse(e tact.Key) ([]byte, error) {
	if r.reuse == nil {
		return nil, casc.ErrNotStored
	}
	b, err := r.reuse.Read(e)
	if err != nil {
		return nil, err
	}
	if err := tact.VerifyBLTE(b, e); err != nil {
		return nil, err
	}
	return b, nil
}
