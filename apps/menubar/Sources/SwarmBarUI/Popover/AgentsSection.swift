import SwarmBarKit
import SwiftUI

struct AgentsSection: View {
    @Bindable var model: AppModel
    let openNewOrchestrator: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            SectionHeader(Copy.agents, open: model.isOpen(.agents),
                          toggle: { model.setSection(.agents, open: !model.isOpen(.agents)) }) {
                Button(model.pauseAllLabel) { Task { await model.pauseAll() } }
                    .disabled(model.pauseAllDisabled)
            }
            if let error = model.actionError {
                Text(error).font(.caption).foregroundStyle(.red)
            }
            if model.isOpen(.agents) {
                if model.agentRows.isEmpty {
                    HStack {
                        Text(Copy.emptyAgents).foregroundStyle(.secondary)
                        Spacer()
                        Button(Copy.newOrchestrator, action: openNewOrchestrator).disabled(!model.connected)
                    }
                }
                ForEach(model.agentRows) { row in
                    switch row.kind {
                    case let .agent(a):
                        AgentRowView(model: model, agent: a, depth: row.depth, expanded: row.expanded)
                    case let .finished(parent, count):
                        Button {
                            model.toggleFinished(parent)
                        } label: {
                            HStack(spacing: 4) {
                                Image(systemName: row.expanded == true ? "chevron.down" : "chevron.right").frame(width: 12)
                                Text(Copy.finished(count)).foregroundStyle(.secondary)
                            }
                        }
                        .buttonStyle(.plain)
                        .padding(.leading, CGFloat(row.depth) * 16)
                        .frame(minHeight: 28)
                    }
                }
            }
        }
    }
}

struct AgentRowView: View {
    @Bindable var model: AppModel
    let agent: AgentNode
    let depth: Int
    let expanded: Bool?
    @State private var confirming: AgentAction?

    var body: some View {
        let state = DisplayState(agent)
        let actions = model.actions(agent)
        HStack(alignment: .center, spacing: 6) {
            if let expanded {
                Button { model.toggleAgent(agent.name) } label: {
                    Image(systemName: expanded ? "chevron.down" : "chevron.right").frame(width: 12, height: 28)
                }
                .buttonStyle(.plain)
            } else {
                Color.clear.frame(width: 12)
            }
            AgentIcon(agent.kind)
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 6) {
                    Text(agent.name).lineLimit(1).truncationMode(.middle).help(agent.name)
                    StateDot(state.tone)
                }
                Text(AgentTree.subtitle(agent)).font(.caption).foregroundStyle(.secondary).lineLimit(1)
            }
            Spacer(minLength: 4)
            ForEach(actions.filter { $0.placement == .button }) { a in
                IconButton(symbol(a), help: a.label, disabled: a.disabled) { run(a) }
            }
        }
        .padding(.leading, CGFloat(depth) * 16)
        .frame(minHeight: 44)
        .contextMenu {
            ForEach(actions) { a in
                Button(a.label) { run(a) }.disabled(a.disabled)
            }
        }
        .confirmationDialog(confirming?.confirm ?? "", isPresented: Binding(get: { confirming != nil }, set: { if !$0 { confirming = nil } })) {
            Button(Copy.cancel, role: .destructive) {
                if let a = confirming { Task { await model.perform(a, on: agent) } }
                confirming = nil
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel("\(agent.name), \(state.label ?? "Running")")
    }

    private func run(_ a: AgentAction) {
        if a.confirm != nil {
            confirming = a
        } else {
            Task { await model.perform(a, on: agent) }
        }
    }

    private func symbol(_ a: AgentAction) -> String {
        switch a.endpoint {
        case .terminal: return "terminal"
        case .pause: return "pause.fill"
        case .resume: return "play.fill"
        case .retry: return "arrow.clockwise"
        case .cancel: return "xmark"
        case .ack: return "checkmark"
        }
    }
}
