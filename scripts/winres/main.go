// Command winres writes the Windows resources (icon, version information, manifest) for the launcher's .exe as a
// .syso file that `go build` links in. scripts/package.sh runs it before each Windows build:
//
//	go run ./scripts/winres -version v0.1.0-alpha.9 -name everlake-launcher-windows-amd64.exe \
//		-out cmd/everlake-launcher/rsrc_windows_amd64.syso
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/winres"
)

// The icon sizes Windows asks for: small icons at 100-250 % scaling, the taskbar, Explorer's views.
var sizes = []int{16, 20, 24, 32, 40, 48, 64, 256}

func main() {
	version := flag.String("version", "dev", "the release name, e.g. v0.1.0-alpha.9")
	name := flag.String("name", "everlake-launcher.exe", "the .exe's file name, for its version information")
	icon := flag.String("icon", "assets/icon-windows.png", "the icon, a square PNG of at least 256 px")
	out := flag.String("out", "cmd/everlake-launcher/rsrc_windows_amd64.syso", "the .syso to write")
	flag.Parse()
	if err := run(*version, *name, *icon, *out); err != nil {
		fmt.Fprintln(os.Stderr, "winres:", err)
		os.Exit(1)
	}
}

func run(version, name, iconPath, out string) error {
	f, err := os.Open(iconPath)
	if err != nil {
		return err
	}
	src, err := png.Decode(f)
	f.Close()
	if err != nil {
		return fmt.Errorf("%s: %w", iconPath, err)
	}
	if b := src.Bounds(); b.Dx() != b.Dy() || b.Dx() < 256 {
		return fmt.Errorf("%s: want a square of at least 256 px, got %dx%d", iconPath, b.Dx(), b.Dy())
	}
	var icons []image.Image
	for _, s := range sizes {
		icons = append(icons, shrink(src, s))
	}
	data, err := winres.Build(winres.Info{
		Version:          version,
		ProductName:      "Everlake Launcher",
		FileDescription:  "Everlake Launcher",
		CompanyName:      "buildthehomelab",
		LegalCopyright:   "buildthehomelab, GPL-2.0",
		InternalName:     "everlake-launcher",
		OriginalFilename: name,
	}, icons)
	if err != nil {
		return err
	}
	return os.WriteFile(out, data, 0o644)
}

// shrink scales src down to side x side by averaging every source pixel each target pixel covers (in
// premultiplied colour, so transparent edges don't darken), which keeps small icons smooth without a library.
func shrink(src image.Image, side int) image.Image {
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, side, side))
	sx, sy := float64(b.Dx())/float64(side), float64(b.Dy())/float64(side)
	for y := 0; y < side; y++ {
		y0, y1 := int(float64(y)*sy), int(float64(y+1)*sy)
		for x := 0; x < side; x++ {
			x0, x1 := int(float64(x)*sx), int(float64(x+1)*sx)
			var r, g, bl, a, n uint64
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					cr, cg, cb, ca := src.At(b.Min.X+xx, b.Min.Y+yy).RGBA()
					r, g, bl, a, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), a+uint64(ca), n+1
				}
			}
			if n == 0 || a == 0 {
				continue
			}
			dst.SetNRGBA(x, y, color.NRGBA{
				R: uint8(r * 0xffff / a >> 8), G: uint8(g * 0xffff / a >> 8), B: uint8(bl * 0xffff / a >> 8),
				A: uint8(a / n >> 8),
			})
		}
	}
	return dst
}
