#!/usr/bin/env bash
#
# Build "Comma Sync.app" from App.swift.
# Requires macOS with the Xcode command line tools (`xcode-select --install`).
#
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$DIR/.." && pwd)"
APP="$ROOT/Comma Sync.app"
# App version (used for the in-app "update available" check). Override per build:
#   APP_VERSION=1.0.1 bash macos-app/build.sh
APP_VERSION="${APP_VERSION:-1.0.3}"

echo "==> Compiling App.swift"
swiftc -parse-as-library -O "$DIR/App.swift" -o "$DIR/CommaSync" \
  -framework SwiftUI -framework AppKit

echo "==> Building app icon"
rm -rf "$DIR/icon.iconset" "$DIR/icon.icns"; mkdir "$DIR/icon.iconset"
for sz in 16 32 128 256 512; do
  sips -z $sz $sz "$DIR/icon_1024.png" --out "$DIR/icon.iconset/icon_${sz}x${sz}.png" >/dev/null
  d=$((sz*2)); sips -z $d $d "$DIR/icon_1024.png" --out "$DIR/icon.iconset/icon_${sz}x${sz}@2x.png" >/dev/null
done
iconutil -c icns "$DIR/icon.iconset" -o "$DIR/icon.icns"

echo "==> Assembling $APP"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
mv "$DIR/CommaSync" "$APP/Contents/MacOS/CommaSync"
cp "$ROOT/comma-sync.sh" "$APP/Contents/Resources/comma-sync.sh"
cp "$DIR/icon.icns" "$APP/Contents/Resources/icon.icns"
chmod +x "$APP/Contents/Resources/comma-sync.sh"

cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>Comma Sync</string>
  <key>CFBundleDisplayName</key><string>Comma Sync</string>
  <key>CFBundleExecutable</key><string>CommaSync</string>
  <key>CFBundleIconFile</key><string>icon</string>
  <key>CFBundleIdentifier</key><string>com.example.commasync</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>${APP_VERSION}</string>
  <key>CFBundleVersion</key><string>${APP_VERSION}</string>
  <key>CFBundleInfoDictionaryVersion</key><string>6.0</string>
  <key>LSMinimumSystemVersion</key><string>13.0</string>
  <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
PLIST

# Ad-hoc sign so Gatekeeper allows it to launch locally.
codesign --force --deep -s - "$APP" 2>/dev/null || true

echo "==> Done. Built: $APP"
echo "    Keep it next to comma-sync.sh (it runs the sibling script)."
