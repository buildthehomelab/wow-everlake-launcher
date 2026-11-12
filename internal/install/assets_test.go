package install

import (
	"context"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/cdn"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/tacttest"
	"path/filepath"
	"testing"
)

func TestSharedAssetsAcrossIndependentInstalls(t *testing.T) {
	b := tacttest.NewBuild(t)
	root := t.TempDir()
	opts := Options{Dir: filepath.Join(root, "first"), AssetStore: filepath.Join(root, "assets"), Pin: b.Pin, CDN: cdn.New([]string{b.CDN.Host()}, b.Pin.CDNPath), Tags: []string{"OSX", "enUS"}, Jobs: 2}
	first, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.Dir = filepath.Join(root, "second")
	second, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.DataFetched == 0 || second.DataFetched != 0 {
		t.Fatalf("first fetched %d, second fetched %d", first.DataFetched, second.DataFetched)
	}
	if second.BytesDownloaded >= first.BytesDownloaded {
		t.Fatalf("no network reuse: first %d, second %d", first.BytesDownloaded, second.BytesDownloaded)
	}
	opts.Verify = true
	report, err := Run(context.Background(), opts)
	if err != nil || report.Failed() {
		t.Fatalf("verify %v %v", report, err)
	}
}
