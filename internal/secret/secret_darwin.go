//go:build darwin && cgo

package secret

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>
static OSStatus credential(int operation, const char *account, const void *bytes, long length, CFDataRef *out) {
 CFStringRef a=CFStringCreateWithCString(NULL,account,kCFStringEncodingUTF8);
 CFMutableDictionaryRef q=CFDictionaryCreateMutable(NULL,0,&kCFTypeDictionaryKeyCallBacks,&kCFTypeDictionaryValueCallBacks);
 CFDictionarySetValue(q,kSecClass,kSecClassGenericPassword);
 CFDictionarySetValue(q,kSecAttrService,CFSTR("com.buildthehomelab.everlake-launcher.passwords"));
 CFDictionarySetValue(q,kSecAttrAccount,a);
 OSStatus status;
 if(operation==0) {
  CFDataRef data=CFDataCreate(NULL,bytes,length);
  CFDictionarySetValue(q,kSecValueData,data);
  CFDictionarySetValue(q,kSecAttrLabel,CFSTR("Everlake remembered login"));
  status=SecItemAdd(q,NULL); CFRelease(data);
 } else if(operation==1) {
  CFDictionarySetValue(q,kSecReturnData,kCFBooleanTrue);
  CFDictionarySetValue(q,kSecMatchLimit,kSecMatchLimitOne);
  status=SecItemCopyMatching(q,(CFTypeRef*)out);
 } else {status=SecItemDelete(q);}
 CFRelease(a); CFRelease(q); return status;
}
*/
import "C"

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"unsafe"
)

const available = true
const keychainPrefix = "everlake-keychain:v1:"

func seal(plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, fmt.Errorf("nothing to remember")
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	ref := keychainPrefix + hex.EncodeToString(id)
	account := C.CString(ref)
	defer C.free(unsafe.Pointer(account))
	status := C.credential(0, account, unsafe.Pointer(&plain[0]), C.long(len(plain)), nil)
	if status != 0 {
		return nil, fmt.Errorf("Keychain save failed (%d)", int(status))
	}
	return []byte(ref), nil
}
func credentialAccount(ref []byte) (*C.char, error) {
	s := string(ref)
	if !strings.HasPrefix(s, keychainPrefix) || len(s) != len(keychainPrefix)+32 {
		return nil, fmt.Errorf("invalid Keychain reference")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(s, keychainPrefix)); err != nil {
		return nil, err
	}
	return C.CString(s), nil
}
func open(ref []byte) ([]byte, error) {
	account, err := credentialAccount(ref)
	if err != nil {
		return nil, err
	}
	defer C.free(unsafe.Pointer(account))
	var data C.CFDataRef
	status := C.credential(1, account, nil, 0, &data)
	if status != 0 {
		return nil, fmt.Errorf("Keychain read failed (%d)", int(status))
	}
	defer C.CFRelease(C.CFTypeRef(data))
	return C.GoBytes(unsafe.Pointer(C.CFDataGetBytePtr(data)), C.int(C.CFDataGetLength(data))), nil
}
func forget(ref []byte) error {
	account, err := credentialAccount(ref)
	if err != nil {
		return err
	}
	defer C.free(unsafe.Pointer(account))
	status := C.credential(2, account, nil, 0, nil)
	if status != 0 && status != C.errSecItemNotFound {
		return fmt.Errorf("Keychain delete failed (%d)", int(status))
	}
	return nil
}
