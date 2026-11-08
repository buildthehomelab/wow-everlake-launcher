// Package mirror is Everlake's own CDN: it answers like Blizzard's CDN, keeps a checked copy of everything it fetches
// from Blizzard, and can fetch a pinned build ahead of time (Prefetch), so launchers install from it when Blizzard's
// CDN is down or slow. See docs/cdn-and-peers.md.
//
// Every request is answered from, in order:
//  1. the overlay folder: our own patched builds, laid out as the CDN is (what wow-project-everlake's cdn-build
//     writes), served as they are;
//  2. the store: what was fetched before, checked again against its hashes before it is served;
//  3. Blizzard's CDN: fetched, checked, kept, served. Bytes that fail their check are never kept or served.
//
// What can be checked is kept: configs (named by their MD5), indexes (they carry their own hashes), loose data files
// (BLTE, named by their encoded key) and byte ranges of archives (each file a range holds is checked against the
// archive's index). Anything else under a served CDN path (other kinds of request, ranges of files that aren't
// archives, very large ranges) is passed through from Blizzard as it comes and not kept.
package mirror

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/cdn"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/tact"
)

// Limits on what is held in memory per request.
const (
	maxConfig = 16 << 20
	maxIndex  = 64 << 20
	maxLoose  = 1 << 30
	maxRange  = 256 << 20 // larger ranges are passed through, not kept (an archive is about 256 MB at most)
)

// Options set up a mirror.
type Options struct {
	Store    *Store
	Overlay  string                           // folder of our own builds served first ("" for none)
	Upstream []string                         // Blizzard's CDN hosts, tried in order
	Paths    []string                         // CDN paths served, e.g. "tpr/wow"; requests outside them get 404
	Jobs     int                              // requests to Blizzard at once (default 16), and as many passed through
	MemoryMB int                              // archive range bytes held at once, over all requests (default 1024)
	Log      func(format string, args ...any) // events: damaged copies, failures, prefetch progress
	Requests func(format string, args ...any) // one line per request served (nil: none)
}

// Mirror serves the CDN layout over HTTP (it is an http.Handler).
type Mirror struct {
	Options
	clients map[string]*cdn.Client
	pass    *http.Client
	sem     chan struct{} // checked fetches from Blizzard
	passSem chan struct{} // passed-through streams (a slow client holds one for as long as it reads)
	mem     *budget

	idxMu sync.Mutex
	idx   map[string]*archIndex // by "<cdn path>/<key>"; nil value: no such index (not an archive)

	// Counters for the log and tests.
	FromOverlay, FromStore, FromUpstream, Passed atomic.Int64
}

// archIndex is an archive's index entries in offset order.
type archIndex struct{ entries []tact.IndexEntry }

