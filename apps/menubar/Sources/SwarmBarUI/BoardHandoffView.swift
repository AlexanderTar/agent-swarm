import AppKit
import SwarmBarKit
import SwiftUI

/// Orchestrate task window: pick an item, then Start/Queue/Hand off with the shared agent pickers.
public struct BoardHandoffView: View {
    @Bindable var form: BoardHandoffForm
    let onDone: () -> Void
    let onCancel: () -> Void

    public init(form: BoardHandoffForm, onDone: @escaping () -> Void, onCancel: @escaping () -> Void) {
        self.form = form
        self.onDone = onDone
        self.onCancel = onCancel
    }

    public var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            VStack(alignment: .leading, spacing: 10) {
                if let failure = form.failure {
                    Text("⚠ \(failure)").font(.callout).foregroundStyle(.red)
                }
                HStack {
                    Text(Copy.item).frame(width: 70, alignment: .leading)
                    if form.loadError != nil {
                        Text(Copy.boardItemsLoadFailed).foregroundStyle(.red)
                    } else if !form.loading && form.rows.isEmpty {
                        Text(Copy.noBoardItems).foregroundStyle(.secondary)
                    } else {
                        WideOptionPicker(Copy.item,
                                         options: form.rows.map { PickerOption($0.id, BoardHandoffRules.title($0)) },
                                         value: form.selectedKey ?? "",
                                         detail: { opt in form.rows.first { $0.id == opt.value }.map(detailLine) }) { form.selectedKey = $0 }
                            .frame(maxWidth: .infinity)
                            .disabled(form.loading)
                    }
                }
                AgentPickerGrid(picker: form.picker)
            }
            .padding(.horizontal, 22).padding(.vertical, 12)
            Divider()
            HStack {
                if let caption = form.caption { Text(caption).font(.caption).foregroundStyle(.secondary) }
                Spacer()
                Button(Copy.cancel, action: onCancel).keyboardShortcut(.cancelAction).glassButtons()
                Button(form.primaryLabel) { Task { if await form.primary() { onDone() } } }
                    .keyboardShortcut(.defaultAction)
                    .prominentDefaultAction()
                    .disabled(!form.canSubmit)
            }
            .padding(.horizontal, 22).padding(.vertical, 12)
        }
        .frame(minWidth: 760, idealWidth: 820)
        .translucentDialogBackground()
        .background(TranslucentWindowAccessor())
    }

    private func detailLine(_ row: BoardItemRow) -> NSAttributedString {
        let attrs: [NSAttributedString.Key: Any] = [.font: NSFont.systemFont(ofSize: NSFont.smallSystemFontSize),
                                                    .foregroundColor: NSColor.secondaryLabelColor]
        let out = NSMutableAttributedString()
        if let kind = row.orchestrator?.kind {
            let a = NSTextAttachment()
            a.image = Icons.tinted(IconName(kind))
            a.bounds = CGRect(x: 0, y: -2, width: 12, height: 12)
            out.append(NSAttributedString(attachment: a))
            out.append(NSAttributedString(string: " ", attributes: attrs))
        }
        out.append(NSAttributedString(string: BoardHandoffRules.detail(row, catalog: form.picker.catalog), attributes: attrs))
        return out
    }
}
