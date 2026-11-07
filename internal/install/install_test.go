package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/cdn"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/pin"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/tact"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/tacttest"
)

// fakeBuild is a whole tiny build on a fake CDN: configs, encoding (loose), one archive holding everything else.
type fakeBuild struct {
	cdn       *tacttest.CDN
	pin       pin.Pin
	macBinary []byte
	exe       []byte
	readme    []byte
	data      map[string]tact.Key // name -> encoded key of the download manifest's files
	archive   tact.Key
	archBytes []byte
}

func newFakeBuild(t *testing.T) *fakeBuild {
	t.Helper()
	f := &fakeBuild{cdn: tacttest.NewCDN(), data: map[string]tact.Key{}}
	t.Cleanup(f.cdn.Close)
	f.macBinary = append([]byte{0xca, 0xfe, 0xba, 0xbe}, bytes.Repeat([]byte("mac game "), 500)...)
	f.exe = append([]byte("MZ"), bytes.Repeat([]byte("windows game "), 400)...)
	f.readme = []byte("read me")

	var archive bytes.Buffer
	var idx []tacttest.IndexEntry
	var rows []tacttest.EncodingRow
	// store puts content into the archive and the encoding file, and returns its keys.
	store := func(content []byte) (ckey, ekey tact.Key) {
		blob, ekey := tacttest.Plain(content)
		idx = append(idx, tacttest.IndexEntry{EKey: ekey, Size: uint32(len(blob)), Offset: uint32(archive.Len())})
		archive.Write(blob)
		archive.Write([]byte("gap!")) // archives aren't packed back to back on the real CDN either
		ckey = tact.KeyOf(content)
		rows = append(rows, tacttest.EncodingRow{CKey: ckey, EKeys: []tact.Key{ekey}, Size: uint64(len(content))})
		return ckey, ekey
	}

	tags := []tacttest.Tag{
		{Name: "OSX", Type: tact.TagPlatform, Entries: []int{0, 2}},
		{Name: "Windows", Type: tact.TagPlatform, Entries: []int{1, 2}},
		{Name: "enUS", Type: tact.TagLocale, Entries: []int{0, 1, 2}},
	}
	macC, _ := store(f.macBinary)
	exeC, _ := store(f.exe)
	readC, _ := store(f.readme)
	install := tacttest.Install(tags, []tacttest.InstallFile{
		{Name: `World of Warcraft.app\Contents\MacOS\World of Warcraft`, CKey: macC, Size: uint32(len(f.macBinary))},
		{Name: `Wow.exe`, CKey: exeC, Size: uint32(len(f.exe))},
		{Name: `Utils\readme.txt`, CKey: readC, Size: uint32(len(f.readme))},
	})
	var dl []tacttest.DownloadFile
	for i, name := range []string{"mac-art", "shared-art", "windows-art"} {
		_, e := store(bytes.Repeat([]byte(name), 300+i))
		f.data[name] = e
		dl = append(dl, tacttest.DownloadFile{EKey: e, Size: 1, Priority: int8(i)})
	}
	download := tacttest.Download(1, false, 0, 0, []tacttest.Tag{
		{Name: "OSX", Type: tact.TagPlatform, Entries: []int{0, 1}},
		{Name: "Windows", Type: tact.TagPlatform, Entries: []int{1, 2}},
		{Name: "enUS", Type: tact.TagLocale, Entries: []int{0, 1, 2}},
	}, dl)
	instC, instE := store(install)
	dlC, dlE := store(download)
	rootC, _ := store([]byte("TSFM a root file"))
	vfsC, vfsE := store([]byte("TVFS a manifest"))

	encContent := tacttest.Encoding(rows)
	encBlob, encE := tacttest.Plain(encContent)
	f.cdn.Put("data", encE, "", encBlob) // the encoding file is loose on the CDN

	f.archBytes = archive.Bytes()
	idxBytes, name := tacttest.Index(idx, true)
	f.archive = name
	f.cdn.Put("data", name, ".index", idxBytes)
	f.cdn.Put("data", name, "", f.archBytes)

	cdnCfg := []byte("# CDN Configuration\narchives = " + name.String() + "\n")
	cdnKey := tact.KeyOf(cdnCfg)
	f.cdn.Put("config", cdnKey, "", cdnCfg)
	build := []byte(fmt.Sprintf("# Build Configuration\nroot = %s\ninstall = %s %s\ndownload = %s %s\n"+
		"encoding = %s %s\nvfs-root = %s %s\nvfs-1 = %s %s\nbuild-name = TEST-1\nbuild-uid = wowf\n",
		rootC, instC, instE, dlC, dlE, tact.KeyOf(encContent), encE, vfsC, vfsE, vfsC, vfsE))
	buildKey := tact.KeyOf(build)
	f.cdn.Put("config", buildKey, "", build)

	f.pin = pin.Pin{Product: "wowf", Region: "us", Version: "1.60.1.1", BuildID: 1, BuildConfig: buildKey.String(),
		CDNConfig: cdnKey.String(), CDNPath: "tpr/test", GameFolder: "_forever_"}
	if err := f.pin.Check(); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fakeBuild) run(t *testing.T, dir string, change func(*Options)) (*Report, error) {
	t.Helper()
	opts := Options{Dir: dir, Pin: f.pin, CDN: cdn.New([]string{f.cdn.Host()}, f.pin.CDNPath),
		Tags: []string{"OSX", "enUS"}, Jobs: 3, Log: t.Logf}
	opts.CDN.Tries = 1
	if change != nil {
		change(&opts)
	}
	return Run(context.Background(), opts)
}

