// Package selfupdate moves the launcher to its newest release: it asks GitHub for the repository's Latest release,
// downloads this build's file from it, checks the file against the release's SHA256SUMS.txt, and puts it in place of
// the running program. The running launcher keeps going on the old copy until it restarts.
//
// Only release builds update: scripts/package.sh bakes in which release file each build is (main.updateAsset), and
// a build without one (go build, go run, CI) never replaces itself.
package selfupdate

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultAPI is the GitHub API address of the launcher's Latest release. Pre-releases and drafts are never Latest.
const DefaultAPI = "https://api.github.com/repos/buildthehomelab/wow-everlake-launcher/releases/latest"

// SumsName is the release file that lists every other file's SHA-256.
const SumsName = "SHA256SUMS.txt"

// maxAsset caps a download: the app files are 10-40 MB.
const maxAsset = 256 << 20

// Release is a published launcher release.
type Release struct {
	Tag    string           // v0.1.0-alpha.14
	Page   string           // the release's web page
	Assets map[string]Asset // by file name
}

// Asset is one file of a release.
type Asset struct {
	Name string
	URL  string
	Size int64
}

// Client talks to GitHub. The zero value uses DefaultAPI and a plain HTTP client.
type Client struct {
	API       string
	HTTP      *http.Client
	UserAgent string
}

func (c *Client) api() string {
	if c.API != "" {
		return c.API
	}
	return DefaultAPI
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) get(ctx context.Context, url string, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	ua := c.UserAgent
	if ua == "" {
		ua = "everlake-launcher"
	}
	req.Header.Set("User-Agent", ua)
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return resp, nil
}

