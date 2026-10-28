#!/bin/sh
# Build the swarmrun.dev site into site/dist/.
# Copies site/ plus the generated tokens, fonts and logo; the page uses
# tokens only. dist/install.sh is the one-line installer served at /install.sh.
set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIST="$ROOT/site/dist"

rm -rf "$DIST"
mkdir -p "$DIST/assets" "$DIST/fonts"

cp "$ROOT/site/index.html" "$ROOT/site/styles.css" "$DIST/"
cp "$ROOT/design/tokens.css" "$DIST/tokens.css"
cp "$ROOT/design/fonts/"*.woff2 "$DIST/fonts/"
cp "$ROOT/site/assets/"* "$DIST/assets/"
cp "$ROOT/design/assets/logo/app-icon.svg" "$DIST/assets/app-icon.svg"
cp "$ROOT/site/install.sh" "$DIST/install.sh"

printf 'site/dist built\n'
