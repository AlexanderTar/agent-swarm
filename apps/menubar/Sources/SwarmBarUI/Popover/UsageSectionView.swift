import SwarmBarKit
import SwiftUI

struct UsageSectionView: View {
    @Bindable var model: AppModel
    let cap: CGFloat

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            SectionHeader(Copy.usage, open: model.isOpen(.usage),
                          toggle: { model.setSection(.usage, open: !model.isOpen(.usage)) }) {
                if model.isOpen(.usage) && !model.usageRows.isEmpty {
                    IconButton("arrow.clockwise", help: Copy.refresh, disabled: !model.connected) {
                        Task { await model.refreshUsage() }
                    }
                }
            }
            if model.isOpen(.usage) {
                if model.usagePicker.count > 1 {
                    Picker(Copy.usage, selection: $model.usageAgent) {
                        ForEach(model.usagePicker, id: \.self) { Text(Copy.agentLabel($0)).tag(Optional($0)) }
                    }
                    .pickerStyle(.segmented)
                    .labelsHidden()
                }
                SectionBody(cap: cap) {
                    if model.usageRows.isEmpty {
                        HStack {
                            Text(Copy.emptyUsage).font(.callout).foregroundStyle(.secondary)
                            Spacer()
                            IconButton("arrow.clockwise", help: Copy.retry, disabled: !model.connected) {
                                Task { await model.refreshUsage() }
                            }
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
                                    UsageBar(row: row).frame(width: 170)
                                    Spacer()
                                    Text(row.trailing).font(.caption).foregroundStyle(.secondary)
                                }
                            }
                        }
                    }
                }
            }
        }
    }
}

/// A 4 pt capsule instead of `ProgressView`'s default bar, tinted by how close the meter is to its
/// limit (§16.2 polish). `ProgressView` won't go this thin on macOS, so the track is drawn directly.
struct UsageBar: View {
    let row: UsageSection.Row

    private var tint: Color {
        switch row.level {
        case .normal: return .blue
        case .warning: return .yellow
        case .critical: return .red
        }
    }

    var body: some View {
        GeometryReader { geo in
            ZStack(alignment: .leading) {
                Capsule().fill(Color.secondary.opacity(0.2))
                Capsule().fill(tint).frame(width: max(0, geo.size.width * row.fraction))
            }
        }
        .frame(height: 4)
        .accessibilityElement()
        .accessibilityLabel("\(row.label), \(row.used)")
    }
}
