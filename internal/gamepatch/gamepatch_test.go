package gamepatch

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
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

func TestKeyStoreFoundAndPatched(t *testing.T) {
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
	if err := kp.patch(mem, stores[0].addr); err != nil {
		t.Fatal(err)
	}
	if st := kp.state(mem, mem.base+at); st != patched {
		t.Fatalf("state after patch: %v", st)
	}
	got := mem.Read(mem.base+at+uint64(7*entry+4), 32)
	want, _ := hex.DecodeString(StockServerKey)
	if !bytes.Equal(got, want) {
		t.Errorf("entry 8 holds %x", got)
	}
	// The other entries are untouched.
	if !bytes.Equal(mem.Read(mem.base+at+4, 32), k1) {
		t.Error("entry 1 changed")
	}
}

func TestWatchPatchesLateStoreAndPutsKeyBack(t *testing.T) {
	kp, _ := NewKeyPatcher(StockServerKey, DefaultRegionGroup)
	mem := &fakeMem{base: 0x40000, data: make([]byte, 32<<10), alive: true}
	var mu sync.Mutex
	readies := 0
	done := make(chan struct{})
	go func() {
		kp.Watch(mem, func(string) {}, func() { mu.Lock(); readies++; mu.Unlock() })
		close(done)
	}()
	waitReadies := func(n int) {
		t.Helper()
		for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			mu.Lock()
			r := readies
			mu.Unlock()
			if r >= n {
				return
			}
		}
		t.Fatalf("key never written (want %d writes)", n)
	}
	time.Sleep(300 * time.Millisecond) // the game hasn't opened the store yet
	const at = 0x2000
	mem.Write(mem.base+at, keyStore(0))
	waitReadies(1)
	// The game reloads Blizzard's key: the watcher puts ours back.
	orig := keyStore(0)
	mem.Write(mem.base+at+uint64(7*entry+4), orig[7*entry+4:7*entry+36])
	waitReadies(2)
	mem.mu.Lock()
	mem.alive = false
	mem.mu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watch didn't stop when the game closed")
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
