// Package gamepatch makes the stock Forever client talk to an Everlake realm, by changing two things in the
// running game's memory. The game files on disk are never touched.
//
//   - The server key (this file). When a character enters the world, the server signs EnterEncryptedMode with an
//     Ed25519 key and the client checks it against one of 12 public keys it keeps in memory, one per region group.
//     The entry of the server's region group (8) gets the server's public key, in the built-in copy the game
//     builds its key store from, written once at start-up (see PatchBuiltIn for why not in the store itself).
//   - The login name (portal.go). The client only logs in at names ending in one of Blizzard's domains; the check is
//     switched off so a realm can use a name of its own.
//
// The key store layout follows jibay442/forever_docker (GPL-2.0, app/src-tauri/src/patcher); where and when to
// patch follow Arctium's launcher (MIT, src/Launcher.cs and src/Patterns/Common.cs: Portal, CryptoEdPublicKey). The
// Windows process work is in process_windows.go.
package gamepatch

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"time"
)

// Memory is access to another process's memory.
type Memory interface {
	// Regions are the private read/write regions, as [lo, hi) pairs.
	Regions() [][2]uint64
	// Read returns up to n bytes at addr, or nil when nothing could be read.
	Read(addr uint64, n int) []byte
	// ReadInto is Read into buf (reused between calls); it returns the bytes read.
	ReadInto(addr uint64, buf []byte) []byte
	Write(addr uint64, data []byte) bool
	Alive() bool
}

// The client's 12 built-in public keys, entry 1 first (Blizzard's; the same in every build so far).
var knownKeys = [count]string{
	"9B0671C815DFF513BFD4A2B26AE1F84EC9106841B2FB620DB65F6ADE7C21AD06",
	"112451B9843C2799E4750AA3D9B9AFABF536A645B863C4ADB2078B932B354E04",
	"A6B858485748BF38BE193517AB90F8DE169EFF0989EA9360DB346A378B0FFE15",
	"50FFDEB807F8FF73A299A16000AA6575C5945BE875ADFC8795374ED3415249AC",
	"9F842D078755647C008E5FE3E12B839A981DE02A1F520CB2545CC04CD2323E0E",
	"B8CC75864D8EF461D8DD6AA7425E2C63C24D3982066B8D773A1549BFE24E05EE",
	"76BB7BBDD9F34E124A4573C3AA227E3C87CC603506697D054F6FDEC342E56EBF",
	"1FD6DD8FA0EC30D39E3F72E755B8A045BDE0F70449DC71008B767C2EAA89FB9F",
	"15D618BD7DB577BD9A8D45769C59E4FC631633BF447398A4B489B4C26FBC03AD",
	"B34EC592D2AC4993F5FFC85B15C3DA9078694051CB224345592AF7136D796C99",
	"9E91586DD4B115AB0568B21959E181F6545910BC5E2F30B8975CA09D7BC3EFCE",
	"769C824A1DADD19AEEFA420414D1732DAF44537F98AA229FC5477BCA55F9D59A",
}

var expectedFlags = [count]byte{1, 1, 1, 1, 1, 1, 1, 1, 0, 1, 1, 1}

// StockServerKey is the public half of TrinityCore's stock EnterEncryptedMode key, which an unchanged core signs with.
const StockServerKey = "02596F0D0C061A8B30745988FD72C59E29EC367FB0F341F28E0F08D037BAFC69"

// DefaultRegionGroup is the key entry the server signs for (the core's Network.EnterEncryptedModeRegionGroup).
const DefaultRegionGroup = 8

const (
	entry = 40 // u32 id (1..12), 32-byte key, u8 flag, 3 bytes padding
	count = 12
	block = entry * count
	chunk = 8 << 20
)

type state int

const (
	invalid state = iota
	original
	patched
)

// KeyPatcher puts a server's public key into a client and checks it arrived in the key store.
type KeyPatcher struct {
	keys   [count][32]byte
	newKey [32]byte
	group  int
	buf    []byte // search buffer, reused between passes
}

// NewKeyPatcher checks the key (64 hex characters) and the region group (2-12, not 9: entry 9 is unused).
func NewKeyPatcher(publicKey string, group int) (*KeyPatcher, error) {
	k := &KeyPatcher{group: group}
	for i, s := range knownKeys {
		b, _ := hex.DecodeString(s)
		copy(k.keys[i][:], b)
	}
	b, err := hex.DecodeString(publicKey)
	if err != nil || len(b) != 32 {
		return nil, fmt.Errorf("server key must be 64 hex characters")
	}
	copy(k.newKey[:], b)
	if group < 2 || group > count || group == 9 {
		return nil, fmt.Errorf("region group must be 2-12, not 9 (got %d)", group)
	}
	return k, nil
}

