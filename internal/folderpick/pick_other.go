//go:build !windows && !darwin

package folderpick

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// pick uses zenity or kdialog when one is installed.
func pick(title, start string) (string, error) {
	if fi, err := os.Stat(start); err != nil || !fi.IsDir() {
		start = ""
	}
	var cmd *exec.Cmd
	if p, err := exec.LookPath("zenity"); err == nil {
		args := []string{"--file-selection", "--directory", "--title", title}
		if start != "" {
			args = append(args, "--filename", start+string(filepath.Separator))
		}
		cmd = exec.Command(p, args...)
	} else if p, err := exec.LookPath("kdialog"); err == nil {
		if start == "" {
			start = "."
		}
		cmd = exec.Command(p, "--getexistingdirectory", start, "--title", title)
	} else {
		return "", ErrUnavailable
	}
	out, err := cmd.Output()
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return "", ErrCancelled // both exit 1 on cancel
		}
		return "", ErrUnavailable
	}
	return strings.TrimSpace(string(out)), nil
}
