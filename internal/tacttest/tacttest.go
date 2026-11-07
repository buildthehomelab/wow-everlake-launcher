// Package tacttest writes small TACT files (BLTE, indexes, encoding, manifests) for tests, and serves a tiny fake
// build over HTTP laid out like Blizzard's CDN. The writers follow the same layouts the tact package reads, and
// the parsers were checked separately against Blizzard's real 1.60.1.70790 files (see the live test).
package tacttest

import (
	"bytes"
	"compress/zlib"
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/tact"
)

// Chunk is one BLTE chunk to write: mode 'N' (plain), 'Z' (zlib) or 'E' (a stand-in for encrypted bytes).
type Chunk struct {
	Mode byte
	Data []byte
}

// BLTE writes a stored file. With one plain chunk and table=false it writes the short form (header size 0).
func BLTE(table bool, chunks ...Chunk) (data []byte, ekey tact.Key) {
	var encoded [][]byte
	for _, c := range chunks {
		var b bytes.Buffer
		b.WriteByte(c.Mode)
		switch c.Mode {
		case 'Z':
			w := zlib.NewWriter(&b)
			w.Write(c.Data)
			w.Close()
		default:
			b.Write(c.Data)
		}
		encoded = append(encoded, b.Bytes())
	}
	var out bytes.Buffer
	out.WriteString("BLTE")
	if !table {
		if len(encoded) != 1 {
			panic("short BLTE form holds one chunk")
		}
		out.Write([]byte{0, 0, 0, 0})
		out.Write(encoded[0])
		return out.Bytes(), md5.Sum(out.Bytes())
	}
	headerSize := 12 + 24*len(encoded)
	binary.Write(&out, binary.BigEndian, uint32(headerSize))
	out.WriteByte(0x0F)
	out.Write([]byte{byte(len(encoded) >> 16), byte(len(encoded) >> 8), byte(len(encoded))})
	for i, e := range encoded {
		binary.Write(&out, binary.BigEndian, uint32(len(e)))
		binary.Write(&out, binary.BigEndian, uint32(len(chunks[i].Data)))
		sum := md5.Sum(e)
		out.Write(sum[:])
	}
	ekey = md5.Sum(out.Bytes())
	for _, e := range encoded {
		out.Write(e)
	}
	return out.Bytes(), ekey
}

// Plain stores content as one zlib chunk with a chunk table, the most common shape on the CDN.
func Plain(content []byte) (data []byte, ekey tact.Key) {
	return BLTE(true, Chunk{'Z', content})
}

// IndexEntry is one row to write into an index.
type IndexEntry struct {
	EKey         tact.Key
	Size, Offset uint32
}

// Index writes a CDN index with 4 KB blocks (offsets=false writes the loose-file index layout). It returns the
// index bytes and its name.
func Index(entries []IndexEntry, offsets bool) ([]byte, tact.Key) {
	sort.Slice(entries, func(i, j int) bool { return bytes.Compare(entries[i].EKey[:], entries[j].EKey[:]) < 0 })
	const block = 4096
	offBytes := 0
	if offsets {
		offBytes = 4
	}
	entry := 16 + 4 + offBytes
	perBlock := block / entry
	var blocks [][]byte
	var lastKeys []tact.Key
	for i := 0; i < len(entries) || len(blocks) == 0; i += perBlock {
		b := make([]byte, block)
		var last tact.Key
		for j := 0; j < perBlock && i+j < len(entries); j++ {
			e := entries[i+j]
			o := j * entry
			copy(b[o:], e.EKey[:])
			binary.BigEndian.PutUint32(b[o+16:], e.Size)
			if offsets {
				binary.BigEndian.PutUint32(b[o+20:], e.Offset)
			}
			last = e.EKey
		}
		blocks = append(blocks, b)
		lastKeys = append(lastKeys, last)
	}
	var out, toc bytes.Buffer
	for _, b := range blocks {
		out.Write(b)
	}
	for _, k := range lastKeys {
		toc.Write(k[:])
	}
	for _, b := range blocks {
		s := md5.Sum(b)
		toc.Write(s[:8])
	}
	out.Write(toc.Bytes())
	tocHash := md5.Sum(toc.Bytes())
	out.Write(tocHash[:8])
	footer := []byte{1, 0, 0, block / 1024, byte(offBytes), 4, 16, 8}
	footer = binary.LittleEndian.AppendUint32(footer, uint32(len(entries)))
	check := md5.Sum(append(append([]byte{}, footer...), make([]byte, 8)...))
	footer = append(footer, check[:8]...)
	out.Write(footer)
	data := out.Bytes()
	return data, md5.Sum(data[len(data)-28:])
}

