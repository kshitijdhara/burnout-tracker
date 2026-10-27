#!/bin/sh
# Wraps the signed universal binary in BurntOut.app inside a .dmg
# (ADR-0005). GoReleaser runs it as a universal-binary post hook, on macOS.
#
# usage: build-dmg.sh BINARY VERSION OUT.dmg
#
# The .app gives the agent a bundle identity for macOS permissions (TCC).
# It is ad-hoc signed here; `burntout setup-signing` re-signs it locally so
# permissions survive updates. Opening it starts nothing: collection starts
# only after `burntout setup` and `burntout service install` (AC-2).
set -eu

bin=$1 version=$2 out=$3
root=$(cd "$(dirname "$0")/../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

stage=$work/stage
app=$stage/BurntOut.app
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
cp "$bin" "$app/Contents/MacOS/burntout"

# Icon: the flattened 1024 px app icon, scaled into an iconset.
set_dir=$work/burntout.iconset
mkdir -p "$set_dir"
for s in 16 32 128 256 512; do
	sips -z $s $s "$root/assets/icon/app/icon-1024.png" --out "$set_dir/icon_${s}x${s}.png" >/dev/null
	d=$((s * 2))
	sips -z $d $d "$root/assets/icon/app/icon-1024.png" --out "$set_dir/icon_${s}x${s}@2x.png" >/dev/null
done
iconutil -c icns -o "$app/Contents/Resources/burntout.icns" "$set_dir"

cat >"$app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleIdentifier</key>
	<string>io.github.kshitijdhara.burntout</string>
	<key>CFBundleName</key>
	<string>BurntOut</string>
	<key>CFBundleExecutable</key>
	<string>burntout</string>
	<key>CFBundleIconFile</key>
	<string>burntout</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>$version</string>
	<key>CFBundleVersion</key>
	<string>$version</string>
	<key>LSMinimumSystemVersion</key>
	<string>13.0</string>
	<key>LSUIElement</key>
	<true/>
</dict>
</plist>
PLIST

codesign --force --sign - --identifier io.github.kshitijdhara.burntout "$app"

cat >"$stage/README.txt" <<TXT
BurntOut $version

1. Drag BurntOut.app to Applications.
2. Put the command on your PATH, for example:
     mkdir -p ~/.local/bin
     ln -sf /Applications/BurntOut.app/Contents/MacOS/burntout ~/.local/bin/burntout
3. Nothing is recorded yet. When you're ready:
     burntout setup             review what is collected and consent
     burntout service install   run in the background from login

The app isn't notarized. If macOS blocks it, open
System Settings > Privacy & Security and choose "Open Anyway".
TXT
ln -s /Applications "$stage/Applications"

rm -f "$out"
hdiutil create -quiet -volname "BurntOut $version" -srcfolder "$stage" -fs HFS+ -format UDZO "$out"
