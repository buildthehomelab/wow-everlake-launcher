package selfupdate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// replaceApp swaps the .app at app for the one in the zip at staged. The zip is unpacked with ditto, the tool
// scripts/package.sh packed it with, so the app's signature and file modes come through as they were; the unpacked
// app has to pass codesign's check before it goes in.
func replaceApp(app, staged string, check func(program string) error) error {
	work, err := os.MkdirTemp(filepath.Dir(app), ".everlake-update-*")
	if err != nil {
		if os.IsPermission(err) {
			return fmt.Errorf("%w: %v", ErrCantReplace, err)
		}
		return err
	}
	defer os.RemoveAll(work)
	if out, err := exec.Command("/usr/bin/ditto", "-x", "-k", staged, work).CombinedOutput(); err != nil {
		return fmt.Errorf("unpacking the update: %v: %s", err, strings.TrimSpace(string(out)))
	}
	found, _ := filepath.Glob(filepath.Join(work, "*.app"))
	if len(found) != 1 {
		return fmt.Errorf("the update holds %d apps, not one", len(found))
	}
	fresh := found[0]
	if out, err := exec.Command("/usr/bin/codesign", "--verify", "--strict", fresh).CombinedOutput(); err != nil {
		return fmt.Errorf("the update's app fails codesign's check: %v: %s", err, strings.TrimSpace(string(out)))
	}
	progs, _ := filepath.Glob(filepath.Join(fresh, "Contents", "MacOS", "*"))
	if len(progs) != 1 {
		return fmt.Errorf("the update's app holds %d programs, not one", len(progs))
	}
	if err := check(progs[0]); err != nil {
		return err
	}
	old := app + ".old"
	os.RemoveAll(old)
	if err := os.Rename(app, old); err != nil {
		if os.IsPermission(err) {
			return fmt.Errorf("%w: %v", ErrCantReplace, err)
		}
		return err
	}
	if err := os.Rename(fresh, app); err != nil {
		if rerr := os.Rename(old, app); rerr != nil {
			return fmt.Errorf("%v; and putting the old app back failed: %v (it's at %s)", err, rerr, old)
		}
		return err
	}
	return nil
}
