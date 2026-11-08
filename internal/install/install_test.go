package install

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/casc"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/cdn"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/tact"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/tacttest"
)

// fakeBuild is the tiny test build (tacttest.Build) with the launcher's helpers on it.
type fakeBuild struct{ *tacttest.Build }

func newFakeBuild(t *testing.T) *fakeBuild { return &fakeBuild{tacttest.NewBuild(t)} }

func (f *fakeBuild) run(t *testing.T, dir string, change func(*Options)) (*Report, error) {
	t.Helper()
	opts := Options{Dir: dir, Pin: f.Pin, CDN: cdn.New([]string{f.CDN.Host()}, f.Pin.CDNPath),
		Tags: []string{"OSX", "enUS"}, Jobs: 3, Log: t.Logf}
	opts.CDN.Tries = 1
	if change != nil {
		change(&opts)
	}
	return Run(context.Background(), opts)
}

// damageStored flips the last byte of a stored file in the game's local storage.
func damageStored(t *testing.T, dir string, e tact.Key) {
	t.Helper()
	st, err := casc.OpenReadOnly(filepath.Join(dir, "Data", "data"))
	if err != nil {
		t.Fatal(err)
	}
	l, ok := st.Lookup(e)
	st.Close()
	if !ok {
		t.Fatalf("%s not in local storage", e)
	}
	f, err := os.OpenFile(filepath.Join(dir, "Data", "data", fmt.Sprintf("data.%03d", l.Archive)), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b := make([]byte, 1)
	at := l.Offset + int64(l.Size) - 1
	f.ReadAt(b, at)
	b[0] ^= 1
	f.WriteAt(b, at)
}

func stored(t *testing.T, dir string, e tact.Key) bool {
	t.Helper()
	st, err := casc.OpenReadOnly(filepath.Join(dir, "Data", "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	data, err := st.Read(e)
	return err == nil && tact.VerifyBLTE(data, e) == nil
}

func (f *fakeBuild) requests() int {
	n := len(f.CDN.Requests)
	f.CDN.Requests = nil
	return n
}

func TestInstallVerifyRepair(t *testing.T) {
	f := newFakeBuild(t)
	dir := t.TempDir()

	rep, err := f.run(t, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Failed() || rep.LooseFetched != 2 || rep.DataFiles != 2+5 || rep.DataKept+rep.DataFetched != 7 {
		t.Fatalf("first install: %+v", rep)
	}
	game := filepath.Join(dir, "_forever_")
	mac := filepath.Join(game, "World of Warcraft.app", "Contents", "MacOS", "World of Warcraft")
	if got, _ := os.ReadFile(mac); !bytes.Equal(got, f.MacBinary) {
		t.Error("Mac binary has the wrong bytes")
	}
	if runtime.GOOS != "windows" { // Windows has no file modes beyond read-only
		if st, err := os.Stat(mac); err != nil || st.Mode().Perm() != 0o755 {
			t.Errorf("Mac binary mode: %v %v", st.Mode(), err)
		}
		if st, err := os.Stat(filepath.Join(game, "Utils", "readme.txt")); err != nil || st.Mode().Perm() != 0o644 {
			t.Errorf("readme: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(game, "Wow.exe")); err == nil {
		t.Error("the Windows exe was installed for OSX")
	}
	if stored(t, dir, f.Data["windows-art"]) {
		t.Error("Windows-only data was stored for OSX")
	}
	for _, e := range []tact.Key{f.Data["mac-art"], f.Data["shared-art"], f.EncE} {
		if !stored(t, dir, e) {
			t.Errorf("%s not in local storage", e)
		}
	}
	if fl, _ := os.ReadFile(filepath.Join(game, ".flavor.info")); string(fl) != "Product Flavor!STRING:0\nwowf\n" {
		t.Errorf(".flavor.info = %q", fl)
	}
	bi, _ := os.ReadFile(filepath.Join(dir, ".build.info"))
	wantRow := fmt.Sprintf("us|1|%s|%s|||tpr/test|%s|http://%s/?maxhosts=4|OSX enUS speech?:OSX enUS text?|||1.60.1.1||wowf\n",
		f.Pin.BuildConfig, f.Pin.CDNConfig, f.CDN.Host(), f.CDN.Host())
	if !strings.HasPrefix(string(bi), "Branch!STRING:0|Active!DEC:1|") || !strings.HasSuffix(string(bi), wantRow) {
		t.Errorf(".build.info:\n%s\nwant the row\n%s", bi, wantRow)
	}
	if _, err := os.Stat(filepath.Join(dir, "Data", "indices", f.Archive.String()+".index")); err != nil {
		t.Error("archive index not kept in Data/indices")
	}
	if s, err := ReadState(dir); err != nil || s.Pin.BuildConfig != f.Pin.BuildConfig || s.DataLimit != 0 || s.MaxPriority != nil {
		t.Errorf("state: %+v %v", s, err)
	}
	f.requests()

	// Verify needs nothing from the CDN once everything is on disk.
	rep, err = f.run(t, dir, func(o *Options) { o.Verify = true })
	if err != nil || rep.Failed() {
		t.Fatalf("verify after install: %+v %v", rep, err)
	}
	if n := f.requests(); n != 0 {
		t.Errorf("verify made %d CDN requests", n)
	}

	// Damage one loose file, delete another, damage a stored data file.
	os.WriteFile(mac, append([]byte{0}, f.MacBinary[1:]...), 0o755)
	os.Remove(filepath.Join(game, "Utils", "readme.txt"))
	damageStored(t, dir, f.Data["mac-art"])

	rep, err = f.run(t, dir, func(o *Options) { o.Verify = true })
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Bad) != 2 || len(rep.Missing) != 1 || !strings.HasSuffix(rep.Missing[0], "readme.txt") {
		t.Fatalf("verify after damage: bad %v, missing %v", rep.Bad, rep.Missing)
	}

	// Install again: only the three broken files come back.
	f.requests()
	rep, err = f.run(t, dir, nil)
	if err != nil || rep.Failed() || rep.LooseFetched != 2 || rep.DataFetched != 1 || rep.DataKept != 6 {
		t.Fatalf("repair: %+v %v", rep, err)
	}
	if n := f.requests(); n != 3 {
		t.Errorf("repair made %d requests, want 3", n)
	}
	rep, err = f.run(t, dir, func(o *Options) { o.Verify = true })
	if err != nil || rep.Failed() {
		t.Fatalf("verify after repair: %+v %v", rep, err)
	}
}

func TestInstallRefusesDamagedCDN(t *testing.T) {
	f := newFakeBuild(t)
	bad := append([]byte{}, f.ArchBytes...)
	bad[len(bad)-10] ^= 1 // inside the last file in the archive
	f.CDN.Put("data", f.Archive, "", bad)
	_, err := f.run(t, t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "checksum") && !strings.Contains(err.Error(), "key") {
		t.Fatalf("damaged archive: err = %v", err)
	}
}

func TestInstallLooseOnlyAndLimits(t *testing.T) {
	f := newFakeBuild(t)
	dir := t.TempDir()
	rep, err := f.run(t, dir, func(o *Options) { o.NoData = true; o.Tags = []string{"Windows", "enUS"} })
	if err != nil || rep.LooseFetched != 2 || rep.DataFiles != 0 {
		t.Fatalf("loose only: %+v %v", rep, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "_forever_", "Wow.exe")); err != nil {
		t.Error("Wow.exe missing")
	}

	zero := 0
	rep, err = f.run(t, t.TempDir(), func(o *Options) { o.MaxPriority = &zero })
	if err != nil || rep.DataFiles != 5+1 {
		t.Fatalf("max priority 0: %+v %v", rep, err)
	}

	// The build's own 5 files never count against the limit; of the 2 content files, the first fits.
	rep, err = f.run(t, t.TempDir(), func(o *Options) { o.DataLimit = 1 })
	if err != nil || rep.DataKept+rep.DataFetched != 5+1 || rep.DataSkippedByLimit != 1 {
		t.Fatalf("data limit: %+v %v", rep, err)
	}

	if _, err := f.run(t, t.TempDir(), func(o *Options) { o.Tags = []string{"Linux"} }); err == nil {
		t.Error("unknown tag accepted")
	}
}

func TestSafePath(t *testing.T) {
	for name, want := range map[string]string{
		`World of Warcraft.app\Contents\Info.plist`: filepath.Join("World of Warcraft.app", "Contents", "Info.plist"),
		`Wow.exe`:              "Wow.exe",
		`Utils/locales/de.pak`: filepath.Join("Utils", "locales", "de.pak"),
	} {
		if got, err := safePath(name); err != nil || got != want {
			t.Errorf("safePath(%q) = %q, %v", name, got, err)
		}
	}
	for _, name := range []string{``, `..\Wow.exe`, `/etc/passwd`, `\Windows\x.dll`, `C:\x.dll`, `a\\b`, `a\.\b`,
		`CON`, `Utils\aux.txt`, `lpt1.dll`, `a?b`, `a|b`, `name.`, `dir \x`, "tab\there"} {
		if got, err := safePath(name); err == nil {
			t.Errorf("safePath(%q) = %q, want an error", name, got)
		}
	}
}

func TestVerifyWritesNothing(t *testing.T) {
	f := newFakeBuild(t)

	empty := filepath.Join(t.TempDir(), "typo")
	if _, err := f.run(t, empty, func(o *Options) { o.Verify = true }); !errors.Is(err, ErrNoInstall) {
		t.Fatalf("verify of an empty folder: %v", err)
	}
	if _, err := os.Stat(empty); err == nil {
		t.Error("verify created the folder")
	}

	dir := t.TempDir()
	if _, err := f.run(t, dir, nil); err != nil {
		t.Fatal(err)
	}
	// Damage a config, an index and the stored encoding file, and delete .build.info: verify must report all four,
	// and leave them as they are.
	cfg := filepath.Join(dir, "Data", "config", f.Pin.BuildConfig[:2], f.Pin.BuildConfig[2:4], f.Pin.BuildConfig)
	b, _ := os.ReadFile(cfg)
	os.WriteFile(cfg, append(b, '\n'), 0o644)
	index := filepath.Join(dir, "Data", "indices", f.Archive.String()+".index")
	ib, _ := os.ReadFile(index)
	ib[0] ^= 1
	os.WriteFile(index, ib, 0o644)
	damageStored(t, dir, f.EncE)
	bi := filepath.Join(dir, ".build.info")
	os.Remove(bi)
	fl := filepath.Join(dir, "_forever_", ".flavor.info")
	os.WriteFile(fl, []byte("Product Flavor!STRING:0\nwow\n"), 0o644)

	rep, err := f.run(t, dir, func(o *Options) { o.Verify = true })
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Bad) != 4 || len(rep.Missing) != 1 || rep.Missing[0] != bi || !strings.Contains(strings.Join(rep.Bad, " "), fl) ||
		!strings.Contains(strings.Join(rep.Bad, " "), f.EncE.String()) {
		t.Fatalf("verify: bad %v, missing %v", rep.Bad, rep.Missing)
	}
	if got, _ := os.ReadFile(cfg); bytes.Equal(got, b) {
		t.Error("verify repaired the config")
	}
	if stored(t, dir, f.EncE) {
		t.Error("verify wrote the encoding file back")
	}
	if _, err := os.Stat(bi); err == nil {
		t.Error("verify wrote .build.info")
	}
	if rep, err := f.run(t, dir, nil); err != nil || rep.Failed() {
		t.Fatalf("repair: %+v %v", rep, err)
	}
	if rep, err := f.run(t, dir, func(o *Options) { o.Verify = true }); err != nil || rep.Failed() {
		t.Fatalf("verify after repair: %+v %v", rep, err)
	}
}

func TestExecutableBit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no executable bit")
	}
	f := newFakeBuild(t)
	dir := t.TempDir()
	if _, err := f.run(t, dir, nil); err != nil {
		t.Fatal(err)
	}
	mac := filepath.Join(dir, "_forever_", "World of Warcraft.app", "Contents", "MacOS", "World of Warcraft")
	os.Chmod(mac, 0o644)
	rep, err := f.run(t, dir, func(o *Options) { o.Verify = true })
	if err != nil || len(rep.Bad) != 1 || !strings.HasSuffix(rep.Bad[0], "(not executable)") {
		t.Fatalf("verify: %+v %v", rep, err)
	}
	if _, err := f.run(t, dir, nil); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(mac); st.Mode().Perm() != 0o755 {
		t.Errorf("mode after repair: %v", st.Mode())
	}
}

func TestDamagedHostIsSkipped(t *testing.T) {
	f := newFakeBuild(t)
	good := f.CDN.Clone()
	defer good.Close()
	bad := append([]byte{}, f.ArchBytes...)
	bad[len(bad)-10] ^= 1
	f.CDN.Put("data", f.Archive, "", bad) // the first host serves a damaged archive
	rep, err := f.run(t, t.TempDir(), func(o *Options) {
		o.CDN = cdn.New([]string{f.CDN.Host(), good.Host()}, f.Pin.CDNPath)
		o.CDN.Tries = 1
	})
	if err != nil || rep.Failed() {
		t.Fatalf("install with one damaged host: %+v %v", rep, err)
	}
}

func TestVerifyPartialInstall(t *testing.T) {
	f := newFakeBuild(t)
	dir := t.TempDir()
	rep, err := f.run(t, dir, func(o *Options) { o.DataLimit = 1 })
	if err != nil || rep.DataSkippedByLimit != 1 {
		t.Fatalf("trial install: %+v %v", rep, err)
	}
	s, err := ReadState(dir)
	if err != nil || s.DataLimit != 1 {
		t.Fatalf("state: %+v %v", s, err)
	}
	// Verify the way the CLI does for a trial install: missing data isn't counted, damaged data is.
	rep, err = f.run(t, dir, func(o *Options) { o.Verify = true; o.PartialData = true })
	if err != nil || rep.Failed() {
		t.Fatalf("verify trial install: %+v %v", rep, err)
	}
	rep, err = f.run(t, dir, func(o *Options) { o.Verify = true })
	if err != nil || len(rep.Missing) != 1 {
		t.Fatalf("full verify of a trial install should miss 1 file: %+v %v", rep, err)
	}
}

func TestStagedFilesMoveIntoStorage(t *testing.T) {
	// A folder an earlier launcher filled keeps its data files in .everlake/staged. Installing again moves them into
	// local storage without fetching them, and removes the staged folder.
	f := newFakeBuild(t)
	dir := t.TempDir()
	if _, err := f.run(t, dir, nil); err != nil {
		t.Fatal(err)
	}
	st, err := casc.OpenReadOnly(filepath.Join(dir, "Data", "data"))
	if err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(dir, ".everlake", "staged")
	keys := append([]tact.Key{f.EncE}, f.Data["mac-art"], f.Data["shared-art"])
	for _, e := range keys {
		data, err := st.Read(e)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(staged, e.String()[:2], e.String())
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, data, 0o644)
	}
	st.Close()
	// A damaged staged file is left out and fetched again; a leftover temporary file is ignored.
	bad := filepath.Join(staged, keys[1].String()[:2], keys[1].String())
	b, _ := os.ReadFile(bad)
	b[len(b)-1] ^= 1
	os.WriteFile(bad, b, 0o644)
	os.WriteFile(filepath.Join(staged, ".part-123"), []byte("half"), 0o644)
	os.RemoveAll(filepath.Join(dir, "Data", "data"))
	os.Remove(filepath.Join(dir, ".everlake", "archives.json"))

	f.requests()
	rep, err := f.run(t, dir, nil)
	if err != nil || rep.Failed() {
		t.Fatalf("install over staged files: %+v %v", rep, err)
	}
	// The encoding file and shared-art come from staged. The install and download manifests are fetched and stored
	// before the data stage (so they count as kept there); root, the TVFS manifest and the damaged mac-art are
	// fetched in it.
	if rep.DataFetched != 3 || rep.DataKept != 4 {
		t.Errorf("fetched %d, kept %d", rep.DataFetched, rep.DataKept)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Errorf("staged folder kept: %v", err)
	}
	if rep, err := f.run(t, dir, func(o *Options) { o.Verify = true }); err != nil || rep.Failed() {
		t.Fatalf("verify: %+v %v", rep, err)
	}
}

func TestBuildInfoTags(t *testing.T) {
	known := []tact.Tag{{Name: "Windows", Type: tact.TagPlatform}, {Name: "x86_64", Type: tact.TagArchitecture},
		{Name: "enUS", Type: tact.TagLocale}, {Name: "US", Type: tact.TagRegion}, {Name: "speech", Type: tact.TagCategory}}
	got := buildInfoTags([]string{"Windows", "x86_64", "enUS", "US"}, known)
	if want := "Windows x86_64 US? enUS speech?:Windows x86_64 US? enUS text?"; got != want {
		t.Errorf("tags = %q, want %q", got, want)
	}
}

func TestFolderLock(t *testing.T) {
	f := newFakeBuild(t)
	dir := t.TempDir()
	if _, err := f.run(t, dir, nil); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockFolder(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.run(t, dir, nil); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Errorf("install while locked: %v", err)
	}
	if _, err := f.run(t, dir, func(o *Options) { o.Verify = true }); err == nil {
		t.Error("verify while locked went ahead")
	}
	unlock()
	if rep, err := f.run(t, dir, func(o *Options) { o.Verify = true }); err != nil || rep.Failed() {
		t.Errorf("verify after unlock: %+v %v", rep, err)
	}
}

func TestBuildInfoNamesGameHosts(t *testing.T) {
	f := newFakeBuild(t)
	dir := t.TempDir()
	if _, err := f.run(t, dir, func(o *Options) { o.GameHosts = []string{"cdn.example"} }); err != nil {
		t.Fatal(err)
	}
	bi, _ := os.ReadFile(filepath.Join(dir, ".build.info"))
	if !strings.Contains(string(bi), "|cdn.example|http://cdn.example/?maxhosts=4|") || strings.Contains(string(bi), f.CDN.Host()) {
		t.Errorf(".build.info names the install's mirror:\n%s", bi)
	}
}

func TestQuickVerify(t *testing.T) {
	f := newFakeBuild(t)
	dir := t.TempDir()
	if _, err := f.run(t, dir, nil); err != nil {
		t.Fatal(err)
	}
	rep, err := QuickVerify(dir)
	if err != nil || rep.Failed() {
		t.Fatalf("quick check after install: %+v %v", rep, err)
	}
	// Changes to the game's own storage and the launcher's records must not trip the quick check.
	os.WriteFile(filepath.Join(dir, "Data", "data", "data.000"), []byte("the game grew this"), 0o644)
	os.WriteFile(filepath.Join(dir, ".everlake", "scratch"), []byte("x"), 0o644)
	if rep, err := QuickVerify(dir); err != nil || rep.Failed() {
		t.Fatalf("quick check minds Data/data: %+v %v", rep, err)
	}

	// A loose file edited to the same length (new mtime), and a deleted config.
	mac := filepath.Join(dir, "_forever_", "World of Warcraft.app", "Contents", "MacOS", "World of Warcraft")
	b, _ := os.ReadFile(mac)
	b[0] ^= 1
	later := time.Now().Add(time.Second)
	os.WriteFile(mac, b, 0o644)
	os.Chtimes(mac, later, later)
	cfgDir := filepath.Join(dir, "Data", "config")
	var gone string
	filepath.WalkDir(cfgDir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && gone == "" {
			gone = p
		}
		return nil
	})
	os.Remove(gone)

	rep, err = QuickVerify(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Bad) != 1 || rep.Bad[0] != mac || len(rep.Missing) != 1 || rep.Missing[0] != gone {
		t.Fatalf("quick check after damage: bad %v, missing %v", rep.Bad, rep.Missing)
	}

	// An install with no fingerprints (an older launcher) can't be quick-checked.
	st, _ := ReadState(dir)
	st.Files = nil
	data, _ := json.MarshalIndent(st, "", "  ")
	os.WriteFile(filepath.Join(dir, ".everlake", "install.json"), data, 0o644)
	if _, err := QuickVerify(dir); !errors.Is(err, ErrNoFingerprints) {
		t.Errorf("no-fingerprints install: %v", err)
	}

	if _, err := QuickVerify(filepath.Join(t.TempDir(), "empty")); !errors.Is(err, ErrNoInstall) {
		t.Errorf("quick check of an empty folder: %v", err)
	}
}

