import AppKit
import SwarmBarKit
import SwiftUI

/// New orchestrator window (§16.3).
public struct NewOrchestratorView: View {
    @Bindable var form: NewOrchestratorForm
    let onStarted: (AgentNode) -> Void
    let onCancel: () -> Void
    @Environment(\.sizeCategory) private var sizeCategory

    public init(form: NewOrchestratorForm, onStarted: @escaping (AgentNode) -> Void, onCancel: @escaping () -> Void) {
        self.form = form
        self.onStarted = onStarted
        self.onCancel = onCancel
    }

    public var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            GeometryReader { geometry in
                if sizeCategory.isAccessibilityCategory || geometry.size.height < 600 {
                    ScrollView {
                        formContents(maxRows: 4)
                            .background(SubtleScrollerConfig())
                    }
                    .scrollIndicators(.automatic)
                } else {
                    formContents(maxRows: geometry.size.height >= 700 ? 8 : 6)
                        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
                }
            }
            Divider()
            HStack {
                if let caption = form.queuedCaption { Text(caption).font(.caption).foregroundStyle(.secondary) }
                Spacer()
                Button(Copy.cancel, action: onCancel).keyboardShortcut(.cancelAction)
                Button(form.startLabel) {
                    Task { if let agent = await form.submit() { onStarted(agent) } }
                }
                .keyboardShortcut(.defaultAction)
                .disabled(!form.canStart)
            }
            .padding(.horizontal, 22)
            .padding(.vertical, 10)
        }
        .frame(minWidth: 760, idealWidth: 820, maxWidth: .infinity,
               minHeight: 700, idealHeight: 790)
        .glassButtons()
        .task { await form.load() }
    }

    private func formContents(maxRows: Int) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(Copy.newOrchestrator).font(.title3.bold())
            if let failure = form.failure {
                Label(failure, systemImage: "exclamationmark.triangle.fill").foregroundStyle(.red)
            }
            nameField
            intentField
            reposField(maxRows: maxRows)
            agentFields
            VStack(alignment: .leading, spacing: 4) {
                Text(Copy.requestOptional)
                RequestEditor(text: $form.request)
            }
            .frame(maxHeight: .infinity, alignment: .top)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .padding(.horizontal, 22)
        .padding(.vertical, 12)
    }

    private var nameField: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(Copy.name)
            TextField(Copy.name, text: $form.name).textFieldStyle(.roundedBorder).labelsHidden()
            if let error = form.nameError {
                Text(error).font(.caption).foregroundStyle(.red)
            } else if !form.preview.isEmpty {
                Text(form.preview).font(.caption.monospaced()).foregroundStyle(.secondary)
            }
        }
    }

    private var intentField: some View {
        VStack(alignment: .leading, spacing: 4) {
            Picker(Copy.intent, selection: $form.intent) {
                Text(Copy.choreIntent).tag(SpikeIntent.chore)
                Text(Copy.featureSpike).tag(SpikeIntent.feature)
                Text(Copy.debugSpike).tag(SpikeIntent.debug)
            }
            .pickerStyle(.segmented)
            Text(form.intentCaption).font(.caption).foregroundStyle(.secondary)
        }
    }

    private func reposField(maxRows: Int) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(Copy.repositoriesOptional)
            Text(Copy.reposCaption).font(.caption).foregroundStyle(.secondary)
            RepoChooser(rows: form.rows, maxRows: maxRows, selection: Binding(
                get: { Set(form.selection) },
                set: { selected in form.selection = form.rows.map(\.id).filter(selected.contains) }
            ), emptyTitle: emptyRepoTitle)
            HStack {
                Button(Copy.addFolder) { chooseFolder() }
                Spacer()
                Text(form.scanLine).font(.caption).foregroundStyle(.secondary)
                Button(Copy.rescan) { Task { await form.rescan() } }.disabled(!form.connected)
            }
            if let error = form.repoError { Text(error).font(.caption).foregroundStyle(.red) }
            if let notice = form.selectionNotice { Text(notice).font(.caption).foregroundStyle(.secondary) }
            if !form.selectedLine.isEmpty { Text(form.selectedLine).font(.caption) }
        }
    }

    private var emptyRepoTitle: String {
        if form.repoError != nil { return "Repositories unavailable." }
        if form.repos.scanning { return "Scanning repositories…" }
        return "No repositories found."
    }

    private var advisorAgentValue: String {
        if case let .pair(agent, _) = form.advisor { return agent.rawValue }
        return "none"
    }

    private var advisorModelValue: String {
        if case let .pair(_, model) = form.advisor { return model }
        return "—"
    }

    private var agentFields: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 8) {
                Text(Copy.agent).frame(width: 72, alignment: .leading)
                WideOptionPicker(Copy.agent, options: form.agentOptions, value: form.choice.agent?.rawValue ?? "",
                             icon: { AgentKind(rawValue: $0.value).map(IconName.init) }) { form.setAgent($0) }
                    .frame(width: 150)
                Text(Copy.model).frame(width: 52, alignment: .leading)
                WideOptionPicker(Copy.model, options: form.modelOptions, value: form.choice.model) { form.setModel($0) }
                    .frame(minWidth: 300, maxWidth: .infinity)
            }
            if let error = form.errors.agent, !error.isEmpty { Text(error).font(.caption).foregroundStyle(.red) }
            if let error = form.errors.model { Text(error).font(.caption).foregroundStyle(.red) }
            HStack(spacing: 8) {
                Text(Copy.advisor).frame(width: 72, alignment: .leading)
                WideOptionPicker(Copy.advisor, options: form.advisorAgentOptions, value: advisorAgentValue,
                             icon: { AgentKind(rawValue: $0.value).map(IconName.init) }) { form.setAdvisorAgent($0) }
                    .frame(width: 150)
                Text(Copy.model).frame(width: 52, alignment: .leading)
                WideOptionPicker("Advisor model", options: form.advisorModelOptions, value: advisorModelValue) { form.setAdvisorModel($0) }
                    .frame(minWidth: 300, maxWidth: .infinity).disabled(form.advisor == .none)
            }
            if let error = form.errors.advisor { Text(error).font(.caption).foregroundStyle(.red) }
            Text(Copy.defaultsFromSettings).font(.caption).foregroundStyle(.secondary)
        }
    }

    private func chooseFolder() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.showsHiddenFiles = true
        guard panel.runModal() == .OK, let url = panel.url else { return }
        Task { await form.addFolder(url.path) }
    }
}

