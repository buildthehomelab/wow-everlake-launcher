package winres

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"image"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

var testInfo = Info{
	Version:          "v0.1.0-alpha.9",
	ProductName:      "Everlake Launcher",
	FileDescription:  "Everlake Launcher",
	CompanyName:      "buildthehomelab",
	LegalCopyright:   "buildthehomelab, GPL-2.0",
	InternalName:     "everlake-launcher",
	OriginalFilename: "everlake-launcher-windows-amd64.exe",
}

func testIcons() []image.Image {
	var out []image.Image
	for _, s := range []int{16, 32, 256} {
		img := image.NewNRGBA(image.Rect(0, 0, s, s))
		for y := 0; y < s; y++ {
			for x := 0; x < s; x++ {
				img.SetNRGBA(x, y, color.NRGBA{R: 240, G: 83, B: 28, A: uint8(255 * x / s)})
			}
		}
		out = append(out, img)
	}
	return out
}

func TestNumericVersion(t *testing.T) {
	for v, want := range map[string][4]uint16{
		"v0.1.0-alpha.9": {0, 1, 0, 9},
		"v1.2.3":         {1, 2, 3, 0},
		"0.4.1-beta.12":  {0, 4, 1, 12},
		"ci-79a1952":     {},
		"dev":            {},
	} {
		if got := NumericVersion(v); got != want {
			t.Errorf("NumericVersion(%q) = %v, want %v", v, got, want)
		}
	}
}

// walk reads a resource section the way Windows does: type, ID and language directories down to data entries
// whose addresses are relative to the image (base is the section's address; 0 in the object file, where the
// relocations aren't applied yet and the entries hold section offsets).
func walk(t *testing.T, sec []byte, base uint32) map[[2]uint16][]byte {
	t.Helper()
	le := binary.LittleEndian
	entries := func(at uint32) [][2]uint32 {
		n := int(le.Uint16(sec[at+12:])) + int(le.Uint16(sec[at+14:]))
		var out [][2]uint32
		for i := 0; i < n; i++ {
			e := at + 16 + uint32(8*i)
			out = append(out, [2]uint32{le.Uint32(sec[e:]), le.Uint32(sec[e+4:])})
		}
		return out
	}
	got := map[[2]uint16][]byte{}
	for _, te := range entries(0) {
		if te[1]&0x80000000 == 0 {
			t.Fatalf("type %d points at data, not a directory", te[0])
		}
		for _, ie := range entries(te[1] &^ 0x80000000) {
			langs := entries(ie[1] &^ 0x80000000)
			if len(langs) != 1 || langs[0][0] != langENUS || langs[0][1]&0x80000000 != 0 {
				t.Fatalf("type %d id %d: languages %v", te[0], ie[0], langs)
			}
			d := langs[0][1]
			rva, size := le.Uint32(sec[d:]), le.Uint32(sec[d+4:])
			off := rva - base
			if int(off)+int(size) > len(sec) {
				t.Fatalf("type %d id %d: data at %#x+%d is outside the section (base %#x)", te[0], ie[0], rva, size, base)
			}
			got[[2]uint16{uint16(te[0]), uint16(ie[0])}] = sec[off : off+size]
		}
	}
	return got
}

func utf16s(s string) []byte {
	var b bytes.Buffer
	for _, c := range utf16.Encode([]rune(s)) {
		binary.Write(&b, binary.LittleEndian, c)
	}
	return b.Bytes()
}

