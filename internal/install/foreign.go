package install

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// foreignExts are the file types the game loads as code from its own folders: DLLs, and .asi plug-ins that DLL
// loaders pick up.
var foreignExts = map[string]bool{".dll": true, ".asi": true}

// foreignPrograms lists the DLLs (and .asi plug-ins) in the game folder, and in the folders the build puts its own
// program files in (Utils, D3D12), that the build doesn't list. Windows loads a DLL from the program's own folder
// before the system's copy, so an overlay, a graphics injector (dxgi.dll, d3d11.dll, version.dll, winmm.dll,
// dinput8.dll) or a leftover from another client sitting there runs inside the game, and a broken or mismatched one
// stops the game from starting. Verify checks the build's files by hash but can't judge a file the build doesn't
// have, so these are named on their own; they aren't counted as damage and nothing removes them.
//
// owned holds the paths, relative to dir and slash-separated, of the files this install is responsible for. Names
// compare case-insensitively, as on Windows. Folders that can't be read are skipped.
func foreignPrograms(dir, gameFolder string, owned []string) []string {
	prefix := strings.ToLower(gameFolder) + "/"
	mine := map[string]bool{}
	folders := map[string]string{"": ""} // folded folder (relative to the game folder) -> as spelled on disk
	for _, p := range owned {
		low := strings.ToLower(filepath.ToSlash(p))
		mine[low] = true
		if !strings.HasPrefix(low, prefix) {
			continue
		}
		rel := filepath.ToSlash(p)[len(prefix):]
		if d := path.Dir(rel); d != "." {
			if _, ok := folders[strings.ToLower(d)]; !ok {
				folders[strings.ToLower(d)] = d
			}
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, d := range folders {
		abs := filepath.Join(dir, gameFolder, filepath.FromSlash(d))
		entries, err := os.ReadDir(abs)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !foreignExts[strings.ToLower(filepath.Ext(e.Name()))] {
				continue
			}
			rel := strings.ToLower(path.Join(gameFolder, d, e.Name()))
			if mine[rel] || seen[rel] {
				continue
			}
			seen[rel] = true
			out = append(out, filepath.Join(abs, e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

// foreign fills the report's Foreign list from the files this run owns.
func (r *run) foreign() {
	r.mu.Lock()
	owned := make([]string, 0, len(r.clientFiles))
	for p := range r.clientFiles {
		owned = append(owned, p)
	}
	r.mu.Unlock()
	r.rep.Foreign = foreignPrograms(r.Dir, r.Pin.GameFolder, owned)
}
