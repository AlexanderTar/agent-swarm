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

    private var draft: Binding<String> {
        Binding(get: { model.answerDrafts[request.id] ?? "" }, set: { model.answerDrafts[request.id] = $0 })
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text("\(request.itemKey) · \(request.itemTitle)").font(.caption).foregroundStyle(.secondary)
            Text(RequestLine.text(request)).lineLimit(3)
            if request.kind == .question || request.kind == .prompt || request.kind == .blocker {
                if request.kind != .prompt, model.answering == request.id {
                    HStack(spacing: 4) {
                        TextField(Copy.answer, text: draft).textFieldStyle(.roundedBorder)
                            .onSubmit { Task { await model.sendAnswer(request.id) } }
                        IconButton("paperplane.fill", help: Copy.sendAnswer,
                                   disabled: !model.connected || draft.wrappedValue.trimmingCharacters(in: .whitespaces).isEmpty) {
                            Task { await model.sendAnswer(request.id) }
                        }
                    }
                }
                HStack(spacing: 2) {
                    if request.kind == .prompt {
                        if let action = request.options?.first {
                            Button(Copy.approve) {
                                Task { await model.resolvePrompt(request.id, action: action) }
                            }
                            .disabled(!model.connected)
                        }
                    } else {
                        Button(Copy.answer) { model.answering = model.answering == request.id ? nil : request.id }
                            .disabled(!model.connected)
                    }
                    Spacer()
                    if let name = model.requestTerminal(request) {
                        IconButton("terminal", help: Copy.openTerminal) { Task { await model.openTerminal(name) } }
                    }
                }
            } else {
                HStack {
                    Spacer()
                    IconButton("doc.text.magnifyingglass", help: Copy.review) { model.review(request) }
                }
            }
        }
    }
}
