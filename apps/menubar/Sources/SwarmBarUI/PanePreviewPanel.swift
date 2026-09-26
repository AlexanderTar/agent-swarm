import AppKit
import SwarmBarKit
import SwiftUI

/// The preview's content. The window around it lives in SwarmBar.
public struct PanePreviewPanel: View {
    let preview: PanePreviewModel
    /// Resolves the hovered agent's name to its header values, e.g.
    /// "login-coder · TASK-101 · Claude · Opus 4.6 (High)". PanePreviewModel only tracks a name
    /// (spec's locked type), so this is a live lookup supplied by the caller rather than plumbing
    /// the node through the model itself. nil (the default, and every existing test's case) falls
    /// back to the name alone.
    let lookup: ((String) -> AgentHeader?)?
    /// The live catalog for human model labels; unknown models fall back to their ID.
    let catalog: [AgentCatalogEntry]

    public init(preview: PanePreviewModel, lookup: ((String) -> AgentHeader?)? = nil,
                catalog: [AgentCatalogEntry] = []) {
        self.preview = preview
        self.lookup = lookup
        self.catalog = catalog
    }

    public static let size = CGSize(width: 760, height: 320)
    /// Air between the dark fill's edge and the text.
    private static let textInset: CGFloat = 6

    private var header: String {
        guard let name = preview.agent else { return "" }
        guard let found = lookup?(name) else { return name }
        let entry = CatalogRules.entry(catalog, found.kind)
        return Copy.paneHeader(name, found.itemKey, Copy.agentLabel(found.kind),
                               CatalogRules.modelLabel(entry, found.model),
                               found.effort.map(Copy.humanEffort))
    }

    public var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(header).font(.system(size: 11, weight: .semibold)).lineLimit(1)
            Divider()
            content
        }
        .padding(12)
        .frame(width: Self.size.width, height: Self.size.height, alignment: .leading)
        .glassPanel(cornerRadius: 12)
        .contentShape(RoundedRectangle(cornerRadius: 12))
        .onHover { inside in
            if inside {
                preview.enterPanel()
            } else {
                preview.leavePanel()
            }
        }
    }

    @ViewBuilder private var content: some View {
        switch preview.status {
        case .loading:
            VStack(spacing: 8) {
                ProgressView()
                Text(Copy.paneLoading).font(.callout).foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        case let .text(text, tmuxAlive):
            VStack(alignment: .leading, spacing: 4) {
                if !tmuxAlive {
                    Text("⚠ " + Copy.paneDead).font(.system(size: 10)).foregroundStyle(.orange)
                }
                GeometryReader { geo in
                    ScrollView([.vertical, .horizontal]) {
                        Text(AnsiText.attributed(text))
                            .font(.system(size: 11, design: .monospaced))
                            .foregroundStyle(AnsiText.defaultFG)
                            .fixedSize(horizontal: true, vertical: false)
                            .padding(Self.textInset)
                            .frame(minWidth: geo.size.width, minHeight: geo.size.height, alignment: .bottomLeading)
                            .textSelection(.enabled)
                            .background(SubtleScrollerConfig())
                    }
                    .scrollIndicators(.automatic)
                    .defaultScrollAnchor(.bottomLeading)
                }
                // TUI palettes assume a dark screen, so the fill is the same in light and dark mode.
                .background(AnsiText.defaultBG, in: RoundedRectangle(cornerRadius: 6))
                .clipShape(RoundedRectangle(cornerRadius: 6))
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        case let .failed(message):
            Text(message).font(.callout).foregroundStyle(.secondary)
                .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .center)
                .multilineTextAlignment(.center)
        }
    }
}


/// Liquid Glass where the OS has it, a vibrancy material where it doesn't -- the same
/// availability shape as `Components.glassButtons()`.
extension View {
    @ViewBuilder func glassPanel(cornerRadius: CGFloat) -> some View {
        if #available(macOS 26, *) {
            glassEffect(.regular, in: RoundedRectangle(cornerRadius: cornerRadius))
        } else {
            background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: cornerRadius))
        }
    }
}

/// Measures the row, its host window and its screen in screen points, on demand.
/// No NSWindow crosses into SwarmBarKit -- only three CGRects.
@MainActor final class ScreenAnchor {
    fileprivate weak var view: NSView?
    func measure() -> PanePreviewModel.Anchor? {
        guard let view, let win = view.window, let screen = win.screen else { return nil }
        return .init(row: win.convertToScreen(view.convert(view.bounds, to: nil)),
                     host: win.frame, screen: screen.visibleFrame)
    }
}

struct ScreenAnchorReader: NSViewRepresentable {
    let anchor: ScreenAnchor

    func makeNSView(context: Context) -> NSView {
        let v = NSView(frame: .zero)
        anchor.view = v
        return v
    }

    func updateNSView(_ nsView: NSView, context: Context) {
        anchor.view = nsView
    }
}
