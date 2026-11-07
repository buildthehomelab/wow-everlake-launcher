// Package casc reads and writes the game's local storage, the Data/data folder of an installed WoW client:
//
//	data.NNN        archives holding the stored (BLTE) files, each behind a 30-byte header, up to 1 GB each
//	BBVVVVVVVV.idx  one key table per bucket BB (00-0f) at version VVVVVVVV: encoded key (first 9 bytes),
//	                archive and offset, size
//
// The format follows CascLib (MIT) and Project Everlake's casc_local.py, which was checked against a real Battle.net
// install of WoW Forever 1.60.1.70790 (every index checksum right, about a million entries) and whose files that
// client then read. The writer here writes the same layout: version 7 index files with one continuous block of
// entries, and the same 30-byte header in front of each stored file.
//
// Files this package writes go into archives of their own (never into ones the client made, whose free space the
// client tracks itself), and index entries the client made are kept as they are.
package casc

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"sync"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/tact"
)

const (
	segmentBits  = 30
	archiveLimit = 1 << segmentBits // an offset has 30 bits, so an archive holds at most 1 GB
	headerLen    = 30               // the header in front of every stored file
	keyLen       = 9                // index entries keep the first 9 bytes of the encoded key
	entryLen     = keyLen + 5 + 4   // key, archive and offset (5 bytes, big endian), size (4 bytes, little endian)
	blockAt      = 0x20             // where the entry block starts in an index file
	maxOffset    = 0xffc0000000     // the "maximum file offset" field Blizzard's files carry
	// FlushEvery is how many bytes Add writes before it writes the index files again, so an interrupted run loses
	// at most this much.
	FlushEvery = 256 << 20
)

// ErrNotStored means the key has no entry in the index files.
var ErrNotStored = errors.New("not in local storage")

// ShortKey is the part of an encoded key the index files keep.
type ShortKey [keyLen]byte

func short(k tact.Key) ShortKey {
	var s ShortKey
	copy(s[:], k[:keyLen])
	return s
}

// Bucket is the index file a key belongs in: the two halves of the XOR of its 9 bytes, XORed together.
func Bucket(k ShortKey) int {
	var x byte
	for _, b := range k {
		x ^= b
	}
	return int(x&0xf ^ x>>4)
}

// Loc is where a stored file is: its archive number, the offset of its header, and its size with the header.
type Loc struct {
	Archive int
	Offset  int64
	Size    uint32
	bucket  uint8 // the index file it's listed in (the client sometimes uses the next bucket over)
}

// Storage is one open Data/data folder. Read is safe to call from many goroutines; Add serialises its writes.
type Storage struct {
	dir     string
	ownPath string

	mu       sync.Mutex
	names    [16]string // index file name per bucket, kept when rewritten; "" = no file yet
	entries  map[ShortKey]Loc
	dirty    [16]bool
	archives map[int]bool // every data.NNN in the folder
	own      []int        // archives this package made (recorded in ownPath)
	readers  map[int]*os.File

	w         *os.File // the archive being written, or nil
	wNum      int
	wOff      int64
	unflushed int64

	limit, flushEvery int64 // archiveLimit and FlushEvery; smaller in tests
	readOnly          bool
}

var (
	idxName  = regexp.MustCompile(`^([0-9a-f]{2})([0-9a-f]{8})\.idx$`)
	dataName = regexp.MustCompile(`^data\.(\d{3})$`)
)

// Open reads the storage in dir (made if missing). ownPath is a file that records the archives this package made:
// archives listed there that no index entry points at are left over from an interrupted run and are removed.
func Open(dir, ownPath string) (*Storage, error) { return open(dir, ownPath, false) }

// OpenReadOnly reads the storage in dir and never writes: a missing folder is an empty storage, leftovers stay,
// and Add fails.
func OpenReadOnly(dir string) (*Storage, error) { return open(dir, "", true) }

