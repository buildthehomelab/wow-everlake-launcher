package selfupdate

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// makeApp builds a small signed app the way scripts/package.sh does, with note as a file inside it.
func makeApp(t *testing.T, dir, note string) string {
	t.Helper()
	app := filepath.Join(dir, "Everlake Launcher.app")
	if err := os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin, _ := os.ReadFile("/usr/bin/true")
	os.WriteFile(filepath.Join(app, "Contents", "MacOS", "everlake-launcher"), bin, 0o755)
	os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>CFBundleExecutable</key><string>everlake-launcher</string>
<key>CFBundleIdentifier</key><string>com.buildthehomelab.everlake-launcher.test</string></dict></plist>`), 0o644)
	os.WriteFile(filepath.Join(app, "Contents", "note"), []byte(note), 0o644)
	if out, err := exec.Command("codesign", "--force", "--sign", "-", app).CombinedOutput(); err != nil {
		t.Skipf("codesign: %v: %s", err, out)
	}
	return app
}

func TestReplaceApp(t *testing.T) {
	installed := t.TempDir()
	app := makeApp(t, installed, "old")

	build := t.TempDir()
	makeApp(t, build, "new")
	zip := filepath.Join(t.TempDir(), "Everlake-Launcher-macos.zip")
	if out, err := exec.Command("ditto", "-c", "-k", "--keepParent", filepath.Join(build, "Everlake Launcher.app"),
		zip).CombinedOutput(); err != nil {
		t.Fatalf("ditto: %v: %s", err, out)
	}

	tg, err := TargetFor(filepath.Join(app, "Contents", "MacOS", "everlake-launcher"))
	if err != nil || !tg.App {
		t.Fatalf("target %+v, %v", tg, err)
	}
	if err := tg.Replace(zip, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(app, "Contents", "note")); string(got) != "new" {
		t.Fatalf("the app holds %q after the update", got)
	}
	if out, err := exec.Command("codesign", "--verify", "--strict", app).CombinedOutput(); err != nil {
		t.Fatalf("the updated app fails codesign: %v: %s", err, out)
	}
	if fi, err := os.Stat(filepath.Join(app, "Contents", "MacOS", "everlake-launcher")); err != nil || fi.Mode()&0o111 == 0 {
		t.Fatalf("program not executable: %v %v", fi, err)
	}
	tg.Cleanup()
	left, _ := os.ReadDir(installed)
	if len(left) != 1 || left[0].Name() != "Everlake Launcher.app" {
		t.Fatalf("left behind in the Applications folder: %v", left)
	}
}

func TestReplaceAppRefusesABrokenSignature(t *testing.T) {
	installed := t.TempDir()
	app := makeApp(t, installed, "old")
	build := t.TempDir()
	fresh := makeApp(t, build, "new")
	os.WriteFile(filepath.Join(fresh, "Contents", "note"), []byte("changed after signing"), 0o644)
	zip := filepath.Join(t.TempDir(), "u.zip")
	if out, err := exec.Command("ditto", "-c", "-k", "--keepParent", fresh, zip).CombinedOutput(); err != nil {
		t.Fatalf("ditto: %v: %s", err, out)
	}
	if err := (Target{Path: app, App: true}).Replace(zip, nil); err == nil {
		t.Fatal("an app with a broken signature went in")
	}
	if got, _ := os.ReadFile(filepath.Join(app, "Contents", "note")); string(got) != "old" {
		t.Fatalf("the app holds %q", got)
	}
}
