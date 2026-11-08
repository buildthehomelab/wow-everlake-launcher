package selfupdate

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Windows won't delete or overwrite a running program, but it lets one be renamed: that's how a launcher replaces
// itself while it runs. A copy of ping.exe stands in for it.
func TestReplaceRunningProgram(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "everlake-launcher-windows-amd64.exe")
	ping, err := os.ReadFile(filepath.Join(os.Getenv("SystemRoot"), "System32", "PING.EXE"))
	if err != nil {
		t.Skip(err)
	}
	if err := os.WriteFile(exe, ping, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-n", "30", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	time.Sleep(500 * time.Millisecond)
	if err := os.Remove(exe); err == nil {
		t.Fatal("a running program could be deleted; this test isn't testing anything")
	}

	staged := filepath.Join(dir, ".everlake-update-1")
	os.WriteFile(staged, []byte("the new launcher"), 0o644)
	tg := Target{Path: exe}
	if err := tg.Replace(staged, nil); err != nil {
		t.Fatalf("replacing a running program: %v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "the new launcher" {
		t.Fatalf("after Replace: %d bytes", len(got))
	}
	tg.Cleanup() // the old copy is still running: it stays, quietly
	if _, err := os.Stat(tg.OldPath()); err != nil {
		t.Fatalf("old copy: %v", err)
	}
	cmd.Process.Kill()
	cmd.Wait()
	time.Sleep(200 * time.Millisecond)
	tg.Cleanup()
	if _, err := os.Stat(tg.OldPath()); !os.IsNotExist(err) {
		t.Fatalf("Cleanup after it ended left the old copy: %v", err)
	}
}
