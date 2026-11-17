package install

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestClearGameCacheEmptiesOnlyWDB(t *testing.T) {
	game := t.TempDir()
	write(t, filepath.Join(game, "Cache", "WDB", "enUS", "creaturecache.wdb"))
	write(t, filepath.Join(game, "Cache", "WDB", "loose.wdb"))
	write(t, filepath.Join(game, "Cache", "ADB", "enUS", "DBCache.bin"))
	write(t, filepath.Join(game, "Cache", "loose.txt"))
	write(t, filepath.Join(game, "WTF", "Config.wtf"))
	write(t, filepath.Join(game, "Interface", "AddOns", "A", "A.toc"))

	removed, failed, err := ClearGameCache(game)
	if err != nil || removed != 2 || failed != 0 {
		t.Fatalf("ClearGameCache = %d, %d, %v; want 2, 0, nil", removed, failed, err)
	}
	left, err := os.ReadDir(filepath.Join(game, "Cache", "WDB"))
	if err != nil {
		t.Fatalf("the WDB folder itself should stay: %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("WDB still holds %d entries", len(left))
	}
	// The hotfixes (ADB) and everything outside WDB stay.
	for _, keep := range []string{"Cache/ADB/enUS/DBCache.bin", "Cache/loose.txt", "WTF/Config.wtf", "Interface/AddOns/A/A.toc"} {
		if _, err := os.Stat(filepath.Join(game, keep)); err != nil {
			t.Errorf("%s was touched: %v", keep, err)
		}
	}
}

func TestClearGameCacheWithoutWDBFolder(t *testing.T) {
	game := t.TempDir()
	write(t, filepath.Join(game, "Cache", "ADB", "enUS", "DBCache.bin"))
	removed, failed, err := ClearGameCache(game)
	if err != nil || removed != 0 || failed != 0 {
		t.Fatalf("ClearGameCache = %d, %d, %v; want 0, 0, nil", removed, failed, err)
	}
	if _, err := os.Stat(filepath.Join(game, "Cache", "ADB", "enUS", "DBCache.bin")); err != nil {
		t.Fatalf("ADB was touched: %v", err)
	}
}

func TestClearGameCacheWithoutCacheFolder(t *testing.T) {
	removed, failed, err := ClearGameCache(t.TempDir())
	if err != nil || removed != 0 || failed != 0 {
		t.Fatalf("ClearGameCache = %d, %d, %v; want 0, 0, nil", removed, failed, err)
	}
}

func TestClearGameCacheLeavesLinkTargets(t *testing.T) {
	game, elsewhere := t.TempDir(), t.TempDir()
	write(t, filepath.Join(elsewhere, "precious.txt"))
	if err := os.MkdirAll(filepath.Join(game, "Cache", "WDB"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(game, "Cache", "WDB", "link")); err != nil {
		t.Skip("symlinks not available:", err)
	}
	if _, _, err := ClearGameCache(game); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "precious.txt")); err != nil {
		t.Fatalf("a link's target was removed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(game, "Cache", "WDB", "link")); !os.IsNotExist(err) {
		t.Fatalf("the link itself should be gone, got %v", err)
	}
}

func TestClearGameCacheIgnoresCacheThatIsALink(t *testing.T) {
	game, elsewhere := t.TempDir(), t.TempDir()
	write(t, filepath.Join(elsewhere, "precious.txt"))
	if err := os.Symlink(elsewhere, filepath.Join(game, "Cache")); err != nil {
		t.Skip("symlinks not available:", err)
	}
	removed, _, err := ClearGameCache(game)
	if err != nil || removed != 0 {
		t.Fatalf("ClearGameCache = %d, %v; want 0, nil", removed, err)
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "precious.txt")); err != nil {
		t.Fatalf("a linked Cache folder's contents were removed: %v", err)
	}
}

func TestClearGameCacheIgnoresWDBThatIsALink(t *testing.T) {
	game, elsewhere := t.TempDir(), t.TempDir()
	write(t, filepath.Join(elsewhere, "precious.txt"))
	if err := os.MkdirAll(filepath.Join(game, "Cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(game, "Cache", "WDB")); err != nil {
		t.Skip("symlinks not available:", err)
	}
	removed, _, err := ClearGameCache(game)
	if err != nil || removed != 0 {
		t.Fatalf("ClearGameCache = %d, %v; want 0, nil", removed, err)
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "precious.txt")); err != nil {
		t.Fatalf("a linked WDB folder's contents were removed: %v", err)
	}
}
