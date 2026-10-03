import AppKit
import SwiftUI
import XCTest
import Vision
@testable import SwarmBarKit
@testable import SwarmBarUI

@MainActor
final class NativeLayoutPolishTests: XCTestCase {
    private func descendants(_ view: NSView) -> [NSView] { [view] + view.subviews.flatMap(descendants) }

    private func host<V: View>(_ view: V, width: CGFloat, height: CGFloat) -> NSHostingView<V> {
        let host = NSHostingView(rootView: view)
        host.frame = NSRect(x: 0, y: 0, width: width, height: height)
        host.layoutSubtreeIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.15))
        return host
    }

    private func assertSubtleScrollers(_ host: NSView, file: StaticString = #filePath, line: UInt = #line) {
        let scrolls = descendants(host).compactMap { $0 as? NSScrollView }
        XCTAssertFalse(scrolls.isEmpty, file: file, line: line)
        for scroll in scrolls {
            XCTAssertEqual(scroll.scrollerStyle, .overlay, file: file, line: line)
            XCTAssertTrue(scroll.autohidesScrollers, file: file, line: line)
            XCTAssertFalse(scroll.drawsBackground, "scroll view must preserve its underlying material", file: file, line: line)
            for scroller in [scroll.verticalScroller, scroll.horizontalScroller].compactMap({ $0 }) {
                XCTAssertEqual(scroller.controlSize, .small, file: file, line: line)
            }
        }
    }

    func testLongRepositoryPathsAndRequestStayInsideNarrowForm() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        client.reposResponse.all = (0..<12).map {
            Repo(id: "long-\($0)", name: "Project-\($0)",
                 path: "/Volumes/" + String(repeating: "long-parent-directory/", count: 20) + "Repository-\($0)")
        }
        let model = makeAppModel(client)
        let form = model.makeNewOrchestratorForm()
        await form.load()
        form.request = String(repeating: "unbroken-request-text", count: 100)
        form.selection = client.reposResponse.all.map(\.id)
        for height: CGFloat in [700, 790] {
            let view = host(NewOrchestratorView(form: form, onStarted: { _ in }, onCancel: {}), width: 760, height: height)
            let scrolls = descendants(view).compactMap { $0 as? NSScrollView }
            XCTAssertGreaterThanOrEqual(scrolls.count, 2)
            for scroll in scrolls {
                let box = scroll.convert(scroll.bounds, to: view)
                XCTAssertGreaterThanOrEqual(box.minX, 0)
                XCTAssertLessThanOrEqual(box.maxX, 760)
                XCTAssertFalse(scroll.hasHorizontalScroller, "form and repository list must scroll vertically only")
                if let document = scroll.documentView {
                    XCTAssertLessThanOrEqual(document.bounds.width, scroll.contentSize.width + 1,
                                             "long paths or request text must not widen the document")
                }
            }
            assertSubtleScrollers(view)
            let bitmap = try captureNativeWindow(view, name: "narrow-form-\(Int(height))")
            let request = VNRecognizeTextRequest()
            try VNImageRequestHandler(cgImage: XCTUnwrap(bitmap.cgImage), options: [:]).perform([request])
            let text = (request.results ?? []).compactMap { $0.topCandidates(1).first?.string }.joined(separator: "\n")
            XCTAssertTrue(text.contains("Intent"), "intent must retain its visible field label")
            XCTAssertTrue(text.lowercased().contains("repository-"), "middle ellipsis must retain the repository path suffix")
        }
    }

    func testWorkerPickersFitMinimumBoardDialogWidth() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeBoardHandoffForm()
        await form.load()
        form.selectedKey = "BUG-7"
        let view = host(BoardHandoffView(form: form, onDone: {}, onCancel: {}), width: 760, height: 500)
        let controls = descendants(view).compactMap { $0 as? NSPopUpButton }
        XCTAssertNotNil(controls.first { $0.accessibilityLabel() == "Coding Agent" })
        for control in controls {
            let box = control.convert(control.bounds, to: view)
            XCTAssertGreaterThanOrEqual(box.minX, 22, "\(control.accessibilityLabel() ?? "picker")")
            XCTAssertLessThanOrEqual(box.maxX, 738, "\(control.accessibilityLabel() ?? "picker")")
        }
    }

    func testAdjacentTextEditorUsesTransparentSubtleScrolling() {
        let view = host(TextEditor(text: .constant(String(repeating: "Rule\n", count: 100)))
            .scrollContentBackground(.hidden)
            .background(SubtleScrollerConfig(adjacentScrollView: true)), width: 700, height: 300)
        assertSubtleScrollers(view)
    }

    func testInstructionsReaderUsesTransparentSubtleScrolling() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        let settings = model.makeSettings()
        await settings.load()
        await settings.setInstructions(String(repeating: "Rule\n", count: 100))
        assertSubtleScrollers(host(InstructionsTab(model: settings), width: 740, height: 520))
    }

    /// One scan line through the field's left edge, inside the field in every state (the header and the
    /// footer buttons differ between states, so only a mid-height line is comparable).
    private func fieldEdgeRow(_ view: NSView, y: Int) -> [UInt32] {
        guard let rep = view.bitmapImageRepForCachingDisplay(in: view.bounds) else { return [] }
        view.cacheDisplay(in: view.bounds, to: rep)
        return (0..<40).map { x in
            let c = rep.colorAt(x: x, y: y)?.usingColorSpace(.deviceRGB)
            let v = { (f: CGFloat?) in UInt32((f ?? 0) * 255) }
            return v(c?.redComponent) << 24 | v(c?.greenComponent) << 16 | v(c?.blueComponent) << 8 | v(c?.alphaComponent)
        }
    }

    func testInstructionsEmptyStateSharesTheFieldSurface() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        let settings = model.makeSettings()
        await settings.load()
        await settings.setInstructions("Rule")
        let filled = fieldEdgeRow(host(InstructionsTab(model: settings), width: 740, height: 520), y: 260)
        await settings.setInstructions("")
        let empty = fieldEdgeRow(host(InstructionsTab(model: settings), width: 740, height: 520), y: 260)
        XCTAssertFalse(filled.isEmpty)
        XCTAssertEqual(empty, filled, "empty state must use dialogFieldSurface like the read/edit field")
    }

    func testPopoverOuterScrollUsesTransparentSubtleScrolling() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        assertSubtleScrollers(host(PopoverView(model: model, openNewOrchestrator: {}, openSettings: {}),
                                  width: 360, height: 700))
    }
}
