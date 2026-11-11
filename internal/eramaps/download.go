// Package eramaps stages historical map assets beside the Forever client files.
package eramaps

import (
	"bufio"
	"context"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

//go:embed map_catalog.py fetch_map_tiles.py tact.py collect.py
var scripts embed.FS

func Download(ctx context.Context, installDir string, log func(string)) error {
	return download(ctx, installDir, log, scripts)
}

func download(ctx context.Context, installDir string, log func(string), tools fs.FS) error {
	stage, err := stagingDir(installDir)
	if err != nil {
		return err
	}
	python, args, err := pythonCommand()
	if err != nil {
		return err
	}
	temp, err := os.MkdirTemp("", "everlake-era-tools-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	entries, err := fs.ReadDir(tools, ".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		data, err := fs.ReadFile(tools, entry.Name())
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(temp, entry.Name()), data, 0600); err != nil {
			return err
		}
	}
	log("Era maps stage in " + stage)
	cmd := exec.CommandContext(ctx, python, append(args, "-u", filepath.Join(temp, "map_catalog.py"), stage)...)
	stream, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(stream)
	for scanner.Scan() {
		log(scanner.Text())
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("Era map collection reported an error (%w). See the catalog log in %s", err, stage)
	}
	return scanner.Err()
}

func stagingDir(installDir string) (string, error) {
	info, err := os.Stat(installDir)
	if err != nil {
		return "", fmt.Errorf("open the selected launcher folder: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("the selected launcher folder is not a directory")
	}
	stage := filepath.Join(installDir, "Era maps")
	if _, err := os.Lstat(stage); os.IsNotExist(err) {
		return stage, nil
	} else if err != nil {
		return "", err
	}
	// A redirected stage or cache could otherwise write into the Forever data tree.
	err = filepath.WalkDir(stage, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 || (path == stage && !entry.IsDir()) {
			return fmt.Errorf("Era map staging contains a redirected or invalid path: %s", path)
		}
		return nil
	})
	return stage, err
}

func pythonCommand() (string, []string, error) {
	if runtime.GOOS == "windows" {
		if py, err := exec.LookPath("py.exe"); err == nil {
			return py, []string{"-3"}, nil
		}
	}
	for _, name := range []string{"python3", "python"} {
		if py, err := exec.LookPath(name); err == nil {
			return py, nil, nil
		}
	}
	return "", nil, fmt.Errorf("install Python 3 to download Era maps")
}
