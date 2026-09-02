#!/usr/bin/env bash
set -euo pipefail

REPO="${SWARM_REPO:-https://github.com/alexandertar/agent-swarm.git}"
SWARM_HOME="${SWARM_HOME:-$HOME/.swarm}"

echo "Agent Swarm installer"
echo "====================="
mkdir -p "$SWARM_HOME/app/releases"

# Find latest semver tag
LATEST_TAG=$(git ls-remote --tags --refs "$REPO" 2>/dev/null | awk -F/ '{print $NF}' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -1)
if [ -z "$LATEST_TAG" ]; then
  echo "No semver tags found; cloning main branch instead."
  LATEST_TAG="main"
  RELEASE_DIR="$SWARM_HOME/app/releases/main"
  rm -rf "$RELEASE_DIR"
  git clone --depth 1 --branch main "$REPO" "$RELEASE_DIR"
else
  RELEASE_DIR="$SWARM_HOME/app/releases/$LATEST_TAG"
  rm -rf "$RELEASE_DIR"
  git clone --depth 1 --branch "$LATEST_TAG" "$REPO" "$RELEASE_DIR"
fi

cd "$RELEASE_DIR"
# pnpm.onlyBuiltDependencies in package.json compiles better-sqlite3 during install.
pnpm install --frozen-lockfile 2>/dev/null || pnpm install
pnpm build

# Atomic symlink swap
TMP_LINK="$SWARM_HOME/app/current.new"
ln -sfn "$RELEASE_DIR" "$TMP_LINK"
mv -f "$TMP_LINK" "$SWARM_HOME/app/current"

echo "Installed to $RELEASE_DIR"
echo "Finishing setup (launchd, plugins, models)..."

# Bootstrap always uses non-interactive defaults so curl|sh and post-build
# installs complete without waiting on @clack prompts. Re-run `swarm install`
# without --yes to customize agents, port, or auto-update.
INSTALL_ARGS=(install --from-bootstrap --yes)
if [ ! -t 0 ] || [ ! -t 1 ]; then
  echo "(non-interactive stdin/stdout — using defaults for all detected agents)"
fi

exec node "$RELEASE_DIR/packages/cli/dist/index.js" "${INSTALL_ARGS[@]}"
