package tact

import (
	"bytes"
	"compress/zlib"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// ErrEncrypted means a BLTE chunk is encrypted. The launcher never needs to decrypt: the files it decodes (configs,
// manifests, the loose files of the install) are not encrypted, and the rest are stored as they come.
var ErrEncrypted = errors.New("BLTE chunk is encrypted")

type blteChunk struct {
	encSize, decSize uint32
	sum              Key
}

// blteHeader splits a BLTE file into its header size and chunk table. A header size of 0 means one chunk with no
// table: the whole rest of the file.
func blteHeader(data []byte) (headerSize uint32, chunks []blteChunk, err error) {
	if len(data) < 8 || string(data[:4]) != "BLTE" {
		return 0, nil, errors.New("not a BLTE file")
	}
	headerSize = binary.BigEndian.Uint32(data[4:8])
	if headerSize == 0 {
		return 0, nil, nil
	}
	if headerSize < 12 || int(headerSize) > len(data) {
		return 0, nil, fmt.Errorf("BLTE header size %d in a %d-byte file", headerSize, len(data))
	}
	flags := data[8]
	count := int(data[9])<<16 | int(data[10])<<8 | int(data[11])
	entry := 24
	if flags&0x10 != 0 {
		entry = 40 // later builds add a decoded checksum per chunk; the encoded one is still at the same place
	}
	if 12+count*entry != int(headerSize) {
		return 0, nil, fmt.Errorf("BLTE header size %d doesn't fit %d chunks", headerSize, count)
	}
	chunks = make([]blteChunk, count)
	for i := range chunks {
		e := data[12+i*entry:]
		chunks[i] = blteChunk{binary.BigEndian.Uint32(e), binary.BigEndian.Uint32(e[4:]), keyFrom(e[8:24])}
	}
	return headerSize, chunks, nil
}

// EncodedKey is the encoded key a BLTE file must be stored under: the MD5 of its header when it has a chunk table,
// otherwise the MD5 of the whole file.
func EncodedKey(data []byte) (Key, error) {
	headerSize, _, err := blteHeader(data)
	if err != nil {
		return Key{}, err
	}
	if headerSize == 0 {
		return KeyOf(data), nil
	}
	return KeyOf(data[:headerSize]), nil
}

// VerifyBLTE checks every byte of a stored file without decoding it: the header against its encoded key, then each
// chunk against the checksum the header lists for it, and that nothing trails the last chunk. This works for
// encrypted files too.
func VerifyBLTE(data []byte, ekey Key) error {
	headerSize, chunks, err := blteHeader(data)
	if err != nil {
		return err
	}
	if headerSize == 0 {
		if KeyOf(data) != ekey {
			return fmt.Errorf("BLTE file doesn't match its encoded key %s", ekey)
		}
		return nil
	}
	if KeyOf(data[:headerSize]) != ekey {
		return fmt.Errorf("BLTE header doesn't match its encoded key %s", ekey)
	}
	at := uint64(headerSize)
	for i, c := range chunks {
		end := at + uint64(c.encSize)
		if end > uint64(len(data)) {
			return fmt.Errorf("BLTE %s: chunk %d runs past the end of the file", ekey, i)
		}
		if md5.Sum(data[at:end]) != c.sum {
			return fmt.Errorf("BLTE %s: chunk %d doesn't match its checksum", ekey, i)
		}
		at = end
	}
	if at != uint64(len(data)) {
		return fmt.Errorf("BLTE %s: %d bytes after the last chunk", ekey, uint64(len(data))-at)
	}
	return nil
}

// DecodeBLTE returns a stored file's content. It checks the chunk checksums as it goes, but the caller must still
// compare the result against the content key (see Decode).
func DecodeBLTE(data []byte) ([]byte, error) {
	headerSize, chunks, err := blteHeader(data)
	if err != nil {
		return nil, err
	}
	if headerSize == 0 {
		return decodeChunk(data[8:])
	}
	var out bytes.Buffer
	at := uint64(headerSize)
	for i, c := range chunks {
		end := at + uint64(c.encSize)
		if end > uint64(len(data)) {
			return nil, fmt.Errorf("BLTE chunk %d runs past the end of the file", i)
		}
		raw := data[at:end]
		if md5.Sum(raw) != c.sum {
			return nil, fmt.Errorf("BLTE chunk %d doesn't match its checksum", i)
		}
		dec, err := decodeChunk(raw)
		if err != nil {
			return nil, fmt.Errorf("BLTE chunk %d: %w", i, err)
		}
		if uint32(len(dec)) != c.decSize {
			return nil, fmt.Errorf("BLTE chunk %d decoded to %d bytes, header says %d", i, len(dec), c.decSize)
		}
		out.Write(dec)
		at = end
	}
	return out.Bytes(), nil
}

func decodeChunk(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, errors.New("empty chunk")
	}
	switch raw[0] {
	case 'N':
		return raw[1:], nil
	case 'Z':
		r, err := zlib.NewReader(bytes.NewReader(raw[1:]))
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return io.ReadAll(r)
	case 'F':
		return DecodeBLTE(raw[1:])
	case 'E':
		return nil, ErrEncrypted
	}
	return nil, fmt.Errorf("unknown chunk mode %q", raw[0])
}

// Decode checks a stored file against its encoded key, decodes it and checks the content against its content key.
// This is the full check for a file whose content we use or write out.
func Decode(data []byte, ekey, ckey Key) ([]byte, error) {
	if err := VerifyBLTE(data, ekey); err != nil {
		return nil, err
	}
	out, err := DecodeBLTE(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ekey, err)
	}
	if KeyOf(out) != ckey {
		return nil, fmt.Errorf("%s decoded to content that doesn't match its content key %s", ekey, ckey)
	}
	return out, nil
}
