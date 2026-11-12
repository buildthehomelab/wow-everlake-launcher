// Package addons packages one maintained source for each supported client.
package addons

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/pin"
)

//go:embed source/Everlake.lua
var source []byte

type Manifest struct {
	Product   string
	Version   string
	Interface int
}

func Package(p pin.Pin) (map[string][]byte, error) {
	if err := p.Check(); err != nil {
		return nil, err
	}
	var api int
	switch {
	case p.Product == "wowf" && p.Version == pin.Default().Version:
		api = 16001
	case p.Product == "wow" && p.Version == pin.Retail().Version:
		api = 120105
	default:
		return nil, fmt.Errorf("addon compatibility is unverified for %s %s", p.Product, p.Version)
	}
	manifest, err := json.Marshal(Manifest{p.Product, p.Version, api})
	if err != nil {
		return nil, err
	}
	config := fmt.Sprintf("local _, addon = ...\naddon.client = %q\naddon.version = %q\n", p.Product, p.Version)
	toc := fmt.Sprintf("## Interface: %d\n## Title: Everlake\n## Notes: Everlake client information\n## Version: 1.0.0\n## SavedVariables: EverlakeDB\nProfile.lua\nEverlake.lua\n", api)
	return map[string][]byte{"Everlake.lua": source, "Profile.lua": []byte(config), "Everlake.toc": []byte(toc), ".everlake-package.json": manifest}, nil
}

// Install expects the caller to hold the client install lock. Previous packages
// remain in .everlake/addon-backups. SavedVariables are outside this package.
func Install(root string, p pin.Pin) error {
	files, err := Package(p)
	if err != nil {
		return err
	}
	parent := filepath.Join(root, p.GameFolder, "Interface", "AddOns")
	if err := os.MkdirAll(parent, 0755); err != nil {
		return err
	}
	// Reject linked paths so installation cannot modify another client's addons.
	for path := parent; path != filepath.Clean(root); path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("addon path is linked: %s", path)
		}
		if filepath.Dir(path) == path {
			return fmt.Errorf("addon root must contain the target")
		}
	}
	target := filepath.Join(parent, "Everlake")
	if info, err := os.Lstat(target); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("Everlake addon path must be a directory")
		}
		data, err := os.ReadFile(filepath.Join(target, ".everlake-package.json"))
		if err != nil {
			return fmt.Errorf("existing Everlake addon is unmanaged; preserve or relocate it before installing")
		}
		var previous Manifest
		if err := json.Unmarshal(data, &previous); err != nil {
			return err
		}
		if previous.Product != p.Product {
			return fmt.Errorf("existing addon belongs to another client product")
		}
		unchanged := true
		for name, want := range files {
			got, err := os.ReadFile(filepath.Join(target, name))
			if err != nil || string(got) != string(want) {
				unchanged = false
				break
			}
		}
		if unchanged {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".everlake-addon-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for name, data := range files {
		if strings.ContainsAny(name, "/\\") {
			return fmt.Errorf("invalid package filename")
		}
		if err := os.WriteFile(filepath.Join(stage, name), data, 0644); err != nil {
			return err
		}
	}
	backup := ""
	if _, err := os.Lstat(target); err == nil {
		backup = filepath.Join(root, ".everlake", "addon-backups", fmt.Sprintf("Everlake-%d", time.Now().UnixNano()))
		if err := os.MkdirAll(filepath.Dir(backup), 0700); err != nil {
			return err
		}
		if err := os.Rename(target, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(stage, target); err != nil {
		if backup != "" {
			if restore := os.Rename(backup, target); restore != nil {
				return fmt.Errorf("publish addon: %v; restore from %s: %v", err, backup, restore)
			}
		}
		return err
	}
	return nil
}

// Verify checks the managed package without treating other addons as launcher files.
func Verify(root string, p pin.Pin) error {
	target := filepath.Join(root, p.GameFolder, "Interface", "AddOns", "Everlake")
	if _, err := os.Stat(filepath.Join(target, ".everlake-package.json")); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	files, err := Package(p)
	if err != nil {
		return err
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(target, name))
		if err != nil {
			return err
		}
		if string(got) != string(want) {
			return fmt.Errorf("managed addon file changed: %s", filepath.Join(target, name))
		}
	}
	return nil
}
