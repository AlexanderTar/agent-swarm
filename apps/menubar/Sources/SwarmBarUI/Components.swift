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
    let action: () -> Void

    public init(_ symbol: String, help: String, disabled: Bool = false, action: @escaping () -> Void) {
        self.symbol = symbol
        self.help = help
        self.disabled = disabled
        self.action = action
    }

    public var body: some View {
        Button(action: action) {
            Image(systemName: symbol)
                .font(.system(size: 12))
                .frame(width: 24, height: 24)
                .contentShape(Rectangle())
        }
        .buttonStyle(.borderless)
        .disabled(disabled)
        .help(help)
        .accessibilityLabel(help)
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
    func makeNSView(context: Context) -> NSView {
        let v = NSView(frame: .zero)
        DispatchQueue.main.async { [weak v] in
            guard let scrollView = v?.enclosingScrollView else { return }
            scrollView.scrollerStyle = .overlay
            scrollView.autohidesScrollers = true
            scrollView.verticalScroller?.controlSize = .small
            scrollView.horizontalScroller?.controlSize = .small
        }
        return v
    }

    func updateNSView(_ nsView: NSView, context: Context) {
        DispatchQueue.main.async { [weak nsView] in
            guard let scrollView = nsView?.enclosingScrollView else { return }
            scrollView.scrollerStyle = .overlay
            scrollView.autohidesScrollers = true
            scrollView.verticalScroller?.controlSize = .small
            scrollView.horizontalScroller?.controlSize = .small
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
    /// below that. Applied once at a window root; inner `.borderless`/`.plain`/`.link` styles win.
    @ViewBuilder public func glassButtons() -> some View {
        if #available(macOS 26, *) { buttonStyle(.glass) } else { self }
    }
}
