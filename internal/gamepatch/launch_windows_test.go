//go:build windows

package gamepatch

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The stand-in game: the test binary started again with EVERLAKE_PATCH_HELPER=1. Like the client, it holds the
// login-name ending as a string of its own in its data, with a table entry {pointer, 18} pointing at it, and its
// built-in keys in its data; a while after it starts it builds its key store from them, on the heap. It writes what it
// saw to EVERLAKE_PATCH_RESULT.
var (
	helperName  = [20]byte{0, '.', 'a', 'c', 't', 'u', 'a', 'l', '.', 'b', 'a', 't', 't', 'l', 'e', '.', 'n', 'e', 't', 0}
	helperTable = struct {
		suffix *byte
		length uint64
	}{&helperName[1], 18}
	helperKeys [count][32]byte // filled in by runHelper, as the game's start-up unpacks them
)

func TestMain(m *testing.M) {
	if os.Getenv("EVERLAKE_PATCH_HELPER") == "1" {
		os.Exit(runHelper(os.Getenv("EVERLAKE_PATCH_RESULT")))
	}
	os.Exit(m.Run())
}

func runHelper(result string) int {
	for e := range helperKeys {
		hex.Decode(helperKeys[e][:], []byte(knownKeys[e]))
	}
	time.Sleep(time.Second) // the player is still at the login screen
	// Logging in: the key store is built from the built-in keys. The launcher must not touch it after this.
	store := keyStore(0x7f)
	for e := range helperKeys {
		copy(store[e*entry+4:], helperKeys[e][:])
	}
	built := append([]byte(nil), store...)
	want, _ := hex.DecodeString(StockServerKey)
	var why string
	for end := time.Now().Add(30 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		blank := bytes.Equal(helperName[1:19], make([]byte, 18))
		zeroed := helperTable.length == 0
		keyed := bytes.Equal(store[7*entry+4:7*entry+36], want)
		untouched := bytes.Equal(store, built)
		why = fmt.Sprintf("ending blanked %v, table length zeroed %v, server key in the store %v, store untouched "+
			"after login %v", blank, zeroed, keyed, untouched)
		if !untouched {
			break
		}
		if blank && zeroed && keyed {
			os.WriteFile(result, []byte("OK "+why), 0o644)
			return 0
		}
	}
	os.WriteFile(result, []byte("FAIL "+why), 0o644)
	return 1
}

func TestLaunchPatchesARealProcess(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	result := filepath.Join(t.TempDir(), "result.txt")
	var mu sync.Mutex
	var logs []string
	game, err := Launch(Options{
		Exe:  exe,
		Args: []string{"-test.run=^$"},
		Env:  append(os.Environ(), "EVERLAKE_PATCH_HELPER=1", "EVERLAKE_PATCH_RESULT="+result),
		// A Go program's image isn't remapped the way the game's protection does it: don't wait long for that.
		UnpackWait: 300 * time.Millisecond,
		AnyPortal:  true,
		Log:        func(l string) { mu.Lock(); logs = append(logs, l); mu.Unlock() },
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-game.Done:
	case <-time.After(60 * time.Second):
		t.Fatal("the stand-in game never finished")
	}
	got, _ := os.ReadFile(result)
	mu.Lock()
	defer mu.Unlock()
	if !strings.HasPrefix(string(got), "OK") {
		t.Fatalf("stand-in game: %q\nlauncher log:\n%s", got, strings.Join(logs, "\n"))
	}
	t.Logf("stand-in game: %s\nlauncher log:\n%s", got, strings.Join(logs, "\n"))
}
