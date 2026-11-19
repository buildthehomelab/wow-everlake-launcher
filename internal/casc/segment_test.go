package casc

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/tact"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/tacttest"
)

func TestChecksumBFollowsPosition(t *testing.T) {
	// Blizzard's installer wrote these: archive 0, the records at offsets 0 and 30 (data.000 of an Agent install).
	for _, c := range []struct {
		hdr string
		off int64
	}{{"bd45ae2f70a11b7b4a81a4bb120000091e000000010032962c4856a58253", 0},
		{"bd45ae2f70a11b7b4a81a4bb120000061e0000000100bded023bed4f7e79", 30}} {
		h, _ := hex.DecodeString(c.hdr)
		var k tact.Key
		for i := 0; i < 16; i++ {
			k[i] = h[15-i]
		}
		if got := entryHeaderFlags(k, headerLen, 0, c.off, [2]byte{1, 0}); !bytes.Equal(got, h) {
			t.Errorf("offset %d: %x, want %x", c.off, got, h)
		}
	}
}

func TestRealSegmentHeader(t *testing.T) {
	p := os.Getenv("EVERLAKE_REAL_ARCHIVE") // a data.000 Blizzard's installer made
	if p == "" {
		t.Skip("set EVERLAKE_REAL_ARCHIVE")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := validSegment(0, b); !ok {
		t.Error("Blizzard's header doesn't pass validSegment")
	}
}

func TestNewArchiveHasSegment(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	s, err := Open(data, filepath.Join(data, "..", "own.json"))
	if err != nil {
		t.Fatal(err)
	}
	blobs, order := files(5)
	for _, k := range order {
		if err := s.Add(k, blobs[k]); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	head, _ := os.ReadFile(filepath.Join(data, "data.000"))
	if _, ok := validSegment(0, head); !ok {
		t.Fatal("new archive lacks a valid segment header")
	}
	s, _ = Open(data, filepath.Join(data, "..", "own.json"))
	defer s.Close()
	if p := s.SegmentProblems(); len(p) > 0 {
		t.Fatalf("problems: %v", p)
	}
	if s.Len() != 5 {
		t.Errorf("Len %d, want 5", s.Len())
	}
	for _, k := range order {
		if got, err := s.Read(k); err != nil || !bytes.Equal(got, blobs[k]) {
			t.Errorf("read %s: %v", k, err)
		}
	}
}

// An archive written by an older launcher: stored files from offset 0, no header. Mending moves the front files,
// writes the header, and every file still reads.
func TestMendSegments(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	os.MkdirAll(data, 0o755)
	var arch []byte
	var ents []entry
	keys := map[tact.Key][]byte{}
	for i := 0; i < 40; i++ {
		blob, e := tacttest.Plain([]byte(fmt.Sprintf("old file %d with some length to it", i)))
		ents = append(ents, entry{short(e), Loc{Archive: 0, Offset: int64(len(arch)), Size: uint32(headerLen + len(blob))}})
		arch = append(arch, entryHeader(e, uint32(headerLen+len(blob)), 0, int64(len(arch)))...)
		arch = append(arch, blob...)
		keys[e] = blob
	}
	os.WriteFile(filepath.Join(data, "data.000"), arch, 0o644)
	var per [16][]entry
	for _, e := range ents {
		per[Bucket(e.key)] = append(per[Bucket(e.key)], e)
	}
	for b := range per {
		os.WriteFile(filepath.Join(data, fmt.Sprintf("%02x%08x.idx", b, 1)), buildIndex(b, per[b], nil), 0o644)
	}
	own := filepath.Join(dir, "own.json")
	j, _ := json.Marshal(map[string][]int{"archives": {0}})
	os.WriteFile(own, j, 0o644)

	s, err := Open(data, own)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.SegmentProblems()) != 1 {
		t.Fatalf("problems before: %v", s.SegmentProblems())
	}
	changed, err := s.MendSegments()
	if err != nil || len(changed) != 1 {
		t.Fatalf("mend: %v %v", changed, err)
	}
	s.Close()

	s, err = Open(data, own)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if p := s.SegmentProblems(); len(p) > 0 {
		t.Fatalf("problems after: %v", p)
	}
	if s.Len() != 40 {
		t.Errorf("Len %d, want 40", s.Len())
	}
	for k, blob := range keys {
		if got, err := s.Read(k); err != nil || !bytes.Equal(got, blob) {
			t.Errorf("read %s: %v", k, err)
		}
	}
	if changed, err := s.MendSegments(); err != nil || len(changed) != 0 {
		t.Errorf("second mend changed %v %v", changed, err)
	}
	// Pruning everything keeps no archive alive for its records alone.
	if _, a, _, err := s.Prune(map[tact.Key]bool{}); err != nil || a != 2 {
		t.Errorf("prune removed %d archives (%v), want 2", a, err)
	}
	if s.Len() != 0 {
		t.Errorf("Len %d after prune", s.Len())
	}
	for b, bf := range s.buckets {
		if len(bf.entries) != 0 {
			t.Errorf("bucket %d keeps %d entries", b, len(bf.entries))
		}
	}
}
