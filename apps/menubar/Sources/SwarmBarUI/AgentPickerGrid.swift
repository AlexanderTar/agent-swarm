import SwarmBarKit
import SwiftUI

struct AgentPickerGrid: View {
    let picker: AgentPickerModel

    private var advisorAgentValue: String {
        if case let .pair(agent, _) = picker.advisor { return agent.rawValue }
        return "none"
    }

    private var advisorModelValue: String {
        if case let .pair(_, model) = picker.advisor { return model }
        return "—"
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Grid(alignment: .leading, horizontalSpacing: 8, verticalSpacing: 6) {
                GridRow {
                    Text(Copy.agent).frame(width: 70, alignment: .leading)
                    WideOptionPicker(Copy.agent, options: picker.agentOptions, value: picker.choice.agent?.rawValue ?? "",
                                     icon: { AgentKind(rawValue: $0.value).map(IconName.init) }) { picker.setAgent($0) }
                        .frame(width: 130)
                    Text(Copy.model).frame(width: 50, alignment: .leading)
                    WideOptionPicker(Copy.model, options: picker.modelOptions, value: picker.choice.model) { picker.setModel($0) }
                        .frame(minWidth: 200, maxWidth: .infinity)
                    if let efforts = picker.effortOptions {
                        Text(Copy.effort).frame(width: 45, alignment: .leading)
                        WideOptionPicker(Copy.agentEffort, options: efforts, value: picker.choice.effort) { picker.setEffort($0) }
                            .frame(width: 170)
                    } else {
                        Text("").frame(width: 45)
                        Color.clear.frame(width: 170, height: 1)
                    }
                }
                if let error = picker.errors.agent, !error.isEmpty {
                    GridRow { Text(error).font(.caption).foregroundStyle(.red).gridCellColumns(6) }
                }
                if let error = picker.errors.model {
                    GridRow { Text(error).font(.caption).foregroundStyle(.red).gridCellColumns(6) }
                }
                if let note = picker.effortNote {
                    GridRow { Text(note).font(.caption).foregroundStyle(.secondary).gridCellColumns(6) }
                }
                GridRow {
                    Text(Copy.advisor).frame(width: 70, alignment: .leading)
                    WideOptionPicker(Copy.advisor, options: picker.advisorAgentOptions, value: advisorAgentValue,
                                     icon: { AgentKind(rawValue: $0.value).map(IconName.init) }) { picker.setAdvisorAgent($0) }
                        .frame(width: 130)
                    Text(Copy.model).frame(width: 50, alignment: .leading)
                    WideOptionPicker("Advisor model", options: picker.advisorModelOptions, value: advisorModelValue) { picker.setAdvisorModel($0) }
                        .frame(minWidth: 200, maxWidth: .infinity).disabled(picker.advisor == .none)
                    if let efforts = picker.advisorEffortOptions {
                        Text(Copy.effort).frame(width: 45, alignment: .leading)
                        WideOptionPicker(Copy.advisorEffort, options: efforts, value: picker.advisorEffort) { picker.setAdvisorEffort($0) }
                            .frame(width: 170)
                    } else {
                        Text("").frame(width: 45)
                        Color.clear.frame(width: 170, height: 1)
                    }
                }
                if let error = picker.errors.advisor {
                    GridRow { Text(error).font(.caption).foregroundStyle(.red).gridCellColumns(6) }
                }
            }
            Text(Copy.defaultsFromSettings).font(.caption).foregroundStyle(.secondary)
        }
    }
}

/// AppKit keeps the visible popup as wide as its SwiftUI frame, including at the window minimum.
/// With `detail`, menu rows get a second line; the closed popup still shows only line one.
struct WideOptionPicker: NSViewRepresentable {
    @Environment(\.isEnabled) private var isEnabled
    let title: String
    let options: [PickerOption]
    let value: String
    let icon: ((PickerOption) -> IconName?)?
    let detail: ((PickerOption) -> NSAttributedString?)?
    let onChange: (String) -> Void

    init(_ title: String, options: [PickerOption], value: String,
         icon: ((PickerOption) -> IconName?)? = nil,
         detail: ((PickerOption) -> NSAttributedString?)? = nil,
         onChange: @escaping (String) -> Void) {
        self.title = title
        self.options = options
        self.value = value
        self.icon = icon
        self.detail = detail
        self.onChange = onChange
    }

    func makeCoordinator() -> Coordinator { Coordinator(onChange: onChange) }

    func makeNSView(context: Context) -> NSPopUpButton {
        let popup = NSPopUpButton(frame: .zero, pullsDown: false)
        popup.target = context.coordinator
        popup.action = #selector(Coordinator.changed(_:))
        popup.setAccessibilityLabel(title)
        return popup
    }

    func updateNSView(_ popup: NSPopUpButton, context: Context) {
        context.coordinator.onChange = onChange
        context.coordinator.usesDetail = detail != nil
        let displayed = options.contains(where: { $0.value == value })
            ? options : [PickerOption(value, value.isEmpty ? " " : value)] + options
        let current = popup.itemArray.map { (($0.representedObject as? String) ?? "", $0.title) }
        let wanted = displayed.map { ($0.value, $0.label) }
        // Detail rows keep `title` = label (attributedTitle only adds line two), so this compare holds.
        if !zip(current, wanted).allSatisfy({ $0 == $1 }) || current.count != wanted.count {
            popup.removeAllItems()
            for option in displayed {
                let item = NSMenuItem(title: option.label, action: nil, keyEquivalent: "")
                item.representedObject = option.value
                if let icon = icon?(option) { item.image = Icons.image(icon) }
                if let d = detail?(option) {
                    let t = NSMutableAttributedString(string: option.label,
                                                      attributes: [.font: NSFont.systemFont(ofSize: NSFont.systemFontSize)])
                    t.append(NSAttributedString(string: "\n"))
                    t.append(d)
                    item.attributedTitle = t
                    item.title = option.label // keeps attributedTitle; title stays line one
                }
                popup.menu?.addItem(item)
            }
        }
        if let index = displayed.firstIndex(where: { $0.value == value }) { popup.selectItem(at: index) }
        Coordinator.showLineOne(popup, detail != nil)
        popup.isEnabled = isEnabled
        popup.setAccessibilityLabel(title)
    }

    @MainActor final class Coordinator: NSObject {
        var onChange: (String) -> Void
        var usesDetail = false
        init(onChange: @escaping (String) -> Void) { self.onChange = onChange }

        /// A two-line attributed menu row would otherwise render two lines in the closed popup.
        static func showLineOne(_ popup: NSPopUpButton, _ on: Bool) {
            guard on, let cell = popup.cell as? NSPopUpButtonCell, let item = popup.selectedItem else { return }
            cell.usesItemFromMenu = false
            cell.menuItem = NSMenuItem(title: item.title, action: nil, keyEquivalent: "")
        }

        @objc func changed(_ popup: NSPopUpButton) {
            guard let value = popup.selectedItem?.representedObject as? String else { return }
            Self.showLineOne(popup, usesDetail)
            onChange(value)
        }
    }
}
