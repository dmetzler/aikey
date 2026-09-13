#!/usr/bin/env bash
#
# Offline checks on the built bundle. Catches the class of bug where the app
# launches and silently does nothing.
#
#   ./packaging/macos/bundle_test.sh dist/aikey.app
#
set -euo pipefail

APP="${1:-dist/aikey.app}"
fail() { echo "FAIL: $*" >&2; exit 1; }

[[ -d "$APP" ]] || fail "no bundle at $APP"

PLIST="$APP/Contents/Info.plist"
[[ -f "$PLIST" ]] || fail "missing Info.plist"

# The single most important invariant: CFBundleExecutable must name a file that
# actually exists and is executable. Finder runs exactly this and nothing else.
EXEC=$(/usr/libexec/PlistBuddy -c "Print :CFBundleExecutable" "$PLIST")
BIN="$APP/Contents/MacOS/$EXEC"
[[ -f "$BIN" ]] || fail "CFBundleExecutable=$EXEC but $BIN does not exist"
[[ -x "$BIN" ]] || fail "$BIN is not executable"
echo "ok  CFBundleExecutable -> $EXEC"

# A bundled launch passes no arguments. Run the binary from inside the bundle
# with none and confirm it does NOT print usage -- that was the original bug.
OUT=$("$BIN" 2>&1 &
      sleep 1; kill %1 2>/dev/null) || true
if grep -qi "^aikey - keep a short-lived" <<<"$OUT"; then
  fail "no-arg launch printed usage; it should default to serve inside a bundle"
fi
echo "ok  no-arg launch does not print usage"

ICON=$(/usr/libexec/PlistBuddy -c "Print :CFBundleIconFile" "$PLIST")
[[ -f "$APP/Contents/Resources/$ICON.icns" ]] || fail "missing icon $ICON.icns"
echo "ok  icon present"

/usr/libexec/PlistBuddy -c "Print :LSUIElement" "$PLIST" | grep -qi true \
  || fail "LSUIElement must be true for a menu bar agent"
echo "ok  LSUIElement"

codesign --verify --deep --strict "$APP" 2>/dev/null \
  && echo "ok  signature valid" \
  || echo "warn: signature not valid (ad-hoc builds are still runnable)"

echo
echo "Bundle looks sane. Launch it with: open $APP"
