package casc

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
		if int(l.bucket) != Bucket(short(e)) {
			t.Errorf("%s listed in bucket %d", e, l.bucket)
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
	l0 := s.entries[a]
	l1.bucket = l0.bucket
	s.entries[a] = l1
	if _, err := s.Read(order[0]); err == nil {
		t.Error("a header naming another key was accepted")
	}
	s.Close()
}

func TestBadIndexRefused(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	s, _ := Open(data, "")
	blobs, order := files(20)
	for _, e := range order {
		s.Add(e, blobs[e])
	}
	s.Close()
	list, _ := os.ReadDir(data)
	var idx []string
	for _, f := range list {
		if idxName.MatchString(f.Name()) {
			idx = append(idx, f.Name())
		}
	}
	sort.Strings(idx)
	p := filepath.Join(data, idx[0])
	b, _ := os.ReadFile(p)
	b[blockAt+8] ^= 1 // first entry's key
	os.WriteFile(p, b, 0o644)
	if _, err := Open(data, ""); err == nil {
		t.Error("an index with a wrong checksum was accepted")
	}
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
	os.WriteFile(filepath.Join(data, name), buildIndex(b, []entry{{k, Loc{Size: uint32(len(arch))}}}), 0o644)

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
