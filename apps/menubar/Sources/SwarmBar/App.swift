import AppKit
import SwarmBarKit
import SwarmBarUI
import SwiftUI

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    let model: AppModel
    private let poster: NotificationPosting
    private var watcher: StatusItemWatcher?

    /// `SWARM_MOCK_FIXTURES=<dir> swift run SwarmBar` shows fixture data with no daemon, no event
    /// stream, in-memory preferences, a temporary cache, and no real terminal/notification side
    /// effects. An unbundled binary can't use Notification Center either, so it gets a silent
    /// poster.
    override init() {
        let env = ProcessInfo.processInfo.environment
        let endpoint = DaemonEndpoint.fromEnvironment(env)
        let mockDir = env["SWARM_MOCK_FIXTURES"]
        let mock = mockDir.flatMap { try? MockDaemonClient(fixtures: URL(fileURLWithPath: $0)) }
        let caches = mock == nil
            ? FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0].appendingPathComponent("dev.swarm.menubar")
            : FileManager.default.temporaryDirectory.appendingPathComponent("swarmbar-mock")
        let userNotifications = (Bundle.main.bundleIdentifier == nil || mock != nil) ? nil : UserNotificationPoster()
        poster = userNotifications ?? SilentPoster()
        var connect = EventStream.urlSession(URLSession(configuration: .default))
        let terminals: Terminals
        if mock != nil {
            connect = { _ in
                try await Task.sleep(for: .seconds(365 * 86400))
                throw CancellationError()
            }
            terminals = Terminals(runner: NoOpCommandRunner(), script: NoOpScriptRunner(), ghosttyPIDs: { [] })
        } else {
            terminals = Terminals(runner: ProcessRunner(), script: AppleScriptRunner(), ghosttyPIDs: Ghostty.pids)
        }
        model = AppModel(client: mock ?? HTTPDaemonClient(endpoint: endpoint), endpoint: endpoint,
                         terminals: terminals,
                         poster: poster,
                         defaults: mock == nil ? UserDefaults.standard : MemoryStore(),
                         cache: StateCache(url: caches.appendingPathComponent("state.json")),
                         connect: connect,
                         openURL: { NSWorkspace.shared.open($0) })
        super.init()
        userNotifications?.onAction = { [weak self] action, info, text in
            await self?.model.handleNotificationAction(action, userInfo: info, text: text)
        }
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.accessory)
        watcher = StatusItemWatcher { [weak self] visible in self?.model.labelVisible(visible) }
        watcher?.start()
        Task {
            await model.start()
            if ProcessInfo.processInfo.environment["SWARM_SMOKE"] != nil {
                let s = model.state
                print("smoke: state agents=\(s.agents.count) requests=\(s.requests.count) active=\(s.activeCount)")
                fflush(stdout)
            }
        }
    }

    func updateTooltips() { watcher?.setTooltips(model.label) }
}

@main
struct SwarmBarApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate

    var body: some Scene {
        MenuBarExtra {
            PopoverHost(model: delegate.model)
        } label: {
            LabelHost(model: delegate.model, onChange: delegate.updateTooltips)
        }
        .menuBarExtraStyle(.window)

        Window(Copy.newOrchestrator, id: "new-orchestrator") {
            NewOrchestratorHost(model: delegate.model)
        }
        .windowResizability(.contentMinSize)

        Settings {
            SettingsHost(model: delegate.model)
        }
    }
}

struct LabelHost: View {
    let model: AppModel
    let onChange: () -> Void

    var body: some View {
        Image(nsImage: LabelRenderer.image(model.label))
            .onChange(of: model.label) { onChange() }
    }
}

struct PopoverHost: View {
    let model: AppModel
    @Environment(\.openWindow) private var openWindow
    @Environment(\.openSettings) private var openSettings

    var body: some View {
        PopoverView(model: model,
                    openNewOrchestrator: {
                        NSApp.activate(ignoringOtherApps: true)
                        openWindow(id: "new-orchestrator")
                    },
                    openSettings: {
                        NSApp.activate(ignoringOtherApps: true)
                        openSettings()
                    })
    }
}

struct NewOrchestratorHost: View {
    let model: AppModel
    @State private var form: NewOrchestratorForm?
    @Environment(\.dismissWindow) private var dismissWindow

    var body: some View {
        Group {
            if let form {
                NewOrchestratorView(form: form,
                                    onStarted: { _ in
                                        dismissWindow(id: "new-orchestrator")
                                        Task { await model.refresh() }
                                    },
                                    onCancel: { dismissWindow(id: "new-orchestrator") })
            } else {
                ProgressView().frame(width: 480, height: 200)
            }
        }
        .onAppear { form = model.makeNewOrchestratorForm() }
        .onDisappear { form = nil }
        .onChange(of: model.connected) { _, up in form?.connected = up }
    }
}

/// Mock mode's terminal seam: never shells out to a real tmux/Ghostty, mirroring the SSE-connect
/// neutering above so `SWARM_MOCK_FIXTURES` never has a real system side effect.
struct NoOpCommandRunner: CommandRunning {
    func run(_ executable: String, _ args: [String]) async -> CommandResult { CommandResult(status: 1) }
}

struct NoOpScriptRunner: ScriptRunning {
    struct NotAvailable: Error {}
    func run(_ source: String) async throws -> String { throw NotAvailable() }
}

struct SettingsHost: View {
    let model: AppModel
    @State private var settings: SettingsModel?

    var body: some View {
        Group {
            if let settings { SettingsView(model: settings) } else { ProgressView().frame(width: 740, height: 520) }
        }
        .onAppear { settings = model.makeSettings() }
        .onChange(of: model.connected) { _, up in settings?.connected = up }
        .onChange(of: model.state.agents) { _, agents in settings?.agents = agents }
    }
}
