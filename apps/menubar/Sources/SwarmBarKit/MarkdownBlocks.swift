import Foundation

/// Block structure of the Markdown subset used in agent instructions: headings,
/// paragraphs and list items. Inline syntax (code, bold, links) stays in the text
/// for the view to render. SwiftUI's `Text` drops block structure, so the view
/// renders one `Text` per block.
public enum MarkdownBlock: Equatable, Sendable {
    case heading(level: Int, text: String)
    case paragraph(String)
    case listItem(marker: String, text: String)
}

public enum MarkdownBlocks {
    public static func parse(_ markdown: String) -> [MarkdownBlock] {
        var blocks: [MarkdownBlock] = []
        var paragraph: [String] = []
        func flush() {
            if !paragraph.isEmpty { blocks.append(.paragraph(paragraph.joined(separator: " "))) }
            paragraph = []
        }
        for raw in markdown.split(separator: "\n", omittingEmptySubsequences: false) {
            let line = raw.trimmingCharacters(in: .whitespaces)
            if line.isEmpty { flush(); continue }
            if let h = heading(line) { flush(); blocks.append(h) }
            else if let item = listItem(line) { flush(); blocks.append(item) }
            else { paragraph.append(line) }
        }
        flush()
        return blocks
    }

    private static func heading(_ line: String) -> MarkdownBlock? {
        let level = line.prefix { $0 == "#" }.count
        guard (1...6).contains(level), line.dropFirst(level).first == " " else { return nil }
        return .heading(level: level, text: line.dropFirst(level).trimmingCharacters(in: .whitespaces))
    }

    private static func listItem(_ line: String) -> MarkdownBlock? {
        if let first = line.first, "-*+".contains(first), line.dropFirst().first == " " {
            return .listItem(marker: "•", text: line.dropFirst(2).trimmingCharacters(in: .whitespaces))
        }
        let digits = line.prefix { $0.isNumber }
        let rest = line.dropFirst(digits.count)
        guard !digits.isEmpty, rest.hasPrefix(". ") else { return nil }
        return .listItem(marker: "\(digits).", text: rest.dropFirst(2).trimmingCharacters(in: .whitespaces))
    }
}
