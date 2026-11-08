// Package winres writes the Windows resources the launcher's .exe carries: its icon, its version information (the
// name, publisher and version Explorer, Task Manager and antivirus scanners read) and its application manifest. They
// go into a COFF object file (.syso) that `go build` links into a Windows program by itself when the file sits in
// the program's package folder.
//
// An .exe without these looks like a dropped tool to Windows Defender's heuristics; with them it reads as an app.
package winres

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
	"regexp"
	"sort"
	"strconv"
	"unicode/utf16"
)

// Resource types and the one language every resource is filed under (US English).
const (
	typeIcon      = 3
	typeGroupIcon = 14
	typeVersion   = 16
	typeManifest  = 24
	langENUS      = 0x0409

	// IconID is the icon group's resource ID: the window loads its title-bar and taskbar icon by it, and Explorer
	// shows the first group as the file's icon.
	IconID = 1
)

// Info is what the version information says about the program.
type Info struct {
	Version          string // as released, e.g. v0.1.0-alpha.9; anything unparsable becomes 0.0.0.0
	ProductName      string
	FileDescription  string // Task Manager's name for the running program
	CompanyName      string
	LegalCopyright   string
	InternalName     string
	OriginalFilename string
}

// Build makes the .syso: the icon in the given sizes (each a square image), the version information and the
// manifest.
func Build(info Info, icons []image.Image) ([]byte, error) {
	var res []resource
	group := new(bytes.Buffer)
	binary.Write(group, binary.LittleEndian, [3]uint16{0, 1, uint16(len(icons))}) // GRPICONDIR: reserved, type 1 = icon, count
	for i, img := range icons {
		data, err := iconImage(img)
		if err != nil {
			return nil, err
		}
		id := uint16(i + 1)
		res = append(res, resource{typeIcon, id, data})
		side := img.Bounds().Dx()
		b := byte(side)
		if side >= 256 {
			b = 0 // 0 means 256
		}
		group.Write([]byte{b, b, 0, 0})                             // width, height, colours, reserved
		binary.Write(group, binary.LittleEndian, [2]uint16{1, 32})  // planes, bits per pixel
		binary.Write(group, binary.LittleEndian, uint32(len(data))) // bytes in the image
		binary.Write(group, binary.LittleEndian, id)                // its RT_ICON id
	}
	if len(icons) > 0 {
		res = append(res, resource{typeGroupIcon, IconID, group.Bytes()})
	}
	res = append(res, resource{typeVersion, 1, versionInfo(info)})
	res = append(res, resource{typeManifest, 1, []byte(manifest(info))})
	return coff(res), nil
}

type resource struct {
	typ, id uint16
	data    []byte
}

// iconImage encodes one icon size. 256 px goes in as PNG, which Windows has read since Vista; smaller sizes as a
// 32-bit bitmap, which every part of Windows reads.
func iconImage(img image.Image) ([]byte, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w != h || w < 1 || w > 256 {
		return nil, fmt.Errorf("winres: icon images are squares up to 256 px, got %dx%d", w, h)
	}
	if w == 256 {
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	maskRow := (w + 31) / 32 * 4 // the 1-bit AND mask, rows padded to 32 bits; all zero, the alpha channel rules
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, struct {
		Size                   uint32
		Width, Height          int32
		Planes, BitCount       uint16
		Compression, SizeImage uint32
		XPels, YPels           int32
		ClrUsed, ClrImportant  uint32
	}{40, int32(w), int32(h * 2), 1, 32, 0, uint32(w*h*4 + maskRow*h), 0, 0, 0, 0}) // height counts the mask too
	for y := h - 1; y >= 0; y-- { // bottom row first
		for x := 0; x < w; x++ {
			r, g, bl, a := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			if a == 0 {
				buf.Write([]byte{0, 0, 0, 0})
				continue
			}
			// Straight (not premultiplied) BGRA.
			buf.Write([]byte{byte(bl * 0xffff / a >> 8), byte(g * 0xffff / a >> 8), byte(r * 0xffff / a >> 8), byte(a >> 8)})
		}
	}
	buf.Write(make([]byte, maskRow*h))
	return buf.Bytes(), nil
}

