package gamepatch

import (
	"bytes"
	"encoding/binary"
)

// The client only logs in at login names ending in one of Blizzard's domains. Its data holds a table of
// {domain ending, its length, sign-in page} entries, ".actual.battle.net" (length 18) first (read in Wow.exe of
// 1.60.1.70790): the ending itself is a string in .rdata, the table entry {pointer to it, 18} is in .data.
//
// Only the table entry is changed: its pointer goes to an empty string (the NUL right after the ending) and its length
// to 0, so every name ends with it. The ending in .rdata is never touched. Arctium's launcher and the first Windows
// builds (alpha.5 to alpha.11) blanked the ending itself, which meant making a page of the protection's read-only
// .rdata writable; the live client's protection (Wow_loader.dll) noticed and ended the game with a "Security Crash"
// about 30 seconds after start, even with the bytes put back (alpha.11, 2026-11-08). .data is written by the game all
// the time, so nothing there has to be made writable.
//
// The check only matters while logging in, so the entry is put back as soon as the player is logged in (Restore) and
// changed again when they're back at the login screen. Both setups known to get into the world never patch anything
// here: Karrz's launcher, and ours while the server ran on the same Mac, use trinity.actual.battle.net through the
// hosts file instead.
//
// The ending is assembled at run time so this program's own copy of it isn't a NUL-terminated string a scan of the
// launcher itself could match.
func portalSuffix() []byte {
	return append([]byte(".actual."), "battle.net"...)
}

// PortalEntry is one table entry {pointer, length} that points at the ending.
type PortalEntry struct {
	At     uint64 // address of the entry's pointer; its length follows
	Ending uint64 // where the pointer points: the ending's first byte
}

// PortalPatch is where the login-name check lives in a game image: the table entries that point at the ending.
type PortalPatch struct {
	Entries []PortalEntry
}

// Found reports whether there is anything to patch.
func (p PortalPatch) Found() bool { return len(p.Entries) > 0 }

// FindPortal looks through a game image (img, loaded at base) for the ending as a string of its own (a NUL before and
// after), then for 64-bit table entries {pointer to it, 18}, and for entries already changed by Apply {pointer to the
// NUL after it, 0}, so a second look finds the same entries.
func FindPortal(img []byte, base uint64) PortalPatch {
	var p PortalPatch
	suf := portalSuffix()
	needle := append(append([]byte{}, suf...), 0)
	at := map[uint64]uint64{} // pointer value that marks an entry -> the ending it belongs to
	for i := 0; ; {
		j := bytes.Index(img[i:], needle)
		if j < 0 {
			break
		}
		pos := i + j
		if pos == 0 || img[pos-1] == 0 {
			a := base + uint64(pos)
			at[a] = a
			at[a+uint64(len(suf))] = a
		}
		i = pos + 1
	}
	if len(at) == 0 {
		return p
	}
	for off := 0; off+16 <= len(img); off += 8 {
		ptr := binary.LittleEndian.Uint64(img[off:])
		end, ok := at[ptr]
		if !ok {
			continue
		}
		n := binary.LittleEndian.Uint64(img[off+8:])
		if (ptr == end && n == uint64(len(suf))) || (ptr != end && n == 0) {
			p.Entries = append(p.Entries, PortalEntry{At: base + uint64(off), Ending: end})
		}
	}
	return p
}

// Apply points every entry at an empty string, length 0, and returns how many were written.
func (p PortalPatch) Apply(m Memory) int {
	n := 0
	for _, e := range p.Entries {
		if m.Write(e.At, entryBytes(e.Ending+uint64(len(portalSuffix())), 0)) {
			n++
		}
	}
	return n
}

// Restore points every entry back at the ending, length 18, and returns how many were written.
func (p PortalPatch) Restore(m Memory) int {
	n := 0
	for _, e := range p.Entries {
		if m.Write(e.At, entryBytes(e.Ending, uint64(len(portalSuffix())))) {
			n++
		}
	}
	return n
}

func entryBytes(ptr, length uint64) []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint64(b, ptr)
	binary.LittleEndian.PutUint64(b[8:], length)
	return b
}
