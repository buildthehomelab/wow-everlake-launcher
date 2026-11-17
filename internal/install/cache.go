package install

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// CacheFolder is the game's cache of what servers have sent it, inside the game folder. Its WDB folder holds the
// creature, quest and item data a realm sent; its ADB folder holds the realm's hotfixes (DBCache.bin).
const CacheFolder = "Cache"

// WDBFolder is the part of CacheFolder that ClearGameCache empties.
const WDBFolder = "WDB"

// ClearGameCache empties the game folder's Cache/WDB folder, so the game doesn't keep creature, quest or item data
// from an earlier state of the realm: it asks the server again for anything it needs. Cache/ADB, the hotfixes, is
// left alone: the game fetches only the hotfix pushes it doesn't hold, and a client that had to fetch all of them
// at once crashed entering the world (1.60.1.70938). Only WDB's contents go; the folder itself stays. Whatever can't
// be removed (a file another program holds open) is left and counted in failed, and the rest still goes. A missing
// folder is nothing to clear, and a Cache or WDB that is a link isn't ours to touch.
func ClearGameCache(gameDir string) (removed, failed int, err error) {
	cache := filepath.Join(gameDir, CacheFolder)
	for _, dir := range []string{cache, filepath.Join(cache, WDBFolder)} {
		info, err := os.Lstat(dir)
		if errors.Is(err, fs.ErrNotExist) {
			return 0, 0, nil
		}
		if err != nil {
			return 0, 0, err
		}
		if !info.IsDir() {
			// A file or a link where the folder should be isn't ours to touch.
			return 0, 0, nil
		}
	}
	wdb := filepath.Join(cache, WDBFolder)
	entries, err := os.ReadDir(wdb)
	if err != nil {
		return 0, 0, err
	}
	for _, e := range entries {
		// RemoveAll removes a link itself, never what it points to.
		if err := os.RemoveAll(filepath.Join(wdb, e.Name())); err != nil {
			failed++
			continue
		}
		removed++
	}
	return removed, failed, nil
}
