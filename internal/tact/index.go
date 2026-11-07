package tact

import (
	"crypto/md5"
	"encoding/binary"
	"errors"
	"fmt"
)

// IndexEntry says where one stored file is: inside an archive at Offset (archive indexes), or on its own on the CDN
// (the loose-file index, where Offset is always 0).
type IndexEntry struct {
	EKey   Key
	Size   uint32
	Offset uint32
}

// Index is a parsed CDN index (an archive's ".index", or the build's loose-file index).
type Index struct {
	Entries    []IndexEntry
	HasOffsets bool // false for the loose-file index
}

const indexFooterSize = 28 // 8-byte TOC hash + 20-byte footer

// ParseIndex reads a CDN index and checks all of its hashes:
//   - its name is the MD5 of its last 28 bytes (TOC hash + footer),
//   - the footer checksum covers the footer's fields,
//   - the TOC hash covers the table of contents (last key of each block, then each block's checksum),
//   - each block's first 8 MD5 bytes are listed in the TOC.
//
// So an index that parses under its name is byte for byte the one the CDN config asked for.
func ParseIndex(name Key, data []byte) (*Index, error) {
	if len(data) < indexFooterSize {
		return nil, errors.New("index too short")
	}
	if md5.Sum(data[len(data)-indexFooterSize:]) != name {
		return nil, fmt.Errorf("index %s: footer doesn't match its name", name)
	}
	f := data[len(data)-20:]
	// f: version, 2 unknown bytes, block size in KB, offset bytes, size bytes, key bytes, checksum bytes,
	// entry count (4 bytes little-endian), footer checksum (8 bytes).
	blockKB, offBytes, sizeBytes, keyBytes, sumBytes := int(f[3]), int(f[4]), int(f[5]), int(f[6]), int(f[7])
	count := int(binary.LittleEndian.Uint32(f[8:]))
	var check [20]byte
	copy(check[:12], f[:12])
	if sum := md5.Sum(check[:]); string(sum[:8]) != string(f[12:20]) {
		return nil, fmt.Errorf("index %s: footer checksum is wrong", name)
	}
	if keyBytes != 16 || sizeBytes != 4 || (offBytes != 0 && offBytes != 4) || sumBytes != 8 || blockKB == 0 {
		return nil, fmt.Errorf("index %s: unknown layout (key %d, size %d, offset %d, checksum %d, block %d KB)",
			name, keyBytes, sizeBytes, offBytes, sumBytes, blockKB)
	}
	block := blockKB * 1024
	body := len(data) - indexFooterSize
	if body%(block+keyBytes+sumBytes) != 0 {
		return nil, fmt.Errorf("index %s: %d bytes don't divide into %d-byte blocks", name, len(data), block)
	}
	blocks := body / (block + keyBytes + sumBytes)
	toc := data[blocks*block : body]
	if sum := md5.Sum(toc); string(sum[:8]) != string(data[body:body+8]) {
		return nil, fmt.Errorf("index %s: table of contents doesn't match its hash", name)
	}
	sums := toc[blocks*keyBytes:]
	entry := keyBytes + sizeBytes + offBytes
	if count > blocks*(block/entry) {
		return nil, fmt.Errorf("index %s: footer says %d entries, %d blocks can't hold them", name, count, blocks)
	}
	idx := &Index{HasOffsets: offBytes != 0, Entries: make([]IndexEntry, 0, count)}
	for b := 0; b < blocks; b++ {
		blk := data[b*block : (b+1)*block]
		if sum := md5.Sum(blk); string(sum[:8]) != string(sums[b*8:b*8+8]) {
			return nil, fmt.Errorf("index %s: block %d doesn't match its checksum", name, b)
		}
		for o := 0; o+entry <= len(blk) && len(idx.Entries) < count; o += entry {
			e := IndexEntry{EKey: keyFrom(blk[o:])}
			if e.EKey.IsZero() {
				break // the rest of the block is padding
			}
			e.Size = binary.BigEndian.Uint32(blk[o+keyBytes:])
			if offBytes != 0 {
				e.Offset = binary.BigEndian.Uint32(blk[o+keyBytes+sizeBytes:])
			}
			idx.Entries = append(idx.Entries, e)
		}
	}
	if len(idx.Entries) != count {
		return nil, fmt.Errorf("index %s: footer says %d entries, blocks hold %d", name, count, len(idx.Entries))
	}
	return idx, nil
}
