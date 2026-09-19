import AppKit
import SwarmBarKit
import SwiftUI

/// Template icons from assets/icons (§21.1). Swarm.app carries them in Contents/Resources;
/// `swift run` falls back to the repo's assets/icons folder.
public enum IconName: String, Sendable {
    case claude, codex, agy, cursor, swarm

    public init(_ kind: AgentKind) {
        switch kind {
        case .claude, .fake: self = .claude
        case .codex: self = .codex
        case .agy: self = .agy
        case .cursor: self = .cursor
        }
    }
}

@MainActor
public enum Icons {
    private static var cache: [IconName: NSImage] = [:]

    public static func image(_ name: IconName) -> NSImage {
        if let hit = cache[name] { return hit }
        let repoAssets = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().appendingPathComponent("../../../../assets/icons").standardized
        let url = Bundle.main.url(forResource: name.rawValue, withExtension: "svg")
            ?? repoAssets.appendingPathComponent("\(name.rawValue).svg")
        let image = NSImage(contentsOf: url)
            ?? NSImage(systemSymbolName: "circle.hexagongrid", accessibilityDescription: name.rawValue)
            ?? NSImage()
        image.isTemplate = true
        image.size = NSSize(width: 14, height: 14)
        cache[name] = image
        return image
    }
}

public struct AgentIcon: View {
    let name: IconName
    public init(_ kind: AgentKind) { name = IconName(kind) }
    public init(_ name: IconName) { self.name = name }

    public var body: some View {
        Image(nsImage: Icons.image(name))
            .renderingMode(.template)
            .accessibilityLabel(name.rawValue)
    }
}
