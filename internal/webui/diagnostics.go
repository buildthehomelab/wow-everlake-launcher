package webui

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (s *Server) diagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Use POST to save diagnostics.", http.StatusMethodNotAllowed)
		return
	}

	s.mu.Lock()
	dir := s.dir
	s.mu.Unlock()
	s.submu.Lock()
	history := strings.Join(s.history, "\n")
	s.submu.Unlock()

	path, err := saveDiagnostics(dir, s.act.LauncherVersion, s.version, history)
	if err != nil {
		http.Error(w, "error: Could not save diagnostics: "+err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, "Diagnostics saved. Attach %s when reporting the problem.", path)
}

func saveDiagnostics(dir, launcherVersion, build, history string) (string, error) {
	report, err := os.CreateTemp(dir, "Everlake-diagnostics-*.zip")
	if err != nil {
		return "", err
	}
	path := report.Name()
	defer report.Close()

	bundle := zip.NewWriter(report)
	err = writeDiagnostics(bundle, dir, launcherVersion, build, history)
	if closeErr := bundle.Close(); err == nil {
		err = closeErr
	}
	if closeErr := report.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

func writeDiagnostics(bundle *zip.Writer, dir, launcherVersion, build, history string) error {
	log, err := bundle.Create("launcher.log")
	if err != nil {
		return err
	}
	if _, err := io.WriteString(log, history); err != nil {
		return err
	}
	inventory, err := bundle.Create("install-report.txt")
	if err != nil {
		return err
	}
	fmt.Fprintf(inventory, "Collected: %s\nLauncher: %s\nBuild: %s\nInstall: %s\n\n",
		time.Now().Format(time.RFC3339), launcherVersion, build, dir)
	fmt.Fprintln(inventory, "Files in log and error directories (local time, bytes, path):")
	var clientLogs []string
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			fmt.Fprintln(inventory, walkErr)
			return nil
		}
		if entry.IsDir() {
			switch strings.ToLower(entry.Name()) {
			case "data", "cache", "wtf", "interface", "classic-era-cache", "tbc-cache", "titan-cache":
				return filepath.SkipDir
			}
			return nil
		}
		parent := strings.ToLower(filepath.Base(filepath.Dir(path)))
		if parent != "logs" && parent != "errors" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			fmt.Fprintln(inventory, err)
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() && parent == "logs" &&
			strings.EqualFold(filepath.Ext(rel), ".log") && !strings.EqualFold(entry.Name(), "catalog.log") &&
			len(clientLogs) < 20 {
			clientLogs = append(clientLogs, rel)
		}
		_, err = fmt.Fprintf(inventory, "%s\t%d\t%s\n", info.ModTime().Format(time.RFC3339), info.Size(), rel)
		return err
	})
	if err != nil {
		return err
	}

	for _, relative := range append(clientLogs,
		filepath.Join("_era_", "logs", "catalog.log"),
		filepath.Join("_era_", "logs", "missing-files.tsv"),
	) {
		source := filepath.Join(dir, relative)
		info, err := os.Lstat(source)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if err := addDiagnosticFile(bundle, filepath.ToSlash(relative), source); err != nil {
			return err
		}
	}
	return nil
}

func addDiagnosticFile(bundle *zip.Writer, name, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	entry, err := bundle.Create(name)
	if err != nil {
		return err
	}
	_, err = io.Copy(entry, io.LimitReader(file, 4<<20))
	return err
}
