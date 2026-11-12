package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRunningClientBlocksOtherLauncher(t *testing.T) {
	root := t.TempDir()
	data, _ := json.Marshal(os.Getpid())
	if err := writeFile(filepath.Join(root, ".everlake", "game.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireClient(root); err == nil {
		t.Fatal("accepted running process")
	}
	if err := os.Remove(filepath.Join(root, ".everlake", "game.json")); err != nil {
		t.Fatal(err)
	}
	unlock, err := AcquireClient(root)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := AcquireClient(root); err == nil {
		t.Fatal("accepted concurrent operation")
	}
}
