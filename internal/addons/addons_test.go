package addons

import (
	"github.com/buildthehomelab/wow-everlake-launcher/internal/pin"
	"os"
	"path/filepath"
	"testing"
)

func TestIndependentPackagesAndRecovery(t *testing.T) {
	root := t.TempDir()
	for _, p := range []pin.Pin{pin.Default(), pin.Retail()} {
		if err := Install(root, p); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(root, p.GameFolder, "Interface", "AddOns", "Everlake")
		profile, err := os.ReadFile(filepath.Join(dir, "Profile.lua"))
		if err != nil || len(profile) == 0 {
			t.Fatal(err)
		}
		saved := filepath.Join(root, p.GameFolder, "WTF", "Account", "test", "SavedVariables", "Everlake.lua")
		if err := os.MkdirAll(filepath.Dir(saved), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(saved, []byte("my settings"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "Everlake.lua"), []byte("custom edit"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := Verify(root, p); err == nil {
			t.Fatal("accepted modified managed addon")
		}
		if err := Install(root, p); err != nil {
			t.Fatal(err)
		}
		if err := Verify(root, p); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(saved)
		if err != nil || string(got) != "my settings" {
			t.Fatal("lost saved variables")
		}
	}
	backups, err := os.ReadDir(filepath.Join(root, ".everlake", "addon-backups"))
	if err != nil || len(backups) != 2 {
		t.Fatalf("backups %d %v", len(backups), err)
	}
}
func TestUnmanagedAddonPreserved(t *testing.T) {
	root := t.TempDir()
	p := pin.Default()
	dir := filepath.Join(root, p.GameFolder, "Interface", "AddOns", "Everlake")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mine.lua"), []byte("mine"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Install(root, p); err == nil {
		t.Fatal("overwrote unmanaged addon")
	}
}
