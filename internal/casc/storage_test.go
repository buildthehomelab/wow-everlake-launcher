package casc

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/tact"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/tacttest"
)

func TestHashlittle(t *testing.T) {
	// lookup3.c's own test values.
	for _, c := range []struct {
		in   string
		init uint32
		want uint32
	}{{"", 0, 0xdeadbeef}, {"Four score and seven years ago", 0, 0x17770551},
		{"Four score and seven years ago", 1, 0xcd628161}} {
		if got := hashlittle([]byte(c.in), c.init); got != c.want {
			t.Errorf("hashlittle(%q, %d) = %#x, want %#x", c.in, c.init, got, c.want)
		}
	}
	// From casc_local.py, which checked a million real index entries with it.
	hi, lo := hashlittle2([]byte("0123456789abcdefghijklmnopqrstuvwxyz"), 7, 9)
	if hi != 0xe7df90e2 || lo != 0xaa5d28e0 {
		t.Errorf("hashlittle2 = %#x %#x", hi, lo)
	}
}

func TestEntryHeader(t *testing.T) {
	// The bytes casc_local.py writes, which the Forever client read from a storage it was given (gate 2).
	k := tact.MustKey("00112233445566778899aabbccddeeff")
	for _, c := range []struct {
		size   uint32
		offset int64
		want   string
	}{{1234, 0, "ffeeddccbbaa99887766554433221100d20400000000cdae61deb87b97a4"},
		{99999, 1073000000, "ffeeddccbbaa998877665544332211009f860100000091a05b64114aa032"}} {
		if got := hex.EncodeToString(entryHeader(k, c.size, c.offset)); got != c.want {
			t.Errorf("entryHeader(%d, %d) = %s, want %s", c.size, c.offset, got, c.want)
		}
	}
	if Bucket(short(k)) != 0 {
		t.Errorf("bucket = %d, want 0", Bucket(short(k)))
	}
}