func checkResources(t *testing.T, res map[[2]uint16][]byte, icons int) {
	t.Helper()
	ver, ok := res[[2]uint16{typeVersion, 1}]
	if !ok {
		t.Fatal("no version information")
	}
	if !bytes.Contains(ver, []byte{0xBD, 0x04, 0xEF, 0xFE}) {
		t.Error("version information has no VS_FIXEDFILEINFO signature")
	}
	if binary.LittleEndian.Uint16(ver) != uint16(len(ver)) {
		t.Errorf("VS_VERSIONINFO length %d, resource is %d bytes", binary.LittleEndian.Uint16(ver), len(ver))
	}
	for _, s := range []string{"FileDescription", "Everlake Launcher", "v0.1.0-alpha.9", "everlake-launcher-windows-amd64.exe"} {
		if !bytes.Contains(ver, utf16s(s)) {
			t.Errorf("version information lacks %q", s)
		}
	}
	man := string(res[[2]uint16{typeManifest, 1}])
	if !strings.Contains(man, `level="asInvoker"`) || !strings.Contains(man, "PerMonitorV2") {
		t.Errorf("manifest: %s", man)
	}
	group := res[[2]uint16{typeGroupIcon, IconID}]
	if len(group) != 6+14*icons || binary.LittleEndian.Uint16(group[4:]) != uint16(icons) {
		t.Fatalf("icon group is %d bytes for %d icons", len(group), icons)
	}
	for i := 0; i < icons; i++ {
		e := group[6+14*i:]
		id := binary.LittleEndian.Uint16(e[12:])
		img, ok := res[[2]uint16{typeIcon, id}]
		if !ok || uint32(len(img)) != binary.LittleEndian.Uint32(e[8:]) {
			t.Errorf("icon %d (id %d): %d bytes, group says %d", i, id, len(img), binary.LittleEndian.Uint32(e[8:]))
			continue
		}
		if e[0] == 0 { // 256 px: PNG
			if !bytes.HasPrefix(img, []byte("\x89PNG")) {
				t.Errorf("256 px icon isn't a PNG")
			}
		} else if w := int32(binary.LittleEndian.Uint32(img[4:])); w != int32(e[0]) ||
			int32(binary.LittleEndian.Uint32(img[8:])) != 2*w || len(img) != 40+int(w)*int(w)*4+int((w+31)/32*4)*int(w) {
			t.Errorf("%d px icon: bitmap header or size wrong (%d bytes)", e[0], len(img))
		}
	}
}

func TestObjectFile(t *testing.T) {
	data, err := Build(testInfo, testIcons())
	if err != nil {
		t.Fatal(err)
	}
	f, err := pe.NewFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if f.Machine != pe.IMAGE_FILE_MACHINE_AMD64 || len(f.Sections) != 1 || f.Sections[0].Name != ".rsrc" {
		t.Fatalf("machine %#x, sections %v", f.Machine, f.Sections)
	}
	s := f.Sections[0]
	// One relocation per resource: 3 icons, the group, version, manifest.
	if len(s.Relocs) != 6 {
		t.Errorf("%d relocations, want 6", len(s.Relocs))
	}
	for _, r := range s.Relocs {
		if r.Type != 3 || r.SymbolTableIndex != 0 {
			t.Errorf("relocation %+v, want ADDR32NB against the section symbol", r)
		}
	}
	if len(f.Symbols) != 1 || f.Symbols[0].Name != ".rsrc" || f.Symbols[0].SectionNumber != 1 {
		t.Errorf("symbols %+v", f.Symbols)
	}
	sec, err := s.Data()
	if err != nil {
		t.Fatal(err)
	}
	checkResources(t, walk(t, sec, 0), 3)
}

func TestBadIcon(t *testing.T) {
	if _, err := Build(testInfo, []image.Image{image.NewNRGBA(image.Rect(0, 0, 32, 16))}); err == nil {
		t.Error("a non-square icon was accepted")
	}
}

// TestLinkedExe builds a small Windows program with the .syso and reads the resources from the linked .exe, so the
// relocations are checked against what Go's linker really does with them.
func TestLinkedExe(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go command")
	}
	data, err := Build(testInfo, testIcons())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module rsrctest\n\ngo 1.24\n")
	write("main.go", "package main\n\nfunc main() {}\n")
	if err := os.WriteFile(filepath.Join(dir, "rsrc_windows_amd64.syso"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "x.exe")
	cmd := exec.Command(goBin, "build", "-o", exe, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0", "GOFLAGS=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	f, err := pe.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	oh, ok := f.OptionalHeader.(*pe.OptionalHeader64)
	if !ok {
		t.Fatal("not a 64-bit image")
	}
	rd := oh.DataDirectory[pe.IMAGE_DIRECTORY_ENTRY_RESOURCE]
	if rd.VirtualAddress == 0 || rd.Size == 0 {
		t.Fatal("the .exe has no resource directory")
	}
	var sec []byte
	var base uint32
	for _, s := range f.Sections {
		if rd.VirtualAddress >= s.VirtualAddress && rd.VirtualAddress < s.VirtualAddress+s.VirtualSize {
			b, err := s.Data()
			if err != nil {
				t.Fatal(err)
			}
			off := rd.VirtualAddress - s.VirtualAddress
			sec, base = b[off:], rd.VirtualAddress
		}
	}
	if sec == nil {
		t.Fatal("no section holds the resource directory")
	}
	checkResources(t, walk(t, sec, base), 3)
}
