package mirror

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/cdn"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/install"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/tact"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/tacttest"
)

// newMirror starts a mirror in front of the build's fake CDN.
func newMirror(t *testing.T, b *tacttest.Build, overlay string) (*Mirror, *httptest.Server) {
	t.Helper()
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(Options{Store: st, Overlay: overlay, Upstream: []string{b.CDN.Host()}, Paths: []string{b.Pin.CDNPath},
		Jobs: 4, Log: t.Logf, Requests: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	return m, srv
}

// installFrom installs the build into a new folder using only host.
func installFrom(t *testing.T, b *tacttest.Build, host string, tags ...string) (*install.Report, error) {
	t.Helper()
	c := cdn.New([]string{host}, b.Pin.CDNPath)
	c.Tries = 1
	return install.Run(context.Background(), install.Options{Dir: t.TempDir(), Pin: b.Pin, CDN: c, Tags: tags,
		Jobs: 3, GameHosts: []string{"cdn.example.invalid"}, Log: t.Logf})
}

func hostOf(s *httptest.Server) string { return strings.TrimPrefix(s.URL, "http://") }

func TestPrefetchThenInstallWithBlizzardDown(t *testing.T) {
	b := tacttest.NewBuild(t)
	m, srv := newMirror(t, b, "")
	rep, err := m.Prefetch(context.Background(), PrefetchOptions{Pin: b.Pin, Tags: []string{"OSX", "Windows", "enUS"},
		Jobs: 2})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Indexes != 1 || len(rep.Missing) != 0 || rep.Bytes == 0 {
		t.Fatalf("prefetch report %+v", rep)
	}
	again, err := m.Prefetch(context.Background(), PrefetchOptions{Pin: b.Pin, Tags: []string{"OSX", "Windows", "enUS"}})
	if err != nil || again.Bytes != 0 || again.Already != again.Requests {
		t.Fatalf("second prefetch fetched again: %+v, %v", again, err)
	}

	b.CDN.Close() // Blizzard is down
	for _, tags := range [][]string{{"OSX", "enUS"}, {"Windows", "enUS"}} {
		r, err := installFrom(t, b, hostOf(srv), tags...)
		if err != nil {
			t.Fatalf("install %v from the mirror alone: %v", tags, err)
		}
		if r.Failed() || r.DataFetched == 0 || r.LooseFetched == 0 {
			t.Fatalf("install %v: %+v", tags, r)
		}
	}
	if m.FromUpstream.Load() != 0 || m.Passed.Load() != 0 {
		t.Errorf("after the prefetch, requests still went to Blizzard: %d fetched, %d passed",
			m.FromUpstream.Load(), m.Passed.Load())
	}
}

func TestKeepsWhatItPassesOn(t *testing.T) {
	b := tacttest.NewBuild(t)
	m, srv := newMirror(t, b, "")
	if _, err := installFrom(t, b, hostOf(srv), "OSX", "enUS"); err != nil {
		t.Fatal(err)
	}
	before := m.FromUpstream.Load()
	b.CDN.Close()
	r, err := installFrom(t, b, hostOf(srv), "OSX", "enUS")
	if err != nil || r.Failed() {
		t.Fatalf("second install with Blizzard down: %+v, %v", r, err)
	}
	if m.FromUpstream.Load() != before {
		t.Errorf("second install went to Blizzard")
	}
}

func TestDamagedUpstreamIsNotKept(t *testing.T) {
	b := tacttest.NewBuild(t)
	bad := append([]byte(nil), b.ArchBytes...)
	bad[len(bad)/2] ^= 0xff
	b.CDN.Put("data", b.Archive, "", bad)
	m, srv := newMirror(t, b, "")
	if _, err := installFrom(t, b, hostOf(srv), "OSX", "enUS"); err == nil {
		t.Fatal("install through a mirror of a damaged CDN worked")
	}
	at := int64(len(bad) / 2)
	if ok, _, err := m.Store.Covered(b.Pin.CDNPath, b.Archive, at, at+1); err != nil || ok {
		t.Fatalf("the damaged byte is in the store (%v, %v)", ok, err)
	}
}

func TestDamagedStoreIsFetchedAgain(t *testing.T) {
	b := tacttest.NewBuild(t)
	m, srv := newMirror(t, b, "")
	if _, err := installFrom(t, b, hostOf(srv), "OSX", "enUS"); err != nil {
		t.Fatal(err)
	}
	// Damage every byte the store holds of the archive (a crash, a bad disk).
	p := m.Store.Path(b.Pin.CDNPath, "data", b.Archive, "")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for i := range data {
		data[i] ^= 0x5a
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := installFrom(t, b, hostOf(srv), "OSX", "enUS")
	if err != nil || r.Failed() {
		t.Fatalf("install after the store was damaged: %+v, %v", r, err)
	}
}

func TestOverlayFirstAndPassThrough(t *testing.T) {
	b := tacttest.NewBuild(t)
	overlay := t.TempDir()
	ours := []byte("our own config")
	k := tact.KeyOf(ours)
	s := k.String()
	op := filepath.Join(overlay, filepath.FromSlash(b.Pin.CDNPath), "config", s[:2], s[2:4], s)
	if err := os.MkdirAll(filepath.Dir(op), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(op, ours, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(overlay, "build.json"), []byte(`{"build":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	m, srv := newMirror(t, b, overlay)
	get := func(path, rg string) (int, []byte) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		if rg != "" {
			req.Header.Set("Range", rg)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, body
	}
	if code, body := get("/"+b.Pin.CDNPath+"/config/"+s[:2]+"/"+s[2:4]+"/"+s, "bytes=4-6"); code != 206 ||
		string(body) != "own" {
		t.Errorf("overlay range: %d %q", code, body)
	}
	if code, body := get("/everlake/build.json", ""); code != 200 || !bytes.Contains(body, []byte("build")) {
		t.Errorf("build.json: %d %q", code, body)
	}
	a := b.Archive.String()
	arch := "/" + b.Pin.CDNPath + "/data/" + a[:2] + "/" + a[2:4] + "/" + a
	if code, body := get(arch, "bytes=0-1,4-5"); code != 206 || len(body) == 0 || m.Passed.Load() != 1 {
		t.Errorf("several ranges weren't passed through: %d, %d passed", code, m.Passed.Load())
	}
	if code, _ := get("/somewhere/else", ""); code != 404 {
		t.Errorf("a path outside the CDN paths answered %d", code)
	}
	if code, _ := get(arch+"/../../../../../../etc/passwd", ""); code != 404 && code != 400 {
		t.Errorf("a path with .. answered %d", code)
	}
}

func TestRangeHeaders(t *testing.T) {
	b := tacttest.NewBuild(t)
	_, srv := newMirror(t, b, "")
	a := b.Archive.String()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/"+b.Pin.CDNPath+"/data/"+a[:2]+"/"+a[2:4]+"/"+a, nil)
	req.Header.Set("Range", "bytes=0-9")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	want := "bytes 0-9/" + strconv.Itoa(len(b.ArchBytes))
	if resp.StatusCode != 206 || resp.Header.Get("Content-Range") != want {
		t.Errorf("got %d %q, want 206 %q", resp.StatusCode, resp.Header.Get("Content-Range"), want)
	}
}

func TestSpans(t *testing.T) {
	var l []span
	l = addSpan(l, span{10, 20})
	l = addSpan(l, span{30, 40})
	l = addSpan(l, span{20, 30}) // touching both: one span
	if len(l) != 1 || l[0] != (span{10, 40}) {
		t.Fatalf("addSpan: %v", l)
	}
	if !covers(l, span{12, 40}) || covers(l, span{5, 15}) || covers(l, span{35, 41}) {
		t.Error("covers is wrong")
	}
	l = removeSpan(l, span{15, 25})
	if len(l) != 2 || l[0] != (span{10, 15}) || l[1] != (span{25, 40}) {
		t.Fatalf("removeSpan: %v", l)
	}
	ix := &archIndex{entries: []tact.IndexEntry{{Offset: 0, Size: 10}, {Offset: 12, Size: 10}, {Offset: 30, Size: 5}}}
	if lo, hi := ix.keepable(5, 32); lo != 10 || hi != 30 {
		t.Errorf("keepable(5, 32) = %d, %d; want 10, 30", lo, hi)
	}
	if lo, hi := ix.keepable(0, 35); lo != 0 || hi != 35 {
		t.Errorf("keepable(0, 35) = %d, %d", lo, hi)
	}
}

func TestPlanArchiveTouchesEndToEnd(t *testing.T) {
	var a tact.Key
	// Files of 10 bytes with gaps of 5; merge through gaps up to 5, at most 30 bytes a request; then a far file.
	ls := []loc{{offset: 0, size: 10}, {offset: 15, size: 10}, {offset: 30, size: 10}, {offset: 45, size: 10},
		{offset: 200, size: 10}}
	got := planArchive(a, ls, 5, 30)
	want := []request{{start: 0, end: 30}, {start: 30, end: 55}, {start: 200, end: 210}}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i].start != want[i].start || got[i].end != want[i].end {
			t.Fatalf("request %d = %d-%d, want %d-%d (all: %v)", i, got[i].start, got[i].end, want[i].start,
				want[i].end, got)
		}
	}
}

// reopen starts a second mirror on the same store (as after a restart), in front of the same CDN.
func reopen(t *testing.T, b *tacttest.Build, st *Store) (*Mirror, *httptest.Server) {
	t.Helper()
	m, err := New(Options{Store: st, Upstream: []string{b.CDN.Host()}, Paths: []string{b.Pin.CDNPath}, Jobs: 4,
		Log: t.Logf, Requests: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	return m, srv
}

func TestStoreRepairsItself(t *testing.T) {
	b := tacttest.NewBuild(t)
	m, srv := newMirror(t, b, "")
	if _, err := installFrom(t, b, hostOf(srv), "OSX", "enUS"); err != nil {
		t.Fatal(err)
	}
	p := m.Store.Path(b.Pin.CDNPath, "data", b.Archive, "")
	for name, damage := range map[string]func() error{
		"unreadable .have":       func() error { return os.WriteFile(p+".have", []byte("{not json"), 0o644) },
		"archive file too short": func() error { return os.Truncate(p, 10) },
		"archive file gone":      func() error { return os.Remove(p) },
	} {
		if err := damage(); err != nil {
			t.Fatal(err)
		}
		_, srv2 := reopen(t, b, m.Store)
		r, err := installFrom(t, b, hostOf(srv2), "OSX", "enUS")
		if err != nil || r.Failed() {
			t.Fatalf("%s: install through the mirror: %+v, %v", name, r, err)
		}
	}
}

func TestPartialRangeFromStoreIsChecked(t *testing.T) {
	b := tacttest.NewBuild(t)
	m, srv := newMirror(t, b, "")
	if _, err := installFrom(t, b, hostOf(srv), "OSX", "enUS"); err != nil {
		t.Fatal(err)
	}
	// Damage the store's copy at byte 50 (inside the first file), then ask for bytes 40-60 only: the mirror must
	// notice (by checking the whole file) and fetch from Blizzard, not hand out the damaged bytes.
	p := m.Store.Path(b.Pin.CDNPath, "data", b.Archive, "")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	data[50] ^= 0xff
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, _, src, ok, err := m.Range(context.Background(), b.Pin.CDNPath, b.Archive, 40, 61)
	if err != nil || !ok || !bytes.Equal(got, b.ArchBytes[40:61]) || src != "blizzard" {
		t.Fatalf("partial range: src %q ok %v err %v, right bytes %v", src, ok, err, bytes.Equal(got, b.ArchBytes[40:61]))
	}
}

func TestSizeZeroEntryAndHugeRange(t *testing.T) {
	ix := &archIndex{entries: []tact.IndexEntry{{Offset: 0, Size: 4}, {Offset: 4, Size: 0}, {Offset: 4, Size: 4}}}
	blob1, e1 := tacttest.Plain([]byte("a"))
	blob2, e2 := tacttest.Plain([]byte("b"))
	ix.entries = []tact.IndexEntry{{EKey: e1, Offset: 0, Size: uint32(len(blob1))},
		{Offset: uint32(len(blob1)), Size: 0}, {EKey: e2, Offset: uint32(len(blob1)), Size: uint32(len(blob2))}}
	if err := ix.verify(0, append(append([]byte{}, blob1...), blob2...)); err != nil {
		t.Errorf("a size-0 index entry broke the check: %v", err)
	}

	b := tacttest.NewBuild(t)
	_, srv := newMirror(t, b, "")
	a := b.Archive.String()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/"+b.Pin.CDNPath+"/data/"+a[:2]+"/"+a[2:4]+"/"+a, nil)
	req.Header.Set("Range", "bytes=0-9223372036854775807")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("a huge range dropped the connection: %v", err)
	}
	resp.Body.Close()
}

func TestBudget(t *testing.T) {
	bud := newBudget(4 << 20)
	ctx := context.Background()
	if err := bud.take(ctx, 3<<20); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := bud.take(short, 2<<20); err == nil {
		t.Fatal("took more than the budget holds")
	}
	bud.give(3 << 20)
	if err := bud.take(ctx, 100<<20); err != nil { // larger than the budget: takes all of it
		t.Fatal(err)
	}
	bud.give(100 << 20)
	if len(bud.tokens) != 4 {
		t.Fatalf("%d of 4 units back", len(bud.tokens))
	}
}
