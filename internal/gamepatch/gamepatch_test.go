package gamepatch

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMem is a process with one private read/write region.
type fakeMem struct {
	mu    sync.Mutex
	base  uint64
	data  []byte
	alive bool
}

func (f *fakeMem) Regions() [][2]uint64 { return [][2]uint64{{f.base, f.base + uint64(len(f.data))}} }

func (f *fakeMem) Read(addr uint64, n int) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if addr < f.base || addr+uint64(n) > f.base+uint64(len(f.data)) {
		return nil
	}
	o := addr - f.base
	return append([]byte(nil), f.data[o:o+uint64(n)]...)
}

func (f *fakeMem) ReadInto(addr uint64, buf []byte) []byte {
	b := f.Read(addr, len(buf))
	return buf[:copy(buf, b)]
}

func (f *fakeMem) Write(addr uint64, d []byte) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if addr < f.base || addr+uint64(len(d)) > f.base+uint64(len(f.data)) {
		return false
	}
	copy(f.data[addr-f.base:], d)
	return true
}

func (f *fakeMem) Alive() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alive
}

// keyStore builds the client's 480-byte key store as it is in memory; pad37 is the uninitialised padding byte.
func keyStore(pad37 byte) []byte {
	b := make([]byte, block)
	for e := 0; e < count; e++ {
		o := b[e*entry:]
		binary.LittleEndian.PutUint32(o, uint32(e+1))
		k, _ := hex.DecodeString(knownKeys[e])
		copy(o[4:], k)
		o[36] = expectedFlags[e]
		o[37] = pad37
	}
	return b
}

func TestKeyStoreFound(t *testing.T) {
	kp, err := NewKeyPatcher(StockServerKey, DefaultRegionGroup)
	if err != nil {
		t.Fatal(err)
	}
	mem := &fakeMem{base: 0x10000, data: make([]byte, 64<<10), alive: true}
	// Decoys: a lone copy of key 1, and a store with one wrong flag.
	k1, _ := hex.DecodeString(knownKeys[0])
	copy(mem.data[0x100:], k1)
	bad := keyStore(0x7f)
	bad[2*entry+36] = 0
	copy(mem.data[0x1000:], bad)
	const at = 0x8004
	copy(mem.data[at:], keyStore(0x7f))

	stores := kp.findStores(mem)
	if len(stores) != 1 || stores[0].addr != mem.base+at || stores[0].state != original {
		t.Fatalf("stores: %+v", stores)
	}
	// A store built from a patched built-in key counts as patched.
	ours, _ := hex.DecodeString(StockServerKey)
	copy(mem.data[at+7*entry+4:], ours)
	if st := kp.state(mem, mem.base+at); st != patched {
		t.Fatalf("state with our key: %v", st)
	}
}

func TestBuiltInKey(t *testing.T) {
	kp, _ := NewKeyPatcher(StockServerKey, DefaultRegionGroup)
	const base = 0x140000000
	img := make([]byte, 0x4000)
	k8, _ := hex.DecodeString(knownKeys[7])
	k9, _ := hex.DecodeString(knownKeys[8])
	copy(img[0x1000:], k8) // the built-in key, as raw bytes in the image
	copy(img[0x1020:], k9) // its neighbour, left alone
	copy(img[0x3000:], k8) // a second copy
	orig, ours := kp.FindBuiltIn(img, base)
	if len(orig) != 2 || orig[0] != base+0x1000 || orig[1] != base+0x3000 || ours != 0 {
		t.Fatalf("found %x, ours %d", orig, ours)
	}
	mem := &fakeMem{base: base, data: img, alive: true}
	if n := kp.PatchBuiltIn(mem, orig); n != 2 {
		t.Fatalf("wrote %d of 2", n)
	}
	want, _ := hex.DecodeString(StockServerKey)
	if !bytes.Equal(img[0x1000:0x1020], want) || !bytes.Equal(img[0x3000:0x3020], want) {
		t.Error("built-in key not replaced")
	}
	if !bytes.Equal(img[0x1020:0x1040], k9) {
		t.Error("the neighbouring key changed")
	}
	if orig, ours := kp.FindBuiltIn(img, base); len(orig) != 0 || ours != 2 {
		t.Errorf("after the patch: found %x, ours %d", orig, ours)
	}
}

