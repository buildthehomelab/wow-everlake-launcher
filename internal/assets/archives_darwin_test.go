//go:build darwin

package assets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveCloneKeepsWritesSeparate(t *testing.T) {
	root := t.TempDir()
	s := Store{Root: filepath.Join(root, "store")}
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("same archive"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{a, b} {
		ok, err := s.ShareArchive(p)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Skip("filesystem lacks cloning")
		}
	}
	if err := os.WriteFile(a, []byte("client write"), 0644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(b)
	if err != nil || string(data) != "same archive" {
		t.Fatalf("other client changed %q %v", data, err)
	}
	entries, err := os.ReadDir(filepath.Join(s.Root, "archives"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("archive objects %d %v", len(entries), err)
	}
}

func TestCorruptArchiveCacheIsReplaced(t *testing.T) {
	root := t.TempDir()
	s := Store{Root: filepath.Join(root, "store")}
	source := filepath.Join(root, "data.000")
	if err := os.WriteFile(source, []byte("verified source archive"), 0644); err != nil {
		t.Fatal(err)
	}
	ok, err := s.ShareArchive(source)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Skip("filesystem lacks cloning")
	}
	files, err := filepath.Glob(filepath.Join(s.Root, "archives", "*"))
	if err != nil || len(files) != 1 {
		t.Fatal("cache fixture missing")
	}
	cached := files[0]
	if err := os.Chmod(cached, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cached, []byte("corrupt"), 0444); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ShareArchive(source); err != nil || !ok {
		t.Fatalf("repair %v %v", ok, err)
	}
	data, err := os.ReadFile(cached)
	if err != nil || string(data) != "verified source archive" {
		t.Fatalf("corrupt cache remains: %q %v", data, err)
	}
	files, _ = filepath.Glob(filepath.Join(s.Root, "archives", ".archive-repair-*"))
	if len(files) != 0 {
		t.Fatal("repair temporary files remain")
	}
}
