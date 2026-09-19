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
                Text(label.count).monospacedDigit()
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
