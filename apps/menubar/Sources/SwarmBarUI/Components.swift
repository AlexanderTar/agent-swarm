import SwarmBarKit
import SwiftUI

public struct StateDot: View {
    let tone: DotTone
    @State private var pulse = false

    public init(_ tone: DotTone) { self.tone = tone }

    private var color: Color {
        switch tone {
        case .green, .greenHollow: return .green
        case .grey, .greyPulse: return .secondary
        case .amber: return .orange
        case .hollow: return .secondary
        case .red: return .red
        }
    }

    public var body: some View {
        ZStack {
            if tone == .hollow {
                Circle().strokeBorder(color, lineWidth: 1.5)
            } else {
                Circle().fill(color)
                if tone == .greenHollow { Circle().fill(Color(nsColor: .windowBackgroundColor)).padding(2.5) }
            }
        }
        .frame(width: 8, height: 8)
        .opacity(tone == .greyPulse && pulse ? 0.3 : 1)
        .animation(tone == .greyPulse ? .easeInOut(duration: 0.8).repeatForever() : nil, value: pulse)
        .onAppear { pulse = tone == .greyPulse }
        .accessibilityHidden(true)
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

/// A 28 pt icon button with a tooltip.
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
            Image(systemName: symbol).frame(width: 28, height: 28).contentShape(Rectangle())
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
                Text(value.isEmpty ? " " : value).tag(value)
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
