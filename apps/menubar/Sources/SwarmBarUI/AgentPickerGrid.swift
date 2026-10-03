import SwarmBarKit
import SwiftUI

/// Column widths shared by the orchestrator grid and the stacked worker-overrides grid, so
/// their Agent/Model/Effort columns line up in one window.
enum PickerColumns {
    static let label: CGFloat = 70
}

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
                    Text(Copy.agent).frame(width: PickerColumns.label, alignment: .leading)
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
                    Text(Copy.advisor).frame(width: PickerColumns.label, alignment: .leading)
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
        }
    }
}

/// AppKit keeps the visible popup as wide as its SwiftUI frame, including at the window minimum.
/// With `detail`, menu rows get a second line and the closed popup shows both lines.
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
        let popup = TwoLinePopUpButton(frame: .zero, pullsDown: false)
        popup.twoLine = detail != nil
        popup.target = context.coordinator
        popup.action = #selector(Coordinator.changed(_:))
        popup.setAccessibilityLabel(title)
        return popup
    }

    func sizeThatFits(_ proposal: ProposedViewSize, nsView popup: NSPopUpButton, context: Context) -> CGSize? {
        // Take exactly the proposed width (never the title's intrinsic one) so a long label
        // can't push a popup past its column into the next label.
        let width = proposal.width ?? popup.intrinsicContentSize.width
        guard detail != nil else { return CGSize(width: width, height: popup.intrinsicContentSize.height) }
        return CGSize(width: width, height: TwoLinePopUpButton.height)
    }

    func updateNSView(_ popup: NSPopUpButton, context: Context) {
        context.coordinator.onChange = onChange
        context.coordinator.usesDetail = detail != nil
        let displayed = options.contains(where: { $0.value == value })
            ? options : [PickerOption(value, value.isEmpty ? " " : value)] + options
        // Detail rows keep `title` = label (attributedTitle only adds line two), so line two is
        // compared separately: a status/model-label change there must rebuild the menu too.
        let current = popup.itemArray.map { (($0.representedObject as? String) ?? "", $0.title, $0.attributedTitle?.string.components(separatedBy: "\n").dropFirst().joined(separator: "\n") ?? "") }
        let wanted = displayed.map { ($0.value, $0.label, detail?($0)?.string ?? "") }
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

        /// The cell would otherwise re-derive its title from the menu and drop line two; pin a copy
        /// of the selected row (attributed two-line title included) as the closed-state face.
        static func showLineOne(_ popup: NSPopUpButton, _ on: Bool) {
            guard on, let cell = popup.cell as? NSPopUpButtonCell,
                  let item = popup.selectedItem?.copy() as? NSMenuItem else { return }
            cell.usesItemFromMenu = false
            cell.menuItem = item
        }

        @objc func changed(_ popup: NSPopUpButton) {
            guard let value = popup.selectedItem?.representedObject as? String else { return }
            Self.showLineOne(popup, usesDetail)
            onChange(value)
        }
    }
}

/// Popup that is one two-line row tall (title + detail) instead of AppKit's single-line 21pt.
final class TwoLinePopUpButton: NSPopUpButton {
    override class var cellClass: AnyClass? {
        get { TwoLinePopUpButtonCell.self }
        set {}
    }
    static let height: CGFloat = 38
    var twoLine = false {
        didSet { if twoLine != oldValue { bezelStyle = twoLine ? .regularSquare : .rounded; invalidateIntrinsicContentSize() } }
    }
    override var intrinsicContentSize: NSSize {
        var size = super.intrinsicContentSize
        if twoLine { size.height = Self.height }
        return size
    }
}

/// The stock cell truncates an attributed title to one line; draw both lines, vertically centred.
final class TwoLinePopUpButtonCell: NSPopUpButtonCell {
    override func drawTitle(_ title: NSAttributedString, withFrame frame: NSRect, in controlView: NSView) -> NSRect {
        guard title.string.contains("\n") else { return super.drawTitle(title, withFrame: frame, in: controlView) }
        let text = NSMutableAttributedString(attributedString: title)
        let color: NSColor = isEnabled ? .labelColor : .disabledControlTextColor
        text.enumerateAttribute(.foregroundColor, in: NSRange(location: 0, length: text.length)) { value, range, _ in
            if value == nil { text.addAttribute(.foregroundColor, value: color, range: range) }
        }
        let width = max(frame.width, controlView.bounds.width - frame.minX - 24) // 24 = arrows
        let height = ceil(text.boundingRect(with: NSSize(width: width, height: .greatestFiniteMagnitude), options: .usesLineFragmentOrigin).height)
        // `frame` is the one-line title strip; centre against the whole control instead.
        let rect = NSRect(x: frame.minX, y: controlView.bounds.minY + max(0, (controlView.bounds.height - height) / 2), width: width, height: height)
        text.draw(with: rect, options: [.usesLineFragmentOrigin, .truncatesLastVisibleLine])
        return rect
    }
}
