package main

import (
	"context"
	"encoding/json"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/install"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/pin"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFetchOverlayManifest(t *testing.T) {
	const manifest = `{"version":"1.60.1.70790"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/everlake/build.json" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(manifest))
	}))
	defer server.Close()

	data, err := fetchOverlayManifest(context.Background(), server.URL+"/")
	if err != nil || string(data) != manifest {
		t.Fatalf("fetchOverlayManifest = %q, %v", data, err)
	}
	if _, err := fetchOverlayManifest(context.Background(), server.URL+"/wrong"); err == nil {
		t.Fatal("accepted a mirror URL with a path")
	}
}

func TestSharedAdoptionMissingOverlayPreservesBuild(t *testing.T) {
	t.Setenv("EVERLAKE_CLIENT_CDN", "")
	dir := t.TempDir()
	p := pin.Default()
	p.BuildConfig = "11111111111111111111111111111111"
	state, err := json.Marshal(install.State{Pin: p, LowSpace: true})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".everlake", "install.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, state, 0600); err != nil {
		t.Fatal(err)
	}
	err = installSharedClientMode(context.Background(), dir, false, true, false, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "restore the shared client overlay") {
		t.Fatalf("expected overlay preservation guard, got %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(state) {
		t.Fatal("changed installed overlay state")
	}
}
