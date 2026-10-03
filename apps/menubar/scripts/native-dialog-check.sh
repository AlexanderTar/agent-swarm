#!/usr/bin/env bash
# Exercise actual menu callbacks and scenes in a fixture-only App.swift build.
set -euo pipefail
pkg="$(cd "$(dirname "$0")/.." && pwd)"
swift build --package-path "$pkg"
bin="$(swift build --package-path "$pkg" --show-bin-path)"
probe="$(mktemp -d "$pkg/.build/native-dialog-check.XXXXXX")"
trap 'rm -rf "$probe"' EXIT
python3 - "$pkg/Sources/SwarmBar/App.swift" "$probe/App.swift" <<'PY'
import sys
from pathlib import Path
source = Path(sys.argv[1]).read_text()
source = source.replace('struct SwarmBarApp: App {', 'struct SwarmBarApp: App {\n    init() { NativeDialogCheck.start() }', 1)
source = source.replace('openNewOrchestrator: { model.requestNewOrchestrator(); openCentered("new-orchestrator") },', 'openNewOrchestrator: NativeDialogCheck.register("new") { model.requestNewOrchestrator(); openCentered("new-orchestrator") },', 1)
source = source.replace('openBoardHandoff: { name in', 'openBoardHandoff: NativeDialogCheck.registerBoard { name in', 1)
source = source.replace('openSettings: {', 'openSettings: NativeDialogCheck.register("settings") {', 1)
Path(sys.argv[2]).write_text(source)
PY
swiftc -swift-version 6 -parse-as-library -I "$bin/Modules" \
  "$probe/App.swift" "$pkg/Sources/SwarmBar/SystemServices.swift" "$pkg/Tests/NativeDialogCheck/Driver.swift" \
  "$bin"/SwarmBarUI.build/*.swift.o "$bin"/SwarmBarKit.build/*.swift.o -o "$probe/check"
SWARM_MOCK_FIXTURES="$pkg/Tests/Fixtures" "$probe/check"