var versionRE = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-[a-z]+\.(\d+))?`)

// NumericVersion turns a release name into the four numbers Windows shows: v0.1.0-alpha.9 is 0.1.0.9.
func NumericVersion(v string) [4]uint16 {
	var n [4]uint16
	m := versionRE.FindStringSubmatch(v)
	if m == nil {
		return n
	}
	for i := 0; i < 4; i++ {
		if x, err := strconv.ParseUint(m[i+1], 10, 16); err == nil {
			n[i] = uint16(x)
		}
	}
	return n
}

// versionInfo is the VS_VERSIONINFO tree: the fixed numbers, then the strings under US English / Unicode.
func versionInfo(info Info) []byte {
	n := NumericVersion(info.Version)
	ms, ls := uint32(n[0])<<16|uint32(n[1]), uint32(n[2])<<16|uint32(n[3])
	var flags uint32
	if regexp.MustCompile(`-[a-z]`).MatchString(info.Version) {
		flags = 0x2 // VS_FF_PRERELEASE
	}
	fixed := new(bytes.Buffer)
	binary.Write(fixed, binary.LittleEndian, [13]uint32{
		0xFEEF04BD, 0x00010000, // signature, structure version
		ms, ls, ms, ls, // file version, product version
		0x3F, flags, // flags mask, flags
		0x00040004, // VOS_NT_WINDOWS32
		0x1, 0,     // VFT_APP, no subtype
		0, 0, // no file date
	})
	pairs := [][2]string{
		{"CompanyName", info.CompanyName},
		{"FileDescription", info.FileDescription},
		{"FileVersion", info.Version},
		{"InternalName", info.InternalName},
		{"LegalCopyright", info.LegalCopyright},
		{"OriginalFilename", info.OriginalFilename},
		{"ProductName", info.ProductName},
		{"ProductVersion", info.Version},
	}
	var strs [][]byte
	for _, p := range pairs {
		if p[1] == "" {
			continue
		}
		v := utf16z(p[1])
		strs = append(strs, verNode(p[0], 1, v, len(v)/2))
	}
	table := verNode("040904B0", 1, nil, 0, strs...)
	sfi := verNode("StringFileInfo", 1, nil, 0, table)
	trans := verNode("Translation", 0, []byte{0x09, 0x04, 0xB0, 0x04}, 4)
	vfi := verNode("VarFileInfo", 1, nil, 0, trans)
	return verNode("VS_VERSION_INFO", 0, fixed.Bytes(), fixed.Len(), sfi, vfi)
}

// verNode writes one version-information block: its length, value length, type (1 = text), key, value and
// children, each starting on a 4-byte boundary. valueLen is in bytes for binary values and in characters for text.
func verNode(key string, typ uint16, value []byte, valueLen int, children ...[]byte) []byte {
	b := new(bytes.Buffer)
	b.Write([]byte{0, 0}) // length, filled in at the end
	binary.Write(b, binary.LittleEndian, [2]uint16{uint16(valueLen), typ})
	b.Write(utf16z(key))
	pad4(b)
	b.Write(value)
	for _, c := range children {
		pad4(b)
		b.Write(c)
	}
	out := b.Bytes()
	binary.LittleEndian.PutUint16(out, uint16(len(out)))
	return out
}

func pad4(b *bytes.Buffer) {
	for b.Len()%4 != 0 {
		b.WriteByte(0)
	}
}

func utf16z(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2*len(u)+2)
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	return b
}

// manifest asks for no elevation, declares Windows 10 and 11, the per-monitor DPI awareness the window sets up
// anyway (internal/appwindow), and the modern look for message boxes.
func manifest(info Info) string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
  <description>` + xmlText(info.FileDescription) + `</description>
  <trustInfo xmlns="urn:schemas-microsoft-com:asm.v3">
    <security>
      <requestedPrivileges>
        <requestedExecutionLevel level="asInvoker" uiAccess="false"/>
      </requestedPrivileges>
    </security>
  </trustInfo>
  <compatibility xmlns="urn:schemas-microsoft-com:compatibility.v1">
    <application>
      <supportedOS Id="{8e0f7a12-bfb3-4fe8-b9a5-48fd50a15a9a}"/>
    </application>
  </compatibility>
  <application xmlns="urn:schemas-microsoft-com:asm.v3">
    <windowsSettings>
      <dpiAware xmlns="http://schemas.microsoft.com/SMI/2005/WindowsSettings">true/pm</dpiAware>
      <dpiAwareness xmlns="http://schemas.microsoft.com/SMI/2016/WindowsSettings">PerMonitorV2, PerMonitor</dpiAwareness>
    </windowsSettings>
  </application>
  <dependency>
    <dependentAssembly>
      <assemblyIdentity type="win32" name="Microsoft.Windows.Common-Controls" version="6.0.0.0"
        processorArchitecture="*" publicKeyToken="6595b64144ccf1df" language="*"/>
    </dependentAssembly>
  </dependency>
</assembly>
`
}