func files(n int) (map[tact.Key][]byte, []tact.Key) {
	out := map[tact.Key][]byte{}
	var order []tact.Key
	for i := 0; i < n; i++ {
		content := []byte(fmt.Sprintf("stored file %d ", i))
		for seed := sha256.Sum256(content); len(content) < 40+i*37; seed = sha256.Sum256(seed[:]) {
			content = append(content, seed[:]...) // doesn't compress, so the sizes are real
		}
		blob, e := tacttest.Plain(content)
		out[e] = blob
		order = append(order, e)
	}
	return out, order
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	own := filepath.Join(dir, "own.json")
	s, err := Open(filepath.Join(dir, "data"), own)
	if err != nil {
		t.Fatal(err)
	}
	s.limit = 4096 // force several archives
	blobs, order := files(60)
	for _, e := range order {
		if err := s.Add(e, blobs[e]); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range order { // readable before the index is written
		if got, err := s.Read(e); err != nil || !bytes.Equal(got, blobs[e]) {
			t.Fatalf("%s before close: %v", e, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(filepath.Join(dir, "data"), own)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Len() != len(order) {
		t.Fatalf("%d entries after reopening, want %d", s.Len(), len(order))
	}
	archives := map[int]bool{}
	for _, e := range order {
		got, err := s.Read(e)
		if err != nil || !bytes.Equal(got, blobs[e]) {
			t.Fatalf("%s: %v", e, err)
		}
		if err := tact.VerifyBLTE(got, e); err != nil {
			t.Fatal(err)
		}
		l, _ := s.Lookup(e)
		archives[l.Archive] = true
		if l.Offset+int64(l.Size) > 4096 {
			t.Errorf("%s runs past the archive limit", e)
		}
		if _, ok := s.buckets[Bucket(short(e))].entries[short(e)]; !ok {
			t.Errorf("%s not listed in its bucket", e)
		}
	}
	if len(archives) < 3 {
		t.Errorf("only %d archives", len(archives))
	}
	if _, err := s.Read(tact.MustKey("ffffffffffffffffffffffffffffffff")); err != ErrNotStored {
		t.Errorf("unknown key: %v", err)
	}
	// Every index file is a valid version 7 file of its bucket.
	list, _ := os.ReadDir(filepath.Join(dir, "data"))
	n := 0
	for _, f := range list {
		if m := idxName.FindStringSubmatch(f.Name()); m != nil {
			n++
			data, _ := os.ReadFile(filepath.Join(dir, "data", f.Name()))
			var b int
			fmt.Sscanf(m[1], "%x", &b)
			if _, err := parseIndex(data, b); err != nil {
				t.Errorf("%s: %v", f.Name(), err)
			}
			if len(data)%0x10000 != 0 {
				t.Errorf("%s: %d bytes, not in 64 KiB steps", f.Name(), len(data))
			}
		}
	}
	if n == 0 {
		t.Error("no index files")
	}
}

func TestReplaceAndDamage(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	s, err := Open(data, "")
	if err != nil {
		t.Fatal(err)
	}
	blobs, order := files(5)
	for _, e := range order {
		if err := s.Add(e, blobs[e]); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()

	// Damage one file in place: the read still succeeds (the header is intact) but the bytes no longer verify.
	s, _ = Open(data, "")
	l, _ := s.Lookup(order[2])
	s.Close()
	f, _ := os.OpenFile(filepath.Join(data, fmt.Sprintf("data.%03d", l.Archive)), os.O_RDWR, 0)
	f.WriteAt([]byte("!"), l.Offset+headerLen+10)
	f.Close()
	s, _ = Open(data, "")
	got, err := s.Read(order[2])
	if err != nil || tact.VerifyBLTE(got, order[2]) == nil {
		t.Fatalf("damage went unnoticed: %v", err)
	}
	// Adding it again moves its entry to a new archive.
	if err := s.Add(order[2], blobs[order[2]]); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, _ = Open(data, "")
	defer s.Close()
	l2, _ := s.Lookup(order[2])
	if l2.Archive == l.Archive {
		t.Errorf("re-added file stayed in archive %d", l.Archive)
	}
	if got, err := s.Read(order[2]); err != nil || tact.VerifyBLTE(got, order[2]) != nil {
		t.Errorf("after repair: %v", err)
	}
	if s.Len() != 5 {
		t.Errorf("%d entries, want 5", s.Len())
	}
}

func TestHeaderMismatch(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	s, _ := Open(data, "")
	blobs, order := files(2)
	for _, e := range order {
		s.Add(e, blobs[e])
	}
	s.Close()
	// Point the first key's entry at the second file.
	s, _ = Open(data, "")
	a := short(order[0])
	l1, _ := s.Lookup(order[1])
	s.buckets[Bucket(a)].entries[a] = l1
	if _, err := s.Read(order[0]); err == nil {
		t.Error("a header naming another key was accepted")
	}
	s.Close()
}

func TestLeftoverArchiveRemoved(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	own := filepath.Join(dir, "own.json")
	s, _ := Open(data, own)
	s.flushEvery = 1 << 40
	blobs, order := files(3)
	s.Add(order[0], blobs[order[0]])
	s.Flush()
	// A run that stops before writing the index again: its next archive is left with no entries pointing at it.
	s.limit = 1 // force a new archive for the next file
	s.mu.Lock()
	s.roll()
	s.w.WriteAt([]byte("half a file"), 0)
	leftover := s.archivePath(s.wNum)
	s.w.Close()
	for _, f := range s.readers { // a killed process leaves no open handles; Windows can't delete open files
		f.Close()
	}
	s.mu.Unlock()
	// A client archive nobody points at is left alone.
	client := filepath.Join(data, "data.099")
	os.WriteFile(client, []byte("client"), 0o644)

	s2, err := Open(data, own)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Errorf("leftover archive kept: %v", err)
	}
	if _, err := os.Stat(client); err != nil {
		t.Errorf("client archive removed: %v", err)
	}
	if got, err := s2.Read(order[0]); err != nil || !bytes.Equal(got, blobs[order[0]]) {
		t.Errorf("flushed file lost: %v", err)
	}
	// New files go after every archive, the client's too.
	s2.Add(order[1], blobs[order[1]])
	if l, _ := s2.Lookup(order[1]); l.Archive != 100 {
		t.Errorf("new archive %d, want 100", l.Archive)
	}
}

func TestKeepsClientEntries(t *testing.T) {
	// An index made elsewhere with an entry in the next bucket over (as the client does for some keys) keeps that
	// entry where it is when this package rewrites the files.
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	os.MkdirAll(data, 0o755)
	blob, e := tacttest.Plain([]byte("client file"))
	k := short(e)
	b := (Bucket(k) + 1) % 16
	arch := append(entryHeader(e, uint32(headerLen+len(blob)), 0), blob...)
	os.WriteFile(filepath.Join(data, "data.000"), arch, 0o644)
	name := fmt.Sprintf("%02x%08x.idx", b, 0x23)
	os.WriteFile(filepath.Join(data, name), buildIndex(b, []entry{{k, Loc{Size: uint32(len(arch))}}}, nil), 0o644)

	s, err := Open(data, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Read(e); err != nil || !bytes.Equal(got, blob) {
		t.Fatalf("client file: %v", err)
	}
	blobs, order := files(40)
	for _, x := range order {
		s.Add(x, blobs[x])
	}
	s.Close()
	got, _ := os.ReadFile(filepath.Join(data, name))
	entries, err := parseIndex(got, b)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, x := range entries {
		found = found || x.key == k
	}
	if !found {
		t.Errorf("client entry moved out of %s", name)
	}
	if _, err := os.Stat(filepath.Join(data, fmt.Sprintf("%02x%08x.idx", b, 1))); err == nil {
		t.Errorf("a second index file was made for bucket %d", b)
	}
}

// TestWriteFixture writes a small storage for checking with casc_local.py:
// EVERLAKE_CASC_FIXTURE=<dir> go test -run WriteFixture ./internal/casc
func TestWriteFixture(t *testing.T) {
	out := os.Getenv("EVERLAKE_CASC_FIXTURE")
	if out == "" {
		t.Skip("EVERLAKE_CASC_FIXTURE not set")
	}
	s, err := Open(filepath.Join(out, "data"), "")
	if err != nil {
		t.Fatal(err)
	}
	blobs, order := files(200)
	var keys []string
	for _, e := range order {
		if err := s.Add(e, blobs[e]); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, e.String())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(keys)
	os.WriteFile(filepath.Join(out, "keys.txt"), []byte(fmt.Sprintln(keys)), 0o644)
}

// clientStorage writes a storage as a client might leave it: one archive, and key tables with the given entries per
// bucket, a header flags byte the writer doesn't use, and old bytes after the entries.
func clientStorage(t *testing.T, data string, per map[int][]entry) {
	t.Helper()
	os.MkdirAll(data, 0o755)
	for b, es := range per {
		f := buildIndex(b, es, nil)
		f[11] = 0x5a // header flags byte
		h := f[8:24]
		binary.LittleEndian.PutUint32(f[4:], hashlittle(h, 0))
		copy(f[len(f)-16:], "old client bytes")
		os.WriteFile(filepath.Join(data, fmt.Sprintf("%02x%08x.idx", b, 0x23)), f, 0o644)
	}
}

func TestClientDuplicatesAndBytesKept(t *testing.T) {
	// A key the client lists in its bucket and in the next one keeps both listings when this package rewrites one of
	// the two tables; the rewritten table keeps its header bytes and the bytes after its entries.
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	blob, e := tacttest.Plain([]byte("client file"))
	arch := append(entryHeader(e, uint32(headerLen+len(blob)), 0), blob...)
	k := short(e)
	b := Bucket(k)
	loc := Loc{Size: uint32(len(arch))}
	clientStorage(t, data, map[int][]entry{b: {{k, loc}}, (b + 1) % 16: {{k, loc}}})
	os.WriteFile(filepath.Join(data, "data.000"), arch, 0o644)

	s, err := Open(data, filepath.Join(dir, "own.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Add files until one lands in bucket b.
	blobs, order := files(80)
	for _, x := range order {
		s.Add(x, blobs[x])
	}
	if len(s.buckets[b].entries) < 2 {
		t.Fatal("no added file landed in the client's bucket")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, at := range []int{b, (b + 1) % 16} {
		p := filepath.Join(data, fmt.Sprintf("%02x%08x.idx", at, 0x23))
		f, _ := os.ReadFile(p)
		es, err := parseIndex(f, at)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, x := range es {
			found = found || x.key == k
		}
		if !found {
			t.Errorf("client key lost from bucket %d", at)
		}
		if f[11] != 0x5a {
			t.Errorf("bucket %d: header flags byte not kept", at)
		}
		if string(f[len(f)-16:]) != "old client bytes" {
			t.Errorf("bucket %d: bytes after the entries not kept", at)
		}
	}
}

func TestReplacedEntryStaysInItsBucket(t *testing.T) {
	// An entry the client put in the next bucket over is replaced there, not moved to the formula's bucket.
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	blob, e := tacttest.Plain([]byte("client file in the next bucket"))
	k := short(e)
	next := (Bucket(k) + 1) % 16
	clientStorage(t, data, map[int][]entry{next: {{k, Loc{Size: uint32(headerLen + len(blob))}}}})
	os.WriteFile(filepath.Join(data, "data.000"), bytes.Repeat([]byte{0}, headerLen+len(blob)), 0o644)
	s, _ := Open(data, "")
	if _, err := s.Read(e); err == nil {
		t.Fatal("a zeroed file read back")
	}
	s.Add(e, blob)
	if _, ok := s.buckets[next].entries[k]; !ok {
		t.Error("replaced entry moved out of its bucket")
	}
	if got, err := s.Read(e); err != nil || !bytes.Equal(got, blob) {
		t.Errorf("after replacing: %v", err)
	}
	s.Close()
}

func TestBadIndexKeptAndRebuilt(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	own := filepath.Join(dir, "own.json")
	s, _ := Open(data, own)
	blobs, order := files(40)
	for _, e := range order {
		s.Add(e, blobs[e])
	}
	s.Close()
	k := short(order[0])
	p := filepath.Join(data, fmt.Sprintf("%02x%08x.idx", Bucket(k), 1))
	b, _ := os.ReadFile(p)
	b[blockAt+8] ^= 1
	os.WriteFile(p, b, 0o644)

	ro, err := OpenReadOnly(data)
	if err != nil {
		t.Fatalf("read-only open of a storage with a bad key table: %v", err)
	}
	if len(ro.Bad()) != 1 {
		t.Errorf("bad = %v", ro.Bad())
	}
	if _, err := ro.Read(order[0]); err != ErrNotStored {
		t.Errorf("file in a bad table: %v", err)
	}
	ro.Close()
	if got, _ := os.ReadFile(p); !bytes.Equal(got, b) {
		t.Error("read-only open changed the bad table")
	}

	s, err = Open(data, own)
	if err != nil {
		t.Fatal(err)
	}
	backups, _ := filepath.Glob(filepath.Join(dir, "bad-index", "*"))
	if len(backups) != 1 {
		t.Errorf("backups: %v", backups)
	}
	s.Add(order[0], blobs[order[0]])
	s.Close()
	s, _ = OpenReadOnly(data)
	defer s.Close()
	if len(s.Bad()) != 0 {
		t.Errorf("still bad after rebuilding: %v", s.Bad())
	}
	if got, err := s.Read(order[0]); err != nil || !bytes.Equal(got, blobs[order[0]]) {
		t.Errorf("re-added file: %v", err)
	}
}

func TestArchiveTakenByOthers(t *testing.T) {
	// Someone else makes the next archive number after Open listed the folder: the add fails, and the number isn't
	// recorded as ours (so it's never removed as a leftover).
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	own := filepath.Join(dir, "own.json")
	s, _ := Open(data, own)
	os.WriteFile(filepath.Join(data, "data.000"), []byte("client's"), 0o644)
	blobs, order := files(1)
	if err := s.Add(order[0], blobs[order[0]]); err == nil {
		t.Fatal("added into an archive someone else made")
	}
	s.Close()
	b, _ := os.ReadFile(own)
	if strings.Contains(string(b), "0") {
		t.Errorf("own.json = %s", b)
	}
	s, _ = Open(data, own)
	defer s.Close()
	if _, err := os.Stat(filepath.Join(data, "data.000")); err != nil {
		t.Error("the other archive was removed")
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	own := filepath.Join(dir, "own.json")

	// A file the client wrote (its own archive + index), which Prune must never touch.
	blob, ce := tacttest.Plain([]byte("client file kept forever"))
	arch := append(entryHeader(ce, uint32(headerLen+len(blob)), 0), blob...)
	ck := short(ce)
	cb := Bucket(ck)
	clientStorage(t, data, map[int][]entry{cb: {{ck, Loc{Size: uint32(len(arch))}}}})
	os.WriteFile(filepath.Join(data, "data.000"), arch, 0o644)

	s, err := Open(data, own)
	if err != nil {
		t.Fatal(err)
	}
	s.limit = 4096 // several of our own archives
	blobs, order := files(60)
	for _, e := range order {
		if err := s.Add(e, blobs[e]); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()

	s, _ = Open(data, own)
	keep := map[tact.Key]bool{ce: true} // keep the client file and the first half of ours
	for _, e := range order[:30] {
		keep[e] = true
	}
	entries, archives, reclaimed, err := s.Prune(keep)
	if err != nil {
		t.Fatal(err)
	}
	if entries != 30 {
		t.Errorf("pruned %d entries, want 30", entries)
	}
	if archives == 0 || reclaimed == 0 {
		t.Errorf("freed %d archives, %d bytes", archives, reclaimed)
	}
	s.Close()

	s, _ = Open(data, own)
	defer s.Close()
	if s.Len() != 31 { // 30 kept + the client file
		t.Errorf("%d entries left, want 31", s.Len())
	}
	for _, e := range order[:30] {
		if got, err := s.Read(e); err != nil || tact.VerifyBLTE(got, e) != nil {
			t.Errorf("kept file %s: %v", e, err)
		}
	}
	for _, e := range order[30:] {
		if _, err := s.Read(e); err != ErrNotStored {
			t.Errorf("pruned file %s still here: %v", e, err)
		}
	}
	if got, err := s.Read(ce); err != nil || !bytes.Equal(got, blob) {
		t.Errorf("client file pruned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "data.000")); err != nil {
		t.Errorf("client archive removed: %v", err)
	}
	// Pruning with everything kept frees nothing.
	all := map[tact.Key]bool{ce: true}
	for _, e := range order[:30] {
		all[e] = true
	}
	if n, _, _, _ := s.Prune(all); n != 0 {
		t.Errorf("a no-op prune removed %d entries", n)
	}
}
