//go:build darwin

package gamepatch

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runCommand(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w (%s)", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func fakeApp(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "World of Warcraft.app")
	exe := filepath.Join(src, "Contents", "MacOS", "World of Warcraft")
	if err := os.MkdirAll(filepath.Dir(exe), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("game"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("MacOS", filepath.Join(src, "Contents", "link")); err != nil {
		t.Fatal(err)
	}
	return src
}

func checkCopy(t *testing.T, app string) {
	t.Helper()
	exe := filepath.Join(app, "Contents", "MacOS", "World of Warcraft")
	data, err := os.ReadFile(exe)
	if err != nil || string(data) != "game" {
		t.Fatalf("copied program: %q, %v", data, err)
	}
	if fi, err := os.Stat(exe); err != nil || fi.Mode()&0100 == 0 {
		t.Fatalf("program not executable: %v", err)
	}
	if target, err := os.Readlink(filepath.Join(app, "Contents", "link")); err != nil || target != "MacOS" {
		t.Fatalf("symlink: %q, %v", target, err)
	}
	if _, err := os.Stat(filepath.Join(app, "World of Warcraft.app")); err == nil {
		t.Fatal("copied into the app instead of as the app")
	}
}

func TestCopyAppClones(t *testing.T) {
	src := fakeApp(t)
	app := filepath.Join(t.TempDir(), "copy.app")
	var used []string
	run := func(name string, args ...string) error { used = append(used, name); return runCommand(name, args...) }
	if err := copyApp(run, src, app); err != nil {
		t.Fatal(err)
	}
	checkCopy(t, app)
	if used[0] != "/bin/cp" {
		t.Fatalf("commands %v", used)
	}
}

func TestCopyAppFallsBackToDitto(t *testing.T) {
	src := fakeApp(t)
	app := filepath.Join(t.TempDir(), "copy.app")
	var used []string
	run := func(name string, args ...string) error {
		used = append(used, name)
		if name == "/bin/cp" {
			os.MkdirAll(filepath.Join(app, "partial"), 0755) // a clone that stopped halfway
			return errors.New("clonefile failed")
		}
		return runCommand(name, args...)
	}
	if err := copyApp(run, src, app); err != nil {
		t.Fatal(err)
	}
	checkCopy(t, app)
	if _, err := os.Stat(filepath.Join(app, "partial")); err == nil {
		t.Fatal("the half clone was not removed before ditto")
	}
	if strings.Join(used, " ") != "/bin/cp /usr/bin/ditto" {
		t.Fatalf("commands %v", used)
	}
}
