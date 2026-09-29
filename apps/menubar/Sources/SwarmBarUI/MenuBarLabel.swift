import AppKit
import SwarmBarKit
import SwiftUI

/// `[swarm] 6   ✳ 42%   ◎ 18%   ▲ 63%   ⌘ 27%` (§16.1). Each value is as wide as its
/// own text, so the item grows and shrinks with the percentages; stale values are dimmed.
public struct MenuBarLabelView: View {
    let label: MenuLabel

    public init(_ label: MenuLabel) { self.label = label }

    /// Layout tokens shared with `slotWidth`/`tooltipSlots` and `StatusItemWatcher.setTooltips`,
    /// so the tooltip rectangles step by exactly the widths the HStack above lays out.
    public static let iconSize: CGFloat = 14
    public static let innerSpacing: CGFloat = 3
    public static let fontSize: CGFloat = 12
    public static let segmentSpacing: CGFloat = 10
    public static let compactSegmentSpacing: CGFloat = 6
    /// Compact shows icons only: one icon plus its trailing gap.
    public static let compactSlotWidth: CGFloat = iconSize + compactSegmentSpacing

    public var body: some View {
        HStack(spacing: label.compact ? Self.compactSegmentSpacing : Self.segmentSpacing) {
            HStack(spacing: Self.innerSpacing) {
                AgentIcon(.swarm)
                if !label.count.isEmpty {
                    Text(label.count).monospacedDigit()
                }
            }
            ForEach(label.segments, id: \.agent) { s in
                HStack(spacing: Self.innerSpacing) {
                    AgentIcon(s.agent)
                    if !label.compact {
                        Text(s.text)
                    }
                }
                .monospacedDigit()
                .opacity(s.dimmed ? 0.45 : 1)
            }
        }
        .font(.system(size: Self.fontSize))
        .fixedSize()
    }

    /// Where the live badge sits: the top-right corner of the 14 pt swarm glyph, which is the
    /// first thing in the HStack above. It is deliberately NOT drawn into `LabelRenderer.image`:
    /// that image is a template, and AppKit paints template images monochrome, so a green dot
    /// inside it would come out black or white. It is overlaid on the SwiftUI side instead.
    public static let badgeOffset = CGPoint(x: 9, y: 0)
    public static let badgeSize: CGFloat = 5

    /// Width of one agent slot: the icon plus the value text beside it plus the trailing
    /// inter-segment gap, so a watcher stepping x by slot widths stays aligned with the
    /// `HStack(spacing:)` layout above. Compact shows icons only (see `compactSlotWidth`).
    static func slotWidth(_ text: String, compact: Bool = false) -> CGFloat {
        if compact { return compactSlotWidth }
        let font = NSFont.monospacedDigitSystemFont(ofSize: fontSize, weight: .regular)
        return iconSize + innerSpacing + ceil((text as NSString).size(withAttributes: [.font: font]).width)
            + segmentSpacing
    }

    /// Slot rectangles (label coordinates, origin top-left) for per-agent tooltips.
    public static func tooltipSlots(_ label: MenuLabel) -> [(String, CGFloat)] {
        label.segments.map { ($0.tooltip, slotWidth($0.text, compact: label.compact)) }
    }
}

@MainActor
public enum LabelRenderer {
    /// MenuBarExtra labels only take a Text or an Image, so the whole label is drawn into a template image.
    public static func image(_ label: MenuLabel) -> NSImage {
        let renderer = ImageRenderer(content: MenuBarLabelView(label).foregroundStyle(.black))
        renderer.scale = NSScreen.main?.backingScaleFactor ?? 2
        let image = renderer.nsImage ?? NSImage()
        image.isTemplate = true
        return image
    }
}

/// The menu bar item: the template label image, plus a colored corner dot — yellow while
/// something needs the user, green while an agent is live, none otherwise. The dot has to live
/// outside the template image (see `MenuBarLabelView.badgeOffset`).
public struct MenuBarLabelImage: View {
    let label: MenuLabel

    public init(_ label: MenuLabel) { self.label = label }

    private var badgeColor: Color? {
        switch label.badge {
        case .none: return nil
        case .green: return .green
        case .yellow: return .yellow
        }
    }

    public var body: some View {
        Image(nsImage: LabelRenderer.image(label))
            .overlay(alignment: .topLeading) {
                if let badgeColor {
                    Circle()
                        .fill(badgeColor)
                        .frame(width: MenuBarLabelView.badgeSize, height: MenuBarLabelView.badgeSize)
                        .offset(x: MenuBarLabelView.badgeOffset.x, y: MenuBarLabelView.badgeOffset.y)
                }
            }
            .accessibilityLabel(label.badge == .yellow ? Copy.needsYouBadge
                                 : label.badge == .green ? Copy.agentsWorking : Copy.appTitle)
    }
}
