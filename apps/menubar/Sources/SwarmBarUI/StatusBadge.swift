import AppKit
import SwarmBarKit

/// The coloured corner dot on the menu bar glyph: yellow while something needs the user,
/// green while an agent is live. MenuBarExtra keeps only its label's image and drops any
/// SwiftUI overlay, and the label image is a template that AppKit paints monochrome, so the
/// dot is a small layer-backed view on the status item's button instead.
@MainActor
public enum StatusBadge {
    private static let id = NSUserInterfaceItemIdentifier("swarm.badge")

    public static func view(in button: NSButton) -> NSView? {
        button.subviews.first { $0.identifier == id }
    }

    public static func apply(_ badge: MenuLabel.Badge, to button: NSButton) {
        let color: NSColor
        switch badge {
        case .none:
            view(in: button)?.removeFromSuperview()
            return
        case .green: color = .systemGreen
        case .yellow: color = .systemYellow
        }
        let dot = view(in: button) ?? {
            let v = NSView()
            v.identifier = id
            v.wantsLayer = true
            v.layer?.cornerRadius = MenuBarLabelView.badgeSize / 2
            button.addSubview(v)
            return v
        }()
        dot.layer?.backgroundColor = color.cgColor
        dot.frame = frame(in: button)
    }

    /// `badgeOffset` from the top-left of the label image, which starts with the swarm glyph.
    static func frame(in button: NSButton) -> NSRect {
        let size = MenuBarLabelView.badgeSize
        let image = button.cell?.imageRect(forBounds: button.bounds) ?? button.bounds
        let x = image.minX + MenuBarLabelView.badgeOffset.x
        let y = button.isFlipped
            ? image.minY + MenuBarLabelView.badgeOffset.y
            : image.maxY - MenuBarLabelView.badgeOffset.y - size
        return NSRect(x: x, y: y, width: size, height: size)
    }
}
