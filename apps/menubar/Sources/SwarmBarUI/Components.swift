import AppKit
import SwarmBarKit
import SwiftUI

public struct StateDot: View {
    let tone: DotTone

    public init(_ tone: DotTone) { self.tone = tone }

    private var pulses: Bool { tone == .greyPulse || tone == .greenPulse }

    private var color: Color {
        switch tone {
        case .green, .greenHollow, .greenPulse: return .green
        case .grey, .greyPulse: return .secondary
        case .amber: return .orange
        case .hollow: return .secondary
        case .red: return .red
        }
    }

    public var body: some View {
        Group {
            if pulses {
                PhaseAnimator([1.0, 0.3]) { opacity in
                    dotShape
                        .opacity(opacity)
                } animation: { _ in
                    .easeInOut(duration: 0.8)
                }
            } else {
                dotShape
            }
        }
        .frame(width: 8, height: 8)
        .transition(.identity)
        .accessibilityHidden(true)
    }

    private var dotShape: some View {
        ZStack {
            if tone == .hollow {
                Circle().strokeBorder(color, lineWidth: 1.5)
            } else {
                Circle().fill(color)
                if tone == .greenHollow { Circle().fill(Color(nsColor: .windowBackgroundColor)).padding(2.5) }
            }
        }
    }
}

/// A section header with a disclosure arrow, a title, and trailing content.
public struct SectionHeader<Trailing: View>: View {
    let title: String
    let open: Bool
    let toggle: () -> Void
    let trailing: Trailing

    public init(_ title: String, open: Bool, toggle: @escaping () -> Void, @ViewBuilder trailing: () -> Trailing) {
        self.title = title
        self.open = open
        self.toggle = toggle
        self.trailing = trailing()
    }

