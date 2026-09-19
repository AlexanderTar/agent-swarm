import AppKit
import SwarmBarKit
import SwiftUI

/// Settings window (§16.4), 740 × 520 pt with four tabs.
public struct SettingsView: View {
    @Bindable var model: SettingsModel

    public init(model: SettingsModel) { self.model = model }

    public var body: some View {
        // TabView is the top-level view here on purpose: macOS only gives the Settings window its
        // icon-over-label toolbar tabs when nothing wraps the TabView, so the save-error row rides
        // in as a bottom safe-area inset instead of a VStack sibling.
        TabView {
            AgentsTab(model: model).tabItem { Label(Copy.tabAgents, systemImage: "person.2") }
            DefaultsTab(model: model).tabItem { Label(Copy.tabDefaults, systemImage: "slider.horizontal.3") }
            NotificationsTab(model: model).tabItem { Label(Copy.tabNotifications, systemImage: "bell") }
            LimitsTab(model: model).tabItem { Label(Copy.tabLimits, systemImage: "speedometer") }
        }
        .safeAreaInset(edge: .bottom, spacing: 0) {
            if let error = model.saveError {
                HStack {
                    Text(error).foregroundStyle(.red)
                    Spacer()
                    if model.connected { Button(Copy.retry) { Task { await model.save() } } }
                }
                .padding(10)
                .background(.bar)
            }
        }
        .frame(width: 740, height: 520)
        .glassButtons()
        .task { await model.load() }
    }
}

struct AgentsTab: View {
    @Bindable var model: SettingsModel

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text(Copy.agentsUsedBySwarm).font(.headline)
            ForEach(model.agentRows) { row in
                VStack(alignment: .leading, spacing: 2) {
                    Toggle(isOn: Binding(get: { row.checked }, set: { on in Task { await model.setEnabled(row.kind, on) } })) {
                        HStack(spacing: 6) {
                            AgentIcon(row.kind)
                            Text(row.label).frame(width: 96, alignment: .leading)
                            Text(row.status).foregroundStyle(.secondary)
                        }
                    }
                    .toggleStyle(.checkbox)
                    .disabled(row.checkboxDisabled)
                    Group {
                        if let note = row.signInNote { Text(note) }
                        if let note = row.superpowersNote { Text(note) }
                    }
                    .font(.caption).foregroundStyle(.secondary).padding(.leading, 24)
                    if model.pendingDisable == row.kind, let notice = model.disableNotice {
                        HStack {
                            Text(notice).font(.caption)
                            Button(Copy.apply) { Task { await model.applyDisable() } }
                            Button(Copy.cancel) { model.cancelDisable() }
                        }
                        .padding(.leading, 24)
                    }
                }
            }
            if let error = model.agentsError { Text(error).foregroundStyle(.red) }
            Button(Copy.checkAgain) { Task { await model.checkAgain() } }.disabled(!model.connected)
            Spacer()
        }
        .padding(20)
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

struct DefaultsTab: View {
    @Bindable var model: SettingsModel

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(Copy.defaultsForNewAgents).font(.headline)
            Grid(alignment: .leading, horizontalSpacing: 12, verticalSpacing: 10) {
                GridRow {
                    Text(Copy.role).foregroundStyle(.secondary)
                    Text(Copy.agent).foregroundStyle(.secondary)
                    Text(Copy.model).foregroundStyle(.secondary)
                    Text(Copy.effort).foregroundStyle(.secondary)
                }
                ForEach(model.defaultsRows) { row in
                    GridRow {
                        Text(row.label)
                        OptionPicker(Copy.agent, options: row.agentOptions, value: row.agent,
                                     icon: { AgentKind(rawValue: $0.value).map(IconName.init) }) { v in
                            Task { await model.setAgent(row.role, v) }
                        }
                        .labelsHidden().frame(width: 132)
                        OptionPicker(Copy.model, options: row.modelOptions, value: row.model) { v in
                            Task { await model.setModel(row.role, v) }
                        }
                        .labelsHidden().frame(width: 200)
                        if let efforts = row.effortOptions {
                            OptionPicker(Copy.effort, options: efforts, value: row.effort) { v in
                                Task { await model.setEffort(row.role, v) }
                            }
                            .labelsHidden().frame(width: 170, alignment: .leading)
                        } else if row.model != RoleDefault.noAdvisorModel {
                            Text(Copy.notSupported).foregroundStyle(.secondary).frame(width: 170, alignment: .leading)
                        } else {
                            Text("").frame(width: 170, alignment: .leading)
                        }
                    }
                    if let error = row.error {
                        GridRow { Text(""); Text(error).foregroundStyle(.red).gridCellColumns(3) }
                    }
                    if let note = row.note {
                        GridRow { Text(""); Text(note).font(.caption).foregroundStyle(.secondary).gridCellColumns(3) }
                    }
                    if row.role == .advisor {
                        GridRow { Text(""); Text(Copy.advisorCaption).font(.caption).foregroundStyle(.secondary).gridCellColumns(3) }
                        Divider().gridCellUnsizedAxes(.horizontal)
                    }
                }
            }
            ForEach(model.staleNotes, id: \.self) { Text($0).font(.caption).foregroundStyle(.orange) }
            HStack {
                if let line = model.catalogLine { Text(line).font(.caption).foregroundStyle(.secondary) }
                Spacer()
                Button(Copy.refreshModels) { Task { await model.refreshModels() } }.disabled(!model.connected)
            }
            Spacer()
        }
        .padding(20)
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

