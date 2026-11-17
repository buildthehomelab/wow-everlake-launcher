package install

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// A DLL dropped next to the game (an overlay, an injector, a leftover from another client) can stop it from starting
// while every file of the build is intact. Verify and the quick check pass such a folder, so they name the DLLs.
func TestVerifyNamesForeignDLLs(t *testing.T) {
	f := newFakeBuild(t)
	dir := t.TempDir()
	if rep, err := f.run(t, dir, nil); err != nil || rep.Failed() || len(rep.Foreign) != 0 {
		t.Fatalf("install: %+v %v", rep, err)
	}
	game := filepath.Join(dir, "_forever_")
	put := func(rel string) string {
		p := filepath.Join(game, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("not the game's"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	want := []string{put("Utils/version.DLL"), put("dxgi.dll"), put("reshade.asi")}
	put("notes.txt")                        // not a program file
	put("Interface/AddOns/Some/helper.dll") // not a folder the game loads DLLs from
	put("Screenshots/old/d3d11.dll")        // likewise
	put("Utils/readme.dll/inside.txt")      // a folder named like a DLL
	sort.Strings(want)

	rep, err := f.run(t, dir, func(o *Options) { o.Verify = true })
	if err != nil {
		t.Fatal(err)
	}
	if rep.Failed() {
		t.Errorf("foreign DLLs counted as damage: bad %v, missing %v", rep.Bad, rep.Missing)
	}
	if !reflect.DeepEqual(rep.Foreign, want) {
		t.Errorf("verify foreign:\n got %v\nwant %v", rep.Foreign, want)
	}

	q, err := QuickVerify(dir)
	if err != nil {
		t.Fatal(err)
	}
	if q.Failed() || !reflect.DeepEqual(q.Foreign, want) {
		t.Errorf("quick check: failed %v, foreign %v, want %v", q.Failed(), q.Foreign, want)
	}

	// Repair names them too, and leaves them alone.
	rep, err = f.run(t, dir, nil)
	if err != nil || rep.Failed() || !reflect.DeepEqual(rep.Foreign, want) {
		t.Fatalf("repair: %+v %v", rep, err)
	}
	for _, p := range want {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("repair touched %s: %v", p, err)
		}
	}
}

// The build's own DLLs aren't foreign, whatever the case of their names on disk.
func TestForeignProgramsIgnoresOwnedFiles(t *testing.T) {
	dir := t.TempDir()
	utils := filepath.Join(dir, "_forever_", "Utils")
	os.MkdirAll(utils, 0o755)
	os.WriteFile(filepath.Join(utils, "libcef.dll"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "_forever_", "wow_loader.dll"), nil, 0o644)
	got := foreignPrograms(dir, "_forever_", []string{"_forever_/Utils/LIBCEF.DLL", "_forever_/wow_loader.dll"})
	if len(got) != 0 {
		t.Errorf("owned DLLs reported as foreign: %v", got)
	}
	if got := foreignPrograms(filepath.Join(dir, "nowhere"), "_forever_", nil); len(got) != 0 {
		t.Errorf("missing folder: %v", got)
	}
}
