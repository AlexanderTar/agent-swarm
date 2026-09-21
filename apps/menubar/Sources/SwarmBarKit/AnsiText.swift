import Foundation
import SwiftUI

/// Terminal text -> `AttributedString`. Only SGR (`ESC [ ... m`) is interpreted; every other
/// escape sequence (cursor moves, erases, mode switches, OSC titles, charset selects) is dropped,
/// as are stray C0 controls other than newline and tab. A sequence cut off by the end of the
/// input is dropped too.
public enum AnsiText {
    /// What a run is drawn with. `nil` colours stay unset so the view's own foreground and fill apply.
    struct Style {
        var fg: Color?
        var bg: Color?
        var bold = false, dim = false, italic = false, underline = false, reverse = false
    }

    /// The colour the panel draws text in when the stream sets none.
    public static let defaultFG = Color(red: 0xD4 / 255, green: 0xD4 / 255, blue: 0xD4 / 255)
    /// The panel's own fill; stands in for a missing background under reverse video.
    public static let defaultBG = Color(red: 0x1E / 255, green: 0x1E / 255, blue: 0x1E / 255)

    public static func attributed(_ raw: String) -> AttributedString {
        var out = AttributedString()
        var style = Style()
        var buf = ""

        func flush() {
            guard !buf.isEmpty else { return }
            out.append(run(buf, style))
            buf = ""
        }

        let s = Array(raw.unicodeScalars)
        var i = 0
        while i < s.count {
            let c = s[i]
            i += 1
            guard c == "\u{1B}" else {
                if c.value >= 0x20 && c.value != 0x7F || c == "\n" || c == "\t" { buf.unicodeScalars.append(c) }
                continue
            }
            guard i < s.count else { break } // lone ESC at the end
            let next = s[i]
            i += 1
            switch next {
            case "[":
                var params = ""
                // Parameter bytes 0x30-0x3F, intermediates 0x20-0x2F, then one final byte 0x40-0x7E.
                while i < s.count, (0x20...0x3F).contains(s[i].value) {
                    params.unicodeScalars.append(s[i])
                    i += 1
                }
                guard i < s.count else { break } // truncated
                let final = s[i]
                if (0x40...0x7E).contains(final.value) {
                    i += 1
                    if final == "m", let first = params.unicodeScalars.first, first == ";" || ("0"..."9").contains(first) {
                        flush()
                        apply(sgr: params, to: &style)
                    } else if final == "m", params.isEmpty {
                        flush()
                        style = Style()
                    }
                }
                // Any other byte aborts the sequence and is re-read as ordinary input
                // (a fresh ESC starts a new sequence).
            case "]", "P", "X", "^", "_":
                // String sequences (OSC, DCS, SOS, PM, APC): up to BEL or ST (ESC \).
                while i < s.count {
                    let t = s[i]
                    i += 1
                    if t == "\u{07}" { break }
                    if t == "\u{1B}" {
                        if i < s.count, s[i] == "\\" { i += 1 } else { i -= 1 } // a bare ESC starts something new
                        break
                    }
                }
            default:
                // ESC <intermediates> <final>, e.g. ESC ( B; or ESC <single>, e.g. ESC 7, ESC M.
                if (0x20...0x2F).contains(next.value) {
                    while i < s.count, (0x20...0x2F).contains(s[i].value) { i += 1 }
                    if i < s.count { i += 1 }
                }
            }
        }
        flush()
        return out
    }

    private static func run(_ text: String, _ style: Style) -> AttributedString {
        var fg = style.fg, bg = style.bg
        if style.reverse { (fg, bg) = (bg ?? defaultBG, fg ?? defaultFG) }
        if style.dim { fg = (fg ?? defaultFG).opacity(0.5) }
        var a = AttributedString(text)
        a.foregroundColor = fg
        a.backgroundColor = bg
        var intent: InlinePresentationIntent = []
        if style.bold { intent.insert(.stronglyEmphasized) }
        if style.italic { intent.insert(.emphasized) }
        if !intent.isEmpty { a.inlinePresentationIntent = intent }
        if style.underline { a.underlineStyle = .single }
        return a
    }

