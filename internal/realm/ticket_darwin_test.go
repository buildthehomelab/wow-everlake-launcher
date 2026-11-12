//go:build darwin && cgo

package realm

import (
	"encoding/hex"
	"testing"
)

func TestMacTicketMatchesExistingHelper(t *testing.T) {
	for name, want := range map[string]string{"alice": "c648fac45e970e5481a51d09a47d3edf8d3fc929ff471e6fa7d8e6d900163ea6", "a-very-long-user-name-xyz": "83d39aad9ede31437dc7fe9083fdf5f88d1da8a4c1f527c9d7d8ef665964c200"} {
		got, err := encryptMacTicket("TC-0123456789abcdef", name)
		if err != nil || hex.EncodeToString(got) != want {
			t.Fatalf("%s: %x %v", name, got, err)
		}
	}
}
