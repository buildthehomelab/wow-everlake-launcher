package mirror

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/tact"
)

// Store is the mirror's copy of the CDN, laid out as the CDN is: <dir>/<cdn path>/<kind>/ab/cd/<key><suffix>.
//
// Whole files (configs, indexes, loose data files) are written in one go and are complete when they exist. An
// archive is filled in by ranges as they are fetched: it is a sparse file with a <key>.have file next to it that
// lists the byte ranges written so far. An archive file without its .have is never read.
type Store struct {
	dir string

	mu   sync.Mutex
	arch map[string]*archive // by file path
}

type archive struct {
	mu     sync.Mutex
	loaded bool
	have   []span // sorted, not touching
	total  int64  // the archive's full size as the CDN gave it (-1: not known)
}

// span is the byte range [Start, End).
type span struct{ Start, End int64 }

type haveFile struct {
	Total int64      `json:"total"`
	Have  [][2]int64 `json:"have"`
}

// OpenStore uses dir (made if missing).
func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir, arch: map[string]*archive{}}, nil
}

// Path is where a file lives in the store.
func (s *Store) Path(cdnPath, kind string, key tact.Key, suffix string) string {
	k := key.String()
	return filepath.Join(s.dir, filepath.FromSlash(cdnPath), kind, k[:2], k[2:4], k+suffix)
}

// ReadWhole returns a whole file, or fs.ErrNotExist. An archive being filled in by ranges doesn't count.
func (s *Store) ReadWhole(cdnPath, kind string, key tact.Key, suffix string) ([]byte, error) {
	p := s.Path(cdnPath, kind, key, suffix)
	if _, err := os.Stat(p + ".have"); err == nil {
		return nil, fs.ErrNotExist
	}
	return os.ReadFile(p)
}

// PutWhole writes a whole file (the caller has checked it).
func (s *Store) PutWhole(cdnPath, kind string, key tact.Key, suffix string, data []byte) error {
	p := s.Path(cdnPath, kind, key, suffix)
	if _, err := os.Stat(p + ".have"); err == nil {
		return fmt.Errorf("%s is an archive being filled in by ranges", p)
	}
	return writeNoSync(p, data)
}