    /// `params` is the text between `ESC [` and `m`. Only `;` separates parameters; a parameter
    /// with a `:` sub-parameter (the ITU colon form) is skipped rather than guessed at.
    static func apply(sgr params: String, to style: inout Style) {
        // An empty parameter means 0, as in `ESC [ ; 1 m`. Values that overflow are skipped.
        let p: [Int?] = params.isEmpty ? [0] : params.split(separator: ";", omittingEmptySubsequences: false).map {
            $0.isEmpty ? 0 : Int($0)
        }
        var i = 0
        while i < p.count {
            defer { i += 1 }
            guard let code = p[i] else { continue }
            switch code {
            case 0: style = Style()
            case 1: style.bold = true
            case 2: style.dim = true
            case 3: style.italic = true
            case 4: style.underline = true
            case 7: style.reverse = true
            case 22: style.bold = false; style.dim = false
            case 23: style.italic = false
            case 24: style.underline = false
            case 27: style.reverse = false
            case 30...37: style.fg = base[code - 30]
            case 90...97: style.fg = base[code - 90 + 8]
            case 40...47: style.bg = base[code - 40]
            case 100...107: style.bg = base[code - 100 + 8]
            case 39: style.fg = nil
            case 49: style.bg = nil
            case 38, 48:
                // Read the colour even when it is out of range, so its arguments are consumed.
                let (color, used) = extended(p, from: i + 1)
                if let color { if code == 38 { style.fg = color } else { style.bg = color } }
                i += used
            default: break
            }
        }
    }

    /// `5;n` or `2;r;g;b` starting at `p[at]`. Returns the colour (nil when malformed or out of
    /// range) and how many parameters it took.
    private static func extended(_ p: [Int?], from at: Int) -> (Color?, Int) {
        guard at < p.count, let mode = p[at] else { return (nil, 0) }
        if mode == 5 {
            guard at + 1 < p.count else { return (nil, p.count - at) }
            if let n = p[at + 1], (0...255).contains(n) { return (palette256(n), 2) }
            return (nil, 2)
        }
        if mode == 2 {
            guard at + 3 < p.count else { return (nil, p.count - at) }
            let rgb = p[at + 1...at + 3]
            if rgb.allSatisfy({ $0 != nil && (0...255).contains($0!) }) {
                let v = rgb.map { Double($0!) / 255 }
                return (Color(red: v[0], green: v[1], blue: v[2]), 4)
            }
            return (nil, 4)
        }
        return (nil, 0)
    }

    private static func rgb(_ hex: Int) -> Color {
        Color(red: Double((hex >> 16) & 255) / 255, green: Double((hex >> 8) & 255) / 255, blue: Double(hex & 255) / 255)
    }

    /// xterm base 16: black, red, green, yellow, blue, magenta, cyan, white, then the bright row.
    private static let base: [Color] = [
        0x000000, 0xCD3131, 0x0DBC79, 0xE5E510, 0x2472C8, 0xBC3FBC, 0x11A8CD, 0xE5E5E5,
        0x666666, 0xF14C4C, 0x23D18B, 0xF5F543, 0x3B8EEA, 0xD670D6, 0x29B8DB, 0xFFFFFF,
    ].map(rgb)

    static func palette256(_ n: Int) -> Color {
        if n < 16 { return base[n] }
        if n >= 232 {
            let g = Double(8 + 10 * (n - 232)) / 255
            return Color(red: g, green: g, blue: g)
        }
        let levels: [Double] = [0, 95, 135, 175, 215, 255]
        let c = n - 16
        return Color(red: levels[c / 36] / 255, green: levels[(c % 36) / 6] / 255, blue: levels[c % 6] / 255)
    }
}
