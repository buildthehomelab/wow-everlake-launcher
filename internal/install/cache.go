package install

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// CacheFolder is the game's cache of what servers have sent it (WDB: creatures, quests, items; ADB: hotfixes),
// inside the game folder.
const CacheFolder = "Cache"

// ClearGameCache empties the game folder's Cache folder, so the game doesn't keep creature, quest or item data
// from an earlier state of the realm: it asks the server again for anything it needs. Only that folder's contents
// go; the folder itself stays. Whatever can't be removed (a file another program holds open) is left and counted
// in failed, and the rest still goes. A missing Cache folder is nothing to clear.
func ClearGameCache(gameDir string) (removed, failed int, err error) {
	cache := filepath.Join(gameDir, CacheFolder)
	info, err := os.Lstat(cache)
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
	entries, err := os.ReadDir(cache)
	if err != nil {
		return 0, 0, err
	}
	for _, e := range entries {
		// RemoveAll removes a link itself, never what it points to.
		if err := os.RemoveAll(filepath.Join(cache, e.Name())); err != nil {
			failed++
			continue
		}
		removed++
	}
	return removed, failed, nil
}
