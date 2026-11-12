package install

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/pin"
)

func TestImportResumesAndPreservesProducts(t *testing.T) {
	p := pin.Default()
	root := t.TempDir()
	source, dest := filepath.Join(root, "source"), filepath.Join(root, "managed")
	files := map[string][]byte{
		".build.info": buildInfo(p, p.CDNHosts, ""),
		filepath.Join(p.GameFolder, "Interface", "AddOns", "Example", "Example.toc"):   []byte("addon"),
		filepath.Join(p.GameFolder, "WTF", "Account", "SavedVariables", "Example.lua"): []byte("settings"),
		filepath.Join("Data", "data", "data.000"):                                      []byte("assets"),
	}
	exe, err := filepath.Rel(source, GameExe(source, p.GameFolder))
	if err != nil {
		t.Fatal(err)
	}
	files[exe] = []byte("fixture executable")
	for path, data := range files {
		if err := writeFile(filepath.Join(source, path), data, 0755); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := Import(ctx, source, dest, p, func(string) { cancel() }); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted import: %v", err)
	}
	if _, err := os.Stat(dest); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("published incomplete import: %v", err)
	}
	target := filepath.Join(dest+".everlake-import", "client", "Data", "data", "data.000")
	if err := writeFile(target, []byte("broken"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Import(context.Background(), source, dest, p, func(s string) { t.Log(s) }); err != nil {
		t.Fatal(err)
	}
	for path, want := range files {
		for _, base := range []string{source, dest} {
			got, err := os.ReadFile(filepath.Join(base, path))
			if err != nil || string(got) != string(want) {
				t.Fatalf("%s in %s: %q %v", path, base, got, err)
			}
		}
	}
	if err := Import(context.Background(), source, dest, p, func(s string) { t.Log(s) }); err == nil {
		t.Fatal("overwrote published destination")
	}
	retail := p
	retail.Product, retail.Flavor, retail.GameFolder = "wow", "wow", "_retail_"
	if err := CheckBuild(dest, retail); err == nil {
		t.Fatal("accepted mismatched product")
	}
	if err := checkInstallTarget(dest, retail); err == nil {
		t.Fatal("accepted cross-product update")
	}
	if err := checkInstallTarget(source, p); err == nil {
		t.Fatal("accepted update to official root")
	}
}

func TestImportSnapshotRejectsMissingAndUnexpectedFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "asset")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	digest, err := hashImport(context.Background(), path, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	journal := importJournal{Entries: []importEntry{{Path: "asset", Mode: 0600, Digest: digest}}}
	if err := validateImportTree(context.Background(), root, journal); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := validateImportTree(context.Background(), root, journal); err == nil {
		t.Fatal("accepted missing file")
	}
	if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateImportTree(context.Background(), root, journal); err == nil {
		t.Fatal("accepted corrupt file")
	}
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "unexpected"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateImportTree(context.Background(), root, journal); err == nil {
		t.Fatal("accepted unexpected file")
	}
}

func TestImportRejectsSourceChanges(t *testing.T) {
	root := t.TempDir()
	source, dest := filepath.Join(root, "source"), filepath.Join(root, "target")
	p := pin.Default()
	if err := writeFile(filepath.Join(source, ".build.info"), buildInfo(p, p.CDNHosts, ""), 0644); err != nil {
		t.Fatal(err)
	}
	exe := GameExe(source, p.GameFolder)
	if err := writeFile(exe, []byte("original"), 0755); err != nil {
		t.Fatal(err)
	}
	err := Import(context.Background(), source, dest, p, func(string) {
		if err := os.WriteFile(exe, []byte("changed source"), 0755); err != nil {
			t.Fatal(err)
		}
	})
	if err == nil {
		t.Fatal("published an import while the source changed")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("partial destination published")
	}
	if err := Import(context.Background(), source, dest, p, func(string) {}); err == nil {
		t.Fatal("accepted a different source snapshot on retry")
	}
}

func TestImportRefusesLinkedSource(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	p := pin.Default()
	if err := writeFile(filepath.Join(source, ".build.info"), buildInfo(p, p.CDNHosts, ""), 0644); err != nil {
		t.Fatal(err)
	}
	exe := GameExe(source, p.GameFolder)
	if err := writeFile(exe, []byte("client"), 0755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(source, "linked")); err != nil {
		t.Skip(err)
	}
	if err := Import(context.Background(), source, filepath.Join(root, "target"), p, func(string) {}); err == nil {
		t.Fatal("followed linked source")
	}
	got, err := os.ReadFile(outside)
	if err != nil || string(got) != "original" {
		t.Fatal("changed external data")
	}
}

func TestConcurrentImportRejected(t *testing.T) {
	root := t.TempDir()
	source, dest := filepath.Join(root, "source"), filepath.Join(root, "target")
	p := pin.Default()
	if err := writeFile(filepath.Join(source, ".build.info"), buildInfo(p, p.CDNHosts, ""), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(GameExe(source, p.GameFolder), []byte("client"), 0755); err != nil {
		t.Fatal(err)
	}
	ready, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- Import(context.Background(), source, dest, p, func(string) { close(ready); <-release })
	}()
	<-ready
	err := Import(context.Background(), source, dest, p, func(string) {})
	close(release)
	if err == nil {
		t.Error("accepted concurrent import")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
