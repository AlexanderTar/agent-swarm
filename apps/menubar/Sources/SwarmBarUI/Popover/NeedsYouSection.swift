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
        if request.isHITL {
            let target = model.requestTarget(request)
            let card = VStack(alignment: .leading, spacing: 4) {
                Text("\(request.itemKey) · \(request.itemTitle)").font(.caption).foregroundStyle(.secondary)
                Text(RequestLine.text(request)).lineLimit(3)
                if case let .unavailable(hint)? = target {
                    Text(hint).font(.caption).foregroundStyle(.secondary)
                }
            }
            if case .terminal? = target {
                Button { Task { await model.openRequest(request) } } label: {
                    HStack(alignment: .top) {
                        card
                        Spacer()
                        Image(systemName: "terminal").foregroundStyle(.secondary)
                    }
                }
                .buttonStyle(.plain)
                .help(Copy.openOrchestratorTerminal)
            } else {
                card
            }
        } else {
            VStack(alignment: .leading, spacing: 4) {
                Text("\(request.itemKey) · \(request.itemTitle)").font(.caption).foregroundStyle(.secondary)
                Text(RequestLine.text(request)).lineLimit(3)
                HStack {
                    Spacer()
                    IconButton("doc.text.magnifyingglass", help: Copy.review) { model.review(request) }
                }
            }
        }
    }
}
