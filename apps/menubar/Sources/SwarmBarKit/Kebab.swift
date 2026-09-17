import Foundation

/// Port of Go `ids.KebabMax` (spec §4). The shared golden cases live in
/// testdata/kebab_cases.json at the repo root; keep all three ports in step.
public enum Kebab {
    public static let maxName = 48
    public static let emptyNameMessage = "Enter a name containing a letter or number."

    /// Returns nil where Go returns ErrEmptyName.
    public static func make(_ input: String, max: Int = maxName) -> String? {
        let ascii = input.decomposedStringWithCompatibilityMapping.unicodeScalars.filter { $0.value < 128 }
        var out = ""
        var gap = false
        for scalar in String(String.UnicodeScalarView(ascii)).lowercased().unicodeScalars {
            if ("a"..."z").contains(scalar) || ("0"..."9").contains(scalar) {
                if gap && !out.isEmpty { out.append("-") }
                gap = false
                out.unicodeScalars.append(scalar)
            } else {
                gap = true
            }
        }
        if out.count > max {
            let chars = Array(out)
            let cut = String(chars[0..<max])
            if chars[max] == "-" {
                out = cut
            } else if let i = cut.lastIndex(of: "-"), i > cut.startIndex {
                out = String(cut[..<i])
            } else {
                out = cut
            }
            out = out.trimmingCharacters(in: CharacterSet(charactersIn: "-"))
        }
        return out.isEmpty ? nil : out
    }
}
