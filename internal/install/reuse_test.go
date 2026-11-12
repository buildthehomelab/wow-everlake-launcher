package install

import (
	"context"
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/casc"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/cdn"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/tacttest"
)

func TestReuseOlderWindowsClient(t *testing.T) {
	for _, damage := range []string{"none", "corrupt", "missing"} {
		t.Run(damage, func(t *testing.T) {
			b := tacttest.NewBuild(t)
			root := t.TempDir()
			source := filepath.Join(root, "Old World of Warcraft")
			opts := Options{Dir: source, Pin: b.Pin, CDN: cdn.New([]string{b.CDN.Host()}, b.Pin.CDNPath), Tags: []string{"Windows", "enUS"}}
			first, err := Run(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			// A different active build is allowed: only content hashes determine reuse.
			if err := os.WriteFile(filepath.Join(source, ".build.info"), []byte("Product|Version|Build Key|CDN Key|Active\nwow|old|old|old|1\n"), 0600); err != nil {
				t.Fatal(err)
			}
			st, err := casc.OpenReadOnly(filepath.Join(source, "Data", "data"))
			if err != nil {
				t.Fatal(err)
			}
			key := b.Data["shared-art"]
			loc, ok := st.Lookup(key)
			if !ok {
				t.Fatal("missing fixture")
			}
			st.Close()
			_ = loc
			if damage != "none" {
				archives, err := filepath.Glob(filepath.Join(source, "Data", "data", "data.*"))
				if err != nil || len(archives) == 0 {
					t.Fatal("no archives")
				}
				if damage == "missing" {
					if err := os.Remove(archives[0]); err != nil {
						t.Fatal(err)
					}
				} else {
					raw, err := os.ReadFile(archives[0])
					if err != nil {
						t.Fatal(err)
					}
					for i := range raw {
						raw[i] ^= 0xff
					}
					if err := os.WriteFile(archives[0], raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			before, err := reuseSnapshot(source)
			if err != nil {
				t.Fatal(err)
			}
			if damage == "none" {
				b.CDN.Close()
			} // all matching bytes must work without a CDN
			opts.Dir = filepath.Join(root, "New Client")
			opts.ReuseFrom = source
			second, err := Run(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if damage == "none" && second.BytesDownloaded != 0 {
				t.Fatalf("unnecessary downloads: %d", second.BytesDownloaded)
			}
			if damage == "none" && second.DataFetched != 0 {
				t.Fatalf("downloaded %d reusable data files", second.DataFetched)
			}
			if damage != "none" && second.DataFetched == 0 {
				t.Fatal("damaged source was not replaced")
			}
			if second.LooseFetched != 0 {
				t.Fatalf("redownloaded matching loose files: %d", second.LooseFetched)
			}
			if damage == "none" && second.BytesDownloaded >= first.BytesDownloaded {
				t.Fatal("reuse saved no bandwidth")
			}
			after, err := reuseSnapshot(source)
			if err != nil {
				t.Fatal(err)
			}
			if len(before) != len(after) {
				t.Fatal("source changed")
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("source content changed")
			}
			opts.ReuseFrom = ""
			opts.Verify = true
			report, err := Run(context.Background(), opts)
			if err != nil || report.Failed() {
				t.Fatalf("verify %v %v", report, err)
			}
		})
	}
}

func reuseSnapshot(root string) (map[string][32]byte, error) {
	result := map[string][32]byte{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[path] = sha256.Sum256(b)
		return nil
	})
	return result, err
}
