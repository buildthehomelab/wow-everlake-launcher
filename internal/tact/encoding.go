package tact

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
)

// Encoding is a build's encoding file: content key -> encoded keys and size. It keeps the decoded file and looks
// keys up page by page, as the client does, instead of loading millions of entries into a map.
type Encoding struct {
	data      []byte
	ckeySize  int
	ekeySize  int
	pageSize  int
	firstKeys [][]byte // first content key of each page, in order
	pageAt    []int    // offset of each page in data
}

// EncodingEntry is one content key's row.
type EncodingEntry struct {
	EKeys []Key // usually one; the first is the one to fetch
	Size  uint64
}

// ParseEncoding reads a decoded encoding file and checks every page of both tables against the MD5 the page index
// lists for it.
func ParseEncoding(data []byte) (*Encoding, error) {
	if len(data) < 22 || string(data[:2]) != "EN" {
		return nil, errors.New("not an encoding file")
	}
	if data[2] != 1 {
		return nil, fmt.Errorf("encoding file version %d", data[2])
	}
	e := &Encoding{data: data, ckeySize: int(data[3]), ekeySize: int(data[4])}
	if e.ckeySize != 16 || e.ekeySize != 16 {
		return nil, fmt.Errorf("encoding file key sizes %d/%d", e.ckeySize, e.ekeySize)
	}
	e.pageSize = int(binary.BigEndian.Uint16(data[5:])) * 1024
	especPageSize := int(binary.BigEndian.Uint16(data[7:])) * 1024
	pages := int(binary.BigEndian.Uint32(data[9:]))
	especPages := int(binary.BigEndian.Uint32(data[13:]))
	especBlock := int(binary.BigEndian.Uint32(data[18:]))

	at := 22 + especBlock
	indexEnd := at + pages*(e.ckeySize+16)
	pagesEnd := indexEnd + pages*e.pageSize
	especIndexEnd := pagesEnd + especPages*(e.ekeySize+16)
	especEnd := especIndexEnd + especPages*especPageSize
	if especEnd > len(data) {
		return nil, fmt.Errorf("encoding file is %d bytes, its header needs %d", len(data), especEnd)
	}
	for p := 0; p < pages; p++ {
		row := data[at+p*(e.ckeySize+16):]
		page := data[indexEnd+p*e.pageSize : indexEnd+(p+1)*e.pageSize]
		if md5.Sum(page) != keyFrom(row[e.ckeySize:]) {
			return nil, fmt.Errorf("encoding content-key page %d doesn't match its checksum", p)
		}
		e.firstKeys = append(e.firstKeys, row[:e.ckeySize])
		e.pageAt = append(e.pageAt, indexEnd+p*e.pageSize)
	}
	for p := 0; p < especPages; p++ {
		row := data[pagesEnd+p*(e.ekeySize+16):]
		page := data[especIndexEnd+p*especPageSize : especIndexEnd+(p+1)*especPageSize]
		if md5.Sum(page) != keyFrom(row[e.ekeySize:]) {
			return nil, fmt.Errorf("encoding encoded-key page %d doesn't match its checksum", p)
		}
	}
	return e, nil
}

// Lookup finds a content key. ok is false when the build has no such file.
func (e *Encoding) Lookup(ckey Key) (EncodingEntry, bool) {
	// The last page whose first key is <= ckey.
	p := sort.Search(len(e.firstKeys), func(i int) bool { return bytes.Compare(e.firstKeys[i], ckey[:]) > 0 }) - 1
	if p < 0 {
		return EncodingEntry{}, false
	}
	page := e.data[e.pageAt[p] : e.pageAt[p]+e.pageSize]
	for o := 0; o+6+e.ckeySize <= len(page); {
		count := int(page[o])
		if count == 0 {
			break
		}
		end := o + 6 + e.ckeySize + count*e.ekeySize
		if end > len(page) {
			break
		}
		if bytes.Equal(page[o+6:o+6+e.ckeySize], ckey[:]) {
			ent := EncodingEntry{Size: be40(page[o+1:])}
			for i := 0; i < count; i++ {
				ent.EKeys = append(ent.EKeys, keyFrom(page[o+6+e.ckeySize+i*e.ekeySize:]))
			}
			return ent, true
		}
		o = end
	}
	return EncodingEntry{}, false
}

// Count walks every page and counts content keys (for summaries and tests).
func (e *Encoding) Count() int {
	n := 0
	for _, at := range e.pageAt {
		page := e.data[at : at+e.pageSize]
		for o := 0; o+6+e.ckeySize <= len(page) && page[o] != 0; o += 6 + e.ckeySize + int(page[o])*e.ekeySize {
			n++
		}
	}
	return n
}
