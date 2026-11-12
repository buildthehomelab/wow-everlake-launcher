package install

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
)

var runningClients atomic.Int64

func RunningClients() int64 { return runningClients.Load() }

func checkRunningClient(dir string) error {
	if pid, err := findRunningClient(dir); err != nil {
		return err
	} else if pid != 0 {
		return fmt.Errorf("client process %d is running in this installation", pid)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".everlake", "game.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var pid int
	if err := json.Unmarshal(data, &pid); err != nil {
		return err
	}
	alive, err := processAlive(pid)
	if err != nil {
		return err
	}
	if alive {
		return fmt.Errorf("client process %d is running; close the game before changing this installation", pid)
	}
	return nil
}

func AcquireClient(dir string) (func(), error) {
	unlock, err := lockFolder(dir, false)
	if err != nil {
		return nil, err
	}
	if err := checkRunningClient(dir); err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}

// TrackClient holds the installation lock through the game's lifetime. A PID
// record also protects the installation if the launcher exits first.
func TrackClient(dir string, pid int, done <-chan struct{}, unlock func()) error {
	data, err := json.Marshal(pid)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, ".everlake", "game.json")
	if err := writeFile(path, data, 0600); err != nil {
		return err
	}
	runningClients.Add(1)
	go func() { <-done; os.Remove(path); runningClients.Add(-1); unlock() }()
	return nil
}
