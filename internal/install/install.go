// Package install puts one pinned build's files into a folder, checking every file against its hash, and checks
// such a folder again later (repair = install again: only missing or damaged files are fetched).
//
// The folder follows the Battle.net layout where one exists:
//
//	Data/config/ab/cd/<key>     build and CDN config
//	Data/indices/<key>.index    archive indexes and the loose-file index
//	<game folder>/...           the install manifest's loose files (the game executable and its friends)
//
// The stored files for the game's data storage (Data/data: data.NNN + .idx) are not written in that format yet.
// Until that writer exists they are kept, still encoded and checked, in .everlake/staged/ab/<encoded key>.
package install

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/cdn"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/pin"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/tact"
)

// Options say what to put where.
type Options struct {
	Dir  string
	Pin  pin.Pin
	CDN  *cdn.Client
	Tags []string // e.g. OSX, arm64, enUS: picks the install and download manifest entries
	Jobs int      // parallel requests

	Verify      bool  // check what's on disk; fetch nothing except what checking needs (configs, indexes, manifests)
	NoData      bool  // stop after the loose files (skip the download manifest)
	MaxPriority *int  // only download manifest entries at or below this priority
	DataLimit   int64 // stop the data stage after about this many bytes (0: no limit); for trial runs

	Log func(format string, args ...any)
}

// Report counts what happened. Bad and Missing are only non-zero after a Verify run or a failed install.
type Report struct {
	LooseFiles, LooseKept, LooseFetched    int
	DataFiles, DataKept, DataFetched       int
	DataSkippedByLimit                     int
	BytesDownloaded                        int64
	Bad, Missing                           []string
	Version, BuildConfig, CDNConfig, Where string
}

// Failed reports whether anything is bad or missing.
func (r *Report) Failed() bool { return len(r.Bad)+len(r.Missing) > 0 }

type run struct {
	Options
	rep        Report
	mu         sync.Mutex
	downloaded atomic.Int64
	build, cdc tact.Config
	enc        *tact.Encoding
}

// Run installs (or with Verify, checks) the pinned build in opts.Dir.
func Run(ctx context.Context, opts Options) (*Report, error) {
	if opts.Jobs < 1 {
		opts.Jobs = 8
	}
	if opts.Log == nil {
		opts.Log = func(string, ...any) {}
	}
	r := &run{Options: opts}
	r.rep.Version, r.rep.BuildConfig, r.rep.CDNConfig = opts.Pin.Version, opts.Pin.BuildConfig, opts.Pin.CDNConfig
	r.rep.Where = opts.Dir
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return nil, err
	}
	stop := r.progress()
	defer stop()

	buildKey, cdnKey := opts.Pin.Keys()
	var err error
	if r.build, err = r.config(ctx, buildKey); err != nil {
		return nil, fmt.Errorf("build config: %w", err)
	}
	if r.cdc, err = r.config(ctx, cdnKey); err != nil {
		return nil, fmt.Errorf("CDN config: %w", err)
	}
	if name := r.build.One("build-uid"); name != opts.Pin.Product {
		return nil, fmt.Errorf("build config is for product %q, the pin says %q", name, opts.Pin.Product)
	}
	opts.Log("build %s (%s), CDN config %s", opts.Pin.Version, r.build.One("build-name"), cdnKey)

	idx, err := r.indexes(ctx)
	if err != nil {
		return nil, err
	}

	// The encoding file comes first: it turns every other content key into the key it's stored under.
	encC, encE, err := r.build.KeyPair("encoding")
	if err != nil {
		return nil, err
	}
	sys := map[tact.Key]*where{encE: nil}
	locate(idx, sys)
	encRaw, err := r.blob(ctx, encE, sys[encE])
	if err != nil {
		return nil, fmt.Errorf("encoding file: %w", err)
	}
	encData, err := tact.Decode(encRaw, encE, encC)
	if err != nil {
		return nil, fmt.Errorf("encoding file: %w", err)
	}
	if r.enc, err = tact.ParseEncoding(encData); err != nil {
		return nil, err
	}
	opts.Log("encoding file: %d content keys", r.enc.Count())

	// The rest of the build's own files: manifests, root and the TVFS manifests the client looks files up with.
	system := map[string][2]tact.Key{"encoding": {encC, encE}}
	names := append([]string{"root", "install", "download", "size"}, r.build.VFSNames()...)
	for _, name := range names {
		if _, ok := r.build[name]; !ok {
			continue
		}
		c, e, err := r.build.KeyPair(name)
		if err != nil {
			return nil, err
		}
		if e.IsZero() {
			ent, ok := r.enc.Lookup(c)
			if !ok {
				return nil, fmt.Errorf("build file %s (%s) is not in the encoding file", name, c)
			}
			e = ent.EKeys[0]
		}
		system[name] = [2]tact.Key{c, e}
		sys[e] = nil
	}
	locate(idx, sys)
	manifest := func(name string) ([]byte, error) {
		k := system[name]
		raw, err := r.blob(ctx, k[1], sys[k[1]])
		if err != nil {
			return nil, fmt.Errorf("%s manifest: %w", name, err)
		}
		return tact.Decode(raw, k[1], k[0])
	}
	instData, err := manifest("install")
	if err != nil {
		return nil, err
	}
	inst, err := tact.ParseInstall(instData)
	if err != nil {
		return nil, err
	}
	dlData, err := manifest("download")
	if err != nil {
		return nil, err
	}
	dl, err := tact.ParseDownload(dlData)
	if err != nil {
		return nil, err
	}

	if err := r.looseFiles(ctx, idx, inst); err != nil {
		return nil, err
	}
	if !opts.NoData {
		if err := r.dataFiles(ctx, idx, dl, system); err != nil {
			return nil, err
		}
	}
	r.rep.BytesDownloaded = r.downloaded.Load()
	sort.Strings(r.rep.Bad)
	sort.Strings(r.rep.Missing)
	if !opts.Verify && !r.rep.Failed() {
		if err := r.writeState(); err != nil {
			return nil, err
		}
	}
	return &r.rep, nil
}

