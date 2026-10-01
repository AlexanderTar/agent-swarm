import AppKit
import SwiftUI
import SwarmBarUI

/// Runs inside a fixture-only build of the actual App.swift scenes. It registers the
/// production menu callbacks, rather than duplicating their presentation logic.
@MainActor
enum NativeDialogCheck {
    static var actions: [String: () -> Void] = [:]
    static var boardAction: ((String?) -> Void)?
    static var failures = 0

    static func register(_ name: String, action: @escaping () -> Void) -> () -> Void {
        actions[name] = action
        return action
    }

    static func registerBoard(action: @escaping (String?) -> Void) -> (String?) -> Void {
        boardAction = action
        return action
    }

    static func check(_ condition: Bool, _ message: String) {
        print("\(condition ? "PASS" : "FAIL"): \(message)")
        if !condition { failures += 1 }
    }

    static var popover: NSWindow? {
        NSApp.windows.first { String(describing: type(of: $0)).contains("MenuBarExtraWindow") }
    }

    static func openMenuOnce() {
        guard let item = StatusItemWatcher.statusItem() else { check(false, "status item exists"); return }
        if item.button?.action != nil {
            item.button?.performClick(nil)
        } else {
            // macOS 27 moves status-item input to NSSceneStatusItem. Its proxy button
            // has no target/action. This private selector is ONLY in the test driver:
            // it requests the same expanded scene as one status-item click, without
            // granting the test external Accessibility automation permission.
            let selector = NSSelectorFromString("_requestExpandedInterfaceSession")
            guard item.responds(to: selector) else { check(false, "menu input available"); return }
            item.perform(selector)
        }
    }

    static func start() {
        Task { @MainActor in
            try? await Task.sleep(for: .milliseconds(600))
            for name in ["new", "board", "board-selected", "settings"] {
                openMenuOnce()
                try? await Task.sleep(for: .milliseconds(400))
                check(popover?.isVisible == true, "\(name): one request opens menu")
                if name == "board" || name == "board-selected" {
                    check(boardAction != nil, "\(name): callback registered")
                    boardAction?(name == "board-selected" ? "alpha" : nil)
                } else {
                    check(actions[name] != nil, "\(name): callback registered")
                    actions[name]?()
                }
                try? await Task.sleep(for: .milliseconds(600))
                check(popover?.isVisible != true, "\(name): dialog dismisses menu")
                let dialogs = NSApp.windows.filter {
                    $0.isVisible && !String(describing: type(of: $0)).contains("StatusBar")
                        && !String(describing: type(of: $0)).contains("MenuBarExtraWindow")
                }
                check(!dialogs.isEmpty, "\(name): dialog presented")
                if let dir = ProcessInfo.processInfo.environment["SWARM_NATIVE_POLISH_EVIDENCE_DIR"], let window = dialogs.first {
                    let capture = Process()
                    capture.executableURL = URL(fileURLWithPath: "/usr/sbin/screencapture")
                    capture.arguments = ["-x", "-o", "-l", "\(window.windowNumber)", "\(dir)/dialog-\(name).png"]
                    try? capture.run()
                    capture.waitUntilExit()
                    check(capture.terminationStatus == 0, "\(name): visual capture")
                }
                dialogs.forEach { $0.close() }
                openMenuOnce()
                try? await Task.sleep(for: .milliseconds(400))
                check(popover?.isVisible == true, "\(name): menu reopens after one request")
                // Close through the real menu action again; direct NSWindow.close()
                // is exactly the stale scene state this regression must avoid.
                if name == "board" || name == "board-selected" { boardAction?(nil) }
                else { actions[name]?() }
                try? await Task.sleep(for: .milliseconds(400))
                check(popover?.isVisible != true, "\(name): repeated launch dismisses menu")
                NSApp.windows.filter {
                    $0.isVisible && !String(describing: type(of: $0)).contains("StatusBar")
                        && !String(describing: type(of: $0)).contains("MenuBarExtraWindow")
                }.forEach { $0.close() }
                try? await Task.sleep(for: .milliseconds(150))
            }
            print("Native dialog check: \(failures) failure(s); OS \(ProcessInfo.processInfo.operatingSystemVersionString)")
            fflush(stdout)
            exit(failures == 0 ? 0 : 1)
        }
    }
}
