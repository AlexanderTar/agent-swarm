import AppKit
import SwiftUI

/// TASK-354: makes the New orchestrator, Orchestrate task and Settings windows
/// semi-transparent like the MenuBarExtra popover. The popover gets its material
/// from `.menuBarExtraStyle(.window)`; plain Window/Settings scenes need the window
/// made transparent here with the material drawn by the content itself.
public enum DialogChrome {
    public static func configure(_ window: NSWindow) {
        window.isOpaque = false
        window.backgroundColor = .clear
    }
}

/// Attaches to a dialog root so its NSWindow gets the translucent treatment once the
/// view joins a window. A plain `.background` child: zero size, no drawing.
struct TranslucentWindowAccessor: NSViewRepresentable {
    final class AccessorView: NSView {
        override func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            if let window { DialogChrome.configure(window) }
        }
    }

    func makeNSView(context: Context) -> AccessorView { AccessorView(frame: .zero) }

    func updateNSView(_ nsView: AccessorView, context: Context) {
        if let window = nsView.window { DialogChrome.configure(window) }
    }
}

extension View {
    /// Liquid Glass where the OS has it (macOS 26), ultra-thin material below —
    /// the same availability shape as `glassPanel`/`glassButtons`.
    @ViewBuilder func translucentDialogBackground() -> some View {
        if #available(macOS 26, *) {
            glassEffect(.regular)
        } else {
            background(.ultraThinMaterial)
        }
    }
}
