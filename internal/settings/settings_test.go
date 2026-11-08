package settings

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSaveLoad(t *testing.T) {
	t.Setenv("EVERLAKE_SETTINGS", filepath.Join(t.TempDir(), "sub", "launcher.json"))
	if got := InstallDir("fallback"); got != "fallback" {
		t.Errorf("no file: %q", got)
	}
	if err := Save(Settings{InstallDir: "/games/Everlake"}); err != nil {
		t.Fatal(err)
	}
	if got := InstallDir("fallback"); got != "/games/Everlake" {
		t.Errorf("after save: %q", got)
	}
	p, _ := Path()
	os.WriteFile(p, []byte("{nope"), 0o644)
	if got := InstallDir("fallback"); got != "fallback" {
		t.Errorf("broken file: %q", got)
	}
}

func TestChooseInstallDir(t *testing.T) {
	root := t.TempDir()
	never := func(string) bool { return false }

	// Missing and empty folders are used as they are (a missing one is made).
	missing := filepath.Join(root, "new", "Games")
	if got, in, err := ChooseInstallDir(missing, never); err != nil || got != missing || in {
		t.Errorf("missing: %q %v", got, err)
	}
	if fi, err := os.Stat(missing); err != nil || !fi.IsDir() {
		t.Error("missing folder not made")
	}
	if got, in, err := ChooseInstallDir(missing, never); err != nil || got != missing || in {
		t.Errorf("empty: %q %v", got, err)
	}
	if left, _ := os.ReadDir(missing); len(left) != 0 {
		t.Errorf("write test left %v", left)
	}

	// A folder with other things in it gets its own Everlake folder.
	docs := filepath.Join(root, "Documents")
	os.MkdirAll(docs, 0o755)
	os.WriteFile(filepath.Join(docs, "notes.txt"), []byte("x"), 0o644)
	if got, in, err := ChooseInstallDir(docs, never); err != nil || got != filepath.Join(docs, Subfolder) || !in {
		t.Errorf("busy folder: %q %v", got, err)
	}

	// ...unless it already holds the game.
	game := filepath.Join(root, "Moved")
	os.MkdirAll(filepath.Join(game, "_forever_"), 0o755)
	isGame := func(d string) bool { return d == game }
	if got, in, err := ChooseInstallDir(game+string(filepath.Separator), isGame); err != nil || got != game || in {
		t.Errorf("existing install: %q %v", got, err)
	}

	// A file, a relative path and nothing are refused.
	file := filepath.Join(root, "file")
	os.WriteFile(file, nil, 0o644)
	if _, _, err := ChooseInstallDir(file, never); err == nil {
		t.Error("file accepted")
	}
	for _, bad := range []string{"", "  ", "relative/dir"} {
		if _, _, err := ChooseInstallDir(bad, never); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}

	// A folder that can't be written is refused.
	if runtime.GOOS != "windows" && os.Getuid() != 0 {
		ro := filepath.Join(root, "ro")
		os.MkdirAll(ro, 0o555)
		defer os.Chmod(ro, 0o755)
		if _, _, err := ChooseInstallDir(ro, never); err == nil {
			t.Error("read-only folder accepted")
		}
	}
}

func TestChooseInstallDirRoot(t *testing.T) {
	// A disk root always gets an Everlake folder. Only the path is checked, through the error for the root that
	// isn't writable; on Windows C:\ often is, and the test mustn't leave C:\Everlake behind.
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("the disk root is writable here")
	}
	root := string(filepath.Separator)
	_, _, err := ChooseInstallDir(root, func(string) bool { return false })
	if want := filepath.Join(root, Subfolder); !contains(err.Error(), want) {
		t.Errorf("root: %v, want a message about %s", err, want)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
