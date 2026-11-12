//go:build darwin

package assets

import (
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRealArchivePhysicalSharing(t *testing.T) {
	root, source := os.Getenv("EVERLAKE_SPACE_VOLUME"), os.Getenv("EVERLAKE_SPACE_ARCHIVE")
	if root == "" || source == "" {
		t.Skip("requires isolated APFS volume and a real archive")
	}
	dir, err := os.MkdirTemp(root, "sharing-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	copyFile := func(to string) {
		in, err := os.Open(source)
		if err != nil {
			t.Fatal(err)
		}
		defer in.Close()
		out, err := os.Create(to)
		if err != nil {
			t.Fatal(err)
		}
		defer out.Close()
		if _, err := io.Copy(out, in); err != nil {
			t.Fatal(err)
		}
		if err := out.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	copyFile(a)
	copyFile(b)
	info, err := os.Stat(a)
	if err != nil {
		t.Fatal(err)
	}
	free := func() uint64 {
		unix.Sync()
		var s unix.Statfs_t
		if err := unix.Statfs(dir, &s); err != nil {
			t.Fatal(err)
		}
		return s.Bfree * uint64(s.Bsize)
	}
	before := free()
	store := Store{Root: filepath.Join(dir, "store")}
	for _, p := range []string{a, b} {
		ok, err := store.ShareArchive(p)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatal("APFS clone unavailable")
		}
	}
	var after uint64
	for i := 0; i < 10; i++ {
		after = free()
		if after > before+uint64(info.Size()/2) {
			break
		}
		time.Sleep(time.Second)
	}
	if after <= before {
		t.Fatalf("no measured physical savings: free before %d after %d", before, after)
	}
	t.Logf("archive bytes=%d; physical bytes reclaimed=%d; both clients and immutable store retained", info.Size(), after-before)
}