// EncodingRow is one content key's row to write into an encoding file.
type EncodingRow struct {
	CKey  tact.Key
	EKeys []tact.Key
	Size  uint64
}

// Encoding writes an encoding file with 1 KB pages (so a few rows already span several pages).
func Encoding(rows []EncodingRow) []byte {
	sort.Slice(rows, func(i, j int) bool { return bytes.Compare(rows[i].CKey[:], rows[j].CKey[:]) < 0 })
	const page = 1024
	espec := []byte("z\x00")
	var pages [][]byte
	var firsts []tact.Key
	cur := []byte{}
	var first tact.Key
	flush := func() {
		if len(cur) == 0 {
			return
		}
		pages = append(pages, append(cur, make([]byte, page-len(cur))...))
		firsts = append(firsts, first)
		cur = []byte{}
	}
	for _, r := range rows {
		row := []byte{byte(len(r.EKeys)), byte(r.Size >> 32), byte(r.Size >> 24), byte(r.Size >> 16), byte(r.Size >> 8), byte(r.Size)}
		row = append(row, r.CKey[:]...)
		for _, e := range r.EKeys {
			row = append(row, e[:]...)
		}
		if len(cur)+len(row) > page {
			flush()
		}
		if len(cur) == 0 {
			first = r.CKey
		}
		cur = append(cur, row...)
	}
	flush()
	// One encoded-key spec page with a row per first encoded key.
	epage := make([]byte, 0, page)
	var efirst tact.Key
	for i, r := range rows {
		if len(epage)+25 > page {
			break
		}
		if i == 0 {
			efirst = r.EKeys[0]
		}
		epage = append(epage, r.EKeys[0][:]...)
		epage = binary.BigEndian.AppendUint32(epage, 0)
		epage = append(epage, byte(r.Size>>32), byte(r.Size>>24), byte(r.Size>>16), byte(r.Size>>8), byte(r.Size))
	}
	epage = append(epage, make([]byte, page-len(epage))...)

	var out bytes.Buffer
	out.WriteString("EN")
	out.Write([]byte{1, 16, 16})
	binary.Write(&out, binary.BigEndian, uint16(page/1024))
	binary.Write(&out, binary.BigEndian, uint16(page/1024))
	binary.Write(&out, binary.BigEndian, uint32(len(pages)))
	binary.Write(&out, binary.BigEndian, uint32(1))
	out.WriteByte(0)
	binary.Write(&out, binary.BigEndian, uint32(len(espec)))
	out.Write(espec)
	for i, p := range pages {
		s := md5.Sum(p)
		out.Write(firsts[i][:])
		out.Write(s[:])
	}
	for _, p := range pages {
		out.Write(p)
	}
	es := md5.Sum(epage)
	out.Write(efirst[:])
	out.Write(es[:])
	out.Write(epage)
	out.WriteString("b:{*=z}") // the encoding file's own spec trails the pages
	return out.Bytes()
}

// Tag is a tag to write into a manifest: the entries (by position) that carry it.
type Tag struct {
	Name    string
	Type    uint16
	Entries []int
}

func writeTags(out *bytes.Buffer, tags []Tag, count int) {
	for _, t := range tags {
		out.WriteString(t.Name)
		out.WriteByte(0)
		binary.Write(out, binary.BigEndian, t.Type)
		mask := make([]byte, (count+7)/8)
		for _, i := range t.Entries {
			mask[i>>3] |= 0x80 >> (i & 7)
		}
		out.Write(mask)
	}
}

