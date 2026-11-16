//go:build darwin

package gamepatch

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// keypatchHost stands in for the game: it holds a key store like the client's (12 entries of 40 bytes on the heap) and
// reports whether the region group 8 entry changed. "bad" breaks one flag, so the store must not be recognised.
const keypatchHost = `#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
static const char *KEYS[12] = { %KEYS% };
static const uint8_t FLAGS[12] = {1, 1, 1, 1, 1, 1, 1, 1, 0, 1, 1, 1};
static void hex(const char *s, uint8_t *out) { for (int i = 0; i < 32; i++) { unsigned v; sscanf(s + 2 * i, "%2x", &v); out[i] = (uint8_t)v; } }
int main(int argc, char **argv) {
    int bad = argc > 1 && strcmp(argv[1], "bad") == 0;
    uint8_t *store = calloc(1, 12 * 40);
    for (int e = 0; e < 12; e++) {
        uint32_t id = (uint32_t)e + 1;
        memcpy(store + e * 40, &id, 4);
        hex(KEYS[e], store + e * 40 + 4);
        store[e * 40 + 36] = FLAGS[e];
    }
    if (bad) store[36] = 0;
    uint8_t want[32];
    hex(getenv("FOREVER_SERVER_KEY"), want);
    for (int i = 0; i < (bad ? 30 : 100); i++) {
        if (memcmp(store + 7 * 40 + 4, want, 32) == 0) { puts("patched"); return 0; }
        usleep(100 * 1000);
    }
    uint8_t orig[32];
    hex(KEYS[7], orig);
    puts(memcmp(store + 7 * 40 + 4, orig, 32) == 0 ? "untouched" : "changed");
    return 0;
}
`

func buildKeypatchHost(t *testing.T) (host, lib string) {
	t.Helper()
	if _, err := exec.LookPath("clang"); err != nil {
		t.Skip("clang not found")
	}
	block := regexp.MustCompile(`(?s)KNOWN_KEYS\[COUNT\] = \{(.*?)\};`).FindSubmatch(macPatch)
	if block == nil {
		t.Fatal("KNOWN_KEYS not found in keypatch.c.txt")
	}
	keys := regexp.MustCompile(`"[0-9A-F]{64}"`).FindAll(block[1], -1)
	if len(keys) != 12 {
		t.Fatalf("%d known keys, want 12", len(keys))
	}
	var quoted []string
	for _, k := range keys {
		quoted = append(quoted, string(k))
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "host.c")
	patchSrc := filepath.Join(dir, "keypatch.c")
	host = filepath.Join(dir, "host")
	lib = filepath.Join(dir, "keypatch.dylib")
	if err := os.WriteFile(src, []byte(strings.Replace(keypatchHost, "%KEYS%", strings.Join(quoted, ", "), 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(patchSrc, macPatch, 0600); err != nil {
		t.Fatal(err)
	}
	// -O0: the helper changes the store behind the host's back, so an optimised host may never read it again.
	for _, args := range [][]string{{"-O0", "-o", host, src}, {"-O2", "-dynamiclib", "-o", lib, patchSrc}} {
		if out, err := exec.Command("clang", args...).CombinedOutput(); err != nil {
			t.Fatalf("clang %v: %v\n%s", args, err, out)
		}
	}
	return host, lib
}

func runKeypatchHost(t *testing.T, host, lib, mode string) (string, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "patch.log")
	cmd := exec.Command(host, mode)
	cmd.Env = append(os.Environ(), "DYLD_INSERT_LIBRARIES="+lib, "FOREVER_REGION_GROUP=8", "FOREVER_PATCH_LOG="+logPath,
		"FOREVER_SERVER_KEY=00112233445566778899AABBCCDDEEFF00112233445566778899AABBCCDDEEFF")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("host: %v", err)
	}
	log, _ := os.ReadFile(logPath)
	return strings.TrimSpace(string(out)), string(log)
}

// The injected helper finds the store and writes the server key into the region group's entry.
func TestKeypatchWritesStore(t *testing.T) {
	host, lib := buildKeypatchHost(t)
	got, log := runKeypatchHost(t, host, lib, "good")
	if got != "patched" {
		t.Fatalf("host says %q, want patched; log:\n%s", got, log)
	}
	if !strings.Contains(log, "server key written (group 8)") {
		t.Fatalf("log lacks the write:\n%s", log)
	}
}

// A block that is not exactly Blizzard's store (one flag differs) is never written.
func TestKeypatchLeavesUnknownStore(t *testing.T) {
	host, lib := buildKeypatchHost(t)
	got, log := runKeypatchHost(t, host, lib, "bad")
	if got != "untouched" {
		t.Fatalf("host says %q, want untouched; log:\n%s", got, log)
	}
}
