package assets

import (
	"github.com/buildthehomelab/wow-everlake-launcher/internal/tact"
	"os"
	"testing"
)

func TestStoreVariantsAndCorruption(t *testing.T) {
	s := Store{Root: t.TempDir()}
	a := append([]byte{'B', 'L', 'T', 'E', 0, 0, 0, 0, 'N'}, []byte("old asset")...)
	b := append([]byte{'B', 'L', 'T', 'E', 0, 0, 0, 0, 'N'}, []byte("new asset")...)
	ka, kb := tact.KeyOf(a), tact.KeyOf(b)
	for _, data := range [][]byte{a, b, a} {
		if err := s.Put(tact.KeyOf(data), data); err != nil {
			t.Fatal(err)
		}
	}
	for _, k := range []tact.Key{ka, kb} {
		if _, err := s.Read(k); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(s.path(ka), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path(ka), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(ka); err == nil {
		t.Fatal("corrupt content accepted")
	}
	if err := os.Chmod(s.path(ka), 0444); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ka, a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(ka); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ka, b); err == nil {
		t.Fatal("variant accepted under wrong key")
	}
}
