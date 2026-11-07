package tact_test

import (
	"bytes"
	"crypto/md5"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/tact"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/tacttest"
)

func key(n int) tact.Key { return md5.Sum([]byte(fmt.Sprint("key", n))) }

func TestParseConfig(t *testing.T) {
	c := tact.ParseConfig("# Build Configuration\r\n\nroot = 2d645aeed4354af0eb5c1c8141491823\n" +
		"install = 3c920752499dc90a71d4dda1494d5ea9 77da5814e5114b9a5971fd73532ccf5e\n" +
		"build-name = WOW-70790patch1.60.1_Retail\nvfs-root = " + key(1).String() + " " + key(2).String() + "\n" +
		"vfs-1 = " + key(1).String() + " " + key(2).String() + "\nvfs-2 = " + key(3).String() + " " + key(4).String() + "\n" +
		"archives = " + key(5).String() + " " + key(6).String() + "\n")
	if c.One("build-name") != "WOW-70790patch1.60.1_Retail" {
		t.Errorf("build-name = %q", c.One("build-name"))
	}
	ck, ek, err := c.KeyPair("install")
	if err != nil || ck.String() != "3c920752499dc90a71d4dda1494d5ea9" || ek.String() != "77da5814e5114b9a5971fd73532ccf5e" {
		t.Errorf("install = %s %s %v", ck, ek, err)
	}
	ck, ek, err = c.KeyPair("root")
	if err != nil || ck.String() != "2d645aeed4354af0eb5c1c8141491823" || !ek.IsZero() {
		t.Errorf("root = %s %s %v", ck, ek, err)
	}
	if _, _, err := c.KeyPair("download"); err == nil {
		t.Error("missing field gave no error")
	}
	if got := strings.Join(c.VFSNames(), " "); got != "vfs-root vfs-1 vfs-2" {
		t.Errorf("VFSNames = %s", got)
	}
	if a, err := c.Keys("archives"); err != nil || len(a) != 2 || a[1] != key(6) {
		t.Errorf("archives = %v %v", a, err)
	}
}

func TestParseTable(t *testing.T) {
	rows, err := tact.ParseTable("Region!STRING:0|BuildConfig!HEX:16|BuildId!DEC:4|VersionsName!String:0\n" +
		"## seqn = 4122833\nus|9006ba6948388dd0d4a0578e64b8a92e|70790|1.60.1.70790\n" +
		"eu|9006ba6948388dd0d4a0578e64b8a92e|70790|1.60.1.70790\n")
	if err != nil || len(rows) != 2 || rows[1]["Region"] != "eu" || rows[0]["VersionsName"] != "1.60.1.70790" {
		t.Fatalf("rows = %v, %v", rows, err)
	}
	if _, err := tact.ParseTable("A!x|B!y\n1|2|3\n"); err == nil {
		t.Error("ragged row gave no error")
	}
}

func TestBLTE(t *testing.T) {
	content := bytes.Repeat([]byte("forever "), 1000)
	cases := map[string]struct {
		table  bool
		chunks []tacttest.Chunk
	}{
		"short form":  {false, []tacttest.Chunk{{Mode: 'N', Data: content}}},
		"zlib":        {true, []tacttest.Chunk{{Mode: 'Z', Data: content}}},
		"many chunks": {true, []tacttest.Chunk{{Mode: 'N', Data: content[:100]}, {Mode: 'Z', Data: content[100:5000]}, {Mode: 'N', Data: content[5000:]}}},
	}
	for name, c := range cases {
		data, ekey := tacttest.BLTE(c.table, c.chunks...)
		if got, err := tact.EncodedKey(data); err != nil || got != ekey {
			t.Errorf("%s: EncodedKey = %s, %v; want %s", name, got, err, ekey)
		}
		out, err := tact.Decode(data, ekey, tact.KeyOf(content))
		if err != nil || !bytes.Equal(out, content) {
			t.Errorf("%s: Decode: %v", name, err)
		}
		if _, err := tact.Decode(data, ekey, key(1)); err == nil {
			t.Errorf("%s: wrong content key accepted", name)
		}
		// Every byte is covered: flip each one in turn (sampled) and the check must fail.
		for i := 0; i < len(data); i += 7 {
			bad := append([]byte{}, data...)
			bad[i] ^= 0x40
			if tact.VerifyBLTE(bad, ekey) == nil {
				t.Errorf("%s: flipped byte %d of %d passed", name, i, len(data))
				break
			}
		}
		if tact.VerifyBLTE(append(append([]byte{}, data...), 0), ekey) == nil && c.table {
			t.Errorf("%s: trailing byte passed", name)
		}
		if tact.VerifyBLTE(data[:len(data)-1], ekey) == nil {
			t.Errorf("%s: cut-off file passed", name)
		}
	}
}