func open(dir, ownPath string, readOnly bool) (*Storage, error) {
	s := &Storage{dir: dir, ownPath: ownPath, entries: map[ShortKey]Loc{}, archives: map[int]bool{},
		readers: map[int]*os.File{}, limit: archiveLimit, flushEvery: FlushEvery, readOnly: readOnly}
	if !readOnly {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	list, err := os.ReadDir(dir)
	if readOnly && os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var versions [16]uint64
	for _, f := range list {
		if m := idxName.FindStringSubmatch(f.Name()); m != nil {
			b, _ := strconv.ParseUint(m[1], 16, 8)
			v, _ := strconv.ParseUint(m[2], 16, 32)
			if b < 16 && (s.names[b] == "" || v > versions[b]) {
				s.names[b], versions[b] = f.Name(), v
			}
		}
		if m := dataName.FindStringSubmatch(f.Name()); m != nil {
			n, _ := strconv.Atoi(m[1])
			s.archives[n] = true
		}
	}
	for b, name := range s.names {
		if name == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		entries, err := parseIndex(data, b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		for _, e := range entries {
			e.loc.bucket = uint8(b)
			s.entries[e.key] = e.loc
		}
	}
	if readOnly {
		return s, nil
	}
	if err := s.readOwn(); err != nil {
		return nil, err
	}
	used := map[int]bool{}
	for _, l := range s.entries {
		used[l.Archive] = true
	}
	var keep []int
	for _, n := range s.own {
		if used[n] {
			keep = append(keep, n)
			continue
		}
		if err := os.Remove(s.archivePath(n)); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		delete(s.archives, n)
	}
	if len(keep) != len(s.own) {
		s.own = keep
		if err := s.writeOwn(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Storage) archivePath(n int) string { return filepath.Join(s.dir, fmt.Sprintf("data.%03d", n)) }

func (s *Storage) readOwn() error {
	if s.ownPath == "" {
		return nil
	}
	data, err := os.ReadFile(s.ownPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var v struct {
		Archives []int `json:"archives"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return fmt.Errorf("%s: %w", s.ownPath, err)
	}
	s.own = v.Archives
	return nil
}

func (s *Storage) writeOwn() error {
	if s.ownPath == "" {
		return nil
	}
	data, _ := json.Marshal(struct {
		Archives []int `json:"archives"`
	}{s.own})
	return writeFile(s.ownPath, append(data, '\n'))
}

// Len is the number of index entries.
func (s *Storage) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// Lookup finds a key's place.
func (s *Storage) Lookup(k tact.Key) (Loc, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.entries[short(k)]
	return l, ok
}

// Read returns a stored file (the BLTE bytes, without the header). It checks the header names this key and size;
// checking the bytes against the key is the caller's job (tact.VerifyBLTE).
func (s *Storage) Read(k tact.Key) ([]byte, error) {
	s.mu.Lock()
	l, ok := s.entries[short(k)]
	var f *os.File
	var err error
	if ok {
		f, err = s.reader(l.Archive)
	}
	s.mu.Unlock()
	if !ok {
		return nil, ErrNotStored
	}
	if err != nil {
		return nil, err
	}
	if l.Size < headerLen {
		return nil, fmt.Errorf("%s: index size %d is shorter than the header", k, l.Size)
	}
	buf := make([]byte, l.Size)
	if _, err := f.ReadAt(buf, l.Offset); err != nil {
		return nil, fmt.Errorf("%s: data.%03d at %d: %w", k, l.Archive, l.Offset, err)
	}
	// The header holds the key reversed: Blizzard's installer writes the 9 bytes it knows behind 7 zeros, this
	// package all 16. Either way bytes 7-15 are the first 9 bytes of the key, reversed.
	for i := 0; i < keyLen; i++ {
		if buf[15-i] != k[i] {
			return nil, fmt.Errorf("%s: data.%03d at %d holds another file", k, l.Archive, l.Offset)
		}
	}
	if got := binary.LittleEndian.Uint32(buf[16:]); got != l.Size {
		return nil, fmt.Errorf("%s: data.%03d at %d: header size %d, index size %d", k, l.Archive, l.Offset, got,
			l.Size)
	}
	return buf[headerLen:], nil
}

// reader returns an open handle on archive n. Callers hold s.mu.
func (s *Storage) reader(n int) (*os.File, error) {
	if s.w != nil && n == s.wNum {
		return s.w, nil
	}
	if f := s.readers[n]; f != nil {
		return f, nil
	}
	f, err := os.Open(s.archivePath(n))
	if err != nil {
		return nil, err
	}
	s.readers[n] = f
	return f, nil
}

// Add stores a file (BLTE bytes named by encoded key k) in this package's current archive, and lists it in the
// index, replacing any entry the key had. The caller checks the bytes first.
func (s *Storage) Add(k tact.Key, blob []byte) error {
	if s.readOnly {
		return errors.New("local storage is open read-only")
	}
	size := int64(headerLen + len(blob))
	if size > s.limit {
		return fmt.Errorf("%s: %d bytes don't fit an archive", k, len(blob))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.w == nil || s.wOff+size > s.limit {
		if err := s.roll(); err != nil {
			return err
		}
	}
	buf := make([]byte, 0, size)
	buf = append(buf, entryHeader(k, uint32(size), s.wOff)...)
	buf = append(buf, blob...)
	if _, err := s.w.WriteAt(buf, s.wOff); err != nil {
		return err
	}
	key := short(k)
	if old, ok := s.entries[key]; ok {
		s.dirty[old.bucket] = true
	}
	b := Bucket(key)
	s.entries[key] = Loc{Archive: s.wNum, Offset: s.wOff, Size: uint32(size), bucket: uint8(b)}
	s.dirty[b] = true
	s.wOff += size
	s.unflushed += size
	if s.unflushed >= s.flushEvery {
		return s.flush()
	}
	return nil
}

// roll closes the current archive (writing the index files) and starts a new one after every existing archive.
// Callers hold s.mu.
func (s *Storage) roll() error {
	if s.w != nil {
		if err := s.flush(); err != nil {
			return err
		}
		if err := s.w.Close(); err != nil {
			return err
		}
		s.w = nil
	}
	n := 0
	for a := range s.archives {
		if a+1 > n {
			n = a + 1
		}
	}
	if n >= 1<<(40-segmentBits) {
		return errors.New("no free archive number left in local storage")
	}
	// Record the archive as ours before writing to it, so an interrupted run's leftovers can be found.
	s.own = append(s.own, n)
	if err := s.writeOwn(); err != nil {
		return err
	}
	f, err := os.OpenFile(s.archivePath(n), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	s.w, s.wNum, s.wOff = f, n, 0
	s.archives[n] = true
	return nil
}

// Flush makes everything added so far durable: the archive is synced, then the changed index files are written.
func (s *Storage) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flush()
}

func (s *Storage) flush() error {
	if s.readOnly {
		return nil
	}
	if s.w != nil {
		if err := s.w.Sync(); err != nil {
			return err
		}
	}
	s.unflushed = 0
	var any bool
	for _, d := range s.dirty {
		any = any || d
	}
	if !any {
		return nil
	}
	var per [16][]entry
	for k, l := range s.entries {
		if s.dirty[l.bucket] {
			per[l.bucket] = append(per[l.bucket], entry{k, l})
		}
	}
	for b := range per {
		if !s.dirty[b] {
			continue
		}
		if s.names[b] == "" {
			s.names[b] = fmt.Sprintf("%02x%08x.idx", b, 1)
		}
		if err := writeFile(filepath.Join(s.dir, s.names[b]), buildIndex(b, per[b])); err != nil {
			return err
		}
		s.dirty[b] = false
	}
	return nil
}

// Close flushes and closes every file.
func (s *Storage) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.flush()
	if s.w != nil {
		if e := s.w.Close(); err == nil {
			err = e
		}
		s.w = nil
	}
	for n, f := range s.readers {
		f.Close()
		delete(s.readers, n)
	}
	return err
}

// --- formats -----------------------------------------------------------------------------------------------------

type entry struct {
	key ShortKey
	loc Loc
}

// parseIndex reads a version 7 index file with one continuous entry block, checking both checksums.
func parseIndex(data []byte, bucket int) ([]entry, error) {
	if len(data) < blockAt+8 {
		return nil, errors.New("index file too short")
	}
	if binary.LittleEndian.Uint32(data) != 16 || binary.LittleEndian.Uint32(data[4:]) != hashlittle(data[8:24], 0) {
		return nil, errors.New("index header checksum doesn't match")
	}
	h := data[8:24]
	if v := binary.LittleEndian.Uint16(h); v != 7 {
		return nil, fmt.Errorf("index version %d (only 7 is known)", v)
	}
	if int(h[2]) != bucket {
		return nil, fmt.Errorf("index file of bucket %d says bucket %d", bucket, h[2])
	}
	if h[4] != 4 || h[5] != 5 || h[6] != keyLen || h[7] != segmentBits {
		return nil, fmt.Errorf("index field sizes %d/%d/%d, segment bits %d (only 4/5/9, 30 are known)", h[4], h[5],
			h[6], h[7])
	}
	size := int(binary.LittleEndian.Uint32(data[blockAt:]))
	hash := binary.LittleEndian.Uint32(data[blockAt+4:])
	if size%entryLen != 0 || blockAt+8+size > len(data) {
		return nil, fmt.Errorf("index entry block of %d bytes in a %d-byte file", size, len(data))
	}
	raw := data[blockAt+8 : blockAt+8+size]
	var hi, lo uint32
	out := make([]entry, 0, size/entryLen)
	for i := 0; i < size; i += entryLen {
		e := raw[i : i+entryLen]
		hi, lo = hashlittle2(e, hi, lo)
		var k ShortKey
		copy(k[:], e)
		var loc [8]byte
		copy(loc[3:], e[keyLen:keyLen+5])
		where := binary.BigEndian.Uint64(loc[:])
		out = append(out, entry{k, Loc{Archive: int(where >> segmentBits), Offset: int64(where & (archiveLimit - 1)),
			Size: binary.LittleEndian.Uint32(e[keyLen+5:])}})
	}
	if hi != hash {
		return nil, errors.New("index entry block checksum doesn't match")
	}
	return out, nil
}

// buildIndex writes a version 7 index file: header, one sorted entry block, and zeros after it, with room to spare
// (the client's own files have spare room too, and the block's size field says where the entries end).
func buildIndex(bucket int, entries []entry) []byte {
	raw := make([][]byte, len(entries))
	for i, e := range entries {
		b := make([]byte, entryLen)
		copy(b, e.key[:])
		var loc [8]byte
		binary.BigEndian.PutUint64(loc[:], uint64(e.loc.Archive)<<segmentBits|uint64(e.loc.Offset))
		copy(b[keyLen:], loc[3:])
		binary.LittleEndian.PutUint32(b[keyLen+5:], e.loc.Size)
		raw[i] = b
	}
	sort.Slice(raw, func(i, j int) bool { return bytes.Compare(raw[i], raw[j]) < 0 })
	used := blockAt + 8 + len(raw)*entryLen
	size := (used + 0x10000 + 0xffff) &^ 0xffff // at least 64 KiB spare, in 64 KiB steps
	out := make([]byte, size)
	h := out[8:24]
	binary.LittleEndian.PutUint16(h, 7)
	h[2], h[3], h[4], h[5], h[6], h[7] = byte(bucket), 0, 4, 5, keyLen, segmentBits
	binary.LittleEndian.PutUint64(h[8:], maxOffset)
	binary.LittleEndian.PutUint32(out, 16)
	binary.LittleEndian.PutUint32(out[4:], hashlittle(h, 0))
	var hi, lo uint32
	at := blockAt + 8
	for _, e := range raw {
		hi, lo = hashlittle2(e, hi, lo)
		at += copy(out[at:], e)
	}
	binary.LittleEndian.PutUint32(out[blockAt:], uint32(len(raw)*entryLen))
	binary.LittleEndian.PutUint32(out[blockAt+4:], hi)
	return out
}

var offsetTable = [16]uint32{0x049396b8, 0x72a82a9b, 0xee626cca, 0x9917754f, 0x15de40b1, 0xf5a8a9b6, 0x421eac7e,
	0xa9d55c9a, 0x317fd40c, 0x04faf80d, 0x3d6be971, 0x52933cfd, 0x27f64b7d, 0xc6f5c11b, 0xd5757e3a, 0x6c388745}

// entryHeader is the 30-byte header in front of a stored file (CascLib's VerifyHeaderSpan): the encoded key
// reversed, the size with the header, two flag bytes, a Jenkins hash of those, and a checksum that mixes in the
// header's offset in its archive.
func entryHeader(k tact.Key, size uint32, offset int64) []byte {
	head := make([]byte, 0, headerLen)
	for i := 15; i >= 0; i-- {
		head = append(head, k[i])
	}
	head = binary.LittleEndian.AppendUint32(head, size)
	head = append(head, 0, 0)
	head = binary.LittleEndian.AppendUint32(head, hashlittle(head, 0x3d6be971))
	v := uint32(offset + headerLen)
	var encoded [4]byte
	binary.LittleEndian.PutUint32(encoded[:], offsetTable[v&0xf]^v)
	var hashed [4]byte
	for i, b := range head {
		hashed[i&3] ^= b
	}
	n := len(head)
	for j := 0; j < 4; j++ {
		i := (n + j) & 3
		head = append(head, hashed[i]^encoded[i])
	}
	return head
}

// writeFile writes through a temporary file and a rename.
func writeFile(p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".part-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), p)
}
