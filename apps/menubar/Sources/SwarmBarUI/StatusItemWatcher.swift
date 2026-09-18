import AppKit
import SwarmBarKit

/// Finds the MenuBarExtra's status bar window. It reports whether macOS is showing the item
/// (M5: hidden = no screen, or not on its screen) and carries the per-agent tooltips.
/// ponytail: relies on the "NSStatusBarWindow" class name; replace with an NSStatusItem-based
/// label if a macOS release renames it.
@MainActor
public final class StatusItemWatcher {
    private var timer: Timer?
    private let onVisibility: @MainActor (Bool) -> Void
    private var last: Bool?

    public init(onVisibility: @escaping @MainActor (Bool) -> Void) {
        self.onVisibility = onVisibility
    }

    public static func statusWindow() -> NSWindow? {
        NSApp.windows.first { String(describing: type(of: $0)).contains("NSStatusBarWindow") }
    }

    public func start() {
        timer = Timer.scheduledTimer(withTimeInterval: 5, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.check() }
        }
        check()
    }

    public func check() {
        guard let w = Self.statusWindow() else { return }
        let visible = w.screen.map { $0.frame.intersects(w.frame) } ?? false
        if visible != last {
            last = visible
            onVisibility(visible)
        }
    }

    /// One tooltip rectangle per agent slot, left to right after the count.
    public func setTooltips(_ label: MenuLabel) {
        guard let view = Self.statusWindow()?.contentView else { return }
        view.removeAllToolTips()
        var x: CGFloat = 40
        for (text, width) in MenuBarLabelView.tooltipSlots(label) {
            let w = label.compact ? 20 : width
            view.addToolTip(NSRect(x: x, y: 0, width: w, height: view.bounds.height), owner: text as NSString, userData: nil)
            x += w
        }
    }
}
