package install

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/pin"
)

// adoptTarget runs under the client lock. It records metadata only, not a second
// copy of game data. The original build is not a rollback installation.
func adoptTarget(dir string, p pin.Pin, tags []string) error {
	if _, err := ReadState(dir); err == nil {
		return checkInstallTarget(dir, p)
	} else if !os.IsNotExist(err) {
		return err
	}
	rows, err := ActiveBuilds(dir)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(dir, ".build.info"))
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(rows) != 1 || rows[0].Product != p.Product || len(lines) != 2 {
		return fmt.Errorf("in-place update requires a single-product %s install; shared Battle.net roots must use a separate destination", p.Product)
	}
	old := p
	old.Version, old.BuildConfig, old.CDNConfig = rows[0].Version, rows[0].Build, rows[0].CDN
	parts := strings.Split(old.Version, ".")
	old.BuildID, err = strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return err
	}
	if err := old.Check(); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "Data", "data")); err != nil {
		return err
	}
	backup := filepath.Join(dir, ".everlake", "adoption-backup")
	files, err := filepath.Glob(filepath.Join(dir, "Data", "data", "*.idx"))
	if err != nil {
		return err
	}
	files = append(files, filepath.Join(dir, ".build.info"))
	for _, file := range files {
		target := filepath.Join(backup, filepath.Base(file))
		if _, err := os.Stat(target); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if err := writeFile(target, data, 0600); err != nil {
			return err
		}
	}
	marker, _ := json.Marshal(old)
	if err := writeFile(filepath.Join(dir, ".everlake", "adopted.json"), marker, 0600); err != nil {
		return err
	}
	state, _ := json.Marshal(State{Pin: old, Tags: tags, LowSpace: true})
	return writeFile(filepath.Join(dir, ".everlake", "install.json"), state, 0600)
}
