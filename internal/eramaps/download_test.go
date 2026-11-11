package eramaps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"testing/fstest"
)

func TestDownloadStagesOutsideForever(t *testing.T) {
	if _, _, err := pythonCommand(); err != nil {
		t.Skip(err)
	}
	root := t.TempDir()
	switch runtime.GOOS {
	case "windows":
		t.Setenv("AppData", root)
	case "darwin":
		t.Setenv("HOME", root)
	default:
		t.Setenv("XDG_CONFIG_HOME", root)
	}
	install := filepath.Join(root, "Forever install")
	if err := os.Mkdir(install, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(install, ".build.info")
	if err := os.WriteFile(sentinel, []byte("forever build"), 0600); err != nil {
		t.Fatal(err)
	}
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("fixture map bytes"))
	}))
	defer cdn.Close()
	t.Setenv("EVERLAKE_TEST_CDN", cdn.URL)
	tools := fstest.MapFS{"map_catalog.py": {Data: []byte(`
import os
from pathlib import Path
import sys
from urllib.request import urlopen
stage = Path(sys.argv[1])
stage.mkdir(parents=True)
with urlopen(os.environ["EVERLAKE_TEST_CDN"], timeout=10) as response:
    (stage / "fixture.wdt").write_bytes(response.read())
`)}}
	if err := download(context.Background(), install, func(line string) { t.Log(line) }, tools); err != nil {
		t.Fatal(err)
	}
	config, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(config, "Everlake", "Era maps", "fixture.wdt"))
	if err != nil || string(data) != "fixture map bytes" {
		t.Fatalf("staged map = %q, %v", data, err)
	}
	entries, err := os.ReadDir(install)
	if err != nil || len(entries) != 1 || entries[0].Name() != ".build.info" {
		t.Fatalf("Forever directory changed: %v, %v", entries, err)
	}
	data, err = os.ReadFile(sentinel)
	if err != nil || string(data) != "forever build" {
		t.Fatalf("Forever build changed: %q, %v", data, err)
	}
}
