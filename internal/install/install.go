// Package install puts one pinned build's files into a folder, checking every file against its hash, and checks
// such a folder again later (repair = install again: only missing or damaged files are fetched).
//
// The folder follows the Battle.net layout where one exists:
//
//	Data/config/ab/cd/<key>     build and CDN config
//	Data/indices/<key>.index    archive indexes and the loose-file index
//	Data/data/                  the game's local storage (data.NNN archives, .idx key tables): the download
//	                            manifest's files and the build's own (encoding, root, TVFS manifests), see casc
//	<game folder>/...           the install manifest's loose files (the game executable and its friends)
//	.build.info                 which build this is and which CDN the game streams missing files from
//	.everlake/                  the launcher's own records (install.json, archives.json)
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
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/casc"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/cdn"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/pin"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/tact"
)

// Options say what to put where.
type Options struct {
	Dir  string
	Pin  pin.Pin
	CDN  *cdn.Client
	Tags []string // e.g. OSX, arm64, enUS, US: picks the install and download manifest entries
	Jobs int      // parallel requests

	// Verify checks what's on disk and writes nothing. It reports every damaged or missing file. Configs, indexes and
	// manifests it needs in order to go on are read from the CDN when the copy on disk is bad (and reported too).
	Verify bool
	// PartialData (Verify only): the install was cut short by a data limit, so missing data files aren't reported;
	// damaged ones still are.
	PartialData bool

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

// ErrNoInstall is returned by Verify for a folder no install finished in.
var ErrNoInstall = errors.New("no finished Everlake install in this folder (.everlake/install.json is missing)")

type run struct {
	Options
	rep        Report
	mu         sync.Mutex
	reported   map[string]bool
	downloaded atomic.Int64
	build, cdc tact.Config
	enc        *tact.Encoding
	st         *casc.Storage // the game's local storage, Data/data
}

// Run installs (or with Verify, checks) the pinned build in opts.Dir.
func Run(ctx context.Context, opts Options) (*Report, error) {
	if opts.Jobs < 1 {
		opts.Jobs = 8
	}
	if opts.Log == nil {
		opts.Log = func(string, ...any) {}
	}
	r := &run{Options: opts, reported: map[string]bool{}}
	r.rep.Version, r.rep.BuildConfig, r.rep.CDNConfig = opts.Pin.Version, opts.Pin.BuildConfig, opts.Pin.CDNConfig
	r.rep.Where = opts.Dir
	if opts.Verify {
		if _, err := ReadState(opts.Dir); err != nil {
			return nil, fmt.Errorf("%s: %w", opts.Dir, ErrNoInstall)
		}
	} else if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return nil, err
	}
	stop := r.progress()
	defer stop()
	rep, err := r.do(ctx)
	if r.st != nil {
		if e := r.st.Close(); e != nil && err == nil {
			return nil, fmt.Errorf("local storage: %w", e)
		}
	}
	return rep, err
}

