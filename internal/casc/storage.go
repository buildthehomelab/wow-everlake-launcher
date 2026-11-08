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
// What the client made is left as it was: files this package writes go into archives of its own (never into ones the
// client made, whose free space the client tracks itself), and a key table it rewrites keeps every entry, its header
// bytes and the bytes after its entries. About 1% of the entries a real client writes sit in the bucket after the
// formula's ("cross reference"); lookups check both, as the client does, and an entry stays in the bucket it was in.
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
	"strings"
	"sync"
	"time"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/tact"
)

const (
	segmentBits  = 30
	archiveLimit = 1 << segmentBits // an offset has 30 bits, so an archive holds at most 1 GB
	maxArchives  = 1000             // data.000 to data.999
	headerLen    = 30               // the header in front of every stored file
	keyLen       = 9                // index entries keep the first 9 bytes of the encoded key
	entryLen     = keyLen + 5 + 4   // key, archive and offset (5 bytes, big endian), size (4 bytes, little endian)
	blockAt      = 0x20             // where the entry block starts in an index file
	maxOffset    = 0xffc0000000     // the "maximum file offset" field Blizzard's files carry
	// FlushEvery and FlushAfter: Add writes the index files again after this many bytes or this much time, whichever
	// comes first, so a run that's killed loses at most that much download.
	FlushEvery = 256 << 20
	FlushAfter = 30 * time.Second
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
}

// bucketFile is one key table: its file name (kept when rewritten), the file as last read or written (whose header
// and tail are kept), and its entries.
type bucketFile struct {
	name    string
	orig    []byte
	entries map[ShortKey]Loc
	dirty   bool
}

// Storage is one open Data/data folder. Read is safe to call from many goroutines, also while Add writes.
type Storage struct {
	dir     string
	ownPath string

	mu       sync.Mutex
	buckets  [16]*bucketFile
	archives map[int]bool // every data.NNN in the folder or named by an entry
	own      []int        // archives this package made (recorded in ownPath)
	readers  map[int]*os.File
	bad      []string // key tables that couldn't be read
	mended   []string // key tables whose update area had broken entries (rewritten on the next flush)

	w         *os.File // the archive being written, or nil
	wNum      int
	wOff      int64
	unflushed int64
	lastFlush time.Time

	limit, flushEvery int64 // archiveLimit and FlushEvery; smaller in tests
	readOnly          bool
}

var (
	idxName  = regexp.MustCompile(`^([0-9a-f]{2})([0-9a-f]{8})\.idx$`)
	dataName = regexp.MustCompile(`^data\.(\d{3,})$`)
)

// Open reads the storage in dir (made if missing). ownPath is a file that records the archives this package made:
// archives listed there that no index entry points at are left over from an interrupted run and are removed. A key
// table that can't be read is copied to bad-index/ next to ownPath and started again empty (its files are then
// missing, so an install fetches them again). The caller must make sure no other process writes the folder.
func Open(dir, ownPath string) (*Storage, error) { return open(dir, ownPath, false) }

// OpenReadOnly reads the storage in dir and never writes: a missing folder is an empty storage, leftovers stay,
// a key table that can't be read is reported by Bad and counts as empty, and Add fails.
func OpenReadOnly(dir string) (*Storage, error) { return open(dir, "", true) }