// Latest reads the repository's Latest release.
func (c *Client) Latest(ctx context.Context) (Release, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	resp, err := c.get(ctx, c.api(), "application/vnd.github+json")
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	var raw struct {
		Tag        string `json:"tag_name"`
		Page       string `json:"html_url"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&raw); err != nil {
		return Release{}, fmt.Errorf("latest release: %w", err)
	}
	if raw.Tag == "" || raw.Draft || raw.Prerelease {
		return Release{}, fmt.Errorf("latest release: no published release")
	}
	r := Release{Tag: raw.Tag, Page: raw.Page, Assets: map[string]Asset{}}
	for _, a := range raw.Assets {
		r.Assets[a.Name] = Asset{Name: a.Name, URL: a.URL, Size: a.Size}
	}
	return r, nil
}

// Sums reads a release's SHA256SUMS.txt: file name to hash.
func (c *Client) Sums(ctx context.Context, r Release) (map[string]string, error) {
	a, ok := r.Assets[SumsName]
	if !ok {
		return nil, fmt.Errorf("%s has no %s, so its files can't be checked", r.Tag, SumsName)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := c.get(ctx, a.URL, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return ParseSums(io.LimitReader(resp.Body, 1<<20))
}

// ParseSums reads sha256sum's output: "<hex>  <name>" a line (a "*" before the name marks binary mode).
func ParseSums(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		sum, name, ok := strings.Cut(line, " ")
		name = strings.TrimPrefix(strings.TrimLeft(name, " "), "*")
		if b, err := hex.DecodeString(sum); !ok || err != nil || len(b) != sha256.Size || name == "" {
			return nil, fmt.Errorf("%s: bad line %q", SumsName, line)
		}
		out[name] = strings.ToLower(sum)
	}
	return out, sc.Err()
}

// How long a download may take in all, and may go without receiving anything, before it's given up: a stalled
// connection must not hold the page's buttons.
var (
	downloadLimit = 15 * time.Minute
	downloadIdle  = 60 * time.Second
)

// Download fetches a release file into a new file in dir and checks it against want (a SHA-256 in hex). The file
// is removed again unless it checks out; it keeps the asset's extension (.exe), so it can be run to check it.
// progress, if set, hears the bytes so far and the expected total (0 when unknown) now and then.
func (c *Client) Download(ctx context.Context, a Asset, want, dir string, progress func(got, total int64)) (string, error) {
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, downloadLimit)
	defer cancel()
	idle := time.AfterFunc(downloadIdle, cancel)
	defer idle.Stop()
	resp, err := c.get(ctx, a.URL, "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	total := a.Size
	if total <= 0 {
		total = resp.ContentLength
	}
	if total > maxAsset {
		return "", fmt.Errorf("%s: %d bytes is more than an update can be", a.URL, total)
	}
	f, err := os.CreateTemp(dir, ".everlake-update-*"+filepath.Ext(a.Name))
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(f.Name())
		}
	}()
	h := sha256.New()
	var got int64
	last := time.Now()
	buf := make([]byte, 256<<10)
	body := io.LimitReader(resp.Body, maxAsset+1)
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			idle.Reset(downloadIdle)
			if _, err := f.Write(buf[:n]); err != nil {
				return "", err
			}
			h.Write(buf[:n])
			got += int64(n)
			if progress != nil && time.Since(last) >= 500*time.Millisecond {
				progress(got, total)
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if ctx.Err() != nil && parent.Err() == nil {
				return "", fmt.Errorf("the download stalled or took too long (%s so far): %w",
					fmt.Sprintf("%.1f MB", float64(got)/(1<<20)), rerr)
			}
			return "", rerr
		}
	}
	if got > maxAsset {
		return "", fmt.Errorf("%s: more than an update can be", a.URL)
	}
	if progress != nil {
		progress(got, total)
	}
	if sum := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(sum, want) {
		return "", fmt.Errorf("the download doesn't match %s (SHA-256 %s, expected %s)", SumsName, sum, want)
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	ok = true
	return f.Name(), nil
}

// Newer reports whether release tag is a later version than current. Both are semantic versions with or without a
// leading "v" (v0.1.0-alpha.14). A current version that isn't one ("dev", "ci-1a2b3c4") is never older: those
// builds don't update.
func Newer(tag, current string) bool {
	t, ok1 := parseVersion(tag)
	c, ok2 := parseVersion(current)
	if !ok1 || !ok2 {
		return false
	}
	return compareVersions(t, c) > 0
}

type version struct {
	core [3]int
	pre  []string
}

func parseVersion(s string) (version, bool) {
	var v version
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	s, _, _ = strings.Cut(s, "+") // build metadata doesn't order
	core, pre, hasPre := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p == "" {
			return v, false
		}
		v.core[i] = n
	}
	if hasPre {
		if pre == "" {
			return v, false
		}
		v.pre = strings.Split(pre, ".")
		for _, p := range v.pre {
			if p == "" {
				return v, false
			}
		}
	}
	return v, true
}

// compareVersions orders versions the semver way: by the numbers, then a pre-release before its release, then the
// pre-release's parts in turn (numbers by value and before words).
func compareVersions(a, b version) int {
	for i := range a.core {
		if a.core[i] != b.core[i] {
			return cmpInt(a.core[i], b.core[i])
		}
	}
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1
	case len(b.pre) == 0:
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		x, y := a.pre[i], b.pre[i]
		xn, xerr := strconv.Atoi(x)
		yn, yerr := strconv.Atoi(y)
		switch {
		case xerr == nil && yerr == nil:
			if xn != yn {
				return cmpInt(xn, yn)
			}
		case xerr == nil:
			return -1
		case yerr == nil:
			return 1
		default:
			if c := strings.Compare(x, y); c != 0 {
				return c
			}
		}
	}
	return cmpInt(len(a.pre), len(b.pre))
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// ErrCantReplace means the launcher can't put a new copy where it is: a read-only folder, or a macOS app run from
// where Gatekeeper put it (App Translocation). The release page is the way to update then.
var ErrCantReplace = errors.New("the launcher can't replace itself where it is")

// Target is what an update replaces: the program file, or on macOS the whole app around it.
type Target struct {
	Path string // the file or the .app folder
	App  bool   // a macOS .app: the update is a zip of one
}

// TargetFor finds what to replace for the running program at exe (os.Executable).
func TargetFor(exe string) (Target, error) {
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	// <name>.app/Contents/MacOS/<program>
	macos := filepath.Dir(exe)
	contents := filepath.Dir(macos)
	app := filepath.Dir(contents)
	if filepath.Base(macos) == "MacOS" && filepath.Base(contents) == "Contents" && strings.HasSuffix(app, ".app") {
		if strings.Contains(app, "/AppTranslocation/") {
			return Target{}, fmt.Errorf("%w: macOS is running it from a temporary copy; move \"%s\" to "+
				"Applications (or any folder) and open it from there", ErrCantReplace, filepath.Base(app))
		}
		return Target{Path: app, App: true}, nil
	}
	return Target{Path: exe}, nil
}

// Dir is the folder a download goes to before it's moved into place: next to the target, so the move is a rename
// on one disk.
func (t Target) Dir() string { return filepath.Dir(t.Path) }

// Replace puts the checked download at staged in place of t. check, if set, gets the new program (the file, or the
// app's executable) before anything is moved, and an error from it stops the update: it should run it, so a copy
// that won't start (a broken build, or one an antivirus takes away) never replaces a working one. The running
// program carries on from its old copy, which is moved aside (Windows keeps a running .exe locked against deletion,
// not against renaming) and removed by Cleanup next time. staged is gone afterwards either way.
func (t Target) Replace(staged string, check func(program string) error) error {
	defer os.Remove(staged)
	if check == nil {
		check = func(string) error { return nil }
	}
	if t.App {
		return replaceApp(t.Path, staged, check)
	}
	if err := os.Chmod(staged, 0o755); err != nil {
		return err
	}
	if err := check(staged); err != nil {
		return err
	}
	return replaceFile(t.Path, staged)
}

// Writable reports ErrCantReplace when the folder the target is in can't take a new copy.
func (t Target) Writable() error {
	f, err := os.CreateTemp(t.Dir(), ".everlake-update-*")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCantReplace, err)
	}
	f.Close()
	os.Remove(f.Name())
	return nil
}

// Restore puts the old copy back when the new one has gone missing from the target's place (an antivirus took
// it). It reports whether it did.
func (t Target) Restore() bool {
	if _, err := os.Stat(t.Path); err == nil {
		return false
	}
	if _, err := os.Stat(t.OldPath()); err != nil {
		return false
	}
	return os.Rename(t.OldPath(), t.Path) == nil
}

// OldPath is where Replace moves the old program to.
func (t Target) OldPath() string { return t.Path + ".old" }

func replaceFile(path, staged string) error {
	old := path + ".old"
	os.Remove(old) // a leftover from the update before; still locked if that launcher is still running
	if err := os.Rename(path, old); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("%w: %v", ErrCantReplace, err)
		}
		return err
	}
	if err := os.Rename(staged, path); err != nil {
		if rerr := os.Rename(old, path); rerr != nil {
			return fmt.Errorf("%v; and putting the old copy back failed: %v (it's at %s)", err, rerr, old)
		}
		return err
	}
	return nil
}

// Cleanup removes what an earlier update left behind next to the target: the old program, and downloads that
// never finished. Best effort: a file still in use stays for next time.
func (t Target) Cleanup() {
	os.RemoveAll(t.OldPath())
	matches, _ := filepath.Glob(filepath.Join(t.Dir(), ".everlake-update-*"))
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && time.Since(fi.ModTime()) > time.Hour {
			os.RemoveAll(m)
		}
	}
}

// Restart starts the updated launcher with args: the .app through `open` (a new instance, as if double-clicked),
// a program file directly. It doesn't wait for it.
func (t Target) Restart(args []string) error {
	var cmd *exec.Cmd
	if t.App {
		a := []string{"-n", t.Path}
		if len(args) > 0 {
			a = append(append(a, "--args"), args...)
		}
		cmd = exec.Command("/usr/bin/open", a...)
		return cmd.Run()
	}
	cmd = exec.Command(t.Path, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