func (r *run) do(ctx context.Context) (*Report, error) {
	opts := r.Options
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

	dataDir := filepath.Join(opts.Dir, "Data", "data")
	if opts.Verify {
		r.st, err = casc.OpenReadOnly(dataDir)
	} else {
		r.st, err = casc.Open(dataDir, filepath.Join(opts.Dir, ".everlake", "archives.json"))
	}
	if err != nil {
		return nil, fmt.Errorf("local storage: %w", err)
	}
	if err := r.moveStaged(); err != nil {
		return nil, fmt.Errorf("moving staged files into local storage: %w", err)
	}

	idx, err := r.indexes(ctx)
	if err != nil {
		return nil, err
	}

	// The encoding file comes first: it turns every other content key into the key it's stored under.
	encC, encE, err := r.build.KeyPair("encoding")
	if err != nil {
		return nil, err
	}
	if encE.IsZero() {
		return nil, errors.New("the build config names no encoded key for the encoding file")
	}
	sys := map[tact.Key]*where{encE: nil}
	locate(idx, sys)
	encRaw, err := r.blob(ctx, encE, encC, sys[encE])
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
		raw, err := r.blob(ctx, k[1], k[0], sys[k[1]])
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
	if err := r.buildInfo(inst); err != nil {
		return nil, err
	}
	r.rep.BytesDownloaded = r.downloaded.Load()
	sort.Strings(r.rep.Bad)
	sort.Strings(r.rep.Missing)
	if !opts.Verify && !r.rep.Failed() {
		if err := r.st.Flush(); err != nil {
			return nil, fmt.Errorf("local storage: %w", err)
		}
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
	data, err := os.ReadFile(p)
	if err == nil && tact.KeyOf(data) == k {
		return tact.ParseConfig(string(data)), nil
	}
	if r.Verify {
		r.problem(p, err == nil)
	}
	data, err = r.CDN.Do(ctx, cdn.Request{Kind: "config", Key: k, Max: 16 << 20, Check: func(b []byte) error {
		if tact.KeyOf(b) != k {
			return fmt.Errorf("config %s doesn't match its name", k)
		}
		return nil
	}})
	if err != nil {
		return nil, err
	}
	r.downloaded.Add(int64(len(data)))
	return tact.ParseConfig(string(data)), r.save(p, data, 0o644)
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
	err = r.parallel(ctx, len(keys), func(ctx context.Context, i int) error {
		k := keys[i]
		p := filepath.Join(r.Dir, "Data", "indices", k.String()+".index")
		data, err := os.ReadFile(p)
		if err == nil {
			if idx, err := tact.ParseIndex(k, data); err == nil {
				out[i] = namedIndex{k, idx}
				return nil
			}
		}
		if r.Verify {
			r.problem(p, err == nil)
		}
		var idx *tact.Index
		data, err = r.CDN.Do(ctx, cdn.Request{Kind: "data", Key: k, Suffix: ".index", Max: 64 << 20,
			Check: func(b []byte) (err error) { idx, err = tact.ParseIndex(k, b); return err }})
		if err != nil {
			return err
		}
		r.downloaded.Add(int64(len(data)))
		out[i] = namedIndex{k, idx}
		return r.save(p, data, 0o644)
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

// locate fills in the place of every wanted encoded key found in the indexes. Keys found nowhere (or listed with
// size 0, which no real file has) stay nil and are tried as loose files on the CDN.
func locate(idx []namedIndex, want map[tact.Key]*where) {
	for _, x := range idx {
		for _, e := range x.idx.Entries {
			if w, ok := want[e.EKey]; ok && w == nil && e.Size > 0 {
				if x.idx.HasOffsets {
					want[e.EKey] = &where{archive: x.archive, offset: e.Offset, size: e.Size}
				} else {
					want[e.EKey] = &where{size: e.Size}
				}
			}
		}
	}
}

// --- .build.info --------------------------------------------------------------------------------------------------

// buildInfo writes .build.info (or, verifying, checks it names the pinned build).
func (r *run) buildInfo(inst *tact.Install) error {
	p := filepath.Join(r.Dir, ".build.info")
	if r.Verify {
		data, err := os.ReadFile(p)
		row := "|" + r.Pin.BuildConfig + "|" + r.Pin.CDNConfig + "|"
		if err != nil || !strings.Contains(string(data), row) || !strings.Contains(string(data), "|"+r.Pin.Version+"|") {
			r.problem(p, err == nil)
		}
		return nil
	}
	return writeFile(p, buildInfo(r.Pin, r.CDN.Hosts, buildInfoTags(r.Tags, inst.Tags)), 0o644)
}

// --- stored files ------------------------------------------------------------------------------------------------

// storedName is how a stored file is named in reports.
func (r *run) storedName(e tact.Key) string {
	return filepath.Join(r.Dir, "Data", "data") + " " + e.String()
}

// store adds a checked file to local storage (not when verifying).
func (r *run) store(e tact.Key, data []byte) error {
	if r.Verify {
		return nil
	}
	return r.st.Add(e, data)
}

// moveStaged moves the files an earlier launcher kept in .everlake/staged into local storage, so they aren't
// fetched again, then removes that folder. Damaged ones are left out and fetched like any missing file.
func (r *run) moveStaged() error {
	root := filepath.Join(r.Dir, ".everlake", "staged")
	if _, err := os.Stat(root); err != nil {
		return nil
	}
	if r.Verify {
		r.Log("note: %s holds files an earlier launcher fetched; install moves them into the game's storage", root)
		return nil
	}
	moved := 0
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		e, err := tact.ParseKey(d.Name())
		if err != nil {
			return nil // a leftover temporary file
		}
		if have, err := r.st.Read(e); err == nil && tact.VerifyBLTE(have, e) == nil {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if tact.VerifyBLTE(data, e) != nil {
			return nil
		}
		moved++
		return r.st.Add(e, data)
	})
	if err != nil {
		return err
	}
	if err := r.st.Flush(); err != nil {
		return err
	}
	r.Log("moved %d files from %s into the game's storage", moved, root)
	return os.RemoveAll(root)
}

// fetchStored gets one stored file from the CDN. check runs on each host's answer after the encoded-key check, so
// a host with a damaged copy is skipped for the next one.
func (r *run) fetchStored(ctx context.Context, e tact.Key, w *where, check func([]byte) error) ([]byte, error) {
	q := cdn.Request{Kind: "data", Key: e, Check: func(b []byte) error {
		if err := tact.VerifyBLTE(b, e); err != nil {
			return err
		}
		if check != nil {
			return check(b)
		}
		return nil
	}}
	if w != nil && !w.archive.IsZero() {
		q.Key, q.Offset, q.Size = w.archive, int64(w.offset), int64(w.size)
	} else if w != nil {
		q.Max = int64(w.size)
	}
	data, err := r.CDN.Do(ctx, q)
	if err == nil {
		r.downloaded.Add(int64(len(data)))
	}
	return data, err
}

// blob returns one of the build's own files (encoding file, manifests), checked against both keys: the copy in
// local storage if it's intact, else from the CDN (stored unless verifying, and reported when verifying).
func (r *run) blob(ctx context.Context, e, c tact.Key, w *where) ([]byte, error) {
	data, err := r.st.Read(e)
	if err == nil && tact.VerifyBLTE(data, e) == nil {
		return data, nil
	}
	if r.Verify {
		r.problem(r.storedName(e), !errors.Is(err, casc.ErrNotStored))
	}
	data, err = r.fetchStored(ctx, e, w, func(b []byte) error { _, err := tact.Decode(b, e, c); return err })
	if err != nil {
		return nil, err
	}
	return data, r.store(e, data)
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
	return r.parallel(ctx, len(jobs), func(ctx context.Context, i int) error {
		j := jobs[i]
		data, err := os.ReadFile(j.path)
		if err == nil && tact.KeyOf(data) == j.e.CKey {
			// Right bytes; programs must also still be executable (Windows has no executable bit).
			if mode := modeFor(data); mode&0o111 != 0 && runtime.GOOS != "windows" {
				if st, err := os.Stat(j.path); err == nil && st.Mode().Perm()&0o111 == 0 {
					if r.Verify {
						r.problem(j.path+" (not executable)", true)
						return nil
					}
					if err := os.Chmod(j.path, mode); err != nil {
						return err
					}
				}
			}
			r.count(&r.rep.LooseKept)
			return nil
		}
		if r.Verify {
			r.problem(j.path, err == nil)
			return nil
		}
		var content []byte
		_, err = r.fetchStored(ctx, j.ekey, want[j.ekey], func(b []byte) (err error) {
			content, err = tact.Decode(b, j.ekey, j.e.CKey)
			return err
		})
		if err != nil {
			return fmt.Errorf("%s: %w", j.e.Name, err)
		}
		if err := writeFile(j.path, content, modeFor(content)); err != nil {
			return err
		}
		r.count(&r.rep.LooseFetched)
		return nil
	})
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

// dataFiles stores (or checks) the download manifest's files for the wanted tags, plus the build's own files.
func (r *run) dataFiles(ctx context.Context, idx []namedIndex, dl *tact.Download, system map[string][2]tact.Key) error {
	entries, err := dl.Select(r.Tags)
	if err != nil {
		return fmt.Errorf("download manifest: %w", err)
	}
	var order []tact.Key
	want := map[tact.Key]*where{}
	manifestSize := map[tact.Key]int64{}
	add := func(e tact.Key) {
		if _, ok := want[e]; !ok {
			want[e] = nil
			order = append(order, e)
		}
	}
	isSystem := map[tact.Key]bool{}
	for _, k := range system {
		add(k[1])
		isSystem[k[1]] = true
	}
	nsys := len(order)
	for _, e := range entries {
		if r.MaxPriority != nil && e.Priority > *r.MaxPriority {
			continue
		}
		add(e.EKey)
		manifestSize[e.EKey] = int64(e.Size)
	}
	locate(idx, want)
	r.rep.DataFiles = len(order)
	r.Log("download manifest: %d of %d stored files match %s (plus the build's own files)",
		len(order)-nsys, len(dl.Entries), strings.Join(r.Tags, " "))

	// Keep what's already stored and intact.
	var need []tact.Key
	var mu sync.Mutex
	err = r.parallel(ctx, len(order), func(ctx context.Context, i int) error {
		e := order[i]
		data, err := r.st.Read(e)
		if err == nil && tact.VerifyBLTE(data, e) == nil {
			r.count(&r.rep.DataKept)
			return nil
		}
		if r.Verify {
			listed := !errors.Is(err, casc.ErrNotStored)
			if listed || !r.PartialData || isSystem[e] {
				r.problem(r.storedName(e), listed)
			}
			return nil
		}
		mu.Lock()
		need = append(need, e)
		mu.Unlock()
		return nil
	})
	if err != nil || r.Verify {
		return err
	}
	// Manifest order is priority order: keep it when a limit cuts the list short.
	pos := make(map[tact.Key]int, len(order))
	for i, e := range order {
		pos[e] = i
	}
	sort.Slice(need, func(a, b int) bool { return pos[need[a]] < pos[need[b]] })
	gap := int64(spanGap)
	if r.DataLimit > 0 {
		// The build's own files sort first and never count against the limit. With a limit, ranges don't read
		// through gaps, so the bytes downloaded stay close to the budget.
		gap = 0
		var used int64
		for i, e := range need {
			if isSystem[e] {
				continue
			}
			size := manifestSize[e]
			if w := want[e]; w != nil {
				size = int64(w.size)
			}
			if used+size > r.DataLimit && used > 0 {
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
			if cur != nil && start-cur.end <= gap && end-cur.start <= spanMax {
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
	r.Log("data: %d to fetch in %d requests, %d already stored", len(need), len(spans), r.rep.DataKept)

	return r.parallel(ctx, len(spans), func(ctx context.Context, i int) error {
		s := spans[i]
		if s.archive.IsZero() {
			e := s.files[0]
			data, err := r.fetchStored(ctx, e, want[e], nil)
			if errors.Is(err, cdn.ErrNotFound) {
				r.problem(e.String()+" (not on the CDN)", false)
				return nil
			}
			if err != nil {
				return err
			}
			if err := r.st.Add(e, data); err != nil {
				return err
			}
			r.count(&r.rep.DataFetched)
			return nil
		}
		part := func(data []byte, e tact.Key) []byte {
			off := int64(want[e].offset) - s.start
			return data[off : off+int64(want[e].size)]
		}
		data, err := r.CDN.Do(ctx, cdn.Request{Kind: "data", Key: s.archive, Offset: s.start, Size: s.end - s.start,
			Check: func(data []byte) error {
				for _, e := range s.files {
					if err := tact.VerifyBLTE(part(data, e), e); err != nil {
						return fmt.Errorf("archive %s at %d: %w", s.archive, want[e].offset, err)
					}
				}
				return nil
			}})
		if err != nil {
			return err
		}
		r.downloaded.Add(int64(len(data)))
		for _, e := range s.files {
			if err := r.st.Add(e, part(data, e)); err != nil {
				return err
			}
			r.count(&r.rep.DataFetched)
		}
		return nil
	})
}

// --- helpers -----------------------------------------------------------------------------------------------------

// windowsReserved are names Windows won't create, with or without an extension.
var windowsReserved = map[string]bool{"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true, "com6": true, "com7": true, "com8": true,
	"com9": true, "lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true, "lpt6": true, "lpt7": true,
	"lpt8": true, "lpt9": true}

// safePath turns a manifest name into a relative path inside the game folder. It refuses anything that could
// escape the folder, and anything Windows can't store (so a name works the same on both platforms).
func safePath(name string) (string, error) {
	bad := func() (string, error) { return "", fmt.Errorf("unsafe file name %q", name) }
	n := strings.ReplaceAll(name, `\`, "/")
	if n == "" || strings.HasPrefix(n, "/") {
		return bad()
	}
	for _, c := range n {
		if c < 0x20 || strings.ContainsRune(`:<>"|?*`, c) {
			return bad()
		}
	}
	for _, part := range strings.Split(n, "/") {
		if part == "" || part == "." || part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return bad()
		}
		base, _, _ := strings.Cut(strings.ToLower(part), ".")
		if windowsReserved[strings.TrimRight(base, " ")] {
			return bad()
		}
	}
	return filepath.FromSlash(path.Clean(n)), nil
}

// modeFor marks programs executable: Mach-O binaries (thin or universal, 32 or 64 bit) and scripts.
func modeFor(data []byte) os.FileMode {
	if len(data) >= 4 {
		for _, magic := range [][]byte{{0xcf, 0xfa, 0xed, 0xfe}, {0xce, 0xfa, 0xed, 0xfe}, {0xca, 0xfe, 0xba, 0xbe},
			{0xca, 0xfe, 0xba, 0xbf}} {
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

// save writes a file unless this is a Verify run, which never writes.
func (r *run) save(p string, data []byte, mode os.FileMode) error {
	if r.Verify {
		return nil
	}
	return writeFile(p, data, mode)
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

// problem records a damaged (exists) or missing file, once per path.
func (r *run) problem(what string, exists bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reported[what] {
		return
	}
	r.reported[what] = true
	if exists {
		r.rep.Bad = append(r.rep.Bad, what)
	} else {
		r.rep.Missing = append(r.rep.Missing, what)
	}
}

// parallel runs fn(0..n-1) on Jobs workers and returns the first error. The context fn gets is cancelled on the
// first error, so requests already in flight stop too.
func (r *run) parallel(ctx context.Context, n int, fn func(context.Context, int) error) error {
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
				if err := fn(ctx, i); err != nil {
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

// State is what .everlake/install.json records after a complete install, so verify can check the same selection.
type State struct {
	Pin         pin.Pin   `json:"pin"`
	Tags        []string  `json:"tags"`
	NoData      bool      `json:"loose_files_only,omitempty"`
	MaxPriority *int      `json:"max_priority,omitempty"`
	DataLimit   int64     `json:"data_limit_bytes,omitempty"` // a trial run: not every data file was fetched
	Installed   time.Time `json:"installed"`
}

func (r *run) writeState() error {
	s := State{Pin: r.Pin, Tags: r.Tags, NoData: r.NoData, MaxPriority: r.MaxPriority, Installed: time.Now().UTC()}
	if r.rep.DataSkippedByLimit > 0 {
		s.DataLimit = r.DataLimit
	}
	data, err := json.MarshalIndent(s, "", "  ")
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
