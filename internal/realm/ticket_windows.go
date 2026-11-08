//go:build windows

package realm

import (
	"fmt"
	"syscall"
	"unsafe"
)

// LaunchOptionsKey is where Battle.net leaves the login for a game started with -launcherlogin (under
// HKEY_CURRENT_USER). Battle.net rewrites it each time it starts WoW itself, so writing it here does no harm.
const LaunchOptionsKey = `Software\Blizzard Entertainment\Battle.net\Launch Options\WoW`

// webTokenEntropy is the extra secret Battle.net encrypts WEB_TOKEN with (Shauren/tc-client-launcher,
// LauncherShared.h; jibay442/forever_docker dpapi.rs).
var webTokenEntropy = []byte{0xC8, 0x76, 0xF4, 0xAE, 0x4C, 0x95, 0x2E, 0xFE, 0xF2, 0xFA, 0x0F, 0x54, 0x19, 0xC0, 0x9C, 0x43}

var (
	advapi32             = syscall.NewLazyDLL("advapi32.dll")
	crypt32              = syscall.NewLazyDLL("crypt32.dll")
	procRegCreateKeyExW  = advapi32.NewProc("RegCreateKeyExW")
	procRegSetValueExW   = advapi32.NewProc("RegSetValueExW")
	procCryptProtectData = crypt32.NewProc("CryptProtectData")
)

const (
	hkeyCurrentUser         = 0x80000001
	keyWrite                = 0x20006
	regSZ                   = 1
	regBinary               = 3
	cryptprotectUIForbidden = 0x1
)

type dataBlob struct {
	n    uint32
	data *byte
}

func protect(plain []byte) ([]byte, error) {
	in := dataBlob{uint32(len(plain)), &plain[0]}
	ent := dataBlob{uint32(len(webTokenEntropy)), &webTokenEntropy[0]}
	var out dataBlob
	desc, _ := syscall.UTF16PtrFromString("Everlake")
	r, _, err := procCryptProtectData.Call(uintptr(unsafe.Pointer(&in)), uintptr(unsafe.Pointer(desc)),
		uintptr(unsafe.Pointer(&ent)), 0, 0, cryptprotectUIForbidden, uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		return nil, fmt.Errorf("encrypt the login ticket: %w", err)
	}
	defer syscall.LocalFree(syscall.Handle(unsafe.Pointer(out.data)))
	return append([]byte(nil), unsafe.Slice(out.data, out.n)...), nil
}

// WriteLaunchOptions stores the login where the game reads it with -launcherlogin: CONNECTION_STRING (the login
// server, name:port), GAME_ACCOUNT, and WEB_TOKEN (the ticket, encrypted for this Windows user with DPAPI).
func WriteLaunchOptions(portal string, l *Login) error {
	if l == nil || l.Ticket == "" {
		return fmt.Errorf("no login ticket")
	}
	blob, err := protect([]byte(l.Ticket))
	if err != nil {
		return err
	}
	sub, _ := syscall.UTF16PtrFromString(LaunchOptionsKey)
	var key syscall.Handle
	if r, _, _ := procRegCreateKeyExW.Call(hkeyCurrentUser, uintptr(unsafe.Pointer(sub)), 0, 0, 0, keyWrite, 0,
		uintptr(unsafe.Pointer(&key)), 0); r != 0 {
		return fmt.Errorf("registry %s: %w", LaunchOptionsKey, syscall.Errno(r))
	}
	defer syscall.RegCloseKey(key)
	set := func(name string, typ uint32, data []byte) error {
		n, _ := syscall.UTF16PtrFromString(name)
		if r, _, _ := procRegSetValueExW.Call(uintptr(key), uintptr(unsafe.Pointer(n)), 0, uintptr(typ),
			uintptr(unsafe.Pointer(&data[0])), uintptr(len(data))); r != 0 {
			return fmt.Errorf("registry %s\\%s: %w", LaunchOptionsKey, name, syscall.Errno(r))
		}
		return nil
	}
	str := func(s string) []byte {
		u, _ := syscall.UTF16FromString(s) // with its NUL
		return unsafe.Slice((*byte)(unsafe.Pointer(&u[0])), len(u)*2)
	}
	if err := set("CONNECTION_STRING", regSZ, str(portal)); err != nil {
		return err
	}
	if err := set("GAME_ACCOUNT", regSZ, str(l.GameAccount)); err != nil {
		return err
	}
	return set("WEB_TOKEN", regBinary, blob)
}
