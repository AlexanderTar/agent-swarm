import SwarmBarKit
import SwiftUI

struct NeedsYouSection: View {
    @Bindable var model: AppModel

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            SectionHeader(Copy.needsYou, open: model.isOpen(.needsYou),
                          toggle: { model.setSection(.needsYou, open: !model.isOpen(.needsYou)) }) {
                Text("\(model.openRequests.count)").foregroundStyle(.secondary).monospacedDigit()
            }
            if model.isOpen(.needsYou) {
                if model.openRequests.isEmpty {
                    Text(Copy.emptyNeedsYou).foregroundStyle(.secondary)
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

struct RequestRow: View {
    @Bindable var model: AppModel
    let request: SwarmRequest

    private var draft: Binding<String> {
        Binding(get: { model.answerDrafts[request.id] ?? "" }, set: { model.answerDrafts[request.id] = $0 })
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text("\(request.itemKey) · \(request.itemTitle)").font(.caption).foregroundStyle(.secondary)
            Text(RequestLine.text(request)).lineLimit(3)
            if request.kind == .question {
                if model.answering == request.id {
                    HStack {
                        TextField(Copy.answer, text: draft).textFieldStyle(.roundedBorder)
                            .onSubmit { Task { await model.sendAnswer(request.id) } }
                        Button(Copy.sendAnswer) { Task { await model.sendAnswer(request.id) } }
                            .disabled(!model.connected || draft.wrappedValue.trimmingCharacters(in: .whitespaces).isEmpty)
                    }
                }
                HStack {
                    Button(Copy.answer) { model.answering = model.answering == request.id ? nil : request.id }
                        .disabled(!model.connected)
                    Spacer()
                    if let name = model.requestTerminal(request) {
                        Button(Copy.openTerminal) { Task { await model.openTerminal(name) } }
                    }
                }
            } else {
                HStack {
                    Spacer()
                    Button(Copy.review) { model.review(request) }
                }
            }
        }
    }
}
