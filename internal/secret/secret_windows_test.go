package secret

import "testing"

func TestSealOpen(t *testing.T) {
	sealed, err := Seal([]byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	if string(sealed) == "hunter2" {
		t.Fatal("not encrypted")
	}
	plain, err := Open(sealed)
	if err != nil || string(plain) != "hunter2" {
		t.Fatalf("open: %q %v", plain, err)
	}
	sealed[len(sealed)-1] ^= 0xff
	if _, err := Open(sealed); err == nil {
		t.Error("a damaged blob opened")
	}
}
