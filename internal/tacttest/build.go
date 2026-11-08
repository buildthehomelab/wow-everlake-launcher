package tacttest

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/pin"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/tact"
)

// Build is a whole tiny build on a fake CDN: configs, encoding (loose), one archive holding everything else.
type Build struct {
	CDN       *CDN
	Pin       pin.Pin
	MacBinary []byte
	Exe       []byte
	Readme    []byte
	Data      map[string]tact.Key // name -> encoded key of the download manifest's files
	Archive   tact.Key
	ArchBytes []byte
	EncE      tact.Key // the encoding file's encoded key
}

// NewBuild makes the build and starts its CDN (closed when the test ends).
func NewBuild(t testing.TB) *Build {
	t.Helper()
	f := &Build{CDN: NewCDN(), Data: map[string]tact.Key{}}
	t.Cleanup(f.CDN.Close)
	f.MacBinary = append([]byte{0xca, 0xfe, 0xba, 0xbe}, bytes.Repeat([]byte("mac game "), 500)...)
	f.Exe = append([]byte("MZ"), bytes.Repeat([]byte("windows game "), 400)...)
	f.Readme = []byte("read me")

	var archive bytes.Buffer
	var idx []IndexEntry
	var rows []EncodingRow
	// store puts content into the archive and the encoding file, and returns its keys.
	store := func(content []byte) (ckey, ekey tact.Key) {
		blob, ekey := Plain(content)
		idx = append(idx, IndexEntry{EKey: ekey, Size: uint32(len(blob)), Offset: uint32(archive.Len())})
		archive.Write(blob)
		archive.Write([]byte("gap!")) // archives aren't packed back to back on the real CDN either
		ckey = tact.KeyOf(content)
		rows = append(rows, EncodingRow{CKey: ckey, EKeys: []tact.Key{ekey}, Size: uint64(len(content))})
		return ckey, ekey
	}

	tags := []Tag{
		{Name: "OSX", Type: tact.TagPlatform, Entries: []int{0, 2}},
		{Name: "Windows", Type: tact.TagPlatform, Entries: []int{1, 2}},
		{Name: "enUS", Type: tact.TagLocale, Entries: []int{0, 1, 2}},
	}
	macC, _ := store(f.MacBinary)
	exeC, _ := store(f.Exe)
	readC, _ := store(f.Readme)
	install := Install(tags, []InstallFile{
		{Name: `World of Warcraft.app\Contents\MacOS\World of Warcraft`, CKey: macC, Size: uint32(len(f.MacBinary))},
		{Name: `Wow.exe`, CKey: exeC, Size: uint32(len(f.Exe))},
		{Name: `Utils\readme.txt`, CKey: readC, Size: uint32(len(f.Readme))},
	})
	var dl []DownloadFile
	for i, name := range []string{"mac-art", "shared-art", "windows-art"} {
		_, e := store(bytes.Repeat([]byte(name), 300+i))
		f.Data[name] = e
		dl = append(dl, DownloadFile{EKey: e, Size: 1, Priority: int8(i)})
	}
	download := Download(1, false, 0, 0, []Tag{
		{Name: "OSX", Type: tact.TagPlatform, Entries: []int{0, 1}},
		{Name: "Windows", Type: tact.TagPlatform, Entries: []int{1, 2}},
		{Name: "enUS", Type: tact.TagLocale, Entries: []int{0, 1, 2}},
	}, dl)
	instC, instE := store(install)
	dlC, dlE := store(download)
	rootC, _ := store([]byte("TSFM a root file"))
	vfsC, vfsE := store([]byte("TVFS a manifest"))

	encContent := Encoding(rows)
	encBlob, encE := Plain(encContent)
	f.EncE = encE
	f.CDN.Put("data", encE, "", encBlob) // the encoding file is loose on the CDN

	f.ArchBytes = archive.Bytes()
	idxBytes, name := Index(idx, true)
	f.Archive = name
	f.CDN.Put("data", name, ".index", idxBytes)
	f.CDN.Put("data", name, "", f.ArchBytes)

	cdnCfg := []byte("# CDN Configuration\narchives = " + name.String() + "\n")
	cdnKey := tact.KeyOf(cdnCfg)
	f.CDN.Put("config", cdnKey, "", cdnCfg)
	build := []byte(fmt.Sprintf("# Build Configuration\nroot = %s\ninstall = %s %s\ndownload = %s %s\n"+
		"encoding = %s %s\nvfs-root = %s %s\nvfs-1 = %s %s\nbuild-name = TEST-1\nbuild-uid = wowf\n",
		rootC, instC, instE, dlC, dlE, tact.KeyOf(encContent), encE, vfsC, vfsE, vfsC, vfsE))
	buildKey := tact.KeyOf(build)
	f.CDN.Put("config", buildKey, "", build)

	f.Pin = pin.Pin{Product: "wowf", Region: "us", Version: "1.60.1.1", BuildID: 1, BuildConfig: buildKey.String(),
		CDNConfig: cdnKey.String(), CDNPath: "tpr/test", GameFolder: "_forever_",
		Flavor: "wowf"}
	if err := f.Pin.Check(); err != nil {
		t.Fatal(err)
	}
	return f
}
