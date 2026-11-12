#!/bin/sh
# Builds the launcher's release files into dist/ (or the folder given).
#
#   scripts/package.sh windows [version] [outdir]   everlake-launcher-windows-amd64.exe     the app: a window, no console
#                                                    everlake-launcher-cli-windows-amd64.exe for typing commands
#   scripts/package.sh macos   [version] [outdir]   Everlake-Launcher-macos.zip and .dmg: "Everlake Launcher.app",
#                                                    one universal binary (Apple silicon + Intel)
#   scripts/package.sh sums    [-]       [outdir]   SHA256SUMS.txt over what's there (last, before uploading)
#
# The version is the release's tag (v0.1.0-alpha.14): launchers compare it with the Latest release's tag to update.
#
# windows builds anywhere (no cgo). macos needs a Mac: the window uses Cocoa and WebKit through cgo.
set -eu

target=${1:?usage: scripts/package.sh windows|macos|sums [version] [outdir]}
version=${2:-dev}
out=${3:-dist}
cd "$(dirname "$0")/.."
mkdir -p "$out"
ldflags="-s -w -X main.version=$version"
# Each build is told which release file it is (main.updateAsset), so it can fetch its own next version. A version
# that isn't a release's ("dev", "ci-1a2b3c4") never updates, whatever it's told.
asset() { echo "-X main.updateAsset=$1"; }

case "$target" in
windows)
	# Each .exe carries its icon, version information and manifest (scripts/winres): a .syso in the package
	# folder that go build links in, written for each file name and removed afterwards.
	syso=cmd/everlake-launcher/rsrc_windows_amd64.syso
	trap 'rm -f "$syso"' EXIT
	# -H=windowsgui: a GUI program, so a double-click opens the window and no console with it.
	go run ./scripts/winres -version "$version" -name everlake-launcher-windows-amd64.exe -out "$syso"
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath \
		-ldflags "$ldflags $(asset everlake-launcher-windows-amd64.exe) -H=windowsgui" \
		-o "$out/everlake-launcher-windows-amd64.exe" ./cmd/everlake-launcher
	# The same program as a console one: a terminal waits for it, and play can ask for the password.
	go run ./scripts/winres -version "$version" -name everlake-launcher-cli-windows-amd64.exe -out "$syso"
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath \
		-ldflags "$ldflags $(asset everlake-launcher-cli-windows-amd64.exe)" \
		-o "$out/everlake-launcher-cli-windows-amd64.exe" ./cmd/everlake-launcher
	;;
macos)
	[ "$(uname -s)" = Darwin ] || { echo "macos: build this on a Mac (cgo: Cocoa + WebKit)" >&2; exit 1; }
	work=$(mktemp -d)
	trap 'rm -rf "$work"' EXIT
	# The oldest macOS the Go toolchain building this supports (Go 1.24: 11, 1.25-1.26: 12, 1.27: 13); the cgo part
	# is built for the same, and the app says so.
	minor=$(go env GOVERSION | sed -E 's/^go1\.([0-9]+).*/\1/')
	if [ "$minor" -le 24 ]; then macos=11.0; elif [ "$minor" -le 26 ]; then macos=12.0; else macos=13.0; fi
	export MACOSX_DEPLOYMENT_TARGET=$macos
	for arch in arm64 amd64; do
		CGO_ENABLED=1 GOOS=darwin GOARCH=$arch go build -trimpath -ldflags "$ldflags $(asset Everlake-Launcher-macos.zip)" \
			-o "$work/everlake-launcher-$arch" ./cmd/everlake-launcher
	done
	app="$work/Everlake Launcher.app"
	mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
    clang -x c -O2 -dynamiclib -arch arm64 -arch x86_64 \
        internal/gamepatch/macos/keypatch.c.txt -o "$app/Contents/Resources/keypatch.dylib"
    codesign --force --sign - "$app/Contents/Resources/keypatch.dylib"
	# The app icon, from assets/icon-macos.png (rendered from assets/icon.html) in every size macOS uses.
	iconset=$work/AppIcon.iconset
	mkdir "$iconset"
	for s in 16 32 128 256 512; do
		sips -z $s $s assets/icon-macos.png --out "$iconset/icon_${s}x${s}.png" >/dev/null
		sips -z $((s * 2)) $((s * 2)) assets/icon-macos.png --out "$iconset/icon_${s}x${s}@2x.png" >/dev/null
	done
	iconutil -c icns -o "$app/Contents/Resources/AppIcon.icns" "$iconset"
	lipo -create -output "$app/Contents/MacOS/everlake-launcher" \
		"$work/everlake-launcher-arm64" "$work/everlake-launcher-amd64"
	cat >"$app/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key><string>Everlake Launcher</string>
	<key>CFBundleDisplayName</key><string>Everlake Launcher</string>
	<key>CFBundleExecutable</key><string>everlake-launcher</string>
	<key>CFBundleIconFile</key><string>AppIcon</string>
	<key>CFBundleIdentifier</key><string>com.buildthehomelab.everlake-launcher</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>$version</string>
	<key>CFBundleVersion</key><string>$version</string>
	<key>CFBundleInfoDictionaryVersion</key><string>6.0</string>
	<key>LSMinimumSystemVersion</key><string>$macos</string>
	<key>LSApplicationCategoryType</key><string>public.app-category.games</string>
	<key>NSHighResolutionCapable</key><true/>
	<key>NSMicrophoneUsageDescription</key><string>World of Warcraft launched by Everlake uses your microphone for in-game voice chat.</string>
	<key>NSSupportsAutomaticGraphicsSwitching</key><true/>
	<key>NSAppTransportSecurity</key>
	<dict><key>NSAllowsLocalNetworking</key><true/></dict>
</dict>
</plist>
EOF
	/usr/libexec/PlistBuddy -c "Print :NSMicrophoneUsageDescription" "$app/Contents/Info.plist" >/dev/null
	# Not notarised: an ad-hoc signature, which Apple silicon needs to run it at all.
	codesign --force --sign - "$app"
	codesign --verify --strict "$app"
	rm -f "$out/Everlake-Launcher-macos.zip" "$out/Everlake-Launcher-macos.dmg"
	ditto -c -k --keepParent "$app" "$out/Everlake-Launcher-macos.zip"
	mkdir "$work/dmg"
	cp -R "$app" "$work/dmg/"
	ln -s /Applications "$work/dmg/Applications"
	hdiutil create -quiet -volname "Everlake Launcher" -srcfolder "$work/dmg" -ov -format UDZO \
		"$out/Everlake-Launcher-macos.dmg"
	;;
sums)
	# SHA256SUMS.txt for every file in the folder: a launcher only puts in an update that matches it.
	cd "$out"
	: >SHA256SUMS.txt.tmp
	for f in *.exe *.zip *.dmg; do
		if [ -f "$f" ]; then shasum -a 256 -- "$f" >>SHA256SUMS.txt.tmp; fi
	done
	mv SHA256SUMS.txt.tmp SHA256SUMS.txt
	cat SHA256SUMS.txt
	exit 0
	;;
*)
	echo "usage: scripts/package.sh windows|macos|sums [version] [outdir]" >&2
	exit 2
	;;
esac
ls -l "$out"
