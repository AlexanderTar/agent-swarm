import AppKit
import Foundation
import SwiftUI
import XCTest
@testable import SwarmBarKit

final class AnsiTextTests: XCTestCase {
    private let esc = "\u{1B}"

    private struct Piece: Equatable {
        var text: String
        var fg: [Int]?
        var bg: [Int]?
    }

    /// sRGB channels 0...255 plus alpha in percent, so tests compare colours without float noise.
    private func rgba(_ c: Color?) -> [Int]? {
        guard let c, let ns = NSColor(c).usingColorSpace(.sRGB) else { return nil }
        return [ns.redComponent, ns.greenComponent, ns.blueComponent].map { Int(($0 * 255).rounded()) } + [Int((ns.alphaComponent * 100).rounded())]
    }

    private func hex(_ v: Int, alpha: Int = 100) -> [Int] { [(v >> 16) & 255, (v >> 8) & 255, v & 255, alpha] }

    private func pieces(_ raw: String) -> [Piece] {
        let a = AnsiText.attributed(raw)
        return a.runs.map { Piece(text: String(a[$0.range].characters), fg: rgba($0.foregroundColor), bg: rgba($0.backgroundColor)) }
    }

    private func intent(_ raw: String, at run: Int = 0) -> InlinePresentationIntent? {
        let a = AnsiText.attributed(raw)
        return Array(a.runs)[run].inlinePresentationIntent
    }

    func testPlainTextPassesThroughAsOneUnstyledRun() {
        let a = AnsiText.attributed("hello\nworld")
        XCTAssertEqual(String(a.characters), "hello\nworld")
        XCTAssertEqual(pieces("hello\nworld"), [Piece(text: "hello\nworld", fg: nil, bg: nil)])
        XCTAssertNil(intent("hello\nworld"))
        XCTAssertEqual(AnsiText.attributed("").runs.count, 0)
    }

    func testBasicForegroundThenReset() {
        XCTAssertEqual(pieces("\(esc)[31mred\(esc)[0m ok"), [
            Piece(text: "red", fg: hex(0xCD3131), bg: nil),
            Piece(text: " ok", fg: nil, bg: nil),
        ])
    }

    func testEveryBaseAndBrightColour() {
        let normal = [0x000000, 0xCD3131, 0x0DBC79, 0xE5E510, 0x2472C8, 0xBC3FBC, 0x11A8CD, 0xE5E5E5]
        let bright = [0x666666, 0xF14C4C, 0x23D18B, 0xF5F543, 0x3B8EEA, 0xD670D6, 0x29B8DB, 0xFFFFFF]
        for i in 0..<8 {
            XCTAssertEqual(pieces("\(esc)[\(30 + i)mx")[0].fg, hex(normal[i]), "fg \(30 + i)")
            XCTAssertEqual(pieces("\(esc)[\(90 + i)mx")[0].fg, hex(bright[i]), "fg \(90 + i)")
            XCTAssertEqual(pieces("\(esc)[\(40 + i)mx")[0].bg, hex(normal[i]), "bg \(40 + i)")
            XCTAssertEqual(pieces("\(esc)[\(100 + i)mx")[0].bg, hex(bright[i]), "bg \(100 + i)")
        }
    }

    func test256ColourPalette() {
        XCTAssertEqual(pieces("\(esc)[38;5;174mX")[0].fg, hex(0xD78787), "cube")
        XCTAssertEqual(pieces("\(esc)[38;5;1mX")[0].fg, hex(0xCD3131), "0-15 are the base palette")
        XCTAssertEqual(pieces("\(esc)[38;5;15mX")[0].fg, hex(0xFFFFFF))
        XCTAssertEqual(pieces("\(esc)[38;5;16mX")[0].fg, hex(0x000000), "cube start")
        XCTAssertEqual(pieces("\(esc)[38;5;231mX")[0].fg, hex(0xFFFFFF), "cube end")
        XCTAssertEqual(pieces("\(esc)[38;5;232mX")[0].fg, hex(0x080808), "grey ramp start")
        XCTAssertEqual(pieces("\(esc)[38;5;255mX")[0].fg, hex(0xEEEEEE), "grey ramp end")
        XCTAssertEqual(pieces("\(esc)[48;5;246mX")[0].bg, hex(0x949494), "48;5 is the background")
    }

