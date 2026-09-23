#!/usr/bin/env bash
# Verifies a built Swarm.app: layout, Info.plist keys, icons and signature.
# Usage: apps/menubar/scripts/check-bundle.sh <path to Swarm.app>
set -euo pipefail
app="$1"
plist="$app/Contents/Info.plist"
fail() { echo "check-bundle: $*" >&2; exit 1; }

[ -x "$app/Contents/MacOS/Swarm" ] || fail "missing executable"
plutil -lint "$plist" >/dev/null || fail "Info.plist does not lint"
[ "$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$plist")" = "dev.swarm.menubar" ] || fail "bundle id"
[ "$(/usr/libexec/PlistBuddy -c 'Print :LSUIElement' "$plist")" = "true" ] || fail "LSUIElement"
/usr/libexec/PlistBuddy -c 'Print :NSAppleEventsUsageDescription' "$plist" >/dev/null || fail "NSAppleEventsUsageDescription"
for icon in claude codex agy cursor muse swarm; do
  [ -f "$app/Contents/Resources/$icon.svg" ] || fail "missing $icon.svg"
done
codesign --verify --strict "$app" || fail "signature does not verify"
info="$(codesign -dv "$app" 2>&1)"
grep -qx 'Identifier=dev.swarm.menubar' <<<"$info" || fail "signing identifier"
echo "check-bundle: ok"
