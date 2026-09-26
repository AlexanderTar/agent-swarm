import SwarmBarKit
import SwiftUI

struct NeedsYouSection: View {
    @Bindable var model: AppModel
    let cap: CGFloat

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            SectionHeader(Copy.needsYou, open: model.isOpen(.needsYou),
                          toggle: { model.setSection(.needsYou, open: !model.isOpen(.needsYou)) }) {
                if !model.openRequests.isEmpty {
                    Text("\(model.openRequests.count)").foregroundStyle(.secondary).monospacedDigit()
                }
            }
            if model.isOpen(.needsYou) {
                SectionBody(cap: cap, spacing: 8) {
                    if model.openRequests.isEmpty {
                        Text(Copy.emptyNeedsYou).font(.callout).foregroundStyle(.secondary)
                    }
                    ForEach(model.visibleRequests) { r in
                        RequestRow(model: model, request: r)
                    }
                    if let more = model.viewAllRequests {
                        Button(more) { model.openInbox() }.buttonStyle(.link)
                    }
                }
            }
        }
    }
}

struct RequestRow: View {
    @Bindable var model: AppModel
    let request: SwarmRequest

    var body: some View {
        let target = model.requestTarget(request)
        let l = NeedsYouRow.lines(request)
        HStack(alignment: .top) {
            VStack(alignment: .leading, spacing: 2) {
                Text(l[0]).font(.caption).foregroundStyle(.secondary).lineLimit(1)
                Text(l[1]).font(.callout).lineLimit(1)
                Text(l[2]).font(.callout)
                if case let .unavailable(hint)? = target {
                    Text(hint).font(.caption).foregroundStyle(.secondary)
                }
            }
            Spacer()
            Button { Task { await model.openRequest(request) } } label: { Image(systemName: "terminal") }
                .buttonStyle(.borderless)
                .disabled({ if case .unavailable? = target { return true }; return false }())
                .help(target == nil ? Copy.openOnBoard : Copy.openAgentTerminal)
        }
        .padding(8)
        .background(Color.yellow.opacity(0.18), in: RoundedRectangle(cornerRadius: 6))
    }
}
