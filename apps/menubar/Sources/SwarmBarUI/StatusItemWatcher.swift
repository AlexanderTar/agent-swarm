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
    private var lastSlots: [(text: String, width: CGFloat)] = []
    private var badge: MenuLabel.Badge = .none

    public init(onVisibility: @escaping @MainActor (Bool) -> Void) {
        self.onVisibility = onVisibility
    }

    public static func statusWindow() -> NSWindow? {
        NSApp.windows.first { String(describing: type(of: $0)).contains("NSStatusBarWindow") }
    }

    public static func statusItem() -> NSStatusItem? {
        guard let w = statusWindow() else { return nil }
        return (w.value(forKey: "statusItem") as? NSStatusItem)
            ?? (Mirror(reflecting: w).descendant("statusItem") as? NSStatusItem)
    }

    public func start() {
        timer = Timer.scheduledTimer(withTimeInterval: 5, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.check() }
        }
        check()
    }

    public func check() {
        applyBadge()
        guard let w = Self.statusWindow() else { return }
        let visible = w.screen.map { $0.frame.intersects(w.frame) } ?? false
        if visible != last {
            last = visible
            onVisibility(visible)
        }
    }

    /// Shows the corner dot for `badge`. Applied on the next run loop turn so the button has
    /// already taken the new label image (its width moves the image rect), and again on every
    /// `check()` in case AppKit re-laid the button out since.
    public func setBadge(_ badge: MenuLabel.Badge) {
        self.badge = badge
        DispatchQueue.main.async { [weak self] in self?.applyBadge() }
    }

    private func applyBadge() {
        guard let button = Self.statusItem()?.button else { return }
        StatusBadge.apply(badge, to: button)
    }

    /// One tooltip rectangle per agent slot, left to right after the count.
    ///
    /// Real crash (2026-09-19): a SIGSEGV inside AppKit's NSToolTipManager
    /// displayToolTip:, triggered by its delayed tooltip timer firing after
    /// this method's removeAllToolTips()+re-add cycle had already invalidated
    /// what the timer was about to display. This method used to run
    /// unconditionally on every MenuLabel change (label.label fires on any
    /// segment's text/badge/etc., not only a tooltip-affecting field), so a
    /// live multi-agent system churned the status bar's tooltip rects far
    /// more often than the tooltip content itself ever actually changed.
    /// Skipping the rebuild when the actual slot text/width didn't change
    /// removes most of that churn without needing to fully understand
    /// NSToolTipManager's internal timer race.
    public func setTooltips(_ label: MenuLabel) {
        guard let view = Self.statusWindow()?.contentView else { return }
        // Slot widths already include the trailing inter-segment gap (and the compact
        // icon-only width), so stepping x by them tracks MenuBarLabelView's HStack layout.
        let slots = MenuBarLabelView.tooltipSlots(label).map { (text: $0.0, width: $0.1) }
        if slots.elementsEqual(lastSlots, by: { $0.text == $1.text && $0.width == $1.width }) {
            return
        }
        lastSlots = slots
        view.removeAllToolTips()
        var x: CGFloat = 40
        for slot in slots {
            view.addToolTip(NSRect(x: x, y: 0, width: slot.width, height: view.bounds.height), owner: slot.text as NSString, userData: nil)
            x += slot.width
        }
    }
}