func open(dir, ownPath string, readOnly bool) (*Storage, error) {
	s := &Storage{dir: dir, ownPath: ownPath, archives: map[int]bool{}, readers: map[int]*os.File{},
		limit: archiveLimit, flushEvery: FlushEvery, readOnly: readOnly, lastFlush: time.Now()}
	for b := range s.buckets {
		s.buckets[b] = &bucketFile{entries: map[ShortKey]Loc{}}
	}
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
		name := f.Name()
		if m := idxName.FindStringSubmatch(name); m != nil {
			b, _ := strconv.ParseUint(m[1], 16, 8)
			v, _ := strconv.ParseUint(m[2], 16, 32)
			if b < 16 && (s.buckets[b].name == "" || v > versions[b]) {
				s.buckets[b].name, versions[b] = name, v
			}
		}
		if m := dataName.FindStringSubmatch(name); m != nil {
			n, _ := strconv.Atoi(m[1])
			s.archives[n] = true
		}
		if strings.HasPrefix(name, ".part-") && !readOnly {
			os.Remove(filepath.Join(dir, name)) // a temporary file from a run that was cut off
		}
	}
	for b, bf := range s.buckets {
		if bf.name == "" {
			continue
		}
		p := filepath.Join(dir, bf.name)
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		entries, err := parseIndex(data, b)
		if err != nil {
			s.bad = append(s.bad, fmt.Sprintf("%s (%v)", p, err))
			if !readOnly {
				if err := s.keepBad(bf.name, data); err != nil {
					return nil, err
				}
				bf.dirty = true // written again with what gets added
			}
			continue
		}
		bf.orig = data
		if _, bad := updateSlots(data); bad > 0 {
			// The update area holds entries that fail their hash: the client then calls the whole storage damaged
			// ("Corruption detected in KMT file (invalid update hash)"). Launchers up to v0.1.0-alpha.5 left it so
			// after the sorted block changed size. Rewriting keeps the good entries and drops the rest.
			s.mended = append(s.mended, fmt.Sprintf("%s (%d update entries fail their hash)", p, bad))
			if !readOnly {
				if err := s.keepBad(bf.name, data); err != nil {
					return nil, err
				}
				bf.dirty = true
			}
		}
		for _, e := range entries {
			bf.entries[e.key] = e.loc
			s.archives[e.loc.Archive] = true
		}
	}
	if readOnly {
		return s, nil
	}
	if err := s.readOwn(); err != nil {
		return nil, err
	}
	used := map[int]bool{}
	for _, bf := range s.buckets {
		for _, l := range bf.entries {
			used[l.Archive] = true
		}
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

// keepBad copies a key table that couldn't be read next to ownPath, so nothing the client made is lost for good.
func (s *Storage) keepBad(name string, data []byte) error {
	if s.ownPath == "" {
		return nil
	}
	p := filepath.Join(filepath.Dir(s.ownPath), "bad-index", time.Now().UTC().Format("20060102-150405")+"-"+name)
	return WriteFile(p, data, 0o644)
}

// Bad lists the key tables that couldn't be read, with why.
func (s *Storage) Bad() []string { return append([]string(nil), s.bad...) }

// Mended lists the key tables whose update area held entries that fail their hash, with how many. The client refuses
// to start on such a table; a writable storage rewrites it without them on the next flush (Flush, Add or Close).
func (s *Storage) Mended() []string { return append([]string(nil), s.mended...) }

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
	return WriteFile(s.ownPath, append(data, '\n'), 0o644)
}

// Len is the number of index entries.
func (s *Storage) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, bf := range s.buckets {
		n += len(bf.entries)
	}
	return n
}

// find looks a key up in its bucket, then in the next one. Callers hold s.mu.
func (s *Storage) find(k ShortKey) (Loc, int, bool) {
	b := Bucket(k)
	for _, at := range []int{b, (b + 1) % 16} {
		if l, ok := s.buckets[at].entries[k]; ok {
			return l, at, true
		}
	}
	return Loc{}, 0, false
}

// Lookup finds a key's place.
func (s *Storage) Lookup(k tact.Key) (Loc, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, _, ok := s.find(short(k))
	return l, ok
}

