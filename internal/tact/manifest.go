package tact

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// Tag types used by WoW's install and download manifests.
const (
	TagPlatform     = 1 // Windows, OSX, ...
	TagArchitecture = 2 // x86_32, x86_64, arm64
	TagLocale       = 3 // enUS, deDE, ...
	TagRegion       = 4 // US, EU, ...
	TagCategory     = 5 // speech, text
)

// Tag is a named group of manifest entries, kept as a bitmask over the entry list.
type Tag struct {
	Name string
	Type uint16
	mask []byte
}

// Has reports whether entry i carries the tag.
func (t Tag) Has(i int) bool { return t.mask[i>>3]&(0x80>>(i&7)) != 0 }

// Count is how many entries carry the tag.
func (t Tag) Count(entries int) int {
	n := 0
	for i := 0; i < entries; i++ {
		if t.Has(i) {
			n++
		}
	}
	return n
}

// readTags reads count tags, each a C string, a big-endian type and a bitmask over entries.
func readTags(data []byte, at, count, entries int) ([]Tag, int, error) {
	maskLen := (entries + 7) / 8
	tags := make([]Tag, 0, count)
	for i := 0; i < count; i++ {
		end := bytes.IndexByte(data[at:], 0)
		if end < 0 {
			return nil, 0, fmt.Errorf("tag %d: unterminated name", i)
		}
		t := Tag{Name: string(data[at : at+end])}
		at += end + 1
		if at+2+maskLen > len(data) {
			return nil, 0, fmt.Errorf("tag %q runs past the end", t.Name)
		}
		t.Type = binary.BigEndian.Uint16(data[at:])
		t.mask = data[at+2 : at+2+maskLen]
		at += 2 + maskLen
		tags = append(tags, t)
	}
	return tags, at, nil
}

