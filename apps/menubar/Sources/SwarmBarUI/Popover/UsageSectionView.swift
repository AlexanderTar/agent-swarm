import SwarmBarKit
import SwiftUI

struct UsageSectionView: View {
    @Bindable var model: AppModel

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            SectionHeader(Copy.usage, open: model.isOpen(.usage),
                          toggle: { model.setSection(.usage, open: !model.isOpen(.usage)) }) { EmptyView() }
            if model.isOpen(.usage) {
                if model.usagePicker.count > 1 {
                    Picker(Copy.usage, selection: $model.usageAgent) {
                        ForEach(model.usagePicker, id: \.self) { Text(Copy.agentLabel($0)).tag(Optional($0)) }
                    }
                    .pickerStyle(.segmented)
                    .labelsHidden()
                }
                if model.usageRows.isEmpty {
                    HStack {
                        Text(Copy.emptyUsage).foregroundStyle(.secondary)
                        Spacer()
                        Button(Copy.retry) { Task { await model.refreshUsage() } }.disabled(!model.connected)
                    }
                } else {
                    ForEach(model.usageRows) { row in
                        VStack(alignment: .leading, spacing: 3) {
                            HStack {
                                Text(row.label)
                                Spacer()
                                Text(row.used).monospacedDigit()
                            }
                            HStack {
                                ProgressView(value: row.fraction).frame(maxWidth: 170)
                                Spacer()
                                Text(row.trailing).font(.caption).foregroundStyle(.secondary)
                            }
                        }
                    }
                    HStack {
                        Spacer()
                        Button(Copy.refresh) { Task { await model.refreshUsage() } }.disabled(!model.connected)
                    }
                }
            }
        }
    }
}
