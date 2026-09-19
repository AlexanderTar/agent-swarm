import AppKit
import SwarmBarKit
import SwiftUI

/// The popover (§16.2): 360 pt wide, fixed header and footer. Each section scrolls on its own so
/// one long list can't push the others out of view, inside a budget that leaves every open
/// section visible without scrolling the popover itself; the outer scroll is the overflow net for
/// four wide-open sections on a short screen.
public struct PopoverView: View {
    @Bindable var model: AppModel
    let openNewOrchestrator: () -> Void
    let openSettings: () -> Void

    public init(model: AppModel, openNewOrchestrator: @escaping () -> Void, openSettings: @escaping () -> Void) {
        self.model = model
        self.openNewOrchestrator = openNewOrchestrator
        self.openSettings = openSettings
    }

    /// Two thirds of the usable screen, so the popover never swallows the desktop.
    private var maxHeight: CGFloat { (NSScreen.main?.visibleFrame.height ?? 800) * 2 / 3 }

    /// How the scrolling room is split between sections. Relative, not absolute: the budget is
    /// shared out among the sections that are actually open, so closing Notifications gives its
    /// room to the others instead of wasting it.
    private static let weights: [AppModel.Section: CGFloat] =
        [.needsYou: 26, .agents: 40, .usage: 18, .notifications: 16]

    /// What's left for the scrolling lists themselves. Everything that shares the popover with
    /// them is subtracted first: the popover header and footer (120), every section's own header
    /// plus its spacing and divider (44 each — a closed section still shows its header), the body
    /// padding (24), and the banner or compact note when they're showing. Without this the shares
    /// came out of a budget that still had ~180 pt of chrome in it and Usage fell below the fold,
    /// which was the complaint this was meant to fix.
    private var capBudget: CGFloat {
        let chrome = CGFloat(AppModel.Section.allCases.count) * 44
        let banner: CGFloat = model.banner == nil ? 0 : 76
        let note: CGFloat = model.compactNote == nil ? 0 : 24
        return max(240, maxHeight - 120 - chrome - 24 - banner - note)
    }

    /// The floor keeps every open section worth opening (two usage bars, two agent rows). It can
    /// push the total just past the budget when all four are open at once, which is the one case
    /// the outer scroll view is still there for.
    private func cap(_ section: AppModel.Section) -> CGFloat {
        let open = AppModel.Section.allCases.filter(model.isOpen).reduce(0) { $0 + (Self.weights[$1] ?? 0) }
        guard open > 0 else { return 0 }
        return max(70, capBudget * (Self.weights[section] ?? 0) / open)
    }

    public var body: some View {
        VStack(spacing: 0) {
            header
            Divider()
            ScrollView {
                VStack(alignment: .leading, spacing: 10) {
                    if let banner = model.banner { DaemonBanner(text: banner) { Task { await model.retryConnection() } } }
                    if let note = model.compactNote { Text(note).font(.callout).foregroundStyle(.secondary) }
                    NeedsYouSection(model: model, cap: cap(.needsYou))
                    Divider()
                    AgentsSection(model: model, cap: cap(.agents))
                    Divider()
                    UsageSectionView(model: model, cap: cap(.usage))
                    Divider()
                    NotificationsSection(model: model, cap: cap(.notifications))
                }
                .padding(12)
            }
            .scrollBounceBehavior(.basedOnSize)
            Divider()
            footer
        }
        .frame(width: 360)
        .frame(maxHeight: maxHeight)
        .controlSize(.small)
        .glassButtons()
        .onAppear { model.popoverShown() }
    }

    private var header: some View {
        HStack(spacing: 6) {
            Text(Copy.appTitle).font(.headline)
            LiveBadge(connected: model.connected)
            Spacer(minLength: 4)
            if model.connected {
                Text(model.activeLine)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .monospacedDigit()
            }
            IconButton("gearshape", help: Copy.settings, action: openSettings)
            Menu {
                Button(Copy.quit) { NSApp.terminate(nil) }
            } label: {
                Image(systemName: "ellipsis")
            }
            .menuStyle(.borderlessButton)
            .menuIndicator(.hidden)
            .frame(width: 22)
            .help(Copy.more)
            .accessibilityLabel(Copy.more)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 6)
    }

    private var footer: some View {
        HStack {
            Button {
                openNewOrchestrator()
            } label: {
                Label(Copy.newOrchestrator, systemImage: "plus")
            }
            .disabled(!model.connected)
            Spacer()
            IconButton("square.grid.2x2", help: Copy.openBoard) { model.openBoard() }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
    }
}

/// "● Live" next to the app title: green while the daemon stream is up, red when it isn't.
struct LiveBadge: View {
    let connected: Bool

    var body: some View {
        HStack(spacing: 3) {
            StateDot(connected ? .green : .red)
            Text(connected ? Copy.live : Copy.offline)
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .help(connected ? Copy.live : Copy.offline)
        .accessibilityElement(children: .combine)
        .accessibilityLabel(connected ? Copy.live : Copy.offline)
    }
}

struct DaemonBanner: View {
    let text: String
    let retry: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Label(text, systemImage: "exclamationmark.triangle.fill")
                .foregroundStyle(.orange)
                .fixedSize(horizontal: false, vertical: true)
            HStack {
                Spacer()
                Button(Copy.retryConnection, action: retry)
            }
        }
        .padding(8)
        .background(Color.orange.opacity(0.12), in: RoundedRectangle(cornerRadius: 6))
    }
}
