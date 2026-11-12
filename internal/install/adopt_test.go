package install

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/casc"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/cdn"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/tacttest"
)

func TestAdoptInPlaceNoDuplicateClientOrCache(t *testing.T) {
	b := tacttest.NewBuild(t)
	root := t.TempDir()
	dir := filepath.Join(root, "World of Warcraft")
	opts := Options{Dir: dir, Pin: b.Pin, CDN: cdn.New([]string{b.CDN.Host()}, b.Pin.CDNPath), Tags: []string{"Windows", "enUS"}}
	if _, err := Run(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, ".everlake")); err != nil {
		t.Fatal(err)
	}
	saved := filepath.Join(dir, b.Pin.GameFolder, "WTF", "SavedVariables", "MyAddon.lua")
	if err := writeFile(saved, []byte("user data"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := filepath.Glob(filepath.Join(dir, "Data", "data", "data.*"))
	if err != nil {
		t.Fatal(err)
	}
	opts.Adopt = true
	opts.AssetStore = filepath.Join(root, "must-not-be-created")
	b.CDN.Close()
	rep, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.BytesDownloaded != 0 || rep.DataFetched != 0 {
		t.Fatalf("downloaded matching files: %+v", rep)
	}
	after, _ := filepath.Glob(filepath.Join(dir, "Data", "data", "data.*"))
	if len(after) != len(before) {
		t.Fatal("copied existing archives")
	}
	if _, err := os.Stat(opts.AssetStore); !os.IsNotExist(err) {
		t.Fatal("duplicate cache created")
	}
	got, err := os.ReadFile(saved)
	if err != nil || string(got) != "user data" {
		t.Fatal("settings changed")
	}
	state, err := ReadState(dir)
	if err != nil || !state.LowSpace {
		t.Fatal("low-space choice not persisted")
	}
	opts.Adopt = false
	opts.Verify = true
	rep, err = Run(context.Background(), opts)
	if err != nil || rep.Failed() {
		t.Fatalf("verify %v %v", rep, err)
	}
}

func TestAdoptRejectsSharedProductRoot(t *testing.T) {
	b := tacttest.NewBuild(t)
	dir := t.TempDir()
	info := "Product|Version|Build Key|CDN Key|Active\nwowf|" + b.Pin.Version + "|" + b.Pin.BuildConfig + "|" + b.Pin.CDNConfig + "|1\nwow|old|old|old|0\n"
	if err := os.WriteFile(filepath.Join(dir, ".build.info"), []byte(info), 0600); err != nil {
		t.Fatal(err)
	}
	if err := adoptTarget(dir, b.Pin, nil); err == nil {
		t.Fatal("shared root adopted")
	}
	if _, err := ReadState(dir); !os.IsNotExist(err) {
		t.Fatal("state written for rejected adoption")
	}
}

func TestInterruptedAdoptionResumesInPlace(t *testing.T) {
	b := tacttest.NewBuild(t)
	dir := t.TempDir()
	opts := Options{Dir: dir, Pin: b.Pin, CDN: cdn.New([]string{b.CDN.Host()}, b.Pin.CDNPath), Tags: []string{"Windows", "enUS"}}
	if _, err := Run(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Join(dir, ".everlake"))
	opts.Adopt = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, opts); err == nil {
		t.Fatal("cancelled install succeeded")
	}
	if _, err := LaunchState(dir); err == nil {
		t.Fatal("interrupted update launchable")
	}
	b.CDN.Close()
	if rep, err := Run(context.Background(), opts); err != nil || rep.BytesDownloaded != 0 {
		t.Fatalf("resume %v %v", rep, err)
	}
	if _, err := LaunchState(dir); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptOlderWindowsInstallRepairsOnlyDamagedObject(t *testing.T) {
	b := tacttest.NewBuild(t)
	root := t.TempDir()
	dir := filepath.Join(root, "Old Forever")
	opts := Options{Dir: dir, Pin: b.Pin, CDN: cdn.New([]string{b.CDN.Host()}, b.Pin.CDNPath), Tags: []string{"Windows", "enUS"}}
	if _, err := Run(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	st, err := casc.OpenReadOnly(filepath.Join(dir, "Data", "data"))
	if err != nil {
		t.Fatal(err)
	}
	damaged, ok := st.Lookup(b.Data["shared-art"])
	if !ok {
		t.Fatal("fixture missing")
	}
	good, ok := st.Lookup(b.Data["windows-art"])
	if !ok {
		t.Fatal("fixture missing")
	}
	st.Close()
	// Simulate an older unmanaged build with reusable content and one damaged object.
	if err := os.RemoveAll(filepath.Join(dir, ".everlake")); err != nil {
		t.Fatal(err)
	}
	info := fmt.Sprintf("Product|Version|Build Key|CDN Key|Active\nwowf|1.60.0.1|%s|%s|1\n", b.Pin.BuildConfig, b.Pin.CDNConfig)
	if err := os.WriteFile(filepath.Join(dir, ".build.info"), []byte(info), 0600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "Data", "data", fmt.Sprintf("data.%03d", damaged.Archive))
	f, err := os.OpenFile(archive, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	// Damage the payload's final byte, preserving the shared archive and its index.
	at := damaged.Offset + int64(damaged.Size) - 1
	value := []byte{0}
	if _, err := f.ReadAt(value, at); err != nil {
		t.Fatal(err)
	}
	value[0] ^= 0xff
	if _, err := f.WriteAt(value, at); err != nil {
		t.Fatal(err)
	}
	f.Close()
	opts.Adopt = true
	opts.AssetStore = filepath.Join(root, "duplicate-cache")
	rep, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.DataFetched != 1 || rep.LooseFetched != 0 {
		t.Fatalf("unexpected downloads: %+v", rep)
	}
	if _, err := os.Stat(opts.AssetStore); !os.IsNotExist(err) {
		t.Fatal("duplicate cache created")
	}
	st, err = casc.OpenReadOnly(filepath.Join(dir, "Data", "data"))
	if err != nil {
		t.Fatal(err)
	}
	after, ok := st.Lookup(b.Data["windows-art"])
	if !ok || after != good {
		t.Fatal("intact object was copied or moved")
	}
	st.Close()
	b.CDN.Close()
	opts.Adopt, opts.Verify = false, true
	rep, err = Run(context.Background(), opts)
	if err != nil || rep.Failed() || rep.BytesDownloaded != 0 {
		t.Fatalf("offline verification failed: %+v %v", rep, err)
	}
}