// HasWhole reports whether a whole file is stored (not checked).
func (s *Store) HasWhole(cdnPath, kind string, key tact.Key, suffix string) bool {
	p := s.Path(cdnPath, kind, key, suffix)
	if _, err := os.Stat(p + ".have"); err == nil {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// RemoveWhole deletes a whole file that turned out damaged. An archive being filled in by ranges is left alone.
func (s *Store) RemoveWhole(cdnPath, kind string, key tact.Key, suffix string) error {
	p := s.Path(cdnPath, kind, key, suffix)
	if _, err := os.Stat(p + ".have"); err == nil {
		return nil
	}
	err := os.Remove(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Store) archive(cdnPath string, key tact.Key) (*archive, string, error) {
	p := s.Path(cdnPath, "data", key, "")
	s.mu.Lock()
	a := s.arch[p]
	if a == nil {
		a = &archive{total: -1}
		s.arch[p] = a
	}
	s.mu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.loaded {
		data, err := os.ReadFile(p + ".have")
		switch {
		case err == nil:
			// A .have that can't be read lists nothing: its ranges are fetched again and it's rewritten then.
			var h haveFile
			if json.Unmarshal(data, &h) == nil {
				a.total = h.Total
				for _, r := range h.Have {
					a.have = addSpan(a.have, span{r[0], r[1]})
				}
			}
		case !errors.Is(err, fs.ErrNotExist):
			return nil, p, err
		}
		a.loaded = true
	}
	return a, p, nil
}

// Covered reports whether all of [start, end) of an archive is stored, and the archive's full size if known. It
// doesn't read the bytes, but an archive file too short for its .have counts as holding nothing.
func (s *Store) Covered(cdnPath string, key tact.Key, start, end int64) (ok bool, total int64, err error) {
	a, p, err := s.archive(cdnPath, key)
	if err != nil {
		return false, -1, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !covers(a.have, span{start, end}) {
		return false, a.total, nil
	}
	if st, err := os.Stat(p); err != nil || st.Size() < end {
		return false, a.total, nil
	}
	return true, a.total, nil
}

// ReadRange returns [start, end) of an archive if all of it is stored (ok false otherwise), and the archive's full
// size if known.
func (s *Store) ReadRange(cdnPath string, key tact.Key, start, end int64) (data []byte, total int64, ok bool, err error) {
	a, p, err := s.archive(cdnPath, key)
	if err != nil {
		return nil, -1, false, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !covers(a.have, span{start, end}) {
		return nil, a.total, false, nil
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, a.total, false, err
	}
	defer f.Close()
	data = make([]byte, end-start)
	if _, err := f.ReadAt(data, start); err != nil {
		return nil, a.total, false, fmt.Errorf("%s: %w", p, err)
	}
	return data, a.total, true, nil
}

// PutRange writes data at start into an archive and records [start, start+len) as stored. total is the archive's
// full size (-1 if not known). The caller has checked the bytes.
func (s *Store) PutRange(cdnPath string, key tact.Key, start int64, data []byte, total int64) error {
	a, p, err := s.archive(cdnPath, key)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	// The .have file goes first (with nothing new in it) so a crash never leaves an archive that looks whole.
	if _, err := os.Stat(p + ".have"); errors.Is(err, fs.ErrNotExist) {
		if err := s.writeHave(p, a.have, a.total); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteAt(data, start); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	have := addSpan(append([]span(nil), a.have...), span{start, start + int64(len(data))})
	if total < 0 {
		total = a.total
	}
	if err := s.writeHave(p, have, total); err != nil {
		return err
	}
	a.have, a.total = have, total
	return nil
}

// DropRange forgets [start, end) of an archive (bytes there turned out damaged).
func (s *Store) DropRange(cdnPath string, key tact.Key, start, end int64) error {
	a, p, err := s.archive(cdnPath, key)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	have := removeSpan(a.have, span{start, end})
	if err := s.writeHave(p, have, a.total); err != nil {
		return err
	}
	a.have = have
	return nil
}

func (s *Store) writeHave(p string, have []span, total int64) error {
	h := haveFile{Total: total, Have: make([][2]int64, len(have))}
	for i, r := range have {
		h.Have[i] = [2]int64{r.Start, r.End}
	}
	data, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return writeNoSync(p+".have", data)
}

// writeNoSync replaces a file through a temporary file and a rename, without forcing it to disk. The mirror never
// forces writes: on Patronus they stall the disk the game's database is on, and anything a crash damages fails its
// hash check when next served and is fetched again.
func writeNoSync(p string, data []byte) error {
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
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// addSpan merges r into a sorted list of spans (touching spans are joined).
func addSpan(list []span, r span) []span {
	if r.End <= r.Start {
		return list
	}
	out := make([]span, 0, len(list)+1)
	for _, x := range list {
		if x.End < r.Start || x.Start > r.End {
			out = append(out, x)
			continue
		}
		if x.Start < r.Start {
			r.Start = x.Start
		}
		if x.End > r.End {
			r.End = x.End
		}
	}
	out = append(out, r)
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// removeSpan takes r out of a sorted list of spans.
func removeSpan(list []span, r span) []span {
	var out []span
	for _, x := range list {
		if x.End <= r.Start || x.Start >= r.End {
			out = append(out, x)
			continue
		}
		if x.Start < r.Start {
			out = append(out, span{x.Start, r.Start})
		}
		if x.End > r.End {
			out = append(out, span{r.End, x.End})
		}
	}
	return out
}

// covers reports whether one span of the list holds all of r (spans in the list never touch, so one is enough).
func covers(list []span, r span) bool {
	i := sort.Search(len(list), func(i int) bool { return list[i].End >= r.End })
	return i < len(list) && list[i].Start <= r.Start && r.End <= list[i].End
}
