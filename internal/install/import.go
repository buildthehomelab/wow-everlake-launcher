package install

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/pin"
)

// BuildRow identifies a client independently of the launcher's install state.
type BuildRow struct {
	Product, Version, Build, CDN string
}

func ActiveBuilds(dir string) ([]BuildRow, error) {
	f, err := os.Open(filepath.Join(dir, ".build.info"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return nil, fmt.Errorf("missing .build.info header")
	}
	columns := map[string]int{}
	header := strings.Split(scanner.Text(), "|")
	for i, name := range header {
		columns[strings.SplitN(name, "!", 2)[0]] = i
	}
	for _, name := range []string{"Product", "Version", "Build Key", "CDN Key", "Active"} {
		if _, ok := columns[name]; !ok {
			return nil, fmt.Errorf("missing .build.info column %s", name)
		}
	}
	var rows []BuildRow
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		cells := strings.Split(scanner.Text(), "|")
		if len(cells) != len(header) {
			return nil, fmt.Errorf("malformed .build.info row")
		}
		if cells[columns["Active"]] == "1" {
			rows = append(rows, BuildRow{cells[columns["Product"]], cells[columns["Version"]], cells[columns["Build Key"]], cells[columns["CDN Key"]]})
		}
	}
	return rows, scanner.Err()
}

func CheckBuild(dir string, p pin.Pin) error {
	if err := p.Check(); err != nil {
		return err
	}
	rows, err := ActiveBuilds(dir)
	if err != nil {
		return err
	}
	want := BuildRow{p.Product, p.Version, p.BuildConfig, p.CDNConfig}
	if len(rows) != 1 || rows[0] != want {
		return fmt.Errorf("%s needs exactly one active build matching %s %s (%s / %s)", dir, p.Product, p.Version, p.BuildConfig, p.CDNConfig)
	}
	return nil
}

type importEntry struct {
	Path        string
	Mode        fs.FileMode
	Size, ModNS int64
	Digest      [sha256.Size]byte
}

type importJournal struct {
	Source  string
	Entries []importEntry
}

type cancellableReader struct {
	ctx context.Context
	r   io.Reader
}

func (r cancellableReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(b)
}

func hashImport(ctx context.Context, path string, w io.Writer) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	f, err := os.Open(path)
	if err != nil {
		return digest, err
	}
	defer f.Close()
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(w, h), cancellableReader{ctx, f})
	copy(digest[:], h.Sum(nil))
	return digest, err
}

