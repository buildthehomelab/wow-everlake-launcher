package eramaps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestDownloadStagesOutsideForever(t *testing.T) {
	if _, _, err := pythonCommand(); err != nil {
		t.Skip(err)
	}
	root := t.TempDir()
	install := filepath.Join(root, "Forever install")
	if err := os.MkdirAll(filepath.Join(install, "Data"), 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(install, "Data", "data.000")
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
	data, err := os.ReadFile(filepath.Join(install, "Era maps", "fixture.wdt"))
	if err != nil || string(data) != "fixture map bytes" {
		t.Fatalf("staged map = %q, %v", data, err)
	}
	entries, err := os.ReadDir(install)
	if err != nil || len(entries) != 2 || entries[0].Name() != "Data" || entries[1].Name() != "Era maps" {
		t.Fatalf("Forever directory changed: %v, %v", entries, err)
	}
	data, err = os.ReadFile(sentinel)
	if err != nil || string(data) != "forever build" {
		t.Fatalf("Forever data changed: %q, %v", data, err)
	}
}

func TestStagingDirRejectsRedirectIntoForever(t *testing.T) {
	install := t.TempDir()
	data := filepath.Join(install, "Data")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(data, filepath.Join(install, "Era maps")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := stagingDir(install); err == nil {
		t.Fatal("accepted staging redirected into Forever Data")
	}
}

func TestStagingDirDoesNotCreateMissingInstallFolder(t *testing.T) {
	install := filepath.Join(t.TempDir(), "unavailable drive", "Everlake")
	if _, err := stagingDir(install); err == nil {
		t.Fatal("accepted a missing launcher folder")
	}
	if _, err := os.Stat(install); !os.IsNotExist(err) {
		t.Fatalf("missing launcher folder was created: %v", err)
	}
}
