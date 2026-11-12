// Package settings keeps the launcher's own choices for this user, outside any install folder: for now, which
// folder the game is installed in. The file is launcher.json in the user's config folder (%AppData%\Everlake on
// Windows, ~/Library/Application Support/Everlake on macOS, ~/.config/Everlake elsewhere).
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Settings is launcher.json.
type Settings struct {
	AccountEmail string          `json:"account_email,omitempty"`
	LoginHost    string          `json:"login_host,omitempty"`
	RetailPin    json.RawMessage `json:"retail_pin,omitempty"`
	RetailDir    string          `json:"retail_dir,omitempty"`
	InstallDir   string          `json:"install_dir,omitempty"`
	// Passwords are remembered passwords, sealed for this user (internal/secret), by PasswordKey.
	Passwords map[string][]byte `json:"passwords,omitempty"`
	// AutoUpdate false stops the launcher updating itself; missing means on.
	AutoUpdate *bool `json:"auto_update,omitempty"`
	// Updated is the last launcher update put in place: a copy that is still older than that release after
	// restarting (a release built with the wrong version) doesn't fetch it again and again.
	Updated *UpdateRecord `json:"updated,omitempty"`
	// School is the launcher window's colour, picked on its page; empty means a different one each launch.
	School string `json:"school,omitempty"`
	// Animation false keeps the window's art still; missing follows the system's "reduce motion" setting.
	Animation *bool `json:"animation,omitempty"`
}

// UpdateRecord is a launcher update that was put in place.
type UpdateRecord struct {
	Path string `json:"path"` // the program or .app replaced
	Tag  string `json:"tag"`  // the release it came from
}

// AutoUpdates reports whether the launcher may update itself.
func (s Settings) AutoUpdates() bool { return s.AutoUpdate == nil || *s.AutoUpdate }

// PasswordKey names a remembered password: one per login server and account.
func PasswordKey(loginHost, email string) string {
	return strings.ToLower(strings.TrimSpace(loginHost)) + " " + strings.ToLower(strings.TrimSpace(email))
}

// Update loads the settings, lets change edit them, and saves them.
func Update(change func(*Settings)) error {
	mu.Lock()
	defer mu.Unlock()
	s, _ := Load() // an unreadable file is replaced
	change(&s)
	return save(s)
}

// Path is where the settings live. EVERLAKE_SETTINGS overrides it (tests, portable setups).
func Path() (string, error) {
	if p := os.Getenv("EVERLAKE_SETTINGS"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "Everlake", "launcher.json"), nil
}

// Load reads the settings. A missing file is no settings, not an error.
func Load() (Settings, error) {
	var s Settings
	p, err := Path()
	if err != nil {
		return s, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return Settings{}, fmt.Errorf("%s: %w", p, err)
	}
	return s, nil
}

// mu keeps this process's writers (a folder change, a remembered password) from losing each other's changes.
var mu sync.Mutex

// Save writes the settings, replacing the file in one step.
func Save(s Settings) error {
	mu.Lock()
	defer mu.Unlock()
	return save(s)
}

func save(s Settings) error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), "launcher-*.tmp") // 0600: it holds sealed passwords
	if err != nil {
		return err
	}
	_, err = f.Write(append(data, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), p)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// InstallDir is the saved install folder, or fallback when none is saved (or the file can't be read).
func InstallDir(fallback string) string {
	if s, err := Load(); err == nil && s.InstallDir != "" {
		return s.InstallDir
	}
	return fallback
}

// Subfolder is the folder made inside a chosen folder that already holds other things.
const Subfolder = "Everlake"

// ChooseInstallDir turns the folder a player picked into the folder the game goes in, and checks it can be written:
//
//   - a folder that already holds an Everlake install is used as it is (a moved or second install);
//   - a missing or empty folder is used as it is;
//   - a drive or disk root, or a folder with other things in it, gets an Everlake folder inside it, so the game's
//     files never end up loose among someone's documents.
//
// isInstall reports whether a folder holds an Everlake install (or the start of one). The folder is created if it's
// missing. inside reports that an Everlake folder was made inside the one picked.
func ChooseInstallDir(picked string, isInstall func(string) bool) (dir string, inside bool, err error) {
	picked = strings.TrimSpace(picked)
	if picked == "" {
		return "", false, errors.New("no folder given")
	}
	if strings.HasPrefix(picked, "~/") || strings.HasPrefix(picked, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			picked = filepath.Join(home, picked[2:])
		}
	}
	if !filepath.IsAbs(picked) {
		return "", false, fmt.Errorf("%s isn't a full path (start from a drive or /)", picked)
	}
	dir = filepath.Clean(picked)
	if !isInstall(dir) {
		root := filepath.Dir(dir) == dir
		empty, err := emptyOrMissing(dir)
		if err != nil {
			return "", false, err
		}
		if root || !empty {
			dir, inside = filepath.Join(dir, Subfolder), true
		}
	}
	if err := writable(dir); err != nil {
		return "", false, err
	}
	return dir, inside, nil
}

// emptyOrMissing reports whether dir has nothing in it (or isn't there); a file in its place is an error.
func emptyOrMissing(dir string) (bool, error) {
	fi, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !fi.IsDir() {
		return false, fmt.Errorf("%s is a file, not a folder", dir)
	}
	f, err := os.Open(dir)
	if err != nil {
		return false, err
	}
	defer f.Close()
	names, err := f.Readdirnames(1)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return len(names) == 0, nil
}

// writable makes dir if it's missing and checks a file can be written there (Program Files, for one, can't be
// without running as administrator).
func writable(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("can't make %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".everlake-write-test-*")
	if err != nil {
		return fmt.Errorf("can't write to %s: %w", dir, err)
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}