// Import copies a closed, single-product installation to a new managed root.
// The original and interrupted stage remain available for recovery.
func Import(ctx context.Context, source, dest string, p pin.Pin, log func(string)) error {
	source, err := filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return err
	}
	dest, err = filepath.Abs(dest)
	if err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(dest))
	if err != nil {
		return err
	}
	dest = filepath.Join(parent, filepath.Base(dest))
	stage := dest + ".everlake-import"
	for _, path := range []string{dest, stage} {
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." || filepath.IsLocal(rel) {
			return fmt.Errorf("import destination must be outside the source")
		}
	}
	if err := CheckBuild(source, p); err != nil {
		return err
	}
	unlockSource, err := lockFolder(source, true)
	if err != nil {
		return err
	}
	defer unlockSource()
	if err := checkRunningClient(source); err != nil {
		return err
	}
	if st, err := LaunchState(source); err == nil {
		if st.Pin.GameFolder != p.GameFolder || st.Pin.Flavor != p.Flavor || st.Pin.Product != p.Product ||
			st.Pin.BuildConfig != p.BuildConfig || st.Pin.CDNConfig != p.CDNConfig || st.Pin.Version != p.Version {
			return fmt.Errorf("source client layout differs from the selected pin")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if _, err := os.Stat(GameExe(source, p.GameFolder)); err != nil {
		return err
	}

	if err := os.Mkdir(stage, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	if info, err := os.Lstat(stage); err != nil || !info.IsDir() {
		return fmt.Errorf("invalid import staging directory: %s", stage)
	}
	lock, err := os.OpenFile(filepath.Join(stage, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := lockFile(lock); err != nil {
		return err
	}
	if err := recoverImportPublication(dest); err != nil {
		return err
	}
	if _, err := os.Lstat(dest); !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("destination must be unused: %s", dest)
	}

	journal := importJournal{Source: source}
	err = filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil || rel == "." || rel == filepath.Join(".everlake", "lock") {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported source entry %s (%s)", path, info.Mode())
		}
		e := importEntry{Path: rel, Mode: info.Mode(), Size: info.Size(), ModNS: info.ModTime().UnixNano()}
		// Directory timestamps and allocation sizes may settle asynchronously on
		// Windows. Membership and each file's content identify the snapshot.
		if info.IsDir() {
			e.Size = 0
			e.ModNS = 0
		}
		if !info.IsDir() {
			e.Digest, err = hashImport(ctx, path, io.Discard)
			if err != nil {
				return err
			}
		}
		journal.Entries = append(journal.Entries, e)
		return ctx.Err()
	})
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	journalPath := filepath.Join(stage, "journal.json")
	previous, err := os.ReadFile(journalPath)
	if err == nil {
		var old importJournal
		if json.Unmarshal(previous, &old) == nil {
			for i := range old.Entries {
				if old.Entries[i].Mode.IsDir() {
					old.Entries[i].Size = 0
					old.Entries[i].ModNS = 0
				}
			}
			previous, _ = json.Marshal(old)
		}
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := writeFile(journalPath, encoded, 0600); err != nil {
			return err
		}
	case err != nil:
		return err
	case string(previous) != string(encoded):
		var old importJournal
		detail := "inventory"
		if json.Unmarshal(previous, &old) == nil {
			if old.Source != journal.Source {
				detail = "source path"
			} else if len(old.Entries) != len(journal.Entries) {
				detail = "entry count"
			} else {
				for i, e := range journal.Entries {
					if old.Entries[i] != e {
						detail = fmt.Sprintf("%s (size %d/%d, mtime %d/%d, mode %v/%v)", e.Path, old.Entries[i].Size, e.Size, old.Entries[i].ModNS, e.ModNS, old.Entries[i].Mode, e.Mode)
						break
					}
				}
			}
		}
		return fmt.Errorf("source inventory changed since staging: %s; keep %s for recovery and choose a new destination", detail, stage)
	}
	tree := filepath.Join(stage, "client")
	if err := importDirectory(tree); err != nil {
		return err
	}
	var pending []importEntry
	var needed uint64
	for _, e := range journal.Entries {
		target := filepath.Join(tree, e.Path)
		if e.Mode.IsDir() {
			if err := importDirectory(target); err != nil {
				return err
			}
			continue
		}
		if info, err := os.Lstat(target); err == nil && info.Mode().IsRegular() && info.Size() == e.Size {
			if sum, err := hashImport(ctx, target, io.Discard); err == nil && sum == e.Digest {
				continue
			}
		}
		pending = append(pending, e)
		needed += uint64(e.Size)
	}
	available, err := importFreeSpace(stage)
	if err != nil {
		return err
	}
	if needed > available {
		return fmt.Errorf("import needs %d bytes; %d available at %s", needed, available, stage)
	}
	log(fmt.Sprintf("Staging %d files (%d bytes) at %s", len(pending), needed, stage))
	for _, e := range pending {
		target := filepath.Join(tree, e.Path)
		if err := copyImportFile(ctx, stage, filepath.Join(source, e.Path), target, e); err != nil {
			return err
		}
	}
	if err := validateImportTree(ctx, source, journal); err != nil {
		return fmt.Errorf("source changed during import: %w", err)
	}
	if err := validateImportTree(ctx, tree, journal); err != nil {
		return err
	}
	if err := CheckBuild(tree, p); err != nil {
		return err
	}
	if _, err := ReadState(tree); errors.Is(err, fs.ErrNotExist) {
		// A Battle.net import preserves its original build metadata. It has no
		// launcher fingerprints or recorded download coverage.
		state, err := json.Marshal(State{Pin: p, Tags: nil})
		if err != nil {
			return err
		}
		if err := writeFile(filepath.Join(tree, ".everlake", "install.json"), state, 0600); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		st, err := ReadState(tree)
		if err != nil {
			return err
		}
		{
			if st.Pin.Product != p.Product || st.Pin.BuildConfig != p.BuildConfig || st.Pin.CDNConfig != p.CDNConfig || st.Pin.GameFolder != p.GameFolder || st.Pin.Flavor != p.Flavor {
				return fmt.Errorf("staged launcher state differs from import pin")
			}
		}
	}
	state, err := ReadState(tree)
	if err != nil {
		return err
	}
	for i := range state.Files {
		rel, err := safePath(state.Files[i].Path)
		if err != nil {
			return err
		}
		info, err := os.Stat(filepath.Join(tree, rel))
		if err != nil {
			return err
		}
		state.Files[i].Size = info.Size()
		state.Files[i].ModNS = info.ModTime().UnixNano()
	}
	stateData, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(tree, ".everlake", "install.json"), stateData, 0600); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return publishImport(tree, dest)
}