/// A native list supplies macOS selection, keyboard range selection, and VoiceOver row focus.
struct RepoChooser: View {
    let rows: [Repo]
    var maxRows = 8
    @Binding var selection: Set<String>
    var emptyTitle = "No repositories found."

    static func visibleHeight(for _: Int, maxRows: Int = 8) -> CGFloat {
        CGFloat(min(8, max(1, maxRows))) * 30
    }

    var body: some View {
        List(rows, selection: $selection) { repo in
            HStack(spacing: 12) {
                Text(repo.name).lineLimit(1)
                Text(RepoPicker.subtitle(repo))
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
                Spacer(minLength: 0)
            }
            .frame(height: 30, alignment: .leading)
            .contentShape(Rectangle())
            .listRowInsets(EdgeInsets(top: 0, leading: 8, bottom: 0, trailing: 8))
            .accessibilityElement(children: .combine)
            .accessibilityLabel("\(repo.name), \(repo.path)")
            .help(repo.path)
        }
        .listStyle(.plain)
        .scrollContentBackground(.hidden)
        .background(SubtleScrollerConfig())
        .frame(height: Self.visibleHeight(for: rows.count, maxRows: maxRows))
        .overlay {
            if rows.isEmpty {
                VStack(spacing: 4) {
                    Text(emptyTitle).font(.callout)
                    Text("Add a folder or rescan.").font(.caption)
                }
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .padding(16)
            }
        }
        .overlay(RoundedRectangle(cornerRadius: 5).stroke(.separator))
        .accessibilityLabel(Copy.repositoriesOptional)
    }
}

struct RequestEditor: View {
    @Binding var text: String
    static let minimumHeight: CGFloat = 108

    var body: some View {
        TextEditor(text: $text)
            .background(SubtleScrollerConfig(adjacentScrollView: true))
            .frame(minHeight: Self.minimumHeight, maxHeight: .infinity)
            .border(.separator)
    }
}

/// AppKit keeps the visible popup as wide as its SwiftUI frame, including at the window minimum.
private struct WideOptionPicker: NSViewRepresentable {
    @Environment(\.isEnabled) private var isEnabled
    let title: String
    let options: [PickerOption]
    let value: String
    let icon: ((PickerOption) -> IconName?)?
    let onChange: (String) -> Void

    init(_ title: String, options: [PickerOption], value: String,
         icon: ((PickerOption) -> IconName?)? = nil, onChange: @escaping (String) -> Void) {
        self.title = title
        self.options = options
        self.value = value
        self.icon = icon
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
        let displayed = options.contains(where: { $0.value == value })
            ? options : [PickerOption(value, value.isEmpty ? " " : value)] + options
        let current = popup.itemArray.map { (($0.representedObject as? String) ?? "", $0.title) }
        let wanted = displayed.map { ($0.value, $0.label) }
        if !zip(current, wanted).allSatisfy({ $0 == $1 }) || current.count != wanted.count {
            popup.removeAllItems()
            for option in displayed {
                let item = NSMenuItem(title: option.label, action: nil, keyEquivalent: "")
                item.representedObject = option.value
                if let icon = icon?(option) { item.image = Icons.image(icon) }
                popup.menu?.addItem(item)
            }
        }
        if let index = displayed.firstIndex(where: { $0.value == value }) { popup.selectItem(at: index) }
        popup.isEnabled = isEnabled
        popup.setAccessibilityLabel(title)
    }

    @MainActor final class Coordinator: NSObject {
        var onChange: (String) -> Void
        init(onChange: @escaping (String) -> Void) { self.onChange = onChange }
        @objc func changed(_ popup: NSPopUpButton) {
            guard let value = popup.selectedItem?.representedObject as? String else { return }
            onChange(value)
        }
    }
}
