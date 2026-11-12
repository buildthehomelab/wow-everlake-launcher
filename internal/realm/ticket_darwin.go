//go:build darwin && cgo

package realm

/*
#cgo LDFLAGS: -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>
static void putString(const char *key,const char *value) {
 CFStringRef k=CFStringCreateWithCString(NULL,key,kCFStringEncodingUTF8);
 CFStringRef v=CFStringCreateWithCString(NULL,value,kCFStringEncodingUTF8);
 CFPreferencesSetAppValue(k,v,CFSTR("net.battle"));
 CFRelease(k); CFRelease(v);
}
static int putTicket(const unsigned char *bytes,long length) {
 CFDataRef d=CFDataCreate(NULL,bytes,length);
 CFPreferencesSetAppValue(CFSTR("Launch Options/WoW/WEB_TOKEN"),d,CFSTR("net.battle"));
 CFRelease(d);
 return CFPreferencesAppSynchronize(CFSTR("net.battle"));
}
*/
import "C"
import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"fmt"
	"os/user"
	"unsafe"
)

func encryptMacTicket(ticket, username string) ([]byte, error) {
	entropy := []byte{0xc8, 0x76, 0xf4, 0xae, 0x4c, 0x95, 0x2e, 0xfe, 0xf2, 0xfa, 0x0f, 0x54, 0x19, 0xc0, 0x9c, 0x43}
	for i, b := range []byte(username) {
		if i >= len(entropy) {
			break
		}
		entropy[i] ^= b
	}
	key, err := pbkdf2.Key(sha1.New, string(entropy), []byte("someSalt"), 1000, 16)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	data := []byte(ticket)
	padding := aes.BlockSize - len(data)%aes.BlockSize
	for i := 0; i < padding; i++ {
		data = append(data, byte(padding))
	}
	cipher.NewCBCEncrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(data, data)
	return data, nil
}

func WriteLaunchOptions(portal string, l *Login) error {
	if l == nil || l.Ticket == "" {
		return fmt.Errorf("no login ticket")
	}
	u, err := user.Current()
	if err != nil {
		return err
	}
	data, err := encryptMacTicket(l.Ticket, u.Username)
	if err != nil {
		return err
	}
	for k, v := range map[string]string{"Launch Options/WoW/GAME_ACCOUNT": l.GameAccount, "Launch Options/WoW/CONNECTION_STRING": portal} {
		key, value := C.CString(k), C.CString(v)
		C.putString(key, value)
		C.free(unsafe.Pointer(key))
		C.free(unsafe.Pointer(value))
	}
	if C.putTicket((*C.uchar)(unsafe.Pointer(&data[0])), C.long(len(data))) == 0 {
		return fmt.Errorf("save macOS launch preferences")
	}
	return nil
}
