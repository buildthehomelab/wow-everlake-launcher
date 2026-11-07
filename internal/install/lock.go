package install

import (
	"fmt"
	"os"
	"path/filepath"
)

// lockFolder makes sure only one launcher works on dir at a time: two installs writing the same local storage
// would overwrite each other's key tables. Verify takes the lock too (so it never reads half-written tables), but
// only when the lock file exists, since verify writes nothing.
func lockFolder(dir string, verify bool) (func(), error) {
	p := filepath.Join(dir, ".everlake", "lock")
	var f *os.File
	var err error
	if verify {
		f, err = os.Open(p)
		if os.IsNotExist(err) {
			return func() {}, nil
		}
	} else {
		if err = os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
			f, err = os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o644)
		}
	}
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("%s is in use by another everlake-launcher (%v)", dir, err)
	}
	return func() { f.Close() }, nil
}
