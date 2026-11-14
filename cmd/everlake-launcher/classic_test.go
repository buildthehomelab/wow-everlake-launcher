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

func writeInstallState(t *testing.T, dir string, p pin.Pin) {
	t.Helper()
	state, err := json.Marshal(install.State{Pin: p})
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
}

// earlierPin is a plain Forever client on the build pinned before the current one.
func earlierPin() pin.Pin {
	p := pin.Default()
	p.Version, p.BuildID = "1.60.1.70790", 70790
	p.BuildConfig, p.CDNConfig = "9006ba6948388dd0d4a0578e64b8a92e", "0e139f0e95542dbe2a5462d8a6b2ebae"
	return p
}

// After the pin moves, a plain client on the earlier build must update without any overlay: only overlay builds are
// held back. The context is cancelled, so the install stops before it asks the network for anything.
func TestEarlierPlainBuildUpdatesWithoutOverlay(t *testing.T) {
	t.Setenv("EVERLAKE_CLIENT_CDN", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, verify := range []bool{false, true} {
		dir := t.TempDir()
		writeInstallState(t, dir, earlierPin())
		err := installSharedClientMode(ctx, dir, verify, false, false, func(string) {})
		if err == nil || strings.Contains(err.Error(), "overlay") {
			t.Fatalf("verify=%v: a plain 70790 client was held back for an overlay: %v", verify, err)
		}
	}
}

// An overlay made for the earlier build is left out for a plain client and holds an overlay install back.
func TestOverlayForAnotherBuild(t *testing.T) {
	t.Setenv("EVERLAKE_CLIENT_CDN", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stale := func(dir string) {
		overlay := filepath.Join(dir, "_era_", "overlay")
		if err := os.MkdirAll(overlay, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(overlay, "build.json"), []byte(`{"version":"1.60.1.70790"}`), 0600); err != nil {
			t.Fatal(err)
		}
	}

	plain := t.TempDir()
	stale(plain)
	writeInstallState(t, plain, earlierPin())
	var lines []string
	err := installSharedClientMode(ctx, plain, false, false, false, func(s string) { lines = append(lines, s) })
	if err == nil || strings.Contains(err.Error(), "overlay") {
		t.Fatalf("a plain client was held back by an overlay for another build: %v", err)
	}
	if len(lines) == 0 || !strings.Contains(lines[0], "left out") {
		t.Fatalf("the left-out overlay wasn't reported: %q", lines)
	}

	withOverlay := t.TempDir()
	stale(withOverlay)
	p := earlierPin()
	p.BuildConfig, p.CDNConfig = "11111111111111111111111111111111", "22222222222222222222222222222222"
	writeInstallState(t, withOverlay, p)
	err = installSharedClientMode(ctx, withOverlay, false, false, false, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "get the overlay for "+pin.Default().Version) {
		t.Fatalf("an overlay install was updated without its overlay: %v", err)
	}
}
