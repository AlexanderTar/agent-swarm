#!/usr/bin/env bash
# Builds Swarm.app from the SwarmBar package (spec §21.1).
# Usage: apps/menubar/scripts/bundle.sh [output .app path]   (default: apps/menubar/.build/Swarm.app)
# Signing: ad-hoc by default. macOS keys Automation and notification permissions to the
# signature, so an ad-hoc rebuild asks again. Set SWARM_SIGN_IDENTITY to a stable
# self-signed code-signing certificate name to keep them.
set -euo pipefail
pkg="$(cd "$(dirname "$0")/.." && pwd)"
root="$(cd "$pkg/../.." && pwd)"
out="${1:-$pkg/.build/Swarm.app}"
identity="${SWARM_SIGN_IDENTITY:--}"

swift build --package-path "$pkg" -c release --product SwarmBar
bin="$(swift build --package-path "$pkg" -c release --show-bin-path)/SwarmBar"

rm -rf "$out"
mkdir -p "$out/Contents/MacOS" "$out/Contents/Resources"
cp "$bin" "$out/Contents/MacOS/Swarm"
cp "$pkg/Resources/Info.plist" "$out/Contents/Info.plist"
cp "$pkg/Resources/Swarm.icns" "$out/Contents/Resources/Swarm.icns"
for icon in claude codex agy cursor muse swarm; do
  cp "$root/assets/icons/$icon.svg" "$out/Contents/Resources/$icon.svg"
done
plutil -lint "$out/Contents/Info.plist" >/dev/null
codesign --force --sign "$identity" --identifier dev.swarm.menubar "$out"
echo "$out"