// selectEntries returns, for each entry, whether it matches the wanted tag names. Wanted tags are grouped by type:
// an entry is in when, for every type that has a wanted tag, it carries at least one of that type's wanted tags.
// Types with no wanted tag don't filter. Unknown names are an error, so a typo can't silently select everything.
func selectEntries(tags []Tag, entries int, wanted []string) ([]bool, error) {
	byType := map[uint16][]Tag{}
	for _, w := range wanted {
		found := false
		for _, t := range tags {
			if strings.EqualFold(t.Name, w) {
				byType[t.Type] = append(byType[t.Type], t)
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("the manifest has no tag %q", w)
		}
	}
	in := make([]bool, entries)
	for i := range in {
		in[i] = true
		for _, group := range byType {
			hit := false
			for _, t := range group {
				if t.Has(i) {
					hit = true
					break
				}
			}
			if !hit {
				in[i] = false
				break
			}
		}
	}
	return in, nil
}

// InstallEntry is a loose file the game needs on disk outside the data storage (the executable and its friends).
type InstallEntry struct {
	Name string // as the manifest has it; the install code turns it into a path
	CKey Key
	Size uint32
}

// Install is a build's install manifest.
type Install struct {
	Tags    []Tag
	Entries []InstallEntry
}

// ParseInstall reads a decoded install manifest ("IN").
func ParseInstall(data []byte) (*Install, error) {
	if len(data) < 10 || string(data[:2]) != "IN" {
		return nil, errors.New("not an install manifest")
	}
	if data[2] != 1 {
		return nil, fmt.Errorf("install manifest version %d", data[2])
	}
	hashSize := int(data[3])
	if hashSize != 16 {
		return nil, fmt.Errorf("install manifest hash size %d", hashSize)
	}
	tagCount := int(binary.BigEndian.Uint16(data[4:]))
	count := int(binary.BigEndian.Uint32(data[6:]))
	if count > len(data)/(1+hashSize+4) {
		return nil, fmt.Errorf("install manifest: %d entries can't fit in %d bytes", count, len(data))
	}
	tags, at, err := readTags(data, 10, tagCount, count)
	if err != nil {
		return nil, fmt.Errorf("install manifest: %w", err)
	}
	m := &Install{Tags: tags, Entries: make([]InstallEntry, 0, count)}
	for i := 0; i < count; i++ {
		end := bytes.IndexByte(data[at:], 0)
		if end < 0 || at+end+1+hashSize+4 > len(data) {
			return nil, fmt.Errorf("install manifest: entry %d runs past the end", i)
		}
		e := InstallEntry{Name: string(data[at : at+end])}
		at += end + 1
		e.CKey = keyFrom(data[at:])
		e.Size = binary.BigEndian.Uint32(data[at+hashSize:])
		at += hashSize + 4
		m.Entries = append(m.Entries, e)
	}
	if at != len(data) {
		return nil, fmt.Errorf("install manifest: %d bytes after the last entry", len(data)-at)
	}
	return m, nil
}

// Select returns the entries that match the wanted tags (see selectEntries).
func (m *Install) Select(wanted []string) ([]InstallEntry, error) {
	in, err := selectEntries(m.Tags, len(m.Entries), wanted)
	if err != nil {
		return nil, err
	}
	var out []InstallEntry
	for i, ok := range in {
		if ok {
			out = append(out, m.Entries[i])
		}
	}
	return out, nil
}

// DownloadEntry is a stored file the installer puts into the game's data storage.
type DownloadEntry struct {
	EKey     Key
	Size     uint64 // encoded (stored) size
	Priority int    // lower is needed sooner; already adjusted by the manifest's base priority
	Flags    []byte
}

// Download is a build's download manifest: every stored file of the build, in the order the installer fetches
// them. (The game itself never reads it; it streams what's missing.)
type Download struct {
	Version int
	Tags    []Tag
	Entries []DownloadEntry
}

// ParseDownload reads a decoded download manifest ("DL", versions 1 to 3).
func ParseDownload(data []byte) (*Download, error) {
	if len(data) < 11 || string(data[:2]) != "DL" {
		return nil, errors.New("not a download manifest")
	}
	m := &Download{Version: int(data[2])}
	if m.Version < 1 || m.Version > 3 {
		return nil, fmt.Errorf("download manifest version %d", m.Version)
	}
	keySize := int(data[3])
	if keySize != 16 {
		return nil, fmt.Errorf("download manifest key size %d", keySize)
	}
	hasChecksum := data[4] != 0
	count := int(binary.BigEndian.Uint32(data[5:]))
	tagCount := int(binary.BigEndian.Uint16(data[9:]))
	at, flagBytes, base := 11, 0, 0
	if m.Version >= 2 {
		flagBytes = int(data[at])
		at++
	}
	if m.Version >= 3 {
		base = int(int8(data[at]))
		at += 4 // base priority + 3 unknown bytes
	}
	entry := keySize + 5 + 1 + flagBytes
	if hasChecksum {
		entry += 4
	}
	if count > len(data)/entry || at+count*entry > len(data) {
		return nil, fmt.Errorf("download manifest: %d entries don't fit", count)
	}
	m.Entries = make([]DownloadEntry, count)
	for i := range m.Entries {
		e := data[at:]
		d := DownloadEntry{EKey: keyFrom(e), Size: be40(e[keySize:]), Priority: int(int8(e[keySize+5])) - base}
		if flagBytes > 0 {
			f := entry - flagBytes
			d.Flags = e[f : f+flagBytes]
		}
		m.Entries[i] = d
		at += entry
	}
	tags, at, err := readTags(data, at, tagCount, count)
	if err != nil {
		return nil, fmt.Errorf("download manifest: %w", err)
	}
	if at != len(data) {
		return nil, fmt.Errorf("download manifest: %d bytes after the last tag", len(data)-at)
	}
	m.Tags = tags
	return m, nil
}

// Select returns the entries that match the wanted tags (see selectEntries), in manifest order.
func (m *Download) Select(wanted []string) ([]DownloadEntry, error) {
	in, err := selectEntries(m.Tags, len(m.Entries), wanted)
	if err != nil {
		return nil, err
	}
	var out []DownloadEntry
	for i, ok := range in {
		if ok {
			out = append(out, m.Entries[i])
		}
	}
	return out, nil
}
