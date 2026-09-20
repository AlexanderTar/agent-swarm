import AppKit
import Observation
import SwarmBarKit
import SwarmBarUI
import SwiftUI

/// The preview's floating window: borderless, non-activating, mouse-transparent,
/// above the MenuBarExtra window, never key. Ordered front with orderFront(nil);
/// makeKeyAndOrderFront would dismiss the popover it is meant to sit beside.
@MainActor
final class PanePreviewWindow {
    private let preview: PanePreviewModel
    private let lookup: (String) -> (kind: String, itemKey: String)?
    private var panel: NSPanel?

    init(preview: PanePreviewModel, lookup: @escaping (String) -> (kind: String, itemKey: String)? = { _ in nil }) {
        self.preview = preview
        self.lookup = lookup
        observe()
    }

    /// Re-registers itself after every fire: `withObservationTracking`'s `onChange` is one-shot.
    private func observe() {
        withObservationTracking {
            _ = preview.anchor
        } onChange: { [weak self] in
            Task { @MainActor [weak self] in
                guard let self else { return }
                if self.preview.anchor == .none { self.hide() } else { self.show(self.preview.anchor) }
                self.observe()
            }
        }
    }

    /// Moves and orders front using PanePreviewGeometry.frame(anchor, …).
    /// `anchor.screen` is the popover's own screen: NSScreen.main is the screen
    /// with keyboard focus, which on a two-display setup is regularly not the
    /// one the menu bar popover is on.
    func show(_ anchor: PanePreviewModel.Anchor) {
        let panel = self.panel ?? makePanel()
        self.panel = panel
        panel.level = NSWindow.Level(rawValue: Self.menuBarExtraLevel().rawValue + 1)
        let frame = PanePreviewGeometry.frame(anchor, size: PanePreviewPanel.size)
        panel.setFrame(frame, display: false)
        panel.orderFront(nil)
    }

    func hide() {
        panel?.orderOut(nil)
    }

    private func makePanel() -> NSPanel {
        let panel = NSPanel(contentRect: NSRect(origin: .zero, size: PanePreviewPanel.size),
                            styleMask: [.borderless, .nonactivatingPanel], backing: .buffered, defer: true)
        panel.isFloatingPanel = true
        panel.isOpaque = false
        panel.backgroundColor = .clear
        panel.hasShadow = true
        panel.ignoresMouseEvents = false // interactive: allows hover and scrolling inside preview
        panel.hidesOnDeactivate = false
        panel.becomesKeyOnlyIfNeeded = true
        panel.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary, .ignoresCycle]
        panel.animationBehavior = .utilityWindow
        panel.contentView = NSHostingView(rootView: PanePreviewPanel(preview: preview, lookup: lookup))
        return panel
    }

    /// One level above the MenuBarExtra window's own level, read fresh at show() time (Task 1's
    /// spike confirmed a non-activating panel ordered above it leaves the popover open; the
    /// window's own level was 101 there, never assumed to be .statusBar).
    private static func menuBarExtraLevel() -> NSWindow.Level {
        for w in NSApp.windows where String(describing: type(of: w)).contains("MenuBarExtraWindow") {
            return w.level
        }
        return .statusBar
    }
}

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    let model: AppModel
    private let poster: NotificationPosting
    private var watcher: StatusItemWatcher?
    private var previewWindow: PanePreviewWindow?

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
        previewWindow = PanePreviewWindow(preview: model.preview) { [weak model] name in
            guard let a = model.flatMap({ AgentTree.flatten($0.state.agents).first { $0.name == name } }) else { return nil }
            return (a.kind.rawValue, a.itemKey)
        }
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
        MenuBarLabelImage(model.label)
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
        .task {
            let s = model.makeSettings()
            settings = s
            await s.load()
        }
        .onChange(of: model.connected) { _, up in settings?.connected = up }
        .onChange(of: model.state.agents) { _, agents in settings?.agents = agents }
    }
}
