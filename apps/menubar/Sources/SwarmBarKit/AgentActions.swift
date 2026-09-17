import Foundation

/// What an agent row shows: the session state, plus the list-only states (§16.2).
public enum DisplayState: String, Sendable, CaseIterable {
    case spawning, running, pauseRequested, quiescing, stopping, paused
    case interrupted, completed, failed, crashed, cancelled
    case queued, waiting, stale, preflightFailed

    public init(_ a: AgentNode) {
        if a.state == .queued { self = .queued; return }
        guard let s = a.session else {
            self = a.preflightError != nil ? .preflightFailed : .queued
            return
        }
        switch s.state {
        case .running: self = s.waiting ? .waiting : s.stale ? .stale : .running
        case .spawning: self = .spawning
        case .pauseRequested: self = .pauseRequested
        case .quiescing: self = .quiescing
        case .stopping: self = .stopping
        case .paused: self = .paused
        case .interrupted: self = .interrupted
        case .completed: self = .completed
        case .failed: self = .failed
        case .crashed: self = .crashed
        case .cancelled: self = .cancelled
        }
    }

    /// Line-2 label; nil while running (§16.2 table).
    public var label: String? {
        switch self {
        case .running: return nil
        case .spawning: return "Starting"
        case .pauseRequested: return "Pause requested"
        case .quiescing: return "Finishing current step"
        case .stopping: return "Stopping"
        case .paused: return "Paused"
        case .interrupted: return "Interrupted"
        case .crashed: return "Crashed"
        case .failed, .preflightFailed: return "Failed"
        case .completed: return "Completed"
        case .cancelled: return "Cancelled"
        case .queued: return "Queued"
        case .waiting: return "Waiting"
        case .stale: return "No activity for 30 min"
        }
    }

    public var tone: DotTone {
        switch self {
        case .running: return .green
        case .waiting: return .greenHollow
        case .spawning: return .greyPulse
        case .queued, .completed, .cancelled: return .grey
        case .pauseRequested, .quiescing, .stopping, .stale: return .amber
        case .paused: return .hollow
        case .interrupted, .crashed, .failed, .preflightFailed: return .red
        }
    }

    /// Counted by "Pause all" as still settling.
    public var isPausing: Bool { [.pauseRequested, .quiescing, .stopping].contains(self) }
    /// Something "Pause all" can pause.
    public var isPausable: Bool { [.spawning, .running, .waiting, .stale].contains(self) }
}

public enum DotTone: String, Sendable {
    case green, greenHollow, grey, greyPulse, amber, hollow, red
}

public struct AgentAction: Equatable, Sendable, Identifiable {
    public enum Placement: Sendable { case button, menu }
    public var endpoint: AgentEndpoint
    public var label: String
    public var disabled = false
    public var scope: PauseScope?
    public var confirm: String?
    public var placement: Placement
    public var id: String { endpoint.rawValue }
}

public enum AgentTree {
    public static func isFinished(_ a: AgentNode) -> Bool { a.state == .finished || a.state == .acknowledged }

    /// Live descendants (children, recursively; finished ones excluded).
    public static func countLive(_ a: AgentNode) -> Int {
        a.children.reduce(0) { $0 + 1 + countLive($1) }
    }

    /// Every node depth-first, including finished children.
    public static func flatten(_ nodes: [AgentNode]) -> [AgentNode] {
        nodes.flatMap { [$0] + flatten($0.children) + flatten($0.finished) }
    }

    /// Exactly the §10.7 row for this agent. `tmuxAlive` gates the terminal button (§16.2);
    /// `connected == false` disables everything except the terminal (§16.2 daemon down).
    public static func actions(_ a: AgentNode, tmuxAlive: Bool, connected: Bool) -> [AgentAction] {
        if isFinished(a) { return [] }
        let orch = a.role == .orchestrator
        let live = countLive(a)
        let off = !connected
        let terminal = AgentAction(endpoint: .terminal, label: Copy.openTerminal, disabled: !tmuxAlive, placement: .button)
        let cancel = AgentAction(endpoint: .cancel, label: Copy.cancel, disabled: off,
                                 confirm: orch && live > 0 ? Copy.cancelOrchestrator(a.name, live) : nil, placement: .menu)
        let ack = AgentAction(endpoint: .ack, label: Copy.acknowledge, disabled: off, placement: .menu)
        let retry = AgentAction(endpoint: .retry, label: Copy.retry, disabled: off, placement: .button)
        let resume = AgentAction(endpoint: .resume, label: Copy.resume, disabled: off, placement: .button)
        switch DisplayState(a) {
        case .queued:
            return [cancel]
        case .spawning:
            return [terminal, cancel]
        case .running, .waiting, .stale:
            let pause = AgentAction(endpoint: .pause, label: orch ? Copy.pauseGroup : Copy.pause, disabled: off,
                                    scope: orch ? .subtree : .session, placement: .button)
            return [terminal, pause, cancel]
        case .pauseRequested, .quiescing, .stopping:
            return [terminal, AgentAction(endpoint: .pause, label: Copy.pausing, disabled: true, placement: .button)]
        case .paused:
            return [resume, cancel]
        case .interrupted:
            return [resume, ack, cancel]
        case .crashed, .failed:
            return tmuxAlive ? [retry, ack, terminal] : [retry, ack]
        case .preflightFailed:
            return [retry, cancel]
        case .completed, .cancelled:
            return []
        }
    }

    public struct Row: Equatable, Sendable, Identifiable {
        public enum Kind: Equatable, Sendable {
            case agent(AgentNode)
            /// "Finished (n)" under `parent` ("" for top-level).
            case finished(parent: String, count: Int)
        }
        public var kind: Kind
        public var depth: Int
        /// Disclosure state; nil when the row has nothing to disclose.
        public var expanded: Bool?

        public var id: String {
            switch kind {
            case let .agent(a): return "agent:" + a.name
            case let .finished(parent, _): return "finished:" + parent
            }
        }
    }

    /// Visible rows. Orchestrators start expanded (`collapsed` holds the user's closes);
    /// "Finished (n)" groups start closed (`openFinished` holds opened parents, "" = top level).
    public static func rows(_ roots: [AgentNode], collapsed: Set<String>, openFinished: Set<String>) -> [Row] {
        var out: [Row] = []
        func add(_ nodes: [AgentNode], finished: [AgentNode], parent: String, depth: Int) {
            for n in nodes {
                let disclosable = !n.children.isEmpty || !n.finished.isEmpty
                let open = !collapsed.contains(n.name)
                out.append(Row(kind: .agent(n), depth: depth, expanded: disclosable ? open : nil))
                if disclosable && open {
                    add(n.children, finished: n.finished, parent: n.name, depth: depth + 1)
                }
            }
            if !finished.isEmpty {
                let open = openFinished.contains(parent)
                out.append(Row(kind: .finished(parent: parent, count: finished.count), depth: depth, expanded: open))
                if open {
                    for f in finished { out.append(Row(kind: .agent(f), depth: depth, expanded: nil)) }
                }
            }
        }
        add(roots.filter { !isFinished($0) }, finished: roots.filter(isFinished), parent: "", depth: 0)
        return out
    }

    /// Line 2: "Coder · TASK-101", plus the state label when not running.
    public static func subtitle(_ a: AgentNode) -> String {
        ([Copy.roleLabel(a.role), a.itemKey] + [DisplayState(a).label].compactMap { $0 }).joined(separator: " · ")
    }
}
