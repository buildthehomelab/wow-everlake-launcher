// Package tact reads the files of Blizzard's content delivery system (TACT): the configs, the encoding file,
// archive indexes, the install and download manifests, and BLTE, the container every stored file is in.
//
// Everything here only parses and checks bytes; fetching them is the cdn package's job. Every format carries its
// own hashes, and every parser here checks them, so a file that parses is a file that arrived intact.
//
// Formats follow wowdev.wiki (TACT, CASC) and were checked against the WoW Forever 1.60.1.70790 build on
// Blizzard's CDN.
package tact

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
)

// Key is a 16-byte TACT key: a content key (MD5 of a file's bytes) or an encoded key (MD5 of its BLTE header).
type Key [16]byte

// ParseKey reads a 32-character hex key.
func ParseKey(s string) (Key, error) {
	var k Key
	if len(s) != 32 {
		return k, fmt.Errorf("key %q: want 32 hex characters", s)
	}
	if _, err := hex.Decode(k[:], []byte(s)); err != nil {
		return k, fmt.Errorf("key %q: %w", s, err)
	}
	return k, nil
}

// MustKey is ParseKey for constants in tests.
func MustKey(s string) Key {
	k, err := ParseKey(s)
	if err != nil {
		panic(err)
	}
	return k
}

func (k Key) String() string { return hex.EncodeToString(k[:]) }

// IsZero reports whether the key is all zero bytes.
func (k Key) IsZero() bool { return k == Key{} }

// KeyOf is the MD5 of data, which is how TACT names configs and content.
func KeyOf(data []byte) Key { return md5.Sum(data) }

func keyFrom(b []byte) Key {
	var k Key
	copy(k[:], b)
	return k
}

// be40 reads a 5-byte big-endian number (TACT stores file sizes this way).
func be40(b []byte) uint64 {
	return uint64(b[0])<<32 | uint64(b[1])<<24 | uint64(b[2])<<16 | uint64(b[3])<<8 | uint64(b[4])
}
