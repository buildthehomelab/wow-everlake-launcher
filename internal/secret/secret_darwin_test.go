//go:build darwin && cgo

package secret

import (
	"bytes"
	"os"
	"testing"
)

func TestRejectInvalidKeychainReference(t *testing.T) {
	for _, ref := range []string{"", "plaintext-password", "everlake-keychain:v1:bad"} {
		if _, err := Open([]byte(ref)); err == nil {
			t.Fatal("invalid reference accepted")
		}
	}
}
func TestKeychainRememberAndForget(t *testing.T) {
	if os.Getenv("EVERLAKE_KEYCHAIN_TEST") != "1" {
		t.Skip("set EVERLAKE_KEYCHAIN_TEST=1 for native Keychain test")
	}
	plain := []byte("Everlake synthetic test secret")
	ref, err := Seal(plain)
	if err != nil {
		t.Fatal(err)
	}
	defer Forget(ref)
	if bytes.Contains(ref, plain) {
		t.Fatal("password leaked into settings reference")
	}
	got, err := Open(ref)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatal("Keychain roundtrip failed", err)
	}
	if err := Forget(ref); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ref); err == nil {
		t.Fatal("forgotten password remains")
	}
}
