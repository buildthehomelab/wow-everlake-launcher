package gamepatch

import (
	"bytes"
	"encoding/binary"
)

// The client only logs in at login names ending in one of Blizzard's domains. Its data holds a table of
// {domain ending, its length, sign-in page} entries, ".actual.battle.net" (length 18) first (read in Wow.exe of
// 1.60.1.70790). Like Arctium's launcher (src/Patterns/Common.cs, Portal), the ending is blanked so every name ends
// with it; its length in the table is set to 0 too, in case the check compares that many bytes. wow-project-everlake's
// client/macos/keypatch.c does the same on macOS.
//
// The ending is assembled at run time so this program's own copy of it isn't a NUL-terminated string a scan of the
// launcher itself could match.
func portalSuffix() []byte {
	return append([]byte(".actual."), "battle.net"...)
}

// PortalPatch is where the login-name check lives in a game image: the addresses of the ending's bytes, and of the
// table length fields that point at them.
type PortalPatch struct {
	Suffixes []uint64
	Lengths  []uint64
}

// Found reports whether there is anything to patch.
func (p PortalPatch) Found() bool { return len(p.Suffixes) > 0 }

// FindPortal looks through a game image (img, loaded at base) for the ending as a string of its own (a NUL before and
// after) and for 64-bit table entries {pointer to it, 18}.
func FindPortal(img []byte, base uint64) PortalPatch {
	var p PortalPatch
	suf := portalSuffix()
	needle := append(append([]byte{}, suf...), 0)
	at := map[uint64]bool{}
	for i := 0; ; {
		j := bytes.Index(img[i:], needle)
		if j < 0 {
			break
		}
		pos := i + j
		if pos == 0 || img[pos-1] == 0 {
			a := base + uint64(pos)
			p.Suffixes = append(p.Suffixes, a)
			at[a] = true
		}
		i = pos + 1
	}
	if len(at) == 0 {
		return p
	}
	for off := 0; off+16 <= len(img); off += 8 {
		ptr := binary.LittleEndian.Uint64(img[off:])
		if at[ptr] && binary.LittleEndian.Uint64(img[off+8:]) == uint64(len(suf)) {
			p.Lengths = append(p.Lengths, base+uint64(off+8))
		}
	}
	return p
}

// Apply writes the patch: zero lengths first, then the blanked endings. It returns how many of each were written.
func (p PortalPatch) Apply(m Memory) (suffixes, lengths int) {
	zero8 := make([]byte, 8)
	for _, a := range p.Lengths {
		if m.Write(a, zero8) {
			lengths++
		}
	}
	blank := make([]byte, len(portalSuffix()))
	for _, a := range p.Suffixes {
		if m.Write(a, blank) {
			suffixes++
		}
	}
	return suffixes, lengths
}