// Read returns a stored file (the BLTE bytes, without the header). It checks the header names this key and size;
// checking the bytes against the key is the caller's job (tact.VerifyBLTE).
func (s *Storage) Read(k tact.Key) ([]byte, error) {
	s.mu.Lock()
	l, _, ok := s.find(short(k))
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
	if l.Size < headerLen || int64(l.Size) > archiveLimit || l.Offset+int64(l.Size) > archiveLimit {
		return nil, fmt.Errorf("%s: index entry for data.%03d at %d has an impossible size %d", k, l.Archive,
			l.Offset, l.Size)
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

// reader returns an open handle on archive n. Handles stay open until Close, so a Read never finds its file closed
// under it. Callers hold s.mu.
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
// index, replacing any entry the key had (in the bucket that entry was in). The caller checks the bytes first.
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
	target := Bucket(key)
	if _, at, ok := s.find(key); ok {
		target = at
	}
	for _, at := range []int{Bucket(key), (Bucket(key) + 1) % 16} { // a key listed in both loses both listings
		if _, ok := s.buckets[at].entries[key]; ok {
			delete(s.buckets[at].entries, key)
			s.buckets[at].dirty = true
		}
	}
	s.buckets[target].entries[key] = Loc{Archive: s.wNum, Offset: s.wOff, Size: uint32(size)}
	s.buckets[target].dirty = true
	s.wOff += size
	s.unflushed += size
	if s.unflushed >= s.flushEvery || time.Since(s.lastFlush) >= FlushAfter {
		return s.flush()
	}
	return nil
}

// roll finishes the current archive (writing the index files) and starts a new one numbered after every archive
// in the folder and every archive an entry names. Callers hold s.mu.
func (s *Storage) roll() error {
	if s.w != nil {
		if err := s.flush(); err != nil {
			return err
		}
		s.readers[s.wNum] = s.w // still read from; closed by Close
		s.w = nil
	}
	n := 0
	for a := range s.archives {
		if a+1 > n {
			n = a + 1
		}
	}
	if n >= maxArchives {
		return errors.New("no free archive number left in local storage")
	}
	// Record the archive as ours before writing to it, so an interrupted run's leftovers can be found; take it back
	// if it can't be made (someone else made that file).
	s.own = append(s.own, n)
	if err := s.writeOwn(); err != nil {
		s.own = s.own[:len(s.own)-1]
		return err
	}
	f, err := os.OpenFile(s.archivePath(n), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		s.own = s.own[:len(s.own)-1]
		s.writeOwn()
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
	s.lastFlush = time.Now()
	for b, bf := range s.buckets {
		if !bf.dirty {
			continue
		}
		if bf.name == "" {
			bf.name = fmt.Sprintf("%02x%08x.idx", b, 1)
		}
		entries := make([]entry, 0, len(bf.entries))
		for k, l := range bf.entries {
			entries = append(entries, entry{k, l})
		}
		data := buildIndex(b, entries, bf.orig)
		if err := WriteFile(filepath.Join(s.dir, bf.name), data, 0o644); err != nil {
			return err
		}
		bf.orig, bf.dirty = data, false
	}
	return nil
}

// Prune drops the entries this package made whose encoded key isn't in keep, and deletes any of its own archives
// that no entry points at any more, reclaiming their space. Entries and archives the game itself wrote are never
// touched (only the archives recorded in archives.json are this package's). It is how an update to a new build
// removes the old build's data. keep holds full encoded keys; an index entry keeps only the first 9 bytes, so a
// key whose 9 bytes collide with a kept one is kept too — safe, since the worst case is keeping an extra file.
func (s *Storage) Prune(keep map[tact.Key]bool) (entries, archives int, reclaimed int64, err error) {
	if s.readOnly {
		return 0, 0, 0, errors.New("local storage is open read-only")
	}
	keepShort := make(map[ShortKey]bool, len(keep))
	for k := range keep {
		keepShort[short(k)] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	own := map[int]bool{}
	for _, n := range s.own {
		own[n] = true
	}
	for _, bf := range s.buckets {
		for k, loc := range bf.entries {
			if !keepShort[k] && own[loc.Archive] {
				delete(bf.entries, k)
				bf.dirty = true
				entries++
			}
		}
	}
	if entries == 0 {
		return 0, 0, 0, nil
	}
	used := map[int]bool{}
	for _, bf := range s.buckets {
		for _, loc := range bf.entries {
			used[loc.Archive] = true
		}
	}
	var keepOwn []int
	for _, n := range s.own {
		if used[n] || (s.w != nil && n == s.wNum) { // never delete the archive currently being written
			keepOwn = append(keepOwn, n)
			continue
		}
		if f := s.readers[n]; f != nil { // Windows can't delete a file it still has open
			f.Close()
			delete(s.readers, n)
		}
		if fi, e := os.Stat(s.archivePath(n)); e == nil {
			reclaimed += fi.Size()
		}
		if e := os.Remove(s.archivePath(n)); e != nil && !os.IsNotExist(e) {
			return entries, archives, reclaimed, e
		}
		delete(s.archives, n)
		archives++
	}
	s.own = keepOwn
	if e := s.writeOwn(); e != nil {
		return entries, archives, reclaimed, e
	}
	return entries, archives, reclaimed, s.flush()
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

// The update area. After the sorted block the client keeps the entries it adds while it runs (the files it
// streams): 0x200-byte pages from the first 4 KiB boundary after the block, each holding 21 slots of 24 bytes (a
// 32-bit hash with the top bit set, the 18-byte entry, a status byte the hash covers, one spare byte). A slot whose
// hash is zero is empty. The client checks every slot's hash and calls the storage damaged when one fails, so the
// area must move with the block when the block changes size (CascLib's LoadIndexFile_V2 reads the same pages).
const (
	updateAlign = 0x1000
	updatePage  = 0x200
	updateSlot  = 24
	updateLen   = 0x7800 // the room a Blizzard-written key table leaves for it
	slotsInPage = updatePage / updateSlot
)

// sortedEnd is where the sorted block of a well-formed key table ends.
func sortedEnd(data []byte) int {
	return blockAt + 8 + int(binary.LittleEndian.Uint32(data[blockAt:]))
}

// updateSlots returns the update area's good slots in file order, and how many non-empty slots fail their hash. It
// reads every slot of every page from the 4 KiB boundary after the sorted block to the end of the file, so it also
// finds an area that an older launcher left further on, behind entries of an earlier, longer block.
func updateSlots(data []byte) (slots [][]byte, bad int) {
	if len(data) < blockAt+8 {
		return nil, 0
	}
	start := (sortedEnd(data) + updateAlign - 1) &^ (updateAlign - 1)
	for page := start; page+updatePage <= len(data); page += updatePage {
		for i := 0; i < slotsInPage; i++ {
			at := page + i*updateSlot
			h := binary.LittleEndian.Uint32(data[at:])
			if h == 0 {
				continue
			}
			if hashlittle(data[at+4:at+4+entryLen+1], 0)|0x80000000 != h {
				bad++
				continue
			}
			slots = append(slots, append([]byte(nil), data[at:at+updateSlot]...))
		}
	}
	return slots, bad
}

// buildIndex writes a version 7 index file: the sorted entry block, then the update area at the first 4 KiB
// boundary after it. Rewriting an existing file (orig) keeps its header bytes and the good entries of its update
// area, in their order, packed from the area's new start; slots that fail their hash and any other bytes after the
// block are dropped. The file keeps at least its old size. A new file gets Blizzard's header values. Either way the
// file has room for at least updateLen bytes of update area, rounded up to 64 KiB.
func buildIndex(bucket int, entries []entry, orig []byte) []byte {
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
	var slots [][]byte
	if orig != nil {
		slots, _ = updateSlots(orig)
	}
	start := (used + updateAlign - 1) &^ (updateAlign - 1)
	area := (len(slots) + slotsInPage - 1) / slotsInPage * updatePage
	if area < updateLen {
		area = updateLen
	}
	size := (start + area + 0xffff) &^ 0xffff
	var out []byte
	if orig != nil {
		if size < len(orig) {
			size = len(orig)
		}
		out = make([]byte, size)
		copy(out, orig[:blockAt])
		for i, sl := range slots {
			copy(out[start+i/slotsInPage*updatePage+i%slotsInPage*updateSlot:], sl)
		}
	} else {
		out = make([]byte, size)
		h := out[8:24]
		binary.LittleEndian.PutUint16(h, 7)
		h[2], h[3], h[4], h[5], h[6], h[7] = byte(bucket), 0, 4, 5, keyLen, segmentBits
		binary.LittleEndian.PutUint64(h[8:], maxOffset)
		binary.LittleEndian.PutUint32(out, 16)
		binary.LittleEndian.PutUint32(out[4:], hashlittle(h, 0))
	}
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

// WriteFile writes a file through a temporary file and a rename, so a cut-off run never leaves a half file under
// the real name. The rename is retried for a few seconds: on Windows it fails while another program (an
// antivirus scanner, the search indexer) has the old file open.
func WriteFile(p string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".part-*")
	if err != nil {
		return err
	}
	fail := func(err error) error {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(mode); err != nil { // CreateTemp makes it owner-only
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	wait := 20 * time.Millisecond
	for try := 0; ; try++ {
		err = os.Rename(tmp.Name(), p)
		if err == nil || try == 8 {
			break
		}
		time.Sleep(wait)
		wait *= 2
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}
