#!/usr/bin/env bash
# Menubar coverage gate (spec §23.3): 80 % line coverage for logic files.
# Logic = Sources/SwarmBarKit. Views (Sources/SwarmBarUI) and the app entry (Sources/SwarmBar) are excluded.
# Usage: apps/menubar/scripts/cover.sh [minimum percent, default 80]
set -euo pipefail
pkg="$(cd "$(dirname "$0")/.." && pwd)"
min="${1:-80}"
swift test --package-path "$pkg" --enable-code-coverage
bin="$(swift build --package-path "$pkg" --show-bin-path)"
# SwiftPM names the bundle SwarmBarPackageTests.xctest; Swift 6.4 builds SwarmBarTests.xctest. Take the newest.
bundle="$(ls -td "$bin"/*Tests.xctest 2>/dev/null | head -1)"
if [[ -z "$bundle" ]]; then
  echo "menubar coverage gate: no *Tests.xctest bundle under $bin"
  exit 1
fi
report="$(xcrun llvm-cov report "$bundle/Contents/MacOS/$(basename "$bundle" .xctest)" \
  -instr-profile "$bin/codecov/default.profdata" \
  -ignore-filename-regex='(/Tests/|/\.build/|/Sources/SwarmBarUI/|/Sources/SwarmBar/)')"
echo "$report"
pct="$(awk '/^TOTAL/ { sub("%", "", $10); print $10 }' <<<"$report")"
if awk -v p="$pct" -v m="$min" 'BEGIN { exit !(p < m) }'; then
  echo "menubar coverage gate: $pct% of logic lines, needs $min%"
  exit 1
fi
echo "menubar coverage gate: passed ($pct%)"
