package tact_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/cdn"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/pin"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/tact"
)

// TestLivePinnedBuild reads the pinned build's real files from Blizzard's CDN and parses them. It needs the
// network, so it only runs with EVERLAKE_LIVE=1 (about 2 MB). EVERLAKE_LIVE=full also reads the encoding file
// (135 MB) and looks up every install manifest file in it.
func TestLivePinnedBuild(t *testing.T) {
	mode := os.Getenv("EVERLAKE_LIVE")
	if mode == "" {
		t.Skip("set EVERLAKE_LIVE=1 to read the pinned build from Blizzard's CDN")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	p := pin.Default()
	c := cdn.New(p.CDNHosts, p.CDNPath)
	buildKey, cdnKey := p.Keys()

	config := func(k tact.Key) tact.Config {
		data, err := c.Get(ctx, "config", k, "")
		if err != nil {
			t.Fatal(err)
		}
		if tact.KeyOf(data) != k {
			t.Fatalf("config %s doesn't match its name", k)
		}
		return tact.ParseConfig(string(data))
	}
	build, cdnCfg := config(buildKey), config(cdnKey)
	if build.One("build-uid") != p.Product {
		t.Fatalf("build-uid = %q", build.One("build-uid"))
	}
	if len(build.VFSNames()) < 2 {
		t.Errorf("only %d TVFS manifests", len(build.VFSNames()))
	}

	read := func(name string) []byte {
		ck, ek, err := build.KeyPair(name)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := c.Get(ctx, "data", ek, "")
		if err != nil {
			t.Fatal(err)
		}
		data, err := tact.Decode(raw, ek, ck)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return data
	}
	inst, err := tact.ParseInstall(read("install"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tags := range [][]string{{"OSX", "arm64", "enUS", "US"}, {"Windows", "x86_64", "enUS", "US"}} {
		files, err := inst.Select(tags)
		if err != nil || len(files) < 10 {
			t.Errorf("install %v: %d files, %v", tags, len(files), err)
		}
	}
	dl, err := tact.ParseDownload(read("download"))
	if err != nil {
		t.Fatal(err)
	}
	if files, err := dl.Select([]string{"OSX", "arm64", "enUS", "US"}); err != nil || len(files) < 100000 {
		t.Errorf("download: %d files, %v", len(files), err)
	}
	t.Logf("install manifest %d files, download manifest v%d %d files", len(inst.Entries), dl.Version, len(dl.Entries))

	archives, err := cdnCfg.Keys("archives")
	if err != nil || len(archives) == 0 {
		t.Fatalf("archives: %d, %v", len(archives), err)
	}
	fileIndex, err := tact.ParseKey(cdnCfg.One("file-index"))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []tact.Key{archives[0], archives[len(archives)/2], archives[len(archives)-1], fileIndex} {
		data, err := c.Get(ctx, "data", k, ".index")
		if err != nil {
			t.Fatal(err)
		}
		idx, err := tact.ParseIndex(k, data)
		if err != nil {
			t.Fatal(err)
		}
		if len(idx.Entries) == 0 || idx.HasOffsets == (k == fileIndex) {
			t.Errorf("index %s: %d entries, offsets %v", k, len(idx.Entries), idx.HasOffsets)
		}
	}

	if mode != "full" {
		return
	}
	enc, err := tact.ParseEncoding(read("encoding"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range inst.Entries {
		if _, ok := enc.Lookup(e.CKey); !ok {
			t.Errorf("install file %s is not in the encoding file", e.Name)
		}
	}
	t.Logf("encoding file: %d content keys", enc.Count())
}