struct NotificationsTab: View {
    @Bindable var model: SettingsModel

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Grid(alignment: .leading, horizontalSpacing: 24, verticalSpacing: 8) {
                GridRow {
                    Text("")
                    Text(Copy.notificationCenter).foregroundStyle(.secondary)
                    Text(Copy.sound).foregroundStyle(.secondary)
                }
                ForEach(model.levelRows) { row in
                    GridRow {
                        VStack(alignment: .leading) {
                            Text(row.label)
                            Text(row.caption).font(.caption).foregroundStyle(.secondary)
                        }
                        Toggle(Copy.notificationCenter, isOn: Binding(get: { row.center },
                                                                      set: { on in Task { await model.setCenter(row.level, on) } }))
                            .labelsHidden().toggleStyle(.switch)
                        Toggle(Copy.sound, isOn: Binding(get: { row.sound },
                                                         set: { on in Task { await model.setSound(row.level, on) } }))
                            .labelsHidden().toggleStyle(.switch).disabled(row.soundDisabled)
                    }
                }
            }
            Text(Copy.notificationsFooter).font(.caption).foregroundStyle(.secondary)
            Spacer()
        }
        .padding(20)
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

struct LimitsTab: View {
    @Bindable var model: SettingsModel

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            LimitField(model: model, title: Copy.maxOrchestrators, limit: .orchestrators)
            LimitField(model: model, title: Copy.maxAgents, limit: .agents)
            LimitField(model: model, title: Copy.maxAgentsPerItem, limit: .agentsPerRoot)
            Text(Copy.orchestratorLimitCaption).font(.caption).foregroundStyle(.secondary)
            if let notice = model.limitNotice {
                HStack {
                    Text(notice).font(.caption)
                    Button(Copy.apply) { Task { await model.applyLimit() } }
                }
            }
            LimitField(model: model, title: Copy.pauseDeadline, limit: .pauseDeadline, unit: Copy.seconds)
            Divider()
            Text(Copy.repositoryDiscovery).font(.headline)
            Text(Copy.discoveryCaption).font(.caption).foregroundStyle(.secondary)
            Text(Copy.excludedFolders)
            ForEach(model.visibleExcludes, id: \.self) { path in
                HStack {
                    Text(path)
                    Spacer()
                    IconButton("minus", help: Copy.removeFolder(path), disabled: !model.connected) {
                        Task { await model.removeExclude(path) }
                    }
                }
                .frame(maxWidth: 360)
            }
            Button(Copy.addFolder) {
                let panel = NSOpenPanel()
                panel.canChooseDirectories = true
                panel.canChooseFiles = false
                if panel.runModal() == .OK, let url = panel.url { Task { await model.addExclude(url.path) } }
            }
            .disabled(!model.connected)
            HStack {
                Text(model.scanLine).font(.caption).foregroundStyle(.secondary)
                IconButton("arrow.clockwise", help: Copy.rescanNow, disabled: !model.connected) {
                    Task { await model.rescanNow() }
                }
            }
            Divider()
            HStack {
                Text(Copy.menuBar)
                Toggle(Copy.compact, isOn: Binding(get: { model.compact }, set: { on in Task { await model.setCompact(on) } }))
                    .toggleStyle(.checkbox)
            }
            Spacer()
        }
        .padding(20)
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

/// A number field that saves when it loses focus or on Return (§16.4).
struct LimitField: View {
    @Bindable var model: SettingsModel
    let title: String
    let limit: SettingsModel.Limit
    var unit: String?
    @State private var text = ""
    @FocusState private var focused: Bool

    var body: some View {
        HStack {
            Text(title).frame(width: 280, alignment: .leading)
            TextField(title, text: $text)
                .labelsHidden()
                .frame(width: 56)
                .multilineTextAlignment(.trailing)
                .focused($focused)
                .onSubmit(commit)
                .onChange(of: focused) { _, isFocused in if !isFocused { commit() } }
            Text([unit, "(\(limit.range.lowerBound)–\(limit.range.upperBound))"].compactMap { $0 }.joined(separator: " "))
                .foregroundStyle(.secondary)
        }
        .onAppear { text = String(model.value(limit)) }
        .onChange(of: model.value(limit)) { _, v in text = String(v) }
    }

    private func commit() {
        guard let v = Int(text.trimmingCharacters(in: .whitespaces)) else {
            text = String(model.value(limit))
            return
        }
        Task {
            await model.setLimit(limit, v)
            text = String(model.value(limit))
        }
    }
}
