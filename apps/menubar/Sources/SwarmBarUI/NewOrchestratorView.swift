import AppKit
import SwarmBarKit
import SwiftUI

/// New orchestrator window (§16.3), 480 pt wide.
public struct NewOrchestratorView: View {
    @Bindable var form: NewOrchestratorForm
    let onStarted: (AgentNode) -> Void
    let onCancel: () -> Void

    public init(form: NewOrchestratorForm, onStarted: @escaping (AgentNode) -> Void, onCancel: @escaping () -> Void) {
        self.form = form
        self.onStarted = onStarted
        self.onCancel = onCancel
    }

    public var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    Text(Copy.newOrchestrator).font(.title3.bold())
                    if let failure = form.failure {
                        Label(failure, systemImage: "exclamationmark.triangle.fill").foregroundStyle(.red)
                    }
                    nameField
                    intentField
                    reposField
                    agentFields
                    VStack(alignment: .leading, spacing: 4) {
                        Text(Copy.requestOptional)
                        TextEditor(text: $form.request).frame(minHeight: 48).border(.separator)
                    }
                }
                .padding(16)
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
            .padding(12)
        }
        .frame(width: 480)
        .glassButtons()
        .frame(minHeight: 420, idealHeight: 720)
        .task { await form.load() }
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

    private var reposField: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(Copy.repositoriesOptional)
            Text(Copy.reposCaption).font(.caption).foregroundStyle(.secondary)
            RepoChooser(rows: form.rows, selection: Binding(
                get: { Set(form.selection) },
                set: { selected in form.selection = form.rows.map(\.id).filter(selected.contains) }
            ))
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
                OptionPicker(Copy.agent, options: form.agentOptions, value: form.choice.agent?.rawValue ?? "",
                             icon: { AgentKind(rawValue: $0.value).map(IconName.init) }) { form.setAgent($0) }
                    .labelsHidden().frame(minWidth: 150)
                Text(Copy.model).frame(width: 52, alignment: .leading)
                OptionPicker(Copy.model, options: form.modelOptions, value: form.choice.model) { form.setModel($0) }
                    .labelsHidden().frame(minWidth: 300)
            }
            if let error = form.errors.agent, !error.isEmpty { Text(error).font(.caption).foregroundStyle(.red) }
            if let error = form.errors.model { Text(error).font(.caption).foregroundStyle(.red) }
            HStack(spacing: 8) {
                Text(Copy.advisor).frame(width: 72, alignment: .leading)
                OptionPicker(Copy.advisor, options: form.advisorAgentOptions, value: advisorAgentValue,
                             icon: { AgentKind(rawValue: $0.value).map(IconName.init) }) { form.setAdvisorAgent($0) }
                    .labelsHidden().frame(minWidth: 150)
                Text(Copy.model).frame(width: 52, alignment: .leading)
                OptionPicker("Advisor model", options: form.advisorModelOptions, value: advisorModelValue) { form.setAdvisorModel($0) }
                    .labelsHidden().frame(minWidth: 300).disabled(form.advisor == .none)
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
    @Binding var selection: Set<String>

    static func visibleHeight(for count: Int) -> CGFloat { CGFloat(min(8, max(1, count))) * 32 }

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
            .accessibilityElement(children: .combine)
            .accessibilityLabel("\(repo.name), \(repo.path)")
            .help(repo.path)
        }
        .listStyle(.plain)
        .scrollContentBackground(.hidden)
        .background(SubtleScrollerConfig())
        .frame(height: Self.visibleHeight(for: rows.count))
        .overlay(RoundedRectangle(cornerRadius: 5).stroke(.separator))
        .accessibilityLabel(Copy.repositoriesOptional)
    }
}