// --- configs and indexes -----------------------------------------------------------------------------------------

func (r *run) configPath(k tact.Key) string {
	s := k.String()
	return filepath.Join(r.Dir, "Data", "config", s[:2], s[2:4], s)
}

// config reads a config from disk if it's there and intact, else from the CDN. Its name is its MD5.
func (r *run) config(ctx context.Context, k tact.Key) (tact.Config, error) {
	p := r.configPath(k)
	if data, err := os.ReadFile(p); err == nil && tact.KeyOf(data) == k {
		return tact.ParseConfig(string(data)), nil
	}
	data, err := r.CDN.Get(ctx, "config", k, "")
	if err != nil {
		return nil, err
	}
	r.downloaded.Add(int64(len(data)))
	if tact.KeyOf(data) != k {
		return nil, fmt.Errorf("config %s doesn't match its name", k)
	}
	return tact.ParseConfig(string(data)), writeFile(p, data, 0o644)
}

type namedIndex struct {
	archive tact.Key
	idx     *tact.Index
}

// indexes loads every archive index of the CDN config, plus the loose-file index, from Data/indices or the CDN.
func (r *run) indexes(ctx context.Context) ([]namedIndex, error) {
	archives, err := r.cdc.Keys("archives")
	if err != nil {
		return nil, err
	}
	keys := append([]tact.Key{}, archives...)
	if fi := r.cdc.One("file-index"); fi != "" {
		k, err := tact.ParseKey(fi)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	out := make([]namedIndex, len(keys))
	err = r.parallel(ctx, len(keys), func(i int) error {
		k := keys[i]
		p := filepath.Join(r.Dir, "Data", "indices", k.String()+".index")
		if data, err := os.ReadFile(p); err == nil {
			if idx, err := tact.ParseIndex(k, data); err == nil {
				out[i] = namedIndex{k, idx}
				return nil
			}
		}
		data, err := r.CDN.Get(ctx, "data", k, ".index")
		if err != nil {
			return err
		}
		r.downloaded.Add(int64(len(data)))
		idx, err := tact.ParseIndex(k, data)
		if err != nil {
			return err
		}
		out[i] = namedIndex{k, idx}
		return writeFile(p, data, 0o644)
	})
	if err != nil {
		return nil, fmt.Errorf("archive indexes: %w", err)
	}
	n := 0
	for _, x := range out {
		n += len(x.idx.Entries)
	}
	r.Log("%d archive indexes, %d stored files", len(archives), n)
	return out, nil
}

// where is a stored file's place on the CDN: an archive range, or loose (archive zero).
type where struct {
	archive tact.Key
	offset  uint32
	size    uint32
}

// locate fills in the place of every wanted encoded key found in the indexes. Keys found nowhere stay nil and are
// tried as loose files on the CDN.
func locate(idx []namedIndex, want map[tact.Key]*where) {
	for _, x := range idx {
		for _, e := range x.idx.Entries {
			if w, ok := want[e.EKey]; ok && w == nil {
				if x.idx.HasOffsets {
					want[e.EKey] = &where{archive: x.archive, offset: e.Offset, size: e.Size}
				} else {
					want[e.EKey] = &where{size: e.Size}
				}
			}
		}
	}
}

// --- stored files ------------------------------------------------------------------------------------------------

func (r *run) stagedPath(e tact.Key) string {
	s := e.String()
	return filepath.Join(r.Dir, ".everlake", "staged", s[:2], s)
}

// blob returns a stored file, checked against its encoded key: the staged copy if it's intact, else from the CDN
// (and staged). In Verify mode a missing or damaged staged copy is fetched only when it's a build file the check
// needs to go on; content files are reported instead (see dataFiles).
func (r *run) blob(ctx context.Context, e tact.Key, w *where) ([]byte, error) {
	p := r.stagedPath(e)
	if data, err := os.ReadFile(p); err == nil && tact.VerifyBLTE(data, e) == nil {
		return data, nil
	}
	var data []byte
	var err error
	if w != nil && !w.archive.IsZero() {
		data, err = r.CDN.GetRange(ctx, "data", w.archive, int64(w.offset), int64(w.size))
	} else {
		data, err = r.CDN.Get(ctx, "data", e, "")
	}
	if err != nil {
		return nil, err
	}
	r.downloaded.Add(int64(len(data)))
	if err := tact.VerifyBLTE(data, e); err != nil {
		return nil, err
	}
	return data, writeFile(p, data, 0o644)
}

// --- loose files -------------------------------------------------------------------------------------------------

// looseFiles writes (or checks) the install manifest's files for the wanted tags into the game folder.
func (r *run) looseFiles(ctx context.Context, idx []namedIndex, inst *tact.Install) error {
	entries, err := inst.Select(r.Tags)
	if err != nil {
		return fmt.Errorf("install manifest: %w", err)
	}
	type job struct {
		e    tact.InstallEntry
		ekey tact.Key
		path string
	}
	var jobs []job
	want := map[tact.Key]*where{}
	seen := map[string]tact.Key{} // folded name -> content key; the manifest lists some files once per tag set
	for _, e := range entries {
		rel, err := safePath(e.Name)
		if err != nil {
			return fmt.Errorf("install manifest: %w", err)
		}
		folded := strings.ToLower(filepath.ToSlash(rel)) // macOS and Windows folders ignore case
		if prev, ok := seen[folded]; ok {
			if prev != e.CKey {
				return fmt.Errorf("install manifest: %s has two different contents for tags %s; add a tag to pick one",
					e.Name, strings.Join(r.Tags, " "))
			}
			continue
		}
		seen[folded] = e.CKey
		ent, ok := r.enc.Lookup(e.CKey)
		if !ok {
			return fmt.Errorf("install manifest file %s (%s) is not in the encoding file", e.Name, e.CKey)
		}
		jobs = append(jobs, job{e, ent.EKeys[0], filepath.Join(r.Dir, r.Pin.GameFolder, rel)})
		want[ent.EKeys[0]] = nil
	}
	locate(idx, want)
	r.rep.LooseFiles = len(jobs)
	r.Log("install manifest: %d of %d loose files match %s", len(jobs), len(inst.Entries), strings.Join(r.Tags, " "))
	return r.parallel(ctx, len(jobs), func(i int) error {
		j := jobs[i]
		if sameContent(j.path, j.e.CKey) {
			r.count(&r.rep.LooseKept)
			return nil
		}
		if r.Verify {
			r.problem(j.path, fileExists(j.path))
			return nil
		}
		raw, err := r.fetchStored(ctx, j.ekey, want[j.ekey])
		if err != nil {
			return fmt.Errorf("%s: %w", j.e.Name, err)
		}
		data, err := tact.Decode(raw, j.ekey, j.e.CKey)
		if err != nil {
			return fmt.Errorf("%s: %w", j.e.Name, err)
		}
		if err := writeFile(j.path, data, modeFor(data)); err != nil {
			return err
		}
		r.count(&r.rep.LooseFetched)
		return nil
	})
}

// fetchStored gets one stored file from the CDN without staging it.
func (r *run) fetchStored(ctx context.Context, e tact.Key, w *where) ([]byte, error) {
	var data []byte
	var err error
	if w != nil && !w.archive.IsZero() {
		data, err = r.CDN.GetRange(ctx, "data", w.archive, int64(w.offset), int64(w.size))
	} else {
		data, err = r.CDN.Get(ctx, "data", e, "")
	}
	if err == nil {
		r.downloaded.Add(int64(len(data)))
	}
	return data, err
}

// --- data files --------------------------------------------------------------------------------------------------

const (
	spanGap = 256 << 10 // read through gaps up to this size between wanted files in one archive
	spanMax = 16 << 20  // largest single range request
)

type span struct {
	archive tact.Key
	start   int64
	end     int64
	files   []tact.Key // with their places in want
}

// dataFiles stages (or checks) the download manifest's files for the wanted tags, plus the build's own files.
func (r *run) dataFiles(ctx context.Context, idx []namedIndex, dl *tact.Download, system map[string][2]tact.Key) error {
	entries, err := dl.Select(r.Tags)
	if err != nil {
		return fmt.Errorf("download manifest: %w", err)
	}
	var order []tact.Key
	want := map[tact.Key]*where{}
	add := func(e tact.Key) {
		if _, ok := want[e]; !ok {
			want[e] = nil
			order = append(order, e)
		}
	}
	for _, k := range system {
		add(k[1])
	}
	nsys := len(order)
	for _, e := range entries {
		if r.MaxPriority != nil && e.Priority > *r.MaxPriority {
			continue
		}
		add(e.EKey)
	}
	locate(idx, want)
	r.rep.DataFiles = len(order)
	r.Log("download manifest: %d of %d stored files match %s (plus the build's own files)",
		len(order)-nsys, len(dl.Entries), strings.Join(r.Tags, " "))

	// Keep what's already staged and intact.
	var need []tact.Key
	var budget = r.DataLimit
	var mu sync.Mutex
	err = r.parallel(ctx, len(order), func(i int) error {
		e := order[i]
		if data, err := os.ReadFile(r.stagedPath(e)); err == nil && tact.VerifyBLTE(data, e) == nil {
			r.count(&r.rep.DataKept)
			return nil
		}
		mu.Lock()
		need = append(need, e)
		mu.Unlock()
		return nil
	})
	if err != nil {
		return err
	}
	if r.Verify {
		for _, e := range need {
			p := r.stagedPath(e)
			r.problem(p, fileExists(p))
		}
		return nil
	}
	// Manifest order is priority order: keep it when a limit cuts the list short.
	pos := make(map[tact.Key]int, len(order))
	for i, e := range order {
		pos[e] = i
	}
	sort.Slice(need, func(a, b int) bool { return pos[need[a]] < pos[need[b]] })
	if budget > 0 {
		// The build's own files sort first (they were added to order first) and never count against the limit.
		isSystem := map[tact.Key]bool{}
		for _, k := range system {
			isSystem[k[1]] = true
		}
		var used int64
		for i, e := range need {
			if isSystem[e] {
				continue
			}
			size := int64(0)
			if w := want[e]; w != nil {
				size = int64(w.size)
			}
			if used+size > budget && used > 0 {
				r.rep.DataSkippedByLimit = len(need) - i
				need = need[:i]
				break
			}
			used += size
		}
	}

	// Group archive files into ranges; loose files go one by one.
	byArchive := map[tact.Key][]tact.Key{}
	var loose []tact.Key
	for _, e := range need {
		if w := want[e]; w != nil && !w.archive.IsZero() {
			byArchive[w.archive] = append(byArchive[w.archive], e)
		} else {
			loose = append(loose, e)
		}
	}
	var spans []span
	for a, files := range byArchive {
		sort.Slice(files, func(i, j int) bool { return want[files[i]].offset < want[files[j]].offset })
		var cur *span
		for _, e := range files {
			w := want[e]
			start, end := int64(w.offset), int64(w.offset)+int64(w.size)
			if cur != nil && start-cur.end <= spanGap && end-cur.start <= spanMax {
				if end > cur.end {
					cur.end = end
				}
				cur.files = append(cur.files, e)
				continue
			}
			spans = append(spans, span{archive: a, start: start, end: end, files: []tact.Key{e}})
			cur = &spans[len(spans)-1]
		}
	}
	for _, e := range loose {
		spans = append(spans, span{files: []tact.Key{e}})
	}
	r.Log("data: %d to fetch in %d requests, %d already staged", len(need), len(spans), r.rep.DataKept)

	return r.parallel(ctx, len(spans), func(i int) error {
		s := spans[i]
		if s.archive.IsZero() {
			e := s.files[0]
			if _, err := r.blob(ctx, e, want[e]); err != nil {
				if errors.Is(err, cdn.ErrNotFound) {
					r.problem(e.String(), false)
					return nil
				}
				return err
			}
			r.count(&r.rep.DataFetched)
			return nil
		}
		data, err := r.CDN.GetRange(ctx, "data", s.archive, s.start, s.end-s.start)
		if err != nil {
			return err
		}
		r.downloaded.Add(int64(len(data)))
		for _, e := range s.files {
			w := want[e]
			part := data[int64(w.offset)-s.start : int64(w.offset)-s.start+int64(w.size)]
			if err := tact.VerifyBLTE(part, e); err != nil {
				return fmt.Errorf("archive %s at %d: %w", s.archive, w.offset, err)
			}
			if err := writeFile(r.stagedPath(e), part, 0o644); err != nil {
				return err
			}
			r.count(&r.rep.DataFetched)
		}
		return nil
	})
}

// --- helpers -----------------------------------------------------------------------------------------------------

// safePath turns a manifest name into a relative path inside the game folder, refusing anything that could escape.
func safePath(name string) (string, error) {
	n := strings.ReplaceAll(name, `\`, "/")
	if n == "" || strings.HasPrefix(n, "/") || strings.Contains(n, ":") {
		return "", fmt.Errorf("unsafe file name %q", name)
	}
	for _, part := range strings.Split(n, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("unsafe file name %q", name)
		}
	}
	return filepath.FromSlash(path.Clean(n)), nil
}

func sameContent(p string, ckey tact.Key) bool {
	data, err := os.ReadFile(p)
	return err == nil && tact.KeyOf(data) == ckey
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// modeFor marks programs executable: Mach-O binaries (thin or universal) and scripts.
func modeFor(data []byte) os.FileMode {
	if len(data) >= 4 {
		for _, magic := range [][]byte{{0xcf, 0xfa, 0xed, 0xfe}, {0xce, 0xfa, 0xed, 0xfe}, {0xca, 0xfe, 0xba, 0xbe}} {
			if bytes.Equal(data[:4], magic) {
				return 0o755
			}
		}
		if data[0] == '#' && data[1] == '!' {
			return 0o755
		}
	}
	return 0o644
}

// writeFile writes through a temporary file and a rename, so a cut-off run never leaves a half file under the
// real name.
func writeFile(p string, data []byte, mode os.FileMode) error {
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
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), p)
}

func (r *run) count(n *int) {
	r.mu.Lock()
	*n++
	r.mu.Unlock()
}

func (r *run) problem(what string, exists bool) {
	r.mu.Lock()
	if exists {
		r.rep.Bad = append(r.rep.Bad, what)
	} else {
		r.rep.Missing = append(r.rep.Missing, what)
	}
	r.mu.Unlock()
}

// parallel runs fn(0..n-1) on Jobs workers and returns the first error (the rest are cancelled).
func (r *run) parallel(ctx context.Context, n int, fn func(int) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var next atomic.Int64
	var first error
	var once sync.Once
	var wg sync.WaitGroup
	for w := 0; w < r.Jobs; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				i := int(next.Add(1) - 1)
				if i >= n {
					return
				}
				if err := fn(i); err != nil {
					once.Do(func() { first = err; cancel() })
					return
				}
			}
		}()
	}
	wg.Wait()
	if first == nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return first
}

// progress logs the download rate every 10 seconds until stopped.
func (r *run) progress() func() {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		var last int64
		for {
			select {
			case <-done:
				return
			case <-t.C:
				now := r.downloaded.Load()
				r.mu.Lock()
				loose, data := r.rep.LooseFetched, r.rep.DataFetched
				r.mu.Unlock()
				r.Log("... %d MB downloaded (%.1f MB/s), %d loose files, %d data files", now>>20,
					float64(now-last)/10/(1<<20), loose, data)
				last = now
			}
		}
	}()
	return func() { close(done) }
}

// State is what .everlake/install.json records after a complete install.
type State struct {
	Pin       pin.Pin   `json:"pin"`
	Tags      []string  `json:"tags"`
	NoData    bool      `json:"loose_files_only,omitempty"`
	Limited   bool      `json:"data_limited,omitempty"`
	Installed time.Time `json:"installed"`
}

func (r *run) writeState() error {
	limited := r.MaxPriority != nil || r.rep.DataSkippedByLimit > 0
	data, err := json.MarshalIndent(State{r.Pin, r.Tags, r.NoData, limited, time.Now().UTC()}, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(r.Dir, ".everlake", "install.json"), append(data, '\n'), 0o644)
}

// ReadState reads what the last complete install recorded, if any.
func ReadState(dir string) (*State, error) {
	data, err := os.ReadFile(filepath.Join(dir, ".everlake", "install.json"))
	if err != nil {
		return nil, err
	}
	var s State
	return &s, json.Unmarshal(data, &s)
}
