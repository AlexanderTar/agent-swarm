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
        .frame(minHeight: 420, idealHeight: 720)
        .task { await form.load() }
        .task(id: form.query) {
            try? await Task.sleep(for: .milliseconds(250))
            if !Task.isCancelled { await form.search() }
        }
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
            TextField(Copy.searchRepos, text: $form.query).textFieldStyle(.roundedBorder)
            VStack(alignment: .leading, spacing: 2) {
                ForEach(form.sections) { section in
                    HStack {
                        Text(section.title).font(.caption.bold()).foregroundStyle(.secondary)
                        Spacer()
                        if section.groupName != nil {
                            Button(Copy.all) { form.selectAll(section) }.buttonStyle(.link).font(.caption)
                        }
                    }
                    ForEach(section.repos) { repo in
                        RepoRow(repo: repo, selected: form.selection.contains(repo.id)) { form.toggle(repo) }
                    }
                }
            }
            HStack {
                Button(Copy.addFolder) { chooseFolder() }
                Spacer()
                Text(form.scanLine).font(.caption).foregroundStyle(.secondary)
                Button(Copy.rescan) { Task { await form.rescan() } }.disabled(!form.connected)
            }
            if let error = form.repoError { Text(error).font(.caption).foregroundStyle(.red) }
            if !form.selectedLine.isEmpty { Text(form.selectedLine).font(.caption) }
        }
    }

    private var agentFields: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                OptionPicker(Copy.agent, options: form.agentOptions, value: form.choice.agent?.rawValue ?? "") { form.setAgent($0) }
                OptionPicker(Copy.model, options: form.modelOptions, value: form.choice.model) { form.setModel($0) }
            }
            if let error = form.errors.agent, !error.isEmpty { Text(error).font(.caption).foregroundStyle(.red) }
            if let error = form.errors.model { Text(error).font(.caption).foregroundStyle(.red) }
            if let efforts = form.effortOptions {
                OptionPicker(Copy.effort, options: efforts, value: form.choice.effort) { form.setEffort($0) }
                    .frame(maxWidth: 260)
            }
            if let note = form.effortNote { Text(note).font(.caption).foregroundStyle(.secondary) }
            OptionPicker(Copy.advisor, options: form.advisorOptions, value: form.advisor.encoded) { form.setAdvisor($0) }
                .frame(maxWidth: 320)
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

struct RepoRow: View {
    let repo: Repo
    let selected: Bool
    let toggle: () -> Void

    var body: some View {
        Toggle(isOn: Binding(get: { selected }, set: { _ in toggle() })) {
            VStack(alignment: .leading, spacing: 1) {
                HStack {
                    Text(repo.name)
                    Text(RepoPicker.subtitle(repo)).foregroundStyle(.secondary)
                }
                if let note = RepoPicker.note(repo) {
                    Text(note).font(.caption).foregroundStyle(repo.missing ? .red : .secondary)
                }
            }
        }
        .toggleStyle(.checkbox)
        .disabled(repo.missing)
        .padding(.leading, 8)
    }
}
