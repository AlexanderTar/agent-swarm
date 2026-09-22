# Spec: Close Menubar Popover When Opening Settings

## Context
When clicking the Settings gear icon in the Swarm menubar popover, the Settings window opens, but the MenuBarExtra popover remains visible on screen, occluding parts of the desktop and requiring the user to click outside or on the status item to dismiss it.

## Root Cause
SwiftUI's `MenuBarExtra(..., content: ...).menuBarExtraStyle(.window)` does not automatically dismiss when an internal button triggers `@Environment(\.openSettings)()`. Because the click occurs inside the popover and the app remains active, macOS AppKit does not receive an outside click or deactivation event to trigger dismissal.

## Locked Decisions
1. In `SwarmBarUI/StatusItemWatcher.swift`, provide `StatusItemWatcher.dismissPopover()`.
2. `dismissPopover()` retrieves the `NSStatusItem` associated with `NSStatusBarWindow` (via Key-Value Coding `value(forKey: "statusItem")` or Mirror reflection).
3. If `statusItem.button` is present, it invokes `button.performClick(nil)`. This simulates clicking the status item, which informs SwiftUI's internal presentation machinery to close the popover and cleanly resets its presentation state (avoiding the "two clicks to reopen" bug that direct `orderOut` causes).
4. If `statusItem.button` cannot be found, it falls back to calling `orderOut(nil)` on any window matching `MenuBarExtraWindow`.
5. In `SwarmBar/App.swift`, `PopoverHost.openSettings` calls `StatusItemWatcher.dismissPopover()` before activating the app and opening Settings.

## File Changes
- `apps/menubar/Sources/SwarmBarUI/StatusItemWatcher.swift`: Add `statusItem()` and `dismissPopover()`.
- `apps/menubar/Sources/SwarmBar/App.swift`: Call `StatusItemWatcher.dismissPopover()` in `openSettings`.
- `apps/menubar/Tests/SwarmBarTests/PopoverRenderTests.swift`: Add unit test for `dismissPopover()`.

## Verification
- `swift test` in `apps/menubar`.
- `apps/menubar/scripts/smoke.sh`.
