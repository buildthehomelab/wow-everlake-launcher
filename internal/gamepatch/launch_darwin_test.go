//go:build darwin

package gamepatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMacClientHelper(t *testing.T) {
	exe := os.Getenv("EVERLAKE_TEST_CLIENT")
	if exe == "" {
		t.Skip("set EVERLAKE_TEST_CLIENT for real client launch")
	}
	product := os.Getenv("EVERLAKE_TEST_PRODUCT")
	if product == "" {
		product = "wow"
	}
	args := []string{"-uid", product}
	if os.Getenv("EVERLAKE_TEST_LOGIN") == "1" {
		args = append(args, "-launcherlogin", "-config", "Everlake.wtf")
	}
	game, err := Launch(Options{Exe: exe, Dir: filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(exe)))), Args: args, AnyPortal: true, Log: func(s string) { t.Log(s) }})
	if err != nil {
		t.Fatal(err)
	}
	process, err := os.FindProcess(game.Pid)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { process.Kill(); <-game.Done }()
	timeout := 20 * time.Second
	if os.Getenv("EVERLAKE_TEST_LOGIN") == "1" {
		timeout = 90 * time.Second
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-game.Done:
			t.Fatal("client exited during startup")
		case <-tick.C:
			if os.Getenv("EVERLAKE_TEST_LOGIN") == "1" {
				dir := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(exe))))
				data, _ := os.ReadFile(filepath.Join(dir, "Logs", "Connection.log"))
				if strings.Contains(string(data), "BattleNet Realm List") {
					t.Log("authenticated realm list reached")
					return
				}
			}
		case <-deadline.C:
			if os.Getenv("EVERLAKE_TEST_LOGIN") == "1" {
				t.Fatal("authenticated realm list not reached before timeout")
			}
			return
		}
	}
}
