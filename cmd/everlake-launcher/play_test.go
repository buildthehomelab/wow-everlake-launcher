package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/addons"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/pin"
)

func TestEarlyExit(t *testing.T) {
	for _, c := range []struct {
		code int
		ran  time.Duration
		want string // a part of the message; "" for none
	}{
		{0xC0000135, 2 * time.Second, "exit code 0xC0000135): Windows couldn't find a DLL"},
		{0xC000007B, time.Second, "built for another kind of Windows"},
		{0xC0000142, time.Second, "failed to start"},
		{0xC0000022, time.Second, "antivirus"},
		{1, 3 * time.Second, "closed 3s after starting (exit code 1)"},
		{-1, time.Second, "closed 1s after starting"},
		{0xC0000135, 2 * time.Minute, ""}, // it ran: a normal quit or a crash in play, not a failed start
	} {
		got := earlyExit(c.code, c.ran)
		if c.want == "" {
			if got != "" {
				t.Errorf("earlyExit(%#x, %s) = %q, want nothing", c.code, c.ran, got)
			}
			continue
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("earlyExit(%#x, %s) = %q, want it to contain %q", c.code, c.ran, got, c.want)
		}
	}
}

func TestRefreshAddonBeforePlay(t *testing.T) {
	root := t.TempDir()
	p := pin.Default()
	var logged []string
	log := func(s string) { logged = append(logged, s) }

	// an older managed package (as left by the previous launcher) is brought up to this launcher's copy
	if err := addons.Install(root, p); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, p.GameFolder, "Interface", "AddOns", "Everlake")
	if err := os.WriteFile(filepath.Join(dir, "Everlake.lua"), []byte("-- older addon"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "Overview.lua")); err != nil {
		t.Fatal(err)
	}
	refreshAddon(root, p, log)
	if err := addons.Verify(root, p); err != nil {
		t.Fatalf("addon not refreshed: %v (log %v)", err, logged)
	}
	if len(logged) != 0 {
		t.Fatalf("unexpected log: %v", logged)
	}

	// an unmanaged Everlake folder is left alone and only noted
	other := t.TempDir()
	mine := filepath.Join(other, p.GameFolder, "Interface", "AddOns", "Everlake")
	if err := os.MkdirAll(mine, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mine, "mine.lua"), []byte("mine"), 0644); err != nil {
		t.Fatal(err)
	}
	refreshAddon(other, p, log)
	if got, err := os.ReadFile(filepath.Join(mine, "mine.lua")); err != nil || string(got) != "mine" {
		t.Fatal("unmanaged addon changed")
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "not updated") {
		t.Fatalf("log %v", logged)
	}
}