func TestBLTEEncryptedVerifiesButDoesNotDecode(t *testing.T) {
	data, ekey := tacttest.BLTE(true, tacttest.Chunk{Mode: 'N', Data: []byte("plain")}, tacttest.Chunk{Mode: 'E', Data: []byte("\x08secret")})
	if err := tact.VerifyBLTE(data, ekey); err != nil {
		t.Fatalf("encrypted file failed its checksum: %v", err)
	}
	if _, err := tact.DecodeBLTE(data); !errors.Is(err, tact.ErrEncrypted) {
		t.Fatalf("DecodeBLTE = %v, want ErrEncrypted", err)
	}
}

func TestParseIndex(t *testing.T) {
	var entries []tacttest.IndexEntry
	for i := 0; i < 500; i++ { // spans several 4 KB blocks
		entries = append(entries, tacttest.IndexEntry{EKey: key(i), Size: uint32(100 + i), Offset: uint32(i * 1000)})
	}
	data, name := tacttest.Index(entries, true)
	idx, err := tact.ParseIndex(name, data)
	if err != nil {
		t.Fatal(err)
	}
	if !idx.HasOffsets || len(idx.Entries) != 500 {
		t.Fatalf("%d entries, offsets %v", len(idx.Entries), idx.HasOffsets)
	}
	byKey := map[tact.Key]tact.IndexEntry{}
	for _, e := range idx.Entries {
		byKey[e.EKey] = e
	}
	if e := byKey[key(321)]; e.Size != 421 || e.Offset != 321000 {
		t.Errorf("entry 321 = %+v", e)
	}
	if _, err := tact.ParseIndex(key(9999), data); err == nil {
		t.Error("index accepted under the wrong name")
	}
	for _, at := range []int{0, 5000, len(data) - 40, len(data) - 10} { // a block, another block, the TOC, the footer
		bad := append([]byte{}, data...)
		bad[at] ^= 1
		if _, err := tact.ParseIndex(name, bad); err == nil {
			t.Errorf("flipped byte %d passed", at)
		}
	}

	loose, lname := tacttest.Index(entries[:10], false)
	li, err := tact.ParseIndex(lname, loose)
	if err != nil || li.HasOffsets || len(li.Entries) != 10 {
		t.Fatalf("loose-file index: %v, %+v", err, li)
	}
}

func TestParseEncoding(t *testing.T) {
	var rows []tacttest.EncodingRow
	for i := 0; i < 200; i++ { // 1 KB pages: several pages
		r := tacttest.EncodingRow{CKey: key(i), EKeys: []tact.Key{key(i + 10000)}, Size: uint64(i) << 30}
		if i%50 == 0 {
			r.EKeys = append(r.EKeys, key(i+20000))
		}
		rows = append(rows, r)
	}
	data := tacttest.Encoding(rows)
	enc, err := tact.ParseEncoding(data)
	if err != nil {
		t.Fatal(err)
	}
	if enc.Count() != 200 {
		t.Errorf("Count = %d", enc.Count())
	}
	for _, i := range []int{0, 1, 50, 137, 199} {
		e, ok := enc.Lookup(key(i))
		if !ok || e.EKeys[0] != key(i+10000) || e.Size != uint64(i)<<30 {
			t.Errorf("Lookup(%d) = %+v %v", i, e, ok)
		}
		if i%50 == 0 && (len(e.EKeys) != 2 || e.EKeys[1] != key(i+20000)) {
			t.Errorf("Lookup(%d) encoded keys = %v", i, e.EKeys)
		}
	}
	if _, ok := enc.Lookup(key(5000)); ok {
		t.Error("found a key that isn't there")
	}
	var low tact.Key // all zeros sorts before every page
	if _, ok := enc.Lookup(low); ok {
		t.Error("found the zero key")
	}
	bad := append([]byte{}, data...)
	bad[len(bad)-2000] ^= 1 // inside the pages
	if _, err := tact.ParseEncoding(bad); err == nil {
		t.Error("damaged page passed")
	}
}

func installTags() []tacttest.Tag {
	// 0: Windows x86_64 exe, 1: Windows arm64 exe, 2: Mac binary for US, 3: Mac binary for CN, 4: shared text.
	return []tacttest.Tag{
		{Name: "OSX", Type: tact.TagPlatform, Entries: []int{2, 3, 4}},
		{Name: "Windows", Type: tact.TagPlatform, Entries: []int{0, 1, 4}},
		{Name: "arm64", Type: tact.TagArchitecture, Entries: []int{1, 2, 3, 4}},
		{Name: "x86_64", Type: tact.TagArchitecture, Entries: []int{0, 2, 3, 4}},
		{Name: "enUS", Type: tact.TagLocale, Entries: []int{0, 1, 2, 3, 4}},
		{Name: "CN", Type: tact.TagRegion, Entries: []int{0, 1, 3, 4}},
		{Name: "US", Type: tact.TagRegion, Entries: []int{0, 1, 2, 4}},
	}
}