func TestReportNeverWrites(t *testing.T) {
	kp, _ := NewKeyPatcher(StockServerKey, DefaultRegionGroup)
	mem := &fakeMem{base: 0x40000, data: make([]byte, 32<<10), alive: true}
	var mu sync.Mutex
	var logs []string
	done := make(chan struct{})
	go func() {
		kp.Report(mem, func(l string) { mu.Lock(); logs = append(logs, l); mu.Unlock() })
		close(done)
	}()
	waitLog := func(part string) {
		t.Helper()
		for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			mu.Lock()
			for _, l := range logs {
				if strings.Contains(l, part) {
					mu.Unlock()
					return
				}
			}
			mu.Unlock()
		}
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("never logged %q; got %q", part, logs)
	}
	time.Sleep(300 * time.Millisecond) // the game hasn't opened the store yet
	waitLog("waiting for the game")
	const at = 0x2000
	mem.Write(mem.base+at, keyStore(0))
	waitLog("still has Blizzard's key")
	if st := kp.state(mem, mem.base+at); st != original {
		t.Fatalf("Report changed the store: %v", st)
	}
	// The game frees the store and builds it again from a patched built-in key.
	mem.Write(mem.base+at, make([]byte, block))
	waitLog("freed its key store")
	st := keyStore(0)
	ours, _ := hex.DecodeString(StockServerKey)
	copy(st[7*entry+4:], ours)
	mem.Write(mem.base+at, st)
	waitLog("in the game's key store")
	mem.mu.Lock()
	mem.alive = false
	mem.mu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Report didn't stop when the game closed")
	}
}

func TestNewKeyPatcherChecks(t *testing.T) {
	for _, c := range []struct {
		key   string
		group int
	}{{"abc", 8}, {StockServerKey, 9}, {StockServerKey, 1}, {StockServerKey, 13}} {
		if _, err := NewKeyPatcher(c.key, c.group); err == nil {
			t.Errorf("accepted key %q group %d", c.key, c.group)
		}
	}
}

func TestFindPortal(t *testing.T) {
	const base = 0x140000000
	img := make([]byte, 0x3000)
	suf := portalSuffix()
	// The real one: NUL before and after, and a table entry {pointer, 18} pointing at it.
	copy(img[0x1001:], suf)
	binary.LittleEndian.PutUint64(img[0x2000:], base+0x1001)
	binary.LittleEndian.PutUint64(img[0x2008:], uint64(len(suf)))
	// Not a string of its own: part of a longer name.
	copy(img[0x1100:], "us"+string(suf))
	// A pointer to it without the length beside it.
	binary.LittleEndian.PutUint64(img[0x2100:], base+0x1001)
	binary.LittleEndian.PutUint64(img[0x2108:], 99)

	pp := FindPortal(img, base)
	if len(pp.Suffixes) != 1 || pp.Suffixes[0] != base+0x1001 {
		t.Fatalf("suffixes: %x", pp.Suffixes)
	}
	if len(pp.Lengths) != 1 || pp.Lengths[0] != base+0x2008 {
		t.Fatalf("lengths: %x", pp.Lengths)
	}
	mem := &fakeMem{base: base, data: img, alive: true}
	s, l := pp.Apply(mem)
	if s != 1 || l != 1 {
		t.Fatalf("applied %d endings, %d lengths", s, l)
	}
	if !bytes.Equal(img[0x1001:0x1001+len(suf)], make([]byte, len(suf))) {
		t.Error("ending not blanked")
	}
	if binary.LittleEndian.Uint64(img[0x2008:]) != 0 || binary.LittleEndian.Uint64(img[0x2108:]) != 99 {
		t.Error("wrong length fields changed")
	}
	if !bytes.Contains(img, append([]byte("us"), suf...)) {
		t.Error("the longer name was changed")
	}
	if FindPortal(make([]byte, 64), base).Found() {
		t.Error("found a portal in an empty image")
	}
}