// InstallFile is an install manifest row.
type InstallFile struct {
	Name string
	CKey tact.Key
	Size uint32
}

// Install writes an install manifest.
func Install(tags []Tag, files []InstallFile) []byte {
	var out bytes.Buffer
	out.WriteString("IN")
	out.Write([]byte{1, 16})
	binary.Write(&out, binary.BigEndian, uint16(len(tags)))
	binary.Write(&out, binary.BigEndian, uint32(len(files)))
	writeTags(&out, tags, len(files))
	for _, f := range files {
		out.WriteString(f.Name)
		out.WriteByte(0)
		out.Write(f.CKey[:])
		binary.Write(&out, binary.BigEndian, f.Size)
	}
	return out.Bytes()
}

// DownloadFile is a download manifest row.
type DownloadFile struct {
	EKey     tact.Key
	Size     uint64
	Priority int8
	Flags    []byte
}

// Download writes a download manifest of the given version (1-3). flagBytes applies to version 2 and up, base to
// version 3.
func Download(version int, checksum bool, flagBytes int, base int8, tags []Tag, files []DownloadFile) []byte {
	var out bytes.Buffer
	out.WriteString("DL")
	hc := byte(0)
	if checksum {
		hc = 1
	}
	out.Write([]byte{byte(version), 16, hc})
	binary.Write(&out, binary.BigEndian, uint32(len(files)))
	binary.Write(&out, binary.BigEndian, uint16(len(tags)))
	if version >= 2 {
		out.WriteByte(byte(flagBytes))
	}
	if version >= 3 {
		out.Write([]byte{byte(base), 0, 0, 0})
	}
	for _, f := range files {
		out.Write(f.EKey[:])
		out.Write([]byte{byte(f.Size >> 32), byte(f.Size >> 24), byte(f.Size >> 16), byte(f.Size >> 8), byte(f.Size)})
		out.WriteByte(byte(f.Priority))
		if checksum {
			out.Write([]byte{0xde, 0xad, 0xbe, 0xef})
		}
		if version >= 2 {
			fl := make([]byte, flagBytes)
			copy(fl, f.Flags)
			out.Write(fl)
		}
	}
	writeTags(&out, tags, len(files))
	return out.Bytes()
}

// CDN is a fake CDN: files by URL path under /tpr/test/, with byte ranges, and a request log.
type CDN struct {
	*httptest.Server
	mu       sync.Mutex
	files    map[string][]byte
	Requests []string
}

// NewCDN starts an empty fake CDN.
func NewCDN() *CDN {
	c := &CDN{files: map[string][]byte{}}
	c.Server = httptest.NewServer(http.HandlerFunc(c.serve))
	return c
}

// Host is the host:port to put in a cdn.Client.
func (c *CDN) Host() string { return strings.TrimPrefix(c.URL, "http://") }

// Put stores a file as kind/ab/cd/<key><suffix>.
func (c *CDN) Put(kind string, key tact.Key, suffix string, data []byte) {
	s := key.String()
	c.mu.Lock()
	c.files[fmt.Sprintf("/tpr/test/%s/%s/%s/%s%s", kind, s[:2], s[2:4], s, suffix)] = data
	c.mu.Unlock()
}

func (c *CDN) serve(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.Requests = append(c.Requests, r.URL.Path+" "+r.Header.Get("Range"))
	data, ok := c.files[r.URL.Path]
	c.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	if rg := r.Header.Get("Range"); rg != "" {
		a, b, _ := strings.Cut(strings.TrimPrefix(rg, "bytes="), "-")
		start, _ := strconv.Atoi(a)
		end, _ := strconv.Atoi(b)
		if start > end || end >= len(data) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(data[start : end+1])
		return
	}
	w.Write(data)
}

// Clone starts a second fake CDN with the same files (for host failover tests).
func (c *CDN) Clone() *CDN {
	n := NewCDN()
	c.mu.Lock()
	for k, v := range c.files {
		n.files[k] = v
	}
	c.mu.Unlock()
	return n
}