func TestParseInstallAndSelect(t *testing.T) {
	files := []tacttest.InstallFile{
		{Name: `Wow.exe`, CKey: key(0), Size: 10}, {Name: `Wow-ARM64.exe`, CKey: key(1), Size: 11},
		{Name: `World of Warcraft.app\Contents\MacOS\World of Warcraft`, CKey: key(2), Size: 12},
		{Name: `World of Warcraft.app\Contents\MacOS\World of Warcraft`, CKey: key(3), Size: 13},
		{Name: `Utils\readme.txt`, CKey: key(4), Size: 14},
	}
	m, err := tact.ParseInstall(tacttest.Install(installTags(), files))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Entries) != 5 || len(m.Tags) != 7 || m.Entries[2].Name != files[2].Name || m.Entries[3].CKey != key(3) {
		t.Fatalf("parsed %+v", m.Entries)
	}
	names := func(tags ...string) string {
		sel, err := m.Select(tags)
		if err != nil {
			return err.Error()
		}
		var out []string
		for _, e := range sel {
			out = append(out, fmt.Sprint(e.Size))
		}
		return strings.Join(out, ",")
	}
	for tags, want := range map[string]string{
		"OSX arm64 enUS US":          "12,14",    // the US Mac binary, not the CN one
		"OSX arm64 enUS":             "12,13,14", // no region: both binaries (the installer refuses that)
		"Windows x86_64 enUS US":     "10,14",
		"Windows arm64 enUS US":      "11,14",
		"Windows OSX x86_64 enUS US": "10,12,14", // two tags of one type: either
		"osx ARM64 enus us":          "12,14",    // names match without case
		"OSX arm64 frFR":             "the manifest has no tag \"frFR\"",
	} {
		if got := names(strings.Fields(tags)...); got != want {
			t.Errorf("Select(%s) = %s, want %s", tags, got, want)
		}
	}
	data := tacttest.Install(installTags(), files)
	if _, err := tact.ParseInstall(append(data, 0)); err == nil {
		t.Error("trailing byte passed")
	}
	if _, err := tact.ParseInstall(data[:len(data)-3]); err == nil {
		t.Error("cut-off manifest passed")
	}
}

func TestParseDownload(t *testing.T) {
	files := []tacttest.DownloadFile{
		{EKey: key(10), Size: 1 << 33, Priority: 0, Flags: []byte{1}},
		{EKey: key(11), Size: 500, Priority: 1},
		{EKey: key(12), Size: 600, Priority: 2},
	}
	tags := []tacttest.Tag{
		{Name: "OSX", Type: tact.TagPlatform, Entries: []int{0, 2}},
		{Name: "Windows", Type: tact.TagPlatform, Entries: []int{0, 1}},
		{Name: "enUS", Type: tact.TagLocale, Entries: []int{0, 1, 2}},
	}
	for _, c := range []struct {
		version, flags int
		checksum       bool
		base           int8
	}{{1, 0, false, 0}, {1, 0, true, 0}, {2, 1, false, 0}, {3, 1, true, -1}} {
		name := fmt.Sprintf("v%d flags %d checksum %v base %d", c.version, c.flags, c.checksum, c.base)
		m, err := tact.ParseDownload(tacttest.Download(c.version, c.checksum, c.flags, c.base, tags, files))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if m.Version != c.version || len(m.Entries) != 3 || m.Entries[0].Size != 1<<33 || m.Entries[2].EKey != key(12) {
			t.Errorf("%s: parsed %+v", name, m.Entries)
		}
		if p := m.Entries[1].Priority; p != 1-int(c.base) {
			t.Errorf("%s: priority %d", name, p)
		}
		if c.flags > 0 && m.Entries[0].Flags[0] != 1 {
			t.Errorf("%s: flags %v", name, m.Entries[0].Flags)
		}
		sel, err := m.Select([]string{"OSX", "enUS"})
		if err != nil || len(sel) != 2 || sel[1].EKey != key(12) {
			t.Errorf("%s: Select = %+v %v", name, sel, err)
		}
	}
	if _, err := tact.ParseDownload([]byte("DL\x09")); err == nil {
		t.Error("short header passed")
	}
}
