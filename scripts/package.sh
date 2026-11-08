#!/bin/sh
# Builds the launcher's release files into dist/ (or the folder given).
#
#   scripts/package.sh windows [version] [outdir]   everlake-launcher-windows-amd64.exe     the app: a window, no console
#                                                    everlake-launcher-cli-windows-amd64.exe for typing commands
#   scripts/package.sh macos   [version] [outdir]   Everlake-Launcher-macos.zip and .dmg: "Everlake Launcher.app",
#                                                    one universal binary (Apple silicon + Intel)
#
# windows builds anywhere (no cgo). macos needs a Mac: the window uses Cocoa and WebKit through cgo.
set -eu

target=${1:?usage: scripts/package.sh windows|macos [version] [outdir]}
version=${2:-dev}
out=${3:-dist}
cd "$(dirname "$0")/.."
mkdir -p "$out"
ldflags="-s -w -X main.version=$version"

case "$target" in
windows)
	# -H=windowsgui: a GUI program, so a double-click opens the window and no console with it.
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$ldflags -H=windowsgui" \
		-o "$out/everlake-launcher-windows-amd64.exe" ./cmd/everlake-launcher
	# The same program as a console one: a terminal waits for it, and play can ask for the password.
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$ldflags" \
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
		CGO_ENABLED=1 GOOS=darwin GOARCH=$arch go build -trimpath -ldflags "$ldflags" \
			-o "$work/everlake-launcher-$arch" ./cmd/everlake-launcher
	done
	app="$work/Everlake Launcher.app"
	mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
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
	<key>CFBundleIdentifier</key><string>com.buildthehomelab.everlake-launcher</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>$version</string>
	<key>CFBundleVersion</key><string>$version</string>
	<key>CFBundleInfoDictionaryVersion</key><string>6.0</string>
	<key>LSMinimumSystemVersion</key><string>$macos</string>
	<key>LSApplicationCategoryType</key><string>public.app-category.games</string>
	<key>NSHighResolutionCapable</key><true/>
	<key>NSSupportsAutomaticGraphicsSwitching</key><true/>
	<key>NSAppTransportSecurity</key>
	<dict><key>NSAllowsLocalNetworking</key><true/></dict>
</dict>
</plist>
EOF
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
*)
	echo "usage: scripts/package.sh windows|macos [version] [outdir]" >&2
	exit 2
	;;
esac
ls -l "$out"
