import AppKit
import SwarmBarKit
import SwiftUI

/// The popover (§16.2): 360 pt wide, fixed header and footer. Each section scrolls on its own so
/// one long list can't push the others out of view; the outer scroll is only the overflow net for
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
    /// What's left for the sections once the header, footer, dividers and padding are paid for.
    private var bodyBudget: CGFloat { max(280, maxHeight - 120) }
    private func cap(_ share: CGFloat) -> CGFloat { max(110, bodyBudget * share) }

    public var body: some View {
        VStack(spacing: 0) {
            header
            Divider()
            ScrollView {
                VStack(alignment: .leading, spacing: 10) {
                    if let banner = model.banner { DaemonBanner(text: banner) { Task { await model.retryConnection() } } }
                    if let note = model.compactNote { Text(note).font(.callout).foregroundStyle(.secondary) }
                    NeedsYouSection(model: model, cap: cap(0.32))
                    Divider()
                    AgentsSection(model: model, cap: cap(0.38))
                    Divider()
                    UsageSectionView(model: model, cap: cap(0.24))
                    Divider()
                    NotificationsSection(model: model, cap: cap(0.30))
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
            Text(model.activeLine)
                .font(.caption)
                .foregroundStyle(.secondary)
                .monospacedDigit()
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
