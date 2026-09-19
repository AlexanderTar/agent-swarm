#!/usr/bin/env bash
# Menubar coverage gate (spec §23.3): 80 % line coverage for logic files.
# Logic = Sources/SwarmBarKit. Views (Sources/SwarmBarUI) and the app entry (Sources/SwarmBar) are excluded.
# Usage: apps/menubar/scripts/cover.sh [minimum percent, default 80]
set -euo pipefail
pkg="$(cd "$(dirname "$0")/.." && pwd)"
min="${1:-80}"
swift test --package-path "$pkg" --enable-code-coverage
bin="$(swift build --package-path "$pkg" --show-bin-path)"
report="$(xcrun llvm-cov report "$bin/SwarmBarPackageTests.xctest/Contents/MacOS/SwarmBarPackageTests" \
  -instr-profile "$bin/codecov/default.profdata" \
  -ignore-filename-regex='(/Tests/|/\.build/|/Sources/SwarmBarUI/|/Sources/SwarmBar/)')"
echo "$report"
pct="$(awk '/^TOTAL/ { sub("%", "", $10); print $10 }' <<<"$report")"
if awk -v p="$pct" -v m="$min" 'BEGIN { exit !(p < m) }'; then
  echo "menubar coverage gate: $pct% of logic lines, needs $min%"
  exit 1
fi
echo "menubar coverage gate: passed ($pct%)"
