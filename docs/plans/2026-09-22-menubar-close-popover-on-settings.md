# Implementation Plan: Close Menubar Popover When Opening Settings

## Tasks
1. Write failing test in `apps/menubar/Tests/SwarmBarTests/PopoverRenderTests.swift` testing `StatusItemWatcher.dismissPopover()`.
2. Implement `StatusItemWatcher.statusItem()` and `StatusItemWatcher.dismissPopover()` in `apps/menubar/Sources/SwarmBarUI/StatusItemWatcher.swift`.
3. Update `PopoverHost.openSettings` in `apps/menubar/Sources/SwarmBar/App.swift` to call `StatusItemWatcher.dismissPopover()`.
4. Run `swift test` in `apps/menubar` and ensure all tests pass.
5. Run `apps/menubar/scripts/smoke.sh` to verify build and runtime smoke test.
