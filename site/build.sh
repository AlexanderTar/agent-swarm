#!/bin/sh
# Build the swarmrun.dev site into site/dist/.
# Copies site/ plus the generated tokens, fonts and logo; the page uses
# tokens only. dist/install.sh is the one-line installer served at /install.sh,
# dist/social-preview.png the social card served at /social-preview.png (og:image).
# The card's tracked source is site/assets/social-preview.png (kept in sync with
# assets/social-preview.png by assets/build-social-preview.sh) so that a
# social-card-only commit still touches site/ and Vercel's ignoreCommand deploys it.
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
cp "$ROOT/site/assets/social-preview.png" "$DIST/social-preview.png"

printf 'site/dist built\n'