// New makes a mirror.
func New(o Options) (*Mirror, error) {
	if o.Store == nil {
		return nil, errors.New("mirror: no store")
	}
	if len(o.Paths) == 0 {
		return nil, errors.New("mirror: no CDN paths")
	}
	if o.Jobs < 1 {
		o.Jobs = 16
	}
	if o.MemoryMB < 1 {
		o.MemoryMB = 1024
	}
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	if o.Requests == nil {
		o.Requests = func(string, ...any) {}
	}
	if o.Overlay != "" {
		abs, err := filepath.Abs(o.Overlay)
		if err != nil {
			return nil, err
		}
		o.Overlay = abs
	}
	m := &Mirror{Options: o, clients: map[string]*cdn.Client{}, sem: make(chan struct{}, o.Jobs),
		passSem: make(chan struct{}, o.Jobs), mem: newBudget(int64(o.MemoryMB) << 20), idx: map[string]*archIndex{}}
	for _, p := range o.Paths {
		p = strings.Trim(p, "/")
		if p == "" || strings.Contains(p, "..") {
			return nil, fmt.Errorf("mirror: bad CDN path %q", p)
		}
		c := cdn.New(o.Upstream, p)
		c.Tries = 2
		m.clients[p] = c
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = 32
	tr.ResponseHeaderTimeout = time.Minute
	m.pass = &http.Client{Transport: tr}
	return m, nil
}

func (m *Mirror) acquire(ctx context.Context) error {
	select {
	case m.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Mirror) release() { <-m.sem }

// upstream runs one checked request against Blizzard's hosts.
func (m *Mirror) upstream(ctx context.Context, cdnPath string, q cdn.Request) ([]byte, error) {
	c := m.clients[cdnPath]
	if c == nil || len(c.Hosts) == 0 {
		return nil, fmt.Errorf("%s/%s/%s: %w (no upstream)", cdnPath, q.Kind, q.Key, cdn.ErrNotFound)
	}
	if err := m.acquire(ctx); err != nil {
		return nil, err
	}
	defer m.release()
	return c.Do(ctx, q)
}

// --- the checked stores of each kind of file ----------------------------------------------------------------------

// Config returns a config: from the store if intact, else from Blizzard (kept).
func (m *Mirror) Config(ctx context.Context, cdnPath string, key tact.Key) ([]byte, string, error) {
	check := func(b []byte) error {
		if tact.KeyOf(b) != key {
			return fmt.Errorf("config %s doesn't match its name", key)
		}
		return nil
	}
	return m.whole(ctx, cdnPath, "config", key, "", maxConfig, check)
}

// Index returns an archive index (or the loose-file index): from the store if intact, else from Blizzard (kept).
func (m *Mirror) Index(ctx context.Context, cdnPath string, key tact.Key) ([]byte, *tact.Index, string, error) {
	var idx *tact.Index
	data, src, err := m.whole(ctx, cdnPath, "data", key, ".index", maxIndex, func(b []byte) (err error) {
		idx, err = tact.ParseIndex(key, b)
		return err
	})
	return data, idx, src, err
}

// Loose returns a loose data file (a whole BLTE file named by its encoded key).
func (m *Mirror) Loose(ctx context.Context, cdnPath string, key tact.Key) ([]byte, string, error) {
	return m.whole(ctx, cdnPath, "data", key, "", maxLoose, func(b []byte) error { return tact.VerifyBLTE(b, key) })
}

// whole serves a file that is kept whole. src says where it came from: "store" or "blizzard".
func (m *Mirror) whole(ctx context.Context, cdnPath, kind string, key tact.Key, suffix string, max int64,
	check func([]byte) error) ([]byte, string, error) {
	data, err := m.Store.ReadWhole(cdnPath, kind, key, suffix)
	if err == nil {
		if check(data) == nil {
			return data, "store", nil
		}
		m.Log("damaged in the store, fetching again: %s/%s/%s%s", cdnPath, kind, key, suffix)
		if err := m.Store.RemoveWhole(cdnPath, kind, key, suffix); err != nil {
			return nil, "", err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, "", err
	}
	data, err = m.upstream(ctx, cdnPath, cdn.Request{Kind: kind, Key: key, Suffix: suffix, Max: max, Check: check})
	if err != nil {
		return nil, "", err
	}
	if err := m.Store.PutWhole(cdnPath, kind, key, suffix, data); err != nil {
		m.Log("couldn't keep %s/%s/%s%s: %v", cdnPath, kind, key, suffix, err)
	}
	return data, "blizzard", nil
}

// archive returns an archive's index in offset order, or nil when the CDN has no index of that name (the key isn't
// an archive).
func (m *Mirror) archive(ctx context.Context, cdnPath string, key tact.Key) (*archIndex, error) {
	id := cdnPath + "/" + key.String()
	m.idxMu.Lock()
	ix, ok := m.idx[id]
	m.idxMu.Unlock()
	if ok {
		return ix, nil
	}
	var parsed *tact.Index
	if m.Overlay != "" { // an archive of our own build: its index is in the overlay
		if b, err := os.ReadFile(filepath.Join(m.Overlay, filepath.FromSlash(cdnPath), "data",
			key.String()[:2], key.String()[2:4], key.String()+".index")); err == nil {
			parsed, _ = tact.ParseIndex(key, b)
		}
	}
	if parsed == nil {
		_, idx, _, err := m.Index(ctx, cdnPath, key)
		switch {
		case errors.Is(err, cdn.ErrNotFound):
		case err != nil:
			return nil, err
		default:
			parsed = idx
		}
	}
	if parsed != nil {
		if !parsed.HasOffsets {
			parsed = nil // the loose-file index isn't an archive
		} else {
			e := append([]tact.IndexEntry(nil), parsed.Entries...)
			sort.Slice(e, func(i, j int) bool { return e[i].Offset < e[j].Offset })
			ix = &archIndex{entries: e}
		}
	}
	m.idxMu.Lock()
	m.idx[id] = ix
	m.idxMu.Unlock()
	return ix, nil
}

// within calls fn for each index entry that overlaps [start, end), in offset order. Entries of size 0 (which no
// real file has) are skipped. Entries are taken not to overlap, as on the CDN, so their ends rise with their offsets.
func (ix *archIndex) within(start, end int64, fn func(e tact.IndexEntry)) {
	i := sort.Search(len(ix.entries), func(i int) bool {
		return int64(ix.entries[i].Offset)+int64(ix.entries[i].Size) > start
	})
	for ; i < len(ix.entries) && int64(ix.entries[i].Offset) < end; i++ {
		if ix.entries[i].Size > 0 {
			fn(ix.entries[i])
		}
	}
}

// whole widens [start, end) to take in the files cut off at either end, so every byte served can be checked.
func (ix *archIndex) whole(start, end int64) (int64, int64) {
	lo, hi := start, end
	ix.within(start, end, func(e tact.IndexEntry) {
		lo = min(lo, int64(e.Offset))
		hi = max(hi, int64(e.Offset)+int64(e.Size))
	})
	return lo, hi
}

// verify checks every file that lies wholly inside the range [start, start+len(data)).
func (ix *archIndex) verify(start int64, data []byte) error {
	end := start + int64(len(data))
	var err error
	ix.within(start, end, func(e tact.IndexEntry) {
		a, b := int64(e.Offset), int64(e.Offset)+int64(e.Size)
		if err != nil || a < start || b > end {
			return
		}
		if e2 := tact.VerifyBLTE(data[a-start:b-start], e.EKey); e2 != nil {
			err = fmt.Errorf("at %d: %w", a, e2)
		}
	})
	return err
}

// keepable is the part of [start, end) that can be kept: the range less any file cut off at either end (whose bytes
// can't be checked). Bytes between files are kept with the files around them.
func (ix *archIndex) keepable(start, end int64) (int64, int64) {
	lo, hi := start, end
	ix.within(start, end, func(e tact.IndexEntry) {
		a, b := int64(e.Offset), int64(e.Offset)+int64(e.Size)
		if a < start && b > lo {
			lo = b
		}
		if b > end && a < hi {
			hi = a
		}
	})
	return lo, hi
}

// Range returns [start, end) of an archive: from the store if all of it is there and intact, else from Blizzard
// (the checkable part is kept). total is the archive's full size (-1 if not known). ok is false when the key isn't
// an archive or the range is too large to keep; the caller then passes the request through.
//
// A range that starts or ends inside a file is served from the store only if the whole of those files is stored,
// so every byte served from the store is checked.
func (m *Mirror) Range(ctx context.Context, cdnPath string, key tact.Key, start, end int64) (data []byte,
	total int64, src string, ok bool, err error) {
	ix, err := m.archive(ctx, cdnPath, key)
	if err != nil || ix == nil || end-start > maxRange {
		return nil, -1, "", false, err
	}
	lo, hi := ix.whole(start, end)
	if err := m.mem.take(ctx, hi-lo); err != nil {
		return nil, -1, "", false, err
	}
	defer m.mem.give(hi - lo)
	stored, total, have, err := m.Store.ReadRange(cdnPath, key, lo, hi)
	if err != nil { // a .have that lists bytes the archive file doesn't hold: forget them and fetch again
		m.Log("store: %v; fetching again", err)
		if err := m.Store.DropRange(cdnPath, key, lo, hi); err != nil {
			return nil, -1, "", false, err
		}
		have = false
	}
	if have {
		if err := ix.verify(lo, stored); err == nil {
			return stored[start-lo : end-lo], total, "store", true, nil
		}
		m.Log("damaged in the store, fetching again: %s/data/%s %d-%d", cdnPath, key, lo, hi)
		if err := m.Store.DropRange(cdnPath, key, lo, hi); err != nil {
			return nil, -1, "", false, err
		}
	}
	stored = nil
	got := int64(-1)
	data, err = m.upstream(ctx, cdnPath, cdn.Request{Kind: "data", Key: key, Offset: start, Size: end - start,
		Total: &got, Check: func(b []byte) error { return ix.verify(start, b) }})
	if err != nil {
		return nil, -1, "", false, err
	}
	if got >= 0 {
		total = got
	}
	if lo, hi := ix.keepable(start, end); lo < hi {
		if err := m.Store.PutRange(cdnPath, key, lo, data[lo-start:hi-start], total); err != nil {
			m.Log("couldn't keep %s/data/%s %d-%d: %v", cdnPath, key, lo, hi, err)
		}
	}
	return data, total, "blizzard", true, nil
}

// --- HTTP ---------------------------------------------------------------------------------------------------------

var cdnFile = regexp.MustCompile(`^(config|data)/([0-9a-f]{2})/([0-9a-f]{2})/([0-9a-f]{32})(\.index)?$`)

func (m *Mirror) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := path.Clean("/" + r.URL.Path)
	rg := r.Header.Get("Range")
	if p == "/everlake/build.json" && m.Overlay != "" { // the patched build's keys (everlake-client.sh gate2)
		if m.serveFile(w, r, filepath.Join(m.Overlay, "build.json")) {
			return
		}
		http.NotFound(w, r)
		return
	}
	cdnPath, rest := m.split(p)
	if cdnPath == "" {
		http.NotFound(w, r)
		return
	}
	if m.Overlay != "" && m.serveFile(w, r, filepath.Join(m.Overlay, filepath.FromSlash(strings.TrimPrefix(p, "/")))) {
		m.FromOverlay.Add(1)
		m.Requests("ours     %s %s", p, rg)
		return
	}
	f := cdnFile.FindStringSubmatch(rest)
	if f == nil || f[2] != f[4][:2] || f[3] != f[4][2:4] {
		m.passThrough(w, r, cdnPath, rest)
		return
	}
	kind, key, suffix := f[1], tact.MustKey(f[4]), f[5]
	ctx := r.Context()
	var data []byte
	var src string
	var err error
	switch {
	case kind == "config" && suffix != "":
		http.NotFound(w, r)
		return
	case kind == "config":
		data, src, err = m.Config(ctx, cdnPath, key)
	case suffix == ".index":
		data, _, src, err = m.Index(ctx, cdnPath, key)
	case rg == "":
		// A loose file, unless the key is an archive: a whole archive isn't one checkable file, so it's passed
		// through without being held in memory. (No launcher asks for one.)
		if !m.Store.HasWhole(cdnPath, "data", key, "") {
			if ix, err := m.archive(ctx, cdnPath, key); err == nil && ix != nil {
				m.passThrough(w, r, cdnPath, rest)
				return
			}
		}
		data, src, err = m.Loose(ctx, cdnPath, key)
	default:
		start, end, simple := parseRange(rg)
		if !simple {
			m.passThrough(w, r, cdnPath, rest)
			return
		}
		var total int64
		var ok bool
		data, total, src, ok, err = m.Range(ctx, cdnPath, key, start, end)
		if err == nil && !ok {
			m.passThrough(w, r, cdnPath, rest)
			return
		}
		if err == nil {
			m.count(src)
			m.Requests("%-8s %s %s", src, p, rg)
			size := "*"
			if total >= 0 {
				size = strconv.FormatInt(total, 10)
			}
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%s", start, end-1, size))
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(data)
			return
		}
	}
	if err != nil {
		m.fail(w, p, rg, err)
		return
	}
	m.count(src)
	m.Requests("%-8s %s %s", src, p, rg)
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

func (m *Mirror) count(src string) {
	if src == "store" {
		m.FromStore.Add(1)
	} else {
		m.FromUpstream.Add(1)
	}
}

func (m *Mirror) fail(w http.ResponseWriter, p, rg string, err error) {
	if errors.Is(err, context.Canceled) {
		return // the client went away
	}
	m.Log("FAIL     %s %s: %v", p, rg, err)
	if errors.Is(err, cdn.ErrNotFound) {
		http.NotFound(w, nil)
		return
	}
	http.Error(w, "the mirror couldn't get this file", http.StatusBadGateway)
}

// split finds the served CDN path a request is under and returns the rest ("" if none).
func (m *Mirror) split(p string) (cdnPath, rest string) {
	for cp := range m.clients {
		if strings.HasPrefix(p, "/"+cp+"/") {
			return cp, strings.TrimPrefix(p, "/"+cp+"/")
		}
	}
	return "", ""
}

// serveFile serves a local file (with ranges) if it exists; false if it doesn't.
func (m *Mirror) serveFile(w http.ResponseWriter, r *http.Request, p string) bool {
	if m.Overlay == "" || (p != m.Overlay && !strings.HasPrefix(p, m.Overlay+string(filepath.Separator))) {
		return false
	}
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		return false
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", st.ModTime(), f)
	return true
}

// parseRange reads a single "bytes=a-b" range as [a, b+1). Anything else (open-ended, suffix or several ranges)
// isn't simple.
func parseRange(h string) (start, end int64, simple bool) {
	a, b, ok := strings.Cut(strings.TrimPrefix(h, "bytes="), "-")
	if !ok || !strings.HasPrefix(h, "bytes=") || strings.Contains(b, ",") {
		return 0, 0, false
	}
	s, err1 := strconv.ParseInt(a, 10, 64)
	e, err2 := strconv.ParseInt(b, 10, 64)
	if err1 != nil || err2 != nil || s < 0 || e < s || e >= 1<<50 {
		return 0, 0, false
	}
	return s, e + 1, true
}

// passThrough streams a request from Blizzard's hosts as it comes, keeping nothing.
func (m *Mirror) passThrough(w http.ResponseWriter, r *http.Request, cdnPath, rest string) {
	ctx := r.Context()
	select {
	case m.passSem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-m.passSem }()
	var last error
	for i, host := range m.Upstream {
		url := cdn.Base(host) + "/" + cdnPath + "/" + rest
		req, err := http.NewRequestWithContext(ctx, r.Method, url, nil)
		if err != nil {
			last = err
			continue
		}
		req.Header.Set("User-Agent", cdn.UserAgent)
		if rg := r.Header.Get("Range"); rg != "" {
			req.Header.Set("Range", rg)
		}
		resp, err := m.pass.Do(req)
		if err != nil {
			last = err
			continue
		}
		// Another host may have what this one hasn't, as cdn.Client also assumes; the last host's answer stands.
		if resp.StatusCode >= 500 || (resp.StatusCode == http.StatusNotFound && i < len(m.Upstream)-1) {
			resp.Body.Close()
			last = fmt.Errorf("%s: HTTP %s", url, resp.Status)
			continue
		}
		for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag",
			"Last-Modified"} {
			if v := resp.Header.Get(h); v != "" {
				w.Header().Set(h, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		resp.Body.Close()
		m.Passed.Add(1)
		m.Requests("passed   /%s/%s %s (%d)", cdnPath, rest, r.Header.Get("Range"), resp.StatusCode)
		return
	}
	if last == nil {
		last = errors.New("no upstream hosts")
	}
	m.fail(w, "/"+cdnPath+"/"+rest, r.Header.Get("Range"), last)
}

// budget limits the bytes held at once over all requests. Requests larger than the whole budget take all of it.
type budget struct {
	mu     sync.Mutex // one taker at a time, so two never wait on each other's half-taken share
	tokens chan struct{}
	unit   int64
}

func newBudget(bytes int64) *budget {
	const unit = 1 << 20
	n := max(int(bytes/unit), 1)
	b := &budget{tokens: make(chan struct{}, n), unit: unit}
	for i := 0; i < n; i++ {
		b.tokens <- struct{}{}
	}
	return b
}

func (b *budget) units(n int64) int { return min(max(int((n+b.unit-1)/b.unit), 1), cap(b.tokens)) }

func (b *budget) take(ctx context.Context, n int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	want := b.units(n)
	for got := 0; got < want; got++ {
		select {
		case <-b.tokens:
		case <-ctx.Done():
			for ; got > 0; got-- {
				b.tokens <- struct{}{}
			}
			return ctx.Err()
		}
	}
	return nil
}

func (b *budget) give(n int64) {
	for i := b.units(n); i > 0; i-- {
		b.tokens <- struct{}{}
	}
}
