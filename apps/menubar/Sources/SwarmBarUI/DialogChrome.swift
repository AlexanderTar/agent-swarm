import AppKit
import SwiftUI

/// TASK-354: makes the New orchestrator, Orchestrate task and Settings windows
/// semi-transparent like the MenuBarExtra popover. The popover gets its material
/// from `.menuBarExtraStyle(.window)`; plain Window/Settings scenes need the window
/// made transparent here with the material drawn by the content itself.
public enum DialogChrome {
    public static func configure(_ window: NSWindow) {
        // Full-size content lets the window material run under the title bar so
        // one surface covers the whole window; without it the title strip shows
        // the desktop unblurred (transparent titlebar, nothing behind it).
        window.styleMask.insert(.fullSizeContentView)
        window.isOpaque = false
        window.backgroundColor = .clear
        window.titlebarAppearsTransparent = true
    }

    /// Which prominent style the default action takes: Liquid Glass where the OS
    /// has it (macOS 26), tinted bordered buttons below.
    public enum ProminentStyle: Equatable {
        case glassProminent
        case borderedProminent
    }

    /// Test seam for offscreen snapshots only: a `.glassProminent` button makes
    /// `cacheDisplay` rasterize its whole snapshot near-black (0.05 brightness,
    /// measured this session), which blinds the OCR layout tests. Real windows
    /// composite it correctly (verified with screencapture), so those tests pin this
    /// to `.borderedProminent` while capturing. Product default is nil: follow the OS.
    nonisolated(unsafe) static var prominentStyleOverride: ProminentStyle?

    /// HIG default action: prominent and tinted on every macOS version.
    public static var prominentStyle: ProminentStyle {
        if let override = prominentStyleOverride { return override }
        if #available(macOS 26, *) { return .glassProminent }
        return .borderedProminent
    }
}

/// Attaches to a dialog root so its NSWindow gets the translucent treatment once the
/// view joins a window. A plain `.background` child: zero size, no drawing.
/// Public so the Window/Settings scene hosts (SwarmBar module) can cover their
/// loading placeholders too — otherwise the window flashes opaque first.
public struct TranslucentWindowAccessor: NSViewRepresentable {
    public init() {}

    public final class AccessorView: NSView {
        override public func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            configureSoon()
        }

        /// Never configure from inside view insertion: AppKit calls viewDidMoveToWindow
        /// from within -[NSWindow setContentView:], and clearing the background there
        /// drops the theme frame's backdrop view that setContentView is about to insert
        /// the content above. The content then lands over the title bar and its
        /// material hides the Settings tabs, title and traffic lights (CHORE-24).
        func configureSoon() {
            DispatchQueue.main.async { [weak self] in
                guard let window = self?.window else { return }
                guard window.isOpaque || window.backgroundColor != .clear
                    || !window.titlebarAppearsTransparent
                    || !window.styleMask.contains(.fullSizeContentView) else { return }
                DialogChrome.configure(window)
            }
        }
    }

    public func makeNSView(context: Context) -> AccessorView { AccessorView(frame: .zero) }

    public func updateNSView(_ nsView: AccessorView, context: Context) {
        if nsView.window != nil { nsView.configureSoon() }
    }
}

extension View {
    /// Identical inset field surfaces for single-line and image-aware multiline input.
    func dialogFieldSurface(focused: Bool = false) -> some View {
        background(Color(nsColor: .textBackgroundColor).opacity(0.5),
                   in: RoundedRectangle(cornerRadius: 6))
            .overlay(RoundedRectangle(cornerRadius: 6).stroke(focused ? AnyShapeStyle(Color.accentColor) : AnyShapeStyle(.separator),
                                                             lineWidth: focused ? 2 : 1))
    }

    /// The window material behind dialog content on every macOS version: the
    /// popover-weight regular material. Ultra-thin let bright backdrops through
    /// almost unblurred and tinted captions to the background, leaving
    /// secondary text unreadable. A full-content `glassEffect` would refract the
    /// text as well; Liquid Glass accents live on the controls themselves via
    /// `dialogGlass()`. Public so the scene hosts (SwarmBar module) can cover
    /// their loading placeholders with the same material.
    public func translucentDialogBackground() -> some View {
        background(.regularMaterial)
    }

    /// The default action (Start/Queue orchestrator, Hand off, Done, Try again):
    /// prominent and tinted per Apple HIG so it stands apart from plain Cancel.
    /// Keep it out from under any `glassButtons()` ancestor: a glass-styled
    /// ancestor flattens an inner `.glassProminent` back to plain glass (verified
    /// with real-window screenshots — identical fills), so dialog roots style
    /// their other buttons with `glassButtons()` per button instead of at the root.
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
    /// (repo list): glass on macOS 26 shaped to the control's own rounded rect —
    /// the default capsule glass draws a dark oval inside a rectangular border —
    /// the control's own bezel below. Never an opaque fill, so the window material
    /// shows through. (The segmented intent picker keeps its own chrome: wrapping
    /// it in a second glass double-draws it.)
    @ViewBuilder func dialogGlass(cornerRadius: CGFloat = 5) -> some View {
        if #available(macOS 26, *) {
            glassEffect(.regular, in: RoundedRectangle(cornerRadius: cornerRadius))
        } else {
            self
        }
    }
}
