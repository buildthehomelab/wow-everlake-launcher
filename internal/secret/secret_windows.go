package secret

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

const available = true

var (
	crypt32                = syscall.NewLazyDLL("crypt32.dll")
	procCryptProtectData   = crypt32.NewProc("CryptProtectData")
	procCryptUnprotectData = crypt32.NewProc("CryptUnprotectData")
)

const cryptprotectUIForbidden = 0x1

type dataBlob struct {
	n    uint32
	data *byte
}

func blob(b []byte) *dataBlob {
	if len(b) == 0 {
		return &dataBlob{}
	}
	return &dataBlob{uint32(len(b)), &b[0]}
}

func (b *dataBlob) take() []byte {
	defer syscall.LocalFree(syscall.Handle(unsafe.Pointer(b.data)))
	return append([]byte(nil), unsafe.Slice(b.data, b.n)...)
}

func seal(plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, errors.New("nothing to seal")
	}
	var out dataBlob
	desc, _ := syscall.UTF16PtrFromString("Everlake launcher")
	r, _, err := procCryptProtectData.Call(uintptr(unsafe.Pointer(blob(plain))), uintptr(unsafe.Pointer(desc)),
		uintptr(unsafe.Pointer(blob(entropy))), 0, 0, cryptprotectUIForbidden, uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		return nil, fmt.Errorf("encrypt: %w", err)
	}
	return out.take(), nil
}

func open(sealed []byte) ([]byte, error) {
	if len(sealed) == 0 {
		return nil, errors.New("nothing to open")
	}
	var out dataBlob
	r, _, err := procCryptUnprotectData.Call(uintptr(unsafe.Pointer(blob(sealed))), 0,
		uintptr(unsafe.Pointer(blob(entropy))), 0, 0, cryptprotectUIForbidden, uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return out.take(), nil
}

func forget([]byte) error { return nil }
