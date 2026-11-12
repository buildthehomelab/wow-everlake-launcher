//go:build !windows

package install

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func processAlive(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	err := syscall.Kill(pid, 0)
	if err == nil || errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return false, err
}

// Check the executable path as well as the recorded PID: clients may restart
// themselves after first-run setup, and can also be started outside the launcher.
func findRunningClient(dir string) (int, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return 0, err
	}
	roots := []string{absolute + string(os.PathSeparator)}
	if canonical, err := filepath.EvalSymlinks(absolute); err == nil {
		roots = append(roots, canonical+string(os.PathSeparator))
	}
	output, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		at := strings.IndexAny(line, " \t")
		if at < 0 {
			continue
		}
		pid, err := strconv.Atoi(line[:at])
		if err != nil || pid == os.Getpid() {
			continue
		}
		command := strings.TrimSpace(line[at:])
		for _, root := range roots {
			if strings.HasPrefix(command, root) {
				return pid, nil
			}
		}
	}
	return 0, nil
}