func importDirectory(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("invalid staging directory: %s", path)
	}
	return nil
}

func copyImportFile(ctx context.Context, stage, source, target string, e importEntry) error {
	tmp, err := os.CreateTemp(stage, ".import-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	digest, err := hashImport(ctx, source, tmp)
	if err != nil {
		return err
	}
	if digest != e.Digest {
		return fmt.Errorf("source changed while copying %s", source)
	}
	if err := tmp.Chmod(e.Mode.Perm()); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	digest, err = hashImport(ctx, tmp.Name(), io.Discard)
	if err != nil {
		return err
	}
	if digest != e.Digest {
		return fmt.Errorf("staged copy failed verification: %s", target)
	}
	t := time.Unix(0, e.ModNS)
	if err := os.Chtimes(tmp.Name(), t, t); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}

func checkInstallTarget(dir string, p pin.Pin) error {
	pending := filepath.Join(dir, ".everlake", "pending.json")
	if data, err := os.ReadFile(pending); err == nil {
		previous, err := pin.Parse(data)
		if err != nil {
			return err
		}
		if previous.Product != p.Product || previous.BuildConfig != p.BuildConfig || previous.CDNConfig != p.CDNConfig {
			return fmt.Errorf("%s has an interrupted install; retry with %s %s", dir, previous.Product, previous.Version)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	state, err := ReadState(dir)
	if err == nil {
		if state.Pin.Product != p.Product || state.Pin.GameFolder != p.GameFolder {
			return fmt.Errorf("%s belongs to %s; choose a separate folder for %s", dir, state.Pin.Product, p.Product)
		}
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if _, err := os.Stat(pending); err == nil {
		return nil
	}
	if _, err := os.Lstat(filepath.Join(dir, ".build.info")); !errors.Is(err, fs.ErrNotExist) {
		if err != nil {
			return err
		}
		return fmt.Errorf("%s has an unmanaged build; import it to a separate folder first", dir)
	}
	return nil
}

// LaunchState rejects installs whose files might still be changing.
func LaunchState(dir string) (*State, error) {
	if _, err := os.Lstat(filepath.Join(dir, ".everlake", "pending.json")); !errors.Is(err, fs.ErrNotExist) {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("an install or repair was interrupted; retry it before launching")
	}
	return ReadState(dir)
}

// validateImportTree checks the complete snapshot immediately before publishing,
// including files reused on a retry and unexpected entries in the staging tree.
func validateImportTree(ctx context.Context, tree string, journal importJournal) error {
	expected := make(map[string]importEntry, len(journal.Entries))
	for _, entry := range journal.Entries {
		expected[entry.Path] = entry
	}
	err := filepath.WalkDir(tree, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(tree, path)
		if err != nil || rel == "." {
			return err
		}
		if rel == filepath.Join(".everlake", "lock") && d.Type().IsRegular() {
			return nil
		}
		entry, ok := expected[rel]
		if !ok {
			// macOS creates AppleDouble metadata beside files on exFAT.
			if strings.HasPrefix(d.Name(), "._") && d.Type().IsRegular() {
				f, err := os.Open(path)
				if err != nil {
					return err
				}
				var header [8]byte
				_, readErr := io.ReadFull(f, header[:])
				f.Close()
				if readErr == nil && binary.BigEndian.Uint32(header[:4]) == 0x00051607 && binary.BigEndian.Uint32(header[4:]) == 0x00020000 {
					return nil
				}
			}
			// These may be generated after copy validation, before a failed publish.
			if rel == ".everlake" && d.IsDir() {
				return nil
			}
			if rel == filepath.Join(".everlake", "install.json") && d.Type().IsRegular() {
				return nil
			}
			return fmt.Errorf("unexpected staging entry %s", path)
		}
		delete(expected, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.IsDir() != entry.Mode.IsDir() || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("staging entry type changed: %s", path)
		}
		if !info.IsDir() {
			digest, err := hashImport(ctx, path, io.Discard)
			if err != nil {
				return err
			}
			if digest != entry.Digest {
				return fmt.Errorf("staged file changed: %s", path)
			}
		}
		return ctx.Err()
	})
	if err != nil {
		return err
	}
	if len(expected) > 0 {
		return fmt.Errorf("staged snapshot is missing %d entries", len(expected))
	}
	return nil
}
