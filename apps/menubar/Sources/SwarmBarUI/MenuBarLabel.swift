import AppKit
import SwarmBarKit
import SwiftUI

/// `[swarm] 6   ✳ 42%   ◎ 18%   ▲ 63%   ⌘ 27%M` (§16.1). Values sit in fixed-width slots sized
/// for "100%" so the item never shifts; stale values are dimmed.
public struct MenuBarLabelView: View {
    let label: MenuLabel

    public init(_ label: MenuLabel) { self.label = label }

    public var body: some View {
        HStack(spacing: label.compact ? 6 : 10) {
            HStack(spacing: 3) {
                AgentIcon(.swarm)
                if !label.count.isEmpty {
                    Text(label.count).monospacedDigit()
                }
            }
            ForEach(label.segments, id: \.agent) { s in
                HStack(spacing: 3) {
                    AgentIcon(s.agent)
                    if !label.compact {
                        ZStack(alignment: .leading) {
                            Text(s.agent == .cursor ? MenuLabel.widestMonthlyValue : MenuLabel.widestValue).hidden()
                            Text(s.text)
                        }
                        .monospacedDigit()
                    }
                }
                .opacity(s.dimmed ? 0.45 : 1)
            }
        }
        .font(.system(size: 12))
        .fixedSize()
    }

    /// Where the live badge sits: the top-right corner of the 14 pt swarm glyph, which is the
    /// first thing in the HStack above. It is deliberately NOT drawn into `LabelRenderer.image`:
    /// that image is a template, and AppKit paints template images monochrome, so a green dot
    /// inside it would come out black or white. It is overlaid on the SwiftUI side instead.
    public static let badgeOffset = CGPoint(x: 9, y: 0)
    public static let badgeSize: CGFloat = 5

    /// Slot rectangles (label coordinates, origin top-left) for per-agent tooltips.
    public static func tooltipSlots(_ label: MenuLabel) -> [(String, CGFloat)] {
        label.segments.map { ($0.tooltip, $0.agent == .cursor ? 64 : 54) }
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

/// The menu bar item: the template label image, plus a colored corner dot — green while an agent
/// is live, red while the daemon is offline, none while connected but idle. The dot has to live
/// outside the template image (see `MenuBarLabelView.badgeOffset`).
public struct MenuBarLabelImage: View {
    let label: MenuLabel

    public init(_ label: MenuLabel) { self.label = label }

    private var badgeColor: Color? {
        switch label.badge {
        case .none: return nil
        case .green: return .green
        case .red: return .red
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
            .accessibilityLabel(label.badge == .green ? Copy.agentsWorking : Copy.appTitle)
    }
}
