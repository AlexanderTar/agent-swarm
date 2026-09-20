import AppKit
import SwarmBarKit
import SwiftUI

/// The preview's content. The window around it lives in SwarmBar.
public struct PanePreviewPanel: View {
    let preview: PanePreviewModel
    /// Resolves the hovered agent's name to (role label, item key) for the header, e.g.
    /// "login-coder · Coder · TASK-101". PanePreviewModel only tracks a name (spec's locked
    /// type), so this is a live lookup supplied by the caller rather than plumbing kind/itemKey
    /// through the model itself. nil (the default, and every existing test's case) falls back
    /// to the name alone.
    let lookup: ((String) -> (kind: String, itemKey: String)?)?

    public init(preview: PanePreviewModel, lookup: ((String) -> (kind: String, itemKey: String)?)? = nil) {
        self.preview = preview
        self.lookup = lookup
    }

    public static let size = CGSize(width: 520, height: 320)

    private var header: String {
        guard let name = preview.agent else { return "" }
        guard let found = lookup?(name) else { return name }
        return Copy.paneHeader(name, found.kind, found.itemKey)
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
                Spacer(minLength: 0)
                Text(text)
                    .font(.system(size: 11, design: .monospaced))
                    .lineLimit(nil)
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .bottomLeading)
            .clipped()
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
