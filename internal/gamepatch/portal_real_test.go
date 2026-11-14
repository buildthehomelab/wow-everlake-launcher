package gamepatch

import (
	"debug/pe"
	"os"
	"testing"
)

// TestRealGamePortalTable looks for the login-name table in a real Windows game executable, laid out as it is in
// memory: EVERLAKE_REAL_WOW_EXE=<install folder>\_forever_\Wow.exe (`install --tags Windows,x86_64,enUS,US --no-data`
// fetches one, about 490 MB). Run it when the pin moves: the launcher can only use a login name of its own while the
// table has the shape FindPortal knows.
func TestRealGamePortalTable(t *testing.T) {
	path := os.Getenv("EVERLAKE_REAL_WOW_EXE")
	if path == "" {
		t.Skip("EVERLAKE_REAL_WOW_EXE not set")
	}
	f, err := pe.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	oh, ok := f.OptionalHeader.(*pe.OptionalHeader64)
	if !ok {
		t.Fatal("not a 64-bit executable")
	}
	img := make([]byte, oh.SizeOfImage)
	section := map[string][2]uint32{}
	for _, s := range f.Sections {
		data, err := s.Data()
		if err != nil {
			t.Fatalf("section %s: %v", s.Name, err)
		}
		copy(img[s.VirtualAddress:], data)
		section[s.Name] = [2]uint32{s.VirtualAddress, s.VirtualAddress + s.VirtualSize}
	}
	where := func(rva uint64) string {
		for name, r := range section {
			if rva >= uint64(r[0]) && rva < uint64(r[1]) {
				return name
			}
		}
		return "?"
	}
	pp := FindPortal(img, oh.ImageBase)
	if len(pp.Entries) != 1 {
		t.Fatalf("%d login-name table entries, want 1", len(pp.Entries))
	}
	e := pp.Entries[0]
	at, ending := e.At-oh.ImageBase, e.Ending-oh.ImageBase
	t.Logf("table entry at rva %x (%s), ending at rva %x (%s)", at, where(at), ending, where(ending))
	// The launcher writes the entry and never the ending: the entry has to be in the section the game writes itself.
	if where(at) != ".data" {
		t.Errorf("the table entry is in %s, not .data: the launcher can't write it without changing page protection",
			where(at))
	}
}