func (k *KeyPatcher) state(m Memory, array uint64) state {
	raw := m.Read(array, block)
	if len(raw) != block {
		return invalid
	}
	for e := 0; e < count; e++ {
		o := raw[e*entry : (e+1)*entry]
		if binary.LittleEndian.Uint32(o) != uint32(e+1) {
			return invalid
		}
		key := o[4:36]
		ok := bytes.Equal(key, k.keys[e][:]) || (e == k.group-1 && bytes.Equal(key, k.newKey[:]))
		// o[37] is uninitialised padding: not checked
		if !ok || o[36] != expectedFlags[e] || o[38] != 0 || o[39] != 0 {
			return invalid
		}
	}
	t := (k.group-1)*entry + 4
	if bytes.Equal(raw[t:t+32], k.newKey[:]) {
		return patched
	}
	return original
}

type store struct {
	addr  uint64
	state state
}

func (k *KeyPatcher) findStores(m Memory) []store {
	needle := k.keys[0][:]
	seen := map[uint64]bool{}
	var found []store
	for _, r := range m.Regions() {
		for off := r[0]; off < r[1]; off += chunk - uint64(len(needle)) {
			take := uint64(chunk)
			if r[1]-off < take {
				take = r[1] - off
			}
			// a part that can't be read is skipped; the rest of the region is still searched
			if k.buf == nil {
				k.buf = make([]byte, chunk)
			}
			buf := m.ReadInto(off, k.buf[:take])
			for i := 0; ; {
				j := bytes.Index(buf[i:], needle)
				if j < 0 {
					break
				}
				hit := off + uint64(i+j)
				if hit%4 == 0 && hit >= 4 && !seen[hit-4] {
					if st := k.state(m, hit-4); st != invalid {
						seen[hit-4] = true
						found = append(found, store{hit - 4, st})
					}
				}
				i += j + 1
			}
			if take < chunk {
				break
			}
		}
	}
	sort.Slice(found, func(a, b int) bool { return found[a].addr < found[b].addr })
	return found
}

// FindBuiltIn looks through the game's image (img, loaded at base) for its built-in copy of the region group's key:
// every place the 32 bytes of Blizzard's key for that entry appear. The game builds its key store from that copy
// when a character logs in. ours counts how many places already hold the server's key (a patch made earlier).
func (k *KeyPatcher) FindBuiltIn(img []byte, base uint64) (orig []uint64, ours int) {
	find := func(needle []byte, hit func(uint64)) {
		for i := 0; ; {
			j := bytes.Index(img[i:], needle)
			if j < 0 {
				return
			}
			hit(base + uint64(i+j))
			i += j + 1
		}
	}
	find(k.keys[k.group-1][:], func(a uint64) { orig = append(orig, a) })
	find(k.newKey[:], func(uint64) { ours++ })
	return orig, ours
}

// PatchBuiltIn writes the server's key at each address FindBuiltIn returned and reports how many writes went in.
//
// This is how the key gets into the game. The live client ships a protection layer, Wow_loader.dll, that the beta
// forever_docker was written for didn't have. Changing the key store on the heap after the game built it made that
// layer kill the game with a "Security Crash" on entering the world (Windows, 2026-11-08). Arctium's launcher changes
// the built-in key in the image instead, once, with the game suspended right after its image unpacks, the same way
// and at the same time as the login-name patch, so this does the same.
func (k *KeyPatcher) PatchBuiltIn(m Memory, addrs []uint64) int {
	n := 0
	for _, a := range addrs {
		if m.Write(a, k.newKey[:]) {
			n++
		}
	}
	return n
}

// Report watches the game's key store until the game closes and says whether the server's key is in it, without
// changing anything there (see PatchBuiltIn). The store only exists once the game starts connecting to a realm, and
// the game may free and rebuild it, so it looks again whenever it goes away.
func (k *KeyPatcher) Report(m Memory, log func(string)) {
	var at uint64
	last := invalid
	saidWaiting := false
	for m.Alive() {
		if at != 0 {
			st := k.state(m, at)
			if st == invalid {
				log("server key: the game freed its key store; looking again at the next login")
				at, last = 0, invalid
				continue
			}
			if st != last {
				k.say(st, log)
				last = st
			}
			time.Sleep(time.Second)
			continue
		}
		stores := k.findStores(m)
		switch len(stores) {
		case 1:
			at = stores[0].addr
		case 0:
			if !saidWaiting {
				log("server key: waiting for the game to open its key store (it does when you log in)")
				saidWaiting = true
			}
			time.Sleep(250 * time.Millisecond)
		default:
			log(fmt.Sprintf("server key: %d key stores at once, waiting", len(stores)))
			time.Sleep(time.Second)
		}
	}
}

func (k *KeyPatcher) say(st state, log func(string)) {
	if st == patched {
		log(fmt.Sprintf("server key: in the game's key store (region group %d)", k.group))
		return
	}
	log(fmt.Sprintf("server key: the game's key store still has Blizzard's key for region group %d, so entering the "+
		"world will fail. The launcher doesn't change it there: the game's protection treats that as tampering", k.group))
}