// capture collects the launcher's log lines for a test.
type capture struct {
	mu    sync.Mutex
	lines []string
}

func (c *capture) log(f string, a ...any) {
	c.mu.Lock()
	c.lines = append(c.lines, fmt.Sprintf(f, a...))
	c.mu.Unlock()
}

func (c *capture) has(sub string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, l := range c.lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// markOldBuild edits install.json so the folder looks like it came from a different build, so the next install
// treats it as an update.
func markOldBuild(t *testing.T, dir string, extraFiles ...FileFP) {
	t.Helper()
	st, err := ReadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Pin.BuildConfig = "00000000000000000000000000000000"
	st.Pin.Version = "1.60.1.00000"
	st.Files = append(st.Files, extraFiles...)
	data, _ := json.MarshalIndent(st, "", "  ")
	os.WriteFile(filepath.Join(dir, ".everlake", "install.json"), data, 0o644)
}

func TestUpdatePrunesOldClientFiles(t *testing.T) {
	f := newFakeBuild(t)
	dir := t.TempDir()
	if _, err := f.run(t, dir, nil); err != nil {
		t.Fatal(err)
	}
	// A loose file the old build had and the new one doesn't.
	old := filepath.Join(dir, "_forever_", "old-tool.exe")
	os.WriteFile(old, []byte("stale"), 0o644)
	markOldBuild(t, dir, FileFP{Path: "_forever_/old-tool.exe", Size: 5, ModNS: 1})

	var cap capture
	rep, err := f.run(t, dir, func(o *Options) { o.Log = cap.log })
	if err != nil || rep.Failed() {
		t.Fatalf("update: %+v %v", rep, err)
	}
	if !cap.has("updating Forever") {
		t.Error("no update message")
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("old loose file kept: %v", err)
	}
	if rep.PrunedFiles != 1 {
		t.Errorf("pruned %d files, want 1", rep.PrunedFiles)
	}
	// The real client files are still there.
	if _, err := os.Stat(filepath.Join(dir, "_forever_", "Utils", "readme.txt")); err != nil {
		t.Error("a real loose file was removed")
	}
	if r, err := f.run(t, dir, func(o *Options) { o.Verify = true }); err != nil || r.Failed() {
		t.Fatalf("verify after update: %+v %v", r, err)
	}
}

// addOrphan stores a file that no manifest lists, in its own archive, and returns its key.
func addOrphan(t *testing.T, dir string) tact.Key {
	t.Helper()
	st, err := casc.Open(filepath.Join(dir, "Data", "data"), filepath.Join(dir, ".everlake", "archives.json"))
	if err != nil {
		t.Fatal(err)
	}
	blob, e := tacttest.Plain([]byte("an orphan from an older build that nothing lists any more"))
	if err := st.Add(e, blob); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestUpdatePrunesOldData(t *testing.T) {
	f := newFakeBuild(t)
	dir := t.TempDir()
	if _, err := f.run(t, dir, nil); err != nil { // full data install
		t.Fatal(err)
	}
	orphan := addOrphan(t, dir)
	if !stored(t, dir, orphan) {
		t.Fatal("orphan not stored")
	}
	markOldBuild(t, dir)

	rep, err := f.run(t, dir, nil)
	if err != nil || rep.Failed() {
		t.Fatalf("update: %+v %v", rep, err)
	}
	if rep.PrunedData < 1 || rep.Reclaimed == 0 {
		t.Errorf("pruned %d data entries, reclaimed %d bytes", rep.PrunedData, rep.Reclaimed)
	}
	if stored(t, dir, orphan) {
		t.Error("orphan data kept after update")
	}
	// Real data survives.
	if !stored(t, dir, f.Data["mac-art"]) {
		t.Error("real data pruned")
	}
	if r, err := f.run(t, dir, func(o *Options) { o.Verify = true }); err != nil || r.Failed() {
		t.Fatalf("verify after update: %+v %v", r, err)
	}
}

func TestUpdateLeavesDataOnLimitedInstall(t *testing.T) {
	f := newFakeBuild(t)
	dir := t.TempDir()
	if _, err := f.run(t, dir, nil); err != nil {
		t.Fatal(err)
	}
	orphan := addOrphan(t, dir)
	markOldBuild(t, dir)
	// A limited install must not prune data (a fuller install may have fetched more than this run wants).
	rep, err := f.run(t, dir, func(o *Options) { o.MaxPriority = intp(0) })
	if err != nil {
		t.Fatal(err)
	}
	if rep.PrunedData != 0 {
		t.Errorf("limited install pruned %d data entries", rep.PrunedData)
	}
	if !stored(t, dir, orphan) {
		t.Error("limited install removed data it shouldn't")
	}
}

func intp(n int) *int { return &n }

func TestUpdateKeepsPlayerFiles(t *testing.T) {
	f := newFakeBuild(t)
	dir := t.TempDir()
	if _, err := f.run(t, dir, nil); err != nil {
		t.Fatal(err)
	}
	// Files the game and the player create in the game folder once it has run.
	player := []string{
		filepath.Join(dir, "_forever_", "WTF", "Config.wtf"),
		filepath.Join(dir, "_forever_", "Interface", "AddOns", "MyAddon", "MyAddon.lua"),
		filepath.Join(dir, "_forever_", "Logs", "Connection.log"),
		filepath.Join(dir, "_forever_", "Screenshots", "shot.jpg"),
	}
	makePlayerFiles := func() {
		for _, p := range player {
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.WriteFile(p, []byte("the player's own data"), 0o644)
		}
	}
	makePlayerFiles()

	// The recorded fingerprints must never include the player's files.
	noPlayerFilesRecorded := func() {
		st, err := ReadState(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, fp := range st.Files {
			for _, bad := range []string{"WTF/", "AddOns", "Logs/", "Screenshots"} {
				if strings.Contains(fp.Path, bad) {
					t.Errorf("install.json records a player file: %s", fp.Path)
				}
			}
		}
	}
	// Two updates in a row — the reviewer's trigger: the second update is where a walk-based fingerprint would have
	// recorded, then deleted, the player's files.
	for i := 0; i < 2; i++ {
		noPlayerFilesRecorded()
		markOldBuild(t, dir)
		if rep, err := f.run(t, dir, nil); err != nil || rep.Failed() {
			t.Fatalf("update %d: %+v %v", i, rep, err)
		}
		for _, p := range player {
			if _, err := os.Stat(p); err != nil {
				t.Fatalf("update %d removed the player file %s", i, p)
			}
		}
		makePlayerFiles() // the player keeps playing between updates
	}
}
