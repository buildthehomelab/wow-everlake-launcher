// Package eramaps stages historical map assets outside the Forever installation.
package eramaps

import (
	"bufio"
	"context"
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

//go:embed map_catalog.py fetch_map_tiles.py tact.py collect.py
var scripts embed.FS

func Download(ctx context.Context, installDir string, log func(string)) error {
	config, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	stage := filepath.Join(config, "Everlake", "Era maps")
	installDir, err = filepath.Abs(installDir)
	if err != nil {
		return err
	}
	if within(installDir, stage) || within(stage, installDir) {
		return fmt.Errorf("Era map staging overlaps the Forever install folder")
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
	entries, err := scripts.ReadDir(".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		data, err := scripts.ReadFile(entry.Name())
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

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
