import Foundation

/// Block structure of the Markdown subset used in agent instructions: headings,
/// paragraphs, list items (indent = nesting level) and fenced code. Inline syntax (code, bold, links) stays in the text
/// for the view to render. SwiftUI's `Text` drops block structure, so the view
/// renders one `Text` per block.
public enum MarkdownBlock: Equatable, Sendable {
    case heading(level: Int, text: String)
    case paragraph(String)
    case listItem(marker: String, text: String, indent: Int = 0)
    case code(String)
}

public enum MarkdownBlocks {
    public static func parse(_ markdown: String) -> [MarkdownBlock] {
        var blocks: [MarkdownBlock] = []
        var paragraph: [String] = []
        func flush() {
            if !paragraph.isEmpty { blocks.append(.paragraph(paragraph.joined(separator: " "))) }
            paragraph = []
        }
        var fence: [String]?
        for raw in markdown.split(separator: "\n", omittingEmptySubsequences: false) {
            let line = raw.trimmingCharacters(in: .whitespaces)
            if line.hasPrefix("```") {
                if let code = fence { blocks.append(.code(code.joined(separator: "\n"))); fence = nil }
                else { flush(); fence = [] }
                continue
            }
            if fence != nil { fence?.append(String(raw)); continue }
            if line.isEmpty { flush(); continue }
            if let h = heading(line) { flush(); blocks.append(h) }
            else if let item = listItem(line, indent: indent(of: raw)) { flush(); blocks.append(item) }
            else { paragraph.append(line) }
        }
        if let code = fence { blocks.append(.code(code.joined(separator: "\n"))) }
        flush()
        return blocks
    }

    /// Two leading spaces (or a tab) per nesting level.
    private static func indent(of raw: Substring) -> Int {
        var width = 0
        for c in raw { if c == " " { width += 1 } else if c == "\t" { width += 2 } else { break } }
        return width / 2
    }

    private static func heading(_ line: String) -> MarkdownBlock? {
        let level = line.prefix { $0 == "#" }.count
        guard (1...6).contains(level), line.dropFirst(level).first == " " else { return nil }
        return .heading(level: level, text: line.dropFirst(level).trimmingCharacters(in: .whitespaces))
    }

    private static func listItem(_ line: String, indent: Int) -> MarkdownBlock? {
        if let first = line.first, "-*+".contains(first), line.dropFirst().first == " " {
            return .listItem(marker: "•", text: line.dropFirst(2).trimmingCharacters(in: .whitespaces), indent: indent)
        }
        let digits = line.prefix { $0.isNumber }
        let rest = line.dropFirst(digits.count)
        guard !digits.isEmpty, rest.hasPrefix(". ") else { return nil }
        return .listItem(marker: "\(digits).", text: rest.dropFirst(2).trimmingCharacters(in: .whitespaces), indent: indent)
    }
}
