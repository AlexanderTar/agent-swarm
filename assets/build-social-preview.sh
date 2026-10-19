#!/usr/bin/env bash
# Renders assets/social-preview.html to assets/social-preview.png (1280x640, the GitHub
# social preview size) with headless Chrome. Run apps/menubar/scripts/build-icon.sh first
# if the icon changed. Set CHROME to use a different browser binary.
set -euo pipefail
dir="$(cd "$(dirname "$0")" && pwd)"
chrome="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
"$chrome" --headless=new --disable-gpu --hide-scrollbars --force-device-scale-factor=1 \
  --window-size=1280,640 --screenshot="$dir/social-preview.png" "file://$dir/social-preview.html" >/dev/null 2>&1
sips -g pixelWidth -g pixelHeight "$dir/social-preview.png"
