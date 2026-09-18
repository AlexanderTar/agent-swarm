import AppKit
import SwarmBarKit
import SwiftUI

/// The popover (§16.2): 360 pt wide, fixed header and footer, one scrolling body.
public struct PopoverView: View {
    @Bindable var model: AppModel
    let openNewOrchestrator: () -> Void
    let openSettings: () -> Void

    public init(model: AppModel, openNewOrchestrator: @escaping () -> Void, openSettings: @escaping () -> Void) {
        self.model = model
        self.openNewOrchestrator = openNewOrchestrator
        self.openSettings = openSettings
    }

    private var maxHeight: CGFloat { (NSScreen.main?.visibleFrame.height ?? 800) - 40 }

    public var body: some View {
        VStack(spacing: 0) {
            header
            Divider()
            ScrollView {
                VStack(alignment: .leading, spacing: 10) {
                    if let banner = model.banner { DaemonBanner(text: banner) { Task { await model.retryConnection() } } }
                    if let note = model.compactNote { Text(note).font(.callout).foregroundStyle(.secondary) }
                    NeedsYouSection(model: model)
                    Divider()
                    AgentsSection(model: model, openNewOrchestrator: openNewOrchestrator)
                    Divider()
                    UsageSectionView(model: model)
                    Divider()
                    NotificationsSection(model: model)
                }
                .padding(12)
            }
            Divider()
            footer
        }
        .frame(width: 360)
        .frame(maxHeight: maxHeight)
        .onAppear { model.popoverShown() }
    }

    private var header: some View {
        HStack {
            Text(Copy.appTitle).font(.headline)
            Spacer()
            Text(model.activeLine)
                .foregroundStyle(.secondary)
                .monospacedDigit()
            IconButton("gearshape", help: "Settings", action: openSettings)
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
            Button(Copy.openBoard) { model.openBoard() }
        }
        .padding(12)
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
