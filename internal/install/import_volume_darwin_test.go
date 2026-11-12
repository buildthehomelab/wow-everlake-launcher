//go:build darwin

package install

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExfatPublicationNeverReplacesDirectory(t *testing.T) {
	root := os.Getenv("EVERLAKE_TEST_EXFAT")
	if root == "" {
		t.Skip("set an exFAT test directory")
	}
	temp, err := os.MkdirTemp(root, "publication-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(temp)
	from, to := filepath.Join(temp, "from"), filepath.Join(temp, "to")
	if err := os.Mkdir(from, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(to, 0700); err != nil {
		t.Fatal(err)
	}
	if err := publishImport(from, to); err == nil {
		t.Fatal("replaced existing directory")
	}
	if err := os.Remove(to); err != nil {
		t.Fatal(err)
	}
	if err := publishImport(from, to); err != nil {
		t.Fatal(err)
	}
}
