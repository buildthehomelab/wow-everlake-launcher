package webui

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A Classic client's old logs sort before _forever_ in the folder walk; the bundle must still hold the newest logs.
func TestDiagnosticsTakesNewestLogs(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-6 * 24 * time.Hour)
	for i := 0; i < 25; i++ {
		p := filepath.Join(dir, "Classic", "_forever_", "Logs", fmt.Sprintf("Old%02d.log", i))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("old"), 0o644)
		os.Chtimes(p, old, old)
	}
	tact := filepath.Join(dir, "_forever_", "Logs", "Tact.log")
	os.MkdirAll(filepath.Dir(tact), 0o755)
	os.WriteFile(tact, []byte("Missing segment header for archive '0', repair needed."), 0o644)
	big := filepath.Join(dir, "_forever_", "Logs", "Hotfix.log")
	os.WriteFile(big, []byte(strings.Repeat("a", maxDiagnosticFile)+"LAST LINE"), 0o644)

	path, err := saveDiagnostics(dir, "test", "1.60.1.1", "history")
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	logs := 0
	found := map[string]string{}
	for _, f := range z.File {
		if strings.HasSuffix(f.Name, ".log") && f.Name != "launcher.log" {
			logs++
		}
		r, _ := f.Open()
		b, _ := io.ReadAll(r)
		r.Close()
		found[f.Name] = string(b)
	}
	if logs != maxClientLogs {
		t.Errorf("%d game logs, want %d", logs, maxClientLogs)
	}
	if !strings.Contains(found["_forever_/Logs/Tact.log"], "Missing segment header") {
		t.Errorf("Tact.log left out: %v", len(found))
	}
	h := found["_forever_/Logs/Hotfix.log"]
	if !strings.HasSuffix(h, "LAST LINE") || !strings.HasPrefix(h, "[first 9 bytes left out]") {
		t.Errorf("long log not cut from the front: %q...", h[:40])
	}
}