func (f *fakeBuild) requests() int {
	n := len(f.cdn.Requests)
	f.cdn.Requests = nil
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
	if got, _ := os.ReadFile(mac); !bytes.Equal(got, f.macBinary) {
		t.Error("Mac binary has the wrong bytes")
	}
	if st, err := os.Stat(mac); err != nil || st.Mode().Perm() != 0o755 {
		t.Errorf("Mac binary mode: %v %v", st.Mode(), err)
	}
	if st, err := os.Stat(filepath.Join(game, "Utils", "readme.txt")); err != nil || st.Mode().Perm() != 0o644 {
		t.Errorf("readme: %v", err)
	}
	if _, err := os.Stat(filepath.Join(game, "Wow.exe")); err == nil {
		t.Error("the Windows exe was installed for OSX")
	}
	if _, err := os.Stat(filepath.Join(dir, ".everlake", "staged", f.data["windows-art"].String()[:2],
		f.data["windows-art"].String())); err == nil {
		t.Error("Windows-only data was staged for OSX")
	}
	if _, err := os.Stat(filepath.Join(dir, "Data", "indices", f.archive.String()+".index")); err != nil {
		t.Error("archive index not kept in Data/indices")
	}
	if s, err := ReadState(dir); err != nil || s.Pin.BuildConfig != f.pin.BuildConfig || s.DataLimit != 0 || s.MaxPriority != nil {
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

	// Damage one loose file, delete another, damage a staged data file.
	os.WriteFile(mac, append([]byte{0}, f.macBinary[1:]...), 0o755)
	os.Remove(filepath.Join(game, "Utils", "readme.txt"))
	staged := filepath.Join(dir, ".everlake", "staged", f.data["mac-art"].String()[:2], f.data["mac-art"].String())
	b, _ := os.ReadFile(staged)
	b[len(b)-1] ^= 1
	os.WriteFile(staged, b, 0o644)

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
	bad := append([]byte{}, f.archBytes...)
	bad[len(bad)-10] ^= 1 // inside the last file in the archive
	f.cdn.Put("data", f.archive, "", bad)
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
	// Damage a config and an index, and delete the staged encoding file: verify must report all three, and leave
	// them as they are.
	cfg := filepath.Join(dir, "Data", "config", f.pin.BuildConfig[:2], f.pin.BuildConfig[2:4], f.pin.BuildConfig)
	b, _ := os.ReadFile(cfg)
	os.WriteFile(cfg, append(b, '\n'), 0o644)
	index := filepath.Join(dir, "Data", "indices", f.archive.String()+".index")
	ib, _ := os.ReadFile(index)
	ib[0] ^= 1
	os.WriteFile(index, ib, 0o644)
	var enc string
	filepath.Walk(filepath.Join(dir, ".everlake", "staged"), func(p string, info os.FileInfo, err error) error {
		if data, _ := os.ReadFile(p); info.Mode().IsRegular() && len(data) > 0 {
			if d, err := tact.DecodeBLTE(data); err == nil && len(d) > 2 && string(d[:2]) == "EN" {
				enc = p
			}
		}
		return nil
	})
	if enc == "" {
		t.Fatal("no staged encoding file")
	}
	os.Remove(enc)

	rep, err := f.run(t, dir, func(o *Options) { o.Verify = true })
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Bad) != 2 || len(rep.Missing) != 1 || rep.Missing[0] != enc {
		t.Fatalf("verify: bad %v, missing %v", rep.Bad, rep.Missing)
	}
	if got, _ := os.ReadFile(cfg); bytes.Equal(got, b) {
		t.Error("verify repaired the config")
	}
	if _, err := os.Stat(enc); err == nil {
		t.Error("verify wrote the encoding file back")
	}
	if rep, err := f.run(t, dir, nil); err != nil || rep.Failed() {
		t.Fatalf("repair: %+v %v", rep, err)
	}
	if rep, err := f.run(t, dir, func(o *Options) { o.Verify = true }); err != nil || rep.Failed() {
		t.Fatalf("verify after repair: %+v %v", rep, err)
	}
}

func TestExecutableBit(t *testing.T) {
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
	good := f.cdn.Clone()
	defer good.Close()
	bad := append([]byte{}, f.archBytes...)
	bad[len(bad)-10] ^= 1
	f.cdn.Put("data", f.archive, "", bad) // the first host serves a damaged archive
	rep, err := f.run(t, t.TempDir(), func(o *Options) {
		o.CDN = cdn.New([]string{f.cdn.Host(), good.Host()}, f.pin.CDNPath)
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