func xmlText(s string) string {
	var b bytes.Buffer
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// coff lays the resources out as a .rsrc section in an AMD64 COFF object: the three-level directory (type, ID,
// language), one data entry per resource, then the data. Each data entry's address is relative to the section,
// so it gets an ADDR32NB relocation against the section's symbol and the linker fixes it to the final address.
func coff(res []resource) []byte {
	sort.Slice(res, func(i, j int) bool {
		if res[i].typ != res[j].typ {
			return res[i].typ < res[j].typ
		}
		return res[i].id < res[j].id
	})
	var types []uint16
	byType := map[uint16][]int{}
	for i, r := range res {
		if len(byType[r.typ]) == 0 {
			types = append(types, r.typ)
		}
		byType[r.typ] = append(byType[r.typ], i)
	}

	const dirHeader, dirEntry, dataEntry = 16, 8, 16
	// Offsets: the type directory, then each type's ID directory, then one language directory per resource, then
	// the data entries, then the data.
	off := dirHeader + dirEntry*len(types)
	idDir := map[uint16]int{}
	for _, t := range types {
		idDir[t] = off
		off += dirHeader + dirEntry*len(byType[t])
	}
	langDir := make([]int, len(res))
	for i := range res {
		langDir[i] = off
		off += dirHeader + dirEntry
	}
	entry := make([]int, len(res))
	for i := range res {
		entry[i] = off
		off += dataEntry
	}
	data := make([]int, len(res))
	for i, r := range res {
		off = (off + 7) &^ 7
		data[i] = off
		off += len(r.data)
	}
	size := (off + 7) &^ 7

	sec := make([]byte, size)
	le := binary.LittleEndian
	dir := func(at, n int) { le.PutUint16(sec[at+14:], uint16(n)) } // ID entries only, no named ones
	ent := func(at int, id uint16, target int, subdir bool) {
		le.PutUint32(sec[at:], uint32(id))
		t := uint32(target)
		if subdir {
			t |= 0x80000000
		}
		le.PutUint32(sec[at+4:], t)
	}
	dir(0, len(types))
	for k, t := range types {
		ent(dirHeader+dirEntry*k, t, idDir[t], true)
		dir(idDir[t], len(byType[t]))
		for j, i := range byType[t] {
			ent(idDir[t]+dirHeader+dirEntry*j, res[i].id, langDir[i], true)
		}
	}
	var relocs []int
	for i, r := range res {
		dir(langDir[i], 1)
		ent(langDir[i]+dirHeader, langENUS, entry[i], false)
		le.PutUint32(sec[entry[i]:], uint32(data[i])) // relocated to the image-relative address
		le.PutUint32(sec[entry[i]+4:], uint32(len(r.data)))
		relocs = append(relocs, entry[i])
		copy(sec[data[i]:], r.data)
	}

	// File header, one section header, the section, its relocations, the symbol table and an empty string table.
	const fileHeader, sectionHeader, relocSize, symSize = 20, 40, 10, 18
	rawAt := fileHeader + sectionHeader
	relocAt := rawAt + len(sec)
	symAt := relocAt + relocSize*len(relocs)
	out := new(bytes.Buffer)
	binary.Write(out, le, struct {
		Machine, Sections               uint16
		Time, SymbolTable, Symbols      uint32
		OptionalHeader, Characteristics uint16
	}{0x8664, 1, 0, uint32(symAt), 1, 0, 0})
	binary.Write(out, le, struct {
		Name                               [8]byte
		VirtualSize, VirtualAddress        uint32
		RawSize, RawAt, RelocAt, LineNumAt uint32
		Relocs, LineNums                   uint16
		Characteristics                    uint32
	}{[8]byte{'.', 'r', 's', 'r', 'c'}, 0, 0, uint32(len(sec)), uint32(rawAt), uint32(relocAt), 0,
		uint16(len(relocs)), 0, 0x40000040}) // initialised data, readable
	out.Write(sec)
	for _, at := range relocs {
		binary.Write(out, le, struct {
			At     uint32
			Symbol uint32
			Type   uint16
		}{uint32(at), 0, 3}) // IMAGE_REL_AMD64_ADDR32NB against symbol 0, the section
	}
	binary.Write(out, le, struct {
		Name              [8]byte
		Value             uint32
		Section, Type     uint16
		Class, AuxSymbols uint8
	}{[8]byte{'.', 'r', 's', 'r', 'c'}, 0, 1, 0, 3, 0}) // IMAGE_SYM_CLASS_STATIC
	binary.Write(out, le, uint32(4)) // string table: just its own length
	return out.Bytes()
}