    func testTruecolour() {
        XCTAssertEqual(pieces("\(esc)[48;2;21;21;21m ")[0].bg, [21, 21, 21, 100])
        XCTAssertEqual(pieces("\(esc)[38;2;255;128;0mX")[0].fg, [255, 128, 0, 100])
    }

    func testOutOfRangeAndTruncatedExtendedColoursAreIgnored() {
        XCTAssertEqual(pieces("\(esc)[38;5;300mX"), [Piece(text: "X", fg: nil, bg: nil)])
        XCTAssertEqual(pieces("\(esc)[38;2;999;0;0mX"), [Piece(text: "X", fg: nil, bg: nil)])
        XCTAssertEqual(pieces("\(esc)[38;5mX"), [Piece(text: "X", fg: nil, bg: nil)])
        XCTAssertEqual(pieces("\(esc)[38;2;1;2mX"), [Piece(text: "X", fg: nil, bg: nil)])
    }

    func testCombinedParametersAndDefaults() {
        XCTAssertEqual(pieces("\(esc)[1;31;44mX"), [Piece(text: "X", fg: hex(0xCD3131), bg: hex(0x2472C8))])
        XCTAssertEqual(intent("\(esc)[1;31;44mX"), .stronglyEmphasized)
        XCTAssertEqual(pieces("\(esc)[31;44mA\(esc)[39mB\(esc)[49mC"), [
            Piece(text: "A", fg: hex(0xCD3131), bg: hex(0x2472C8)),
            Piece(text: "B", fg: nil, bg: hex(0x2472C8)),
            Piece(text: "C", fg: nil, bg: nil),
        ])
        XCTAssertEqual(pieces("\(esc)[31mA\(esc)[mB"), [Piece(text: "A", fg: hex(0xCD3131), bg: nil), Piece(text: "B", fg: nil, bg: nil)],
                       "bare ESC[m is a reset")
    }

    func testBoldItalicUnderlineAndTheirResets() {
        let a = AnsiText.attributed("\(esc)[1;3;4mA\(esc)[22mB\(esc)[23mC\(esc)[24mD")
        let runs = Array(a.runs)
        XCTAssertEqual(runs.count, 4)
        XCTAssertEqual(runs[0].inlinePresentationIntent, [.stronglyEmphasized, .emphasized])
        XCTAssertEqual(runs[0].underlineStyle, .single)
        XCTAssertEqual(runs[1].inlinePresentationIntent, .emphasized, "22 clears bold only")
        XCTAssertEqual(runs[1].underlineStyle, .single)
        XCTAssertNil(runs[2].inlinePresentationIntent, "23 clears italic")
        XCTAssertEqual(runs[2].underlineStyle, .single)
        XCTAssertNil(runs[3].underlineStyle, "24 clears underline")
    }

    func testReverseSubstitutesPanelFillAndDefaultForeground() {
        // No colours set: fg becomes the panel fill, bg becomes the default fg -> a light block.
        XCTAssertEqual(pieces("\(esc)[7m \(esc)[0m"), [Piece(text: " ", fg: hex(0x1E1E1E), bg: hex(0xD4D4D4))])
        // Only a background set: it becomes the foreground, the default fg becomes the background.
        XCTAssertEqual(pieces("\(esc)[48;2;21;21;21;7mX")[0], Piece(text: "X", fg: [21, 21, 21, 100], bg: hex(0xD4D4D4)))
        // Both set: swapped.
        XCTAssertEqual(pieces("\(esc)[31;44;7mX")[0], Piece(text: "X", fg: hex(0x2472C8), bg: hex(0xCD3131)))
        // 27 turns it off again.
        XCTAssertEqual(pieces("\(esc)[7mA\(esc)[27mB"), [
            Piece(text: "A", fg: hex(0x1E1E1E), bg: hex(0xD4D4D4)),
            Piece(text: "B", fg: nil, bg: nil),
        ])
    }

    func testDimIsHalfAlphaForeground() {
        XCTAssertEqual(pieces("\(esc)[2mtext"), [Piece(text: "text", fg: hex(0xD4D4D4, alpha: 50), bg: nil)])
        XCTAssertEqual(pieces("\(esc)[2;31mtext")[0].fg, hex(0xCD3131, alpha: 50))
        XCTAssertEqual(pieces("\(esc)[2mA\(esc)[22mB"), [
            Piece(text: "A", fg: hex(0xD4D4D4, alpha: 50), bg: nil),
            Piece(text: "B", fg: nil, bg: nil),
        ], "22 clears dim")
    }

