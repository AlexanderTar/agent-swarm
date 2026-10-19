#!/usr/bin/env bash
# Rebuilds the app icon headlessly from the committed sources: renders
# Resources/AppIconLayers/Glyph.svg to Glyph.png, copies the layers into the
# Icon Composer document Resources/Swarm.icon, renders the real Liquid Glass icon
# with Xcode's ictool, and packs Resources/Swarm.icns (16-1024 px) plus
# AppIconLayers/Composed-1024.png (the full-bleed Icon Composer export).
# Needs Xcode (ictool ships inside Icon Composer.app) and rsvg-convert (brew install librsvg).
set -euo pipefail

res="$(cd "$(dirname "$0")/../Resources" && pwd)"
layers="$res/AppIconLayers"
ictool="$(xcode-select -p)/../Applications/Icon Composer.app/Contents/Executables/ictool"
[ -x "$ictool" ] || { echo "ictool not found at $ictool" >&2; exit 1; }
command -v rsvg-convert >/dev/null || { echo "rsvg-convert not found (brew install librsvg)" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

rsvg-convert -w 1024 -h 1024 "$layers/Glyph.svg" -o "$layers/Glyph.png"
cp "$layers/Background.png" "$layers/Glyph.png" "$res/Swarm.icon/Assets/"

render() { # <tile px> <output png>
  "$ictool" "$res/Swarm.icon" --export-image --output-file "$2" --platform macOS \
    --rendition Default --width "$1" --height "$1" --scale 1 >/dev/null
}

render 1024 "$layers/Composed-1024.png"

# icns art sits inside the standard macOS margins: an 824 px tile on a 1024 px
# canvas, rendered at its final size so the glass lighting stays crisp. The tile
# keeps the canvas parity so the margins are even.
iconset="$work/Swarm.iconset"
mkdir "$iconset"
for px in 16 32 64 128 256 512 1024; do
  tile=$(( (px * 824 + 512) / 1024 ))
  [ $(( (px - tile) % 2 )) -eq 0 ] || tile=$(( tile + 1 ))
  render "$tile" "$work/tile-$px.png"
  sips --padToHeightWidth "$px" "$px" "$work/tile-$px.png" --out "$work/icon-$px.png" >/dev/null
done
for spec in 16:16x16 32:16x16@2x 32:32x32 64:32x32@2x 128:128x128 256:128x128@2x \
            256:256x256 512:256x256@2x 512:512x512 1024:512x512@2x; do
  cp "$work/icon-${spec%%:*}.png" "$iconset/icon_${spec#*:}.png"
done
iconutil -c icns "$iconset" -o "$res/Swarm.icns"
echo "built $res/Swarm.icns"
