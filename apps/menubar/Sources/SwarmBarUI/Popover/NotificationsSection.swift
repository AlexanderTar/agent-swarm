import SwarmBarKit
import SwiftUI

struct NotificationsSection: View {
    @Bindable var model: AppModel

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            SectionHeader(model.notificationsTitle, open: model.isOpen(.notifications),
                          toggle: { model.setSection(.notifications, open: !model.isOpen(.notifications)) }) {
                Button(Copy.readAll) { Task { await model.readAll() } }
                    .disabled(!model.connected || model.state.notifications.unread == 0)
            }
            if model.isOpen(.notifications) {
                if model.visibleNotifications.isEmpty {
                    Text(Copy.emptyNotifications).foregroundStyle(.secondary)
                }
                ForEach(model.visibleNotifications) { n in
                    Button {
                        Task { await model.open(n) }
                    } label: {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(n.title).fontWeight(n.readAt == nil ? .semibold : .regular)
                            Text(n.body).font(.caption).foregroundStyle(.secondary).lineLimit(2)
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                }
                if model.showViewAllNotifications {
                    Button(Copy.viewAllNotifications) { Task { await model.viewAllNotifications() } }.buttonStyle(.link)
                }
            }
        }
    }
}
