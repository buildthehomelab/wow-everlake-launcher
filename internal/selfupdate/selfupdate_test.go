package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		tag, current string
		want         bool
	}{
		{"v0.1.0-alpha.14", "v0.1.0-alpha.13", true},
		{"v0.1.0-alpha.14", "0.1.0-alpha.13", true}, // with or without the v
		{"v0.1.0-alpha.10", "v0.1.0-alpha.9", true}, // numbers by value, not as text
		{"v0.1.0-alpha.9", "v0.1.0-alpha.10", false},
		{"v0.1.0-alpha.13", "v0.1.0-alpha.13", false},
		{"v0.1.0-alpha.12", "v0.1.0-alpha.13", false}, // never back
		{"v0.1.0-beta.1", "v0.1.0-alpha.20", true},
		{"v0.1.0", "v0.1.0-rc.3", true}, // a release after its pre-releases
		{"v0.1.0-rc.3", "v0.1.0", false},
		{"v0.2.0-alpha.1", "v0.1.9", true},
		{"v0.1.0-alpha.1.1", "v0.1.0-alpha.1", true},
		{"v1.0.0+build.5", "v1.0.0", false}, // build metadata doesn't order
		{"v0.1.0-alpha.14", "dev", false},   // local and CI builds never update
		{"v0.1.0-alpha.14", "ci-1a2b3c4", false},
		{"nightly", "v0.1.0-alpha.13", false},
		{"v0.1", "v0.0.9", false},
		{"v0.1.0-", "v0.0.9", false},
	} {
		if got := Newer(c.tag, c.current); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.tag, c.current, got, c.want)
		}
	}
}