    public var body: some View {
        HStack {
            Button(action: toggle) {
                HStack(spacing: 4) {
                    Image(systemName: open ? "chevron.down" : "chevron.right").frame(width: 12)
                    Text(title).font(.headline)
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            Spacer()
            trailing
        }
        .frame(minHeight: 28)
    }
}

/// A 24 pt icon button with a tooltip. Icon-only controls always carry `help` as both the
/// tooltip and the accessibility label, so nothing loses its meaning when the words go away.
public struct IconButton: View {
    let symbol: String
    let help: String
    let disabled: Bool
    let tint: Color?
    let toggleValue: Bool?
    let action: () -> Void

    /// `tint` recolours the symbol; a non-nil `toggleValue` makes it a VoiceOver toggle reading On/Off.
    public init(_ symbol: String, help: String, disabled: Bool = false, tint: Color? = nil, toggleValue: Bool? = nil,
                action: @escaping () -> Void) {
        self.symbol = symbol
        self.help = help
        self.disabled = disabled
        self.tint = tint
        self.toggleValue = toggleValue
        self.action = action
    }

    public var body: some View {
        Button(action: action) {
            Image(systemName: symbol)
                .font(.system(size: 12))
                .ifLet(tint) { $0.foregroundStyle($1) }
                .frame(width: 24, height: 24)
                .contentShape(Rectangle())
        }
        .buttonStyle(.borderless)
        .disabled(disabled)
        .opacity(disabled ? 0.35 : 1) // .borderless barely dims a disabled symbol in the popover
        .help(help)
        .accessibilityLabel(help)
        .ifLet(toggleValue) { $0.accessibilityAddTraits(.isToggle).accessibilityValue($1 ? Copy.on : Copy.off) }
    }
}

private extension View {
    @ViewBuilder func ifLet<T>(_ value: T?, _ apply: (Self, T) -> some View) -> some View {
        if let value { apply(self, value) } else { self }
    }
}

/// The low-token mode mark on an orchestrator row: a 9 pt green leaf, the same green as a running `StateDot`.
/// Hidden from VoiceOver; the row label carries "low-token mode" instead (`AgentTree.rowLabel`).
public struct EcoLeaf: View {
    public init() {}

    public var body: some View {
        Image(systemName: "leaf.fill")
            .font(.system(size: 9))
            .foregroundStyle(.green)
            .help(Copy.lowTokenOnHelp)
            .accessibilityHidden(true)
    }
}

/// A Picker bound to a String value with PickerOption choices. Pass `icon` to render an
/// `AgentIcon` next to each option's label (used for agent pickers); other pickers leave it nil.
public struct OptionPicker: View {
    let title: String
    let options: [PickerOption]
    let value: String
    let icon: ((PickerOption) -> IconName?)?
    let onChange: (String) -> Void

    public init(
        _ title: String, options: [PickerOption], value: String,
        icon: ((PickerOption) -> IconName?)? = nil, onChange: @escaping (String) -> Void
    ) {
        self.title = title
        self.options = options
        self.value = value
        self.icon = icon
        self.onChange = onChange
    }

    public var body: some View {
        let onChange = self.onChange
        Picker(title, selection: Binding(get: { value }, set: { v in onChange(v) })) {
            if !options.contains(where: { $0.value == value }) {
                Text(value.isEmpty ? " " : (Copy.claudeAliasName(value) ?? value)).tag(value)
            }
            ForEach(options) { option in
                if let iconName = icon?(option) {
                    Label { Text(option.label) } icon: { AgentIcon(iconName) }.tag(option.value)
                } else {
                    Text(option.label).tag(option.value)
                }
            }
        }
    }
}

/// Configures the enclosing NSScrollView to use a subtle overlay scrollbar:
/// hidden by default, thin (.small), with a transparent background.
struct SubtleScrollerConfig: NSViewRepresentable {
    var adjacentScrollView = false

    final class ConfigurationView: NSView {
        var adjacentScrollView = false

        // AppKit flips scroll views back to the legacy style (opaque track) whenever the
        // preferred style changes, e.g. "Show scroll bars: Always" or a mouse attaching.
        override init(frame: NSRect) {
            super.init(frame: frame)
            NotificationCenter.default.addObserver(
                self, selector: #selector(preferredStyleChanged),
                name: NSScroller.preferredScrollerStyleDidChangeNotification, object: nil)
        }

        required init?(coder: NSCoder) { nil }

        @objc private func preferredStyleChanged() {
            // AppKit applies its reset after observers run; configure on the next turn.
            DispatchQueue.main.async { [weak self] in self?.configure() }
        }

        override func layout() {
            super.layout()
            configure()
        }

        private var styleObservation: NSKeyValueObservation?
        private weak var observed: NSScrollView?

        func configure() {
            guard let scrollView = SubtleScrollerConfig.scrollView(for: self, adjacent: adjacentScrollView) else { return }
            if observed !== scrollView {
                observed = scrollView
                // SwiftUI resets an adjacent List's scroll view to legacy on updates (a
                // selection change) without any notification; undo it as it happens.
                styleObservation = scrollView.observe(\.scrollerStyle, options: [.new]) { scroll, _ in
                    if scroll.scrollerStyle != .overlay { scroll.scrollerStyle = .overlay }
                }
            }
            scrollView.drawsBackground = false
            scrollView.contentView.drawsBackground = false
            scrollView.scrollerStyle = .overlay
            scrollView.autohidesScrollers = true
            scrollView.verticalScroller?.controlSize = .small
            scrollView.horizontalScroller?.controlSize = .small
        }
    }

    private static func scrollView(for view: NSView, adjacent: Bool) -> NSScrollView? {
        if adjacent {
            // SwiftUI inserts extra hosting layers for glass/background modifiers.
            // Search the nearest ancestor that contains an overlapping native scroll view,
            // rather than assuming a fixed sibling depth.
            func scrolls(in root: NSView) -> [NSScrollView] {
                if let scroll = root as? NSScrollView { return [scroll] }
                return root.subviews.flatMap { scrolls(in: $0) }
            }
            var ancestor = view.superview
            while let container = ancestor {
                let target = view.convert(view.bounds, to: container)
                let matches = scrolls(in: container).compactMap { scroll -> (NSScrollView, CGFloat)? in
                    let overlap = target.intersection(scroll.convert(scroll.bounds, to: container))
                    guard !overlap.isNull, overlap.width > 0, overlap.height > 0 else { return nil }
                    return (scroll, overlap.width * overlap.height)
                }
                if let match = matches.max(by: { $0.1 < $1.1 }) { return match.0 }
                ancestor = container.superview
            }
        }
        return view.enclosingScrollView
    }

    func makeNSView(context: Context) -> ConfigurationView {
        let v = ConfigurationView(frame: .zero)
        v.adjacentScrollView = adjacentScrollView
        DispatchQueue.main.async { [weak v] in
            v?.configure()
        }
        return v
    }

    func updateNSView(_ nsView: ConfigurationView, context: Context) {
        nsView.adjacentScrollView = adjacentScrollView
        DispatchQueue.main.async { [weak nsView] in
            nsView?.configure()
        }
    }
}

/// A section body that scrolls on its own once it outgrows `cap`. Section headers stay outside it,
/// so a long agent list scrolls under its own header instead of shoving other sections off the popover.
/// Uses a subtle overlay scrollbar that is hidden by default and only shows when scrolling.
public struct SectionBody<Content: View>: View {
    let cap: CGFloat
    let spacing: CGFloat
    let content: Content

    public init(cap: CGFloat, spacing: CGFloat = 6, @ViewBuilder content: () -> Content) {
        self.cap = cap
        self.spacing = spacing
        self.content = content()
    }

    public var body: some View {
        ScrollView(.vertical) {
            VStack(alignment: .leading, spacing: spacing) { content }
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(SubtleScrollerConfig())
        }
        .frame(maxHeight: cap)
        .fixedSize(horizontal: false, vertical: true)
        .scrollIndicators(.automatic)
        .scrollBounceBehavior(.basedOnSize)
    }
}

extension View {
    /// Liquid Glass buttons where the OS has them (macOS 26), the platform's own bordered buttons
    /// below that. Applied once at a window root; inner `.borderless`/`.plain`/`.link` styles win —
    /// but an inner `.glassProminent` does NOT (it flattens to plain glass), so windows with a
    /// default action style their buttons per button instead of at the root (see `prominentDefaultAction`).
    @ViewBuilder public func glassButtons() -> some View {
        if #available(macOS 26, *) { buttonStyle(.glass) } else { self }
    }
}
