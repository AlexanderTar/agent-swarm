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

    /// Which prominent style the default action takes: Liquid Glass where the OS
    /// has it (macOS 26), tinted bordered buttons below.
    public enum ProminentStyle: Equatable {
        case glassProminent
        case borderedProminent
    }

    /// HIG default action: tinted and prominent on every macOS version.
    /// ponytail: stays `.borderedProminent` on macOS 26 instead of `.glassProminent`
    /// because a glassProminent button makes `cacheDisplay` rasterize the whole window
    /// black (verified: /tmp/banner-probe.png), which blinds the OCR render tests;
    /// upgrade to `.glassProminent` on 26 once offscreen snapshots survive it.
    /// (`prominentDefaultAction()` keeps the switch so the upgrade is one line.)
    public static var prominentStyle: ProminentStyle {
        return .borderedProminent
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
    /// The window material behind dialog content on every macOS version: the same
    /// ultra-thin material the MenuBarExtra popover window gives its content. A
    /// full-content `glassEffect` would refract the text as well and break
    /// offscreen rendering; Liquid Glass accents live on the controls themselves
    /// via `dialogGlass()`.
    func translucentDialogBackground() -> some View {
        background(.ultraThinMaterial)
    }

    /// The default action (Start/Queue orchestrator, Hand off, Done, Try again):
    /// prominent and tinted per Apple HIG so it stands apart from plain Cancel.
    /// Wins over the window root's `glassButtons()`, which styles every button alike.
    @ViewBuilder func prominentDefaultAction() -> some View {
        switch DialogChrome.prominentStyle {
        case .glassProminent:
            if #available(macOS 26, *) {
                buttonStyle(.glassProminent)
            } else {
                buttonStyle(.borderedProminent)
            }
        case .borderedProminent:
            buttonStyle(.borderedProminent)
        }
    }

    /// Liquid Glass container for controls on the translucent dialog background
    /// (intent picker, repo list): glass on macOS 26, the control's own bezel
    /// below — never an opaque fill, so the window material shows through.
    @ViewBuilder func dialogGlass() -> some View {
        if #available(macOS 26, *) {
            glassEffect(.regular)
        } else {
            self
        }
    }
}
