#!/usr/bin/env bash
#
# Build aikey.app, a native macOS bundle.
#
# Run this ON macOS: the tray needs cgo, which does not cross-compile from
# Linux. Produces a universal binary when both Go toolchains are present.
#
#   ./packaging/macos/build_app.sh              # -> dist/aikey.app
#   SIGN_ID="Developer ID Application: ..." ./packaging/macos/build_app.sh
#
set -euo pipefail

cd "$(dirname "$0")/../.."

APP_NAME="aikey"
BUNDLE_ID="io.github.dmetzler.aikey"
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo 0.1.0)}"
DIST="dist"
APP="$DIST/$APP_NAME.app"
ICNS="packaging/macos/aikey.icns"

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "error: this must run on macOS (the tray needs cgo)." >&2
  exit 1
fi

echo "==> building $APP_NAME $VERSION"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"

# Universal binary when possible; a single arch is a fine fallback.
build_arch() {
  local arch="$1" out="$2"
  CGO_ENABLED=1 GOOS=darwin GOARCH="$arch" \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$out" ./cmd/aikey
}

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if build_arch arm64 "$TMP/aikey-arm64" 2>/dev/null && \
   build_arch amd64 "$TMP/aikey-amd64" 2>/dev/null; then
  echo "==> universal binary (arm64 + amd64)"
  lipo -create -output "$APP/Contents/MacOS/$APP_NAME" \
    "$TMP/aikey-arm64" "$TMP/aikey-amd64"
else
  echo "==> single-arch binary ($(go env GOARCH))"
  CGO_ENABLED=1 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" \
    -o "$APP/Contents/MacOS/$APP_NAME" ./cmd/aikey
fi
chmod +x "$APP/Contents/MacOS/$APP_NAME"

if [[ ! -f "$ICNS" ]]; then
  echo "==> generating icon"
  python3 packaging/macos/make_icon.py --out "$ICNS"
fi
cp "$ICNS" "$APP/Contents/Resources/$APP_NAME.icns"

# LSUIElement=true keeps aikey out of the Dock and the app switcher: it is a
# menu bar agent, and a Dock tile for a background token holder is just noise.
cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key>                  <string>$APP_NAME</string>
	<key>CFBundleDisplayName</key>           <string>$APP_NAME</string>
	<key>CFBundleIdentifier</key>            <string>$BUNDLE_ID</string>
	<key>CFBundleVersion</key>               <string>$VERSION</string>
	<key>CFBundleShortVersionString</key>    <string>$VERSION</string>
	<key>CFBundleExecutable</key>            <string>$APP_NAME</string>
	<key>CFBundleIconFile</key>              <string>$APP_NAME</string>
	<key>CFBundlePackageType</key>           <string>APPL</string>
	<key>LSMinimumSystemVersion</key>        <string>11.0</string>
	<key>LSUIElement</key>                   <true/>
	<key>NSHighResolutionCapable</key>       <true/>
	<key>NSHumanReadableCopyright</key>      <string>MIT</string>
</dict>
</plist>
PLIST

# No wrapper script: CFBundleExecutable must name the real binary, and the
# binary itself defaults to `serve` when it detects it is running from a bundle.
# (An earlier version shipped a `launch` wrapper that Finder never called, so a
# double-click just printed usage to a stderr nobody could see.)

if [[ -n "${SIGN_ID:-}" ]]; then
  echo "==> signing as $SIGN_ID"
  codesign --force --deep --options runtime --timestamp \
    --sign "$SIGN_ID" "$APP"
  codesign --verify --strict --verbose=2 "$APP"
else
  # Ad-hoc signature. Without it, Gatekeeper rejects the app outright on Apple
  # Silicon; with it, the user still gets the "unidentified developer" prompt
  # once, which is expected for an unnotarised build.
  echo "==> ad-hoc signing (set SIGN_ID to use a Developer ID)"
  codesign --force --deep --sign - "$APP"
fi

echo
echo "built $APP"
echo
echo "Install:   cp -R $APP /Applications/"
echo "Run:       open $APP"
echo "CLI:       $APP/Contents/MacOS/aikey status"
echo
echo "First run needs configuration:"
echo "  $APP/Contents/MacOS/aikey init"
echo
echo "Unsigned builds: macOS will warn on first launch."
echo "Right-click the app and choose Open, or: xattr -dr com.apple.quarantine $APP"