func TestParseSums(t *testing.T) {
	a := strings.Repeat("ab", 32)
	b := strings.Repeat("CD", 32)
	got, err := ParseSums(strings.NewReader(a + "  everlake-launcher-windows-amd64.exe\n\n" + b + " *Everlake-Launcher-macos.zip\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got["everlake-launcher-windows-amd64.exe"] != a || got["Everlake-Launcher-macos.zip"] != strings.ToLower(b) ||
		len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	for _, bad := range []string{"abc  x.exe", a, a[:62] + "zz  x.exe"} {
		if _, err := ParseSums(strings.NewReader(bad)); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

// fakeGitHub serves a release list and its files the way GitHub's API does.
type fakeGitHub struct {
	tag        string
	prerelease bool
	files      map[string][]byte
	sums       string // SHA256SUMS.txt; "" makes it from files
}

func (f *fakeGitHub) serve(t *testing.T) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Errorf("%s: no User-Agent (GitHub refuses those)", r.URL.Path)
		}
		switch {
		case r.URL.Path == "/releases/latest":
			type asset struct {
				Name string `json:"name"`
				URL  string `json:"browser_download_url"`
				Size int64  `json:"size"`
			}
			var assets []asset
			for name, data := range f.files {
				assets = append(assets, asset{name, srv.URL + "/download/" + name, int64(len(data))})
			}
			assets = append(assets, asset{SumsName, srv.URL + "/download/" + SumsName, int64(len(f.sumsText()))})
			json.NewEncoder(w).Encode(map[string]any{
				"tag_name": f.tag, "html_url": srv.URL + "/page", "prerelease": f.prerelease, "assets": assets,
			})
		case r.URL.Path == "/download/"+SumsName:
			fmt.Fprint(w, f.sumsText())
		case strings.HasPrefix(r.URL.Path, "/download/"):
			data, ok := f.files[strings.TrimPrefix(r.URL.Path, "/download/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeGitHub) sumsText() string {
	if f.sums != "" {
		return f.sums
	}
	var b strings.Builder
	for name, data := range f.files {
		sum := sha256.Sum256(data)
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	return b.String()
}

func TestDownloadAndReplace(t *testing.T) {
	ctx := context.Background()
	fresh := []byte("the new launcher")
	gh := &fakeGitHub{tag: "v0.1.0-alpha.14", files: map[string][]byte{"launcher.exe": fresh}}
	srv := gh.serve(t)
	c := &Client{API: srv.URL + "/releases/latest", UserAgent: "test"}

	rel, err := c.Latest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rel.Tag != "v0.1.0-alpha.14" || rel.Assets["launcher.exe"].Size != int64(len(fresh)) {
		t.Fatalf("release %+v", rel)
	}
	sums, err := c.Sums(ctx, rel)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	exe := filepath.Join(dir, "launcher.exe")
	if err := os.WriteFile(exe, []byte("the old launcher"), 0o755); err != nil {
		t.Fatal(err)
	}
	target, err := TargetFor(exe)
	if err != nil || target.App {
		t.Fatalf("target %+v, %v", target, err)
	}
	var calls int
	staged, err := c.Download(ctx, rel.Assets["launcher.exe"], sums["launcher.exe"], target.Dir(),
		func(got, total int64) { calls++ })
	if err != nil {
		t.Fatal(err)
	}
	if calls == 0 {
		t.Error("progress never called")
	}
	if err := target.Replace(staged, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); string(got) != string(fresh) {
		t.Fatalf("after Replace: %q", got)
	}
	if got, _ := os.ReadFile(target.OldPath()); string(got) != "the old launcher" {
		t.Fatalf("old copy: %q", got)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Errorf("staged file still there: %v", err)
	}
	target.Cleanup()
	if _, err := os.Stat(target.OldPath()); !os.IsNotExist(err) {
		t.Errorf("Cleanup left the old copy: %v", err)
	}
	// A second update moves the first one's copy aside the same way.
	staged, err = c.Download(ctx, rel.Assets["launcher.exe"], sums["launcher.exe"], target.Dir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.Replace(staged, nil); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadRefusesAMismatch(t *testing.T) {
	ctx := context.Background()
	gh := &fakeGitHub{tag: "v0.1.0-alpha.14", files: map[string][]byte{"launcher.exe": []byte("tampered")},
		sums: strings.Repeat("00", 32) + "  launcher.exe\n"}
	srv := gh.serve(t)
	c := &Client{API: srv.URL + "/releases/latest"}
	rel, err := c.Latest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sums, err := c.Sums(ctx, rel)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	_, err = c.Download(ctx, rel.Assets["launcher.exe"], sums["launcher.exe"], dir, nil)
	if err == nil || !strings.Contains(err.Error(), "doesn't match") {
		t.Fatalf("err = %v", err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("the bad download was left in %s: %v", dir, left)
	}
}

func TestLatestSkipsPrereleases(t *testing.T) {
	gh := &fakeGitHub{tag: "v0.1.0-alpha.14", prerelease: true}
	srv := gh.serve(t)
	c := &Client{API: srv.URL + "/releases/latest"}
	if _, err := c.Latest(context.Background()); err == nil {
		t.Fatal("a pre-release counted as the latest release")
	}
}

func TestReplaceFileReadOnlyFolder(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can write anywhere")
	}
	if strings.HasPrefix(filepath.VolumeName(t.TempDir()), "C:") || os.PathSeparator == '\\' {
		t.Skip("folder permissions don't work this way on Windows")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "launcher")
	os.WriteFile(exe, []byte("old"), 0o755)
	staged := filepath.Join(t.TempDir(), "new")
	os.WriteFile(staged, []byte("new"), 0o644)
	os.Chmod(dir, 0o555)
	defer os.Chmod(dir, 0o755)
	err := Target{Path: exe}.Replace(staged, nil)
	if !errors.Is(err, ErrCantReplace) {
		t.Fatalf("err = %v, want ErrCantReplace", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Fatalf("launcher is now %q", got)
	}
}

func TestTargetFor(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Everlake Launcher.app")
	exe := filepath.Join(app, "Contents", "MacOS", "everlake-launcher")
	os.MkdirAll(filepath.Dir(exe), 0o755)
	os.WriteFile(exe, nil, 0o755)
	tg, err := TargetFor(exe)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(app)
	if !tg.App || tg.Path != want {
		t.Fatalf("target %+v, want the app %s", tg, want)
	}
	if _, err := TargetFor("/private/var/folders/x/AppTranslocation/ABC/d/Everlake Launcher.app/Contents/MacOS/e"); !errors.Is(err, ErrCantReplace) {
		t.Fatalf("translocated app: err = %v", err)
	}
}

func TestReplaceStopsWhenTheCheckFails(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "launcher.exe")
	os.WriteFile(exe, []byte("old"), 0o755)
	staged := filepath.Join(dir, ".everlake-update-1.exe")
	os.WriteFile(staged, []byte("new"), 0o644)
	var checked string
	err := Target{Path: exe}.Replace(staged, func(p string) error { checked = p; return errors.New("won't start") })
	if err == nil || checked != staged {
		t.Fatalf("err %v, checked %q", err, checked)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Fatalf("launcher is now %q", got)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Errorf("staged copy left behind: %v", err)
	}
}

func TestRestore(t *testing.T) {
	dir := t.TempDir()
	tg := Target{Path: filepath.Join(dir, "launcher.exe")}
	os.WriteFile(tg.OldPath(), []byte("old"), 0o755)
	if !tg.Restore() {
		t.Fatal("didn't put the old copy back")
	}
	if got, _ := os.ReadFile(tg.Path); string(got) != "old" {
		t.Fatalf("got %q", got)
	}
	if tg.Restore() {
		t.Error("restored over a copy that's there")
	}
}

func TestDownloadGivesUpWhenStalled(t *testing.T) {
	old := downloadIdle
	downloadIdle = 200 * time.Millisecond
	defer func() { downloadIdle = old }()
	hang := make(chan struct{})
	defer close(hang)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.Write([]byte("some"))
		w.(http.Flusher).Flush()
		select {
		case <-hang:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	c := &Client{}
	dir := t.TempDir()
	start := time.Now()
	_, err := c.Download(context.Background(), Asset{Name: "x.exe", URL: srv.URL}, strings.Repeat("00", 32), dir, nil)
	if err == nil || !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("took %s to give up", time.Since(start))
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("left %v", left)
	}
}