    func testUnderlineColourIsConsumedNotReadAsStandaloneCodes() {
        // 58;2;255;0;0 must not parse as dim (2) then reset (0): red survives, undimmed.
        XCTAssertEqual(pieces("\(esc)[31m\(esc)[58;2;255;0;0mX"), [Piece(text: "X", fg: hex(0xCD3131), bg: nil)])
        // 58;5;1 must not parse as bold (1).
        XCTAssertNil(intent("\(esc)[58;5;1mX"))
        // 59 resets the underline colour, which is not modelled: a no-op.
        XCTAssertEqual(pieces("\(esc)[59mX"), [Piece(text: "X", fg: nil, bg: nil)])
        XCTAssertNil(intent("\(esc)[59mX"))
    }

    func testNonSGRSequencesAreDroppedWithoutGarbage() {
        XCTAssertEqual(String(AnsiText.attributed("a\(esc)[?25lb\(esc)[2Kc\(esc)]0;title\u{07}d").characters), "abcd")
        XCTAssertEqual(String(AnsiText.attributed("a\(esc)]0;title\(esc)\\b").characters), "ab", "OSC ended by ST")
        XCTAssertEqual(String(AnsiText.attributed("a\(esc)(Bb\(esc)7c\(esc)Md").characters), "abcd", "charset and single-char escapes")
        XCTAssertEqual(String(AnsiText.attributed("a\(esc)[1;1Hb\(esc)[3Jc").characters), "abc", "cursor and erase")
        XCTAssertEqual(pieces("\(esc)[>4;2mX"), [Piece(text: "X", fg: nil, bg: nil)], "a private-prefixed m is not SGR")
        XCTAssertEqual(String(AnsiText.attributed("a\rb\u{07}c").characters), "abc", "stray control characters go")
    }

    func testMalformedAndTruncatedInputDoesNotCrash() {
        XCTAssertEqual(String(AnsiText.attributed("a\(esc)[3").characters), "a")
        XCTAssertEqual(String(AnsiText.attributed("a\(esc)").characters), "a")
        XCTAssertEqual(String(AnsiText.attributed("a\(esc)]0;never ends").characters), "a")
        XCTAssertEqual(String(AnsiText.attributed("a\(esc)[38;5").characters), "a")
        XCTAssertEqual(String(AnsiText.attributed("a\(esc)[3\(esc)[31mb").characters), "ab", "a new ESC aborts the broken CSI")
        XCTAssertEqual(pieces("\(esc)[3\(esc)[31mb"), [Piece(text: "b", fg: hex(0xCD3131), bg: nil)])
        XCTAssertEqual(String(AnsiText.attributed("\(esc)[999999999999999999999mx").characters), "x")
    }

    func testMultibyteTextSurvives() {
        XCTAssertEqual(String(AnsiText.attributed("\(esc)[31m─❯ é 🙂\(esc)[0m").characters), "─❯ é 🙂")
    }

    func testRealFixturesLeaveNoEscapeBehind() throws {
        let dir = Fixture.repoRoot.appendingPathComponent("internal/adapter/testdata")
        for name in ["cursor/pane-idle-ansi.txt", "claude/pane-busy-ansi.txt"] {
            let raw = try String(contentsOf: dir.appendingPathComponent(name), encoding: .utf8)
            XCTAssertTrue(raw.contains(esc), "\(name) should be a raw capture")
            let out = String(AnsiText.attributed(raw).characters)
            XCTAssertFalse(out.contains(esc), "\(name) left an ESC in the text")
            XCTAssertFalse(out.contains("[38;5;"), "\(name) left an SGR tail in the text")
            XCTAssertFalse(out.isEmpty)
        }
        // The claude capture colours its spinner with 256-colour 174.
        let claude = try String(contentsOf: dir.appendingPathComponent("claude/pane-busy-ansi.txt"), encoding: .utf8)
        XCTAssertTrue(pieces(claude).contains { $0.fg == hex(0xD78787) })
    }
}
