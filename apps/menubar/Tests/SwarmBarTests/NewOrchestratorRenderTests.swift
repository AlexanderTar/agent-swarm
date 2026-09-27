import AppKit
import SwiftUI
import Vision
import XCTest
@testable import SwarmBarKit
@testable import SwarmBarUI

@MainActor
final class NewOrchestratorRenderTests: XCTestCase {
    func testNativeEffortRowsAppearForSimulatedPairAndFitNormalWindow() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeNewOrchestratorForm()
        await form.load()
        func controls(width: CGFloat = 820, capture name: String? = nil) -> (labels: Set<String>, editorBottom: CGFloat, editorHeight: CGFloat, effortWidth: CGFloat, scrollCount: Int) {
            let host = NSHostingView(rootView: NewOrchestratorView(form: form, onStarted: { _ in }, onCancel: {}))
            host.frame = NSRect(x: 0, y: 0, width: width, height: 860)
            host.layoutSubtreeIfNeeded()
            if let name, let dir = ProcessInfo.processInfo.environment["SWARM_NEW_ORCH_SCREENSHOT_DIR"],
               let image = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: Int(width), pixelsHigh: 860,
                                            bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true,
                                            isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0) {
                host.cacheDisplay(in: host.bounds, to: image)
                try? image.representation(using: .png, properties: [:])?
                    .write(to: URL(fileURLWithPath: dir).appendingPathComponent("effort-\(name).png"))
            }
            func descendants(_ view: NSView) -> [NSView] { [view] + view.subviews.flatMap(descendants) }
            let views = descendants(host)
            let popups = views.compactMap { $0 as? NSPopUpButton }
            let labels = Set(popups.compactMap { $0.accessibilityLabel() })
            let scrolls = views.compactMap { $0 as? NSScrollView }
            let editor = scrolls.last
            let effortWidth = popups.first { $0.accessibilityLabel() == Copy.advisorEffort }?.bounds.width ?? 0
            return (labels, editor.map { $0.convert($0.bounds, to: host).maxY } ?? 0,
                    editor?.bounds.height ?? 0, effortWidth, scrolls.count)
        }
        XCTAssertTrue(controls().0.contains("Agent Effort"))
        XCTAssertFalse(controls().0.contains("Advisor Effort"), "native Claude pairing has no independent effort")
        form.setAdvisorAgent("codex")
        let simulated = controls(capture: "820")
        let narrow = controls(width: 760, capture: "760")
        XCTAssertTrue(simulated.0.contains("Agent Effort"))
        XCTAssertTrue(simulated.0.contains("Advisor Effort"))
        XCTAssertLessThanOrEqual(simulated.1, 815, "request editor must stay clear of the footer")
        XCTAssertGreaterThanOrEqual(simulated.editorHeight, 250, "five or more request lines must fit")
        XCTAssertEqual(simulated.scrollCount, 2, "normal window needs only repository and request scrolling")
        XCTAssertLessThanOrEqual(simulated.effortWidth, 320, "effort menu should balance with model menu")
        XCTAssertTrue(narrow.labels.contains(Copy.advisorEffort))
        XCTAssertEqual(narrow.scrollCount, 2)
        XCTAssertGreaterThanOrEqual(narrow.editorHeight, 250)
        form.setAdvisorAgent("none")
        XCTAssertFalse(controls().0.contains("Advisor Effort"))
    }

    func testFourRowsFitBeforeOuterScrollAtOrdinaryTextSize() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeNewOrchestratorForm()
        for index in 0..<12 {
            client.reposResponse.all.append(Repo(id: "extra-\(index)", name: "project-\(index)",
                                                  path: "/Users/alex/GitHub/project-\(index)"))
        }
        await form.load()
        form.name = "x"
        client.spikeResult = .failure(.unreachable)
        _ = await form.submit()
        let host = NSHostingView(rootView: NewOrchestratorView(form: form, onStarted: { _ in }, onCancel: {}))
        host.frame = NSRect(x: 0, y: 0, width: 820, height: 700)
        host.layoutSubtreeIfNeeded()
        func scrolls(in view: NSView) -> [NSScrollView] {
            let own = (view as? NSScrollView).map { [$0] } ?? []
            return own + view.subviews.flatMap(scrolls)
        }
        let views = scrolls(in: host)
        XCTAssertEqual(views.count, 2, "four list rows should fit without outer scrolling")
        guard views.count == 2 else { return }
        XCTAssertEqual(views[0].bounds.height, 120)
        XCTAssertGreaterThanOrEqual(views[1].bounds.height, 108)
        XCTAssertLessThanOrEqual(views[1].convert(views[1].bounds, to: host).maxY, host.bounds.height - 45)
    }

    func testLongSubmissionErrorAloneUsesOuterScroll() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeNewOrchestratorForm()
        for index in 0..<12 {
            client.reposResponse.all.append(Repo(id: "extra-\(index)", name: "project-\(index)",
                                                  path: "/Users/alex/GitHub/project-\(index)"))
        }
        await form.load()
        form.name = "x"
        client.spikeResult = .failure(.api(status: 422, code: "preflight_failed",
                                           message: String(repeating: "Repository needs attention. ", count: 28)))
        _ = await form.submit()
        let host = NSHostingView(rootView: NewOrchestratorView(form: form, onStarted: { _ in }, onCancel: {}))
        host.frame = NSRect(x: 0, y: 0, width: 820, height: 700)
        host.layoutSubtreeIfNeeded()
        func scrolls(in view: NSView) -> [NSScrollView] {
            let own = (view as? NSScrollView).map { [$0] } ?? []
            return own + view.subviews.flatMap(scrolls)
        }
        let views = scrolls(in: host)
        XCTAssertEqual(views.count, 3)
        guard views.count == 3 else { return }
        let outer = views[0].convert(views[0].bounds, to: host)
        XCTAssertLessThan(outer.maxY, host.bounds.height - 30)
        XCTAssertGreaterThan(views[2].convert(views[2].bounds, to: host).maxY, outer.maxY)
    }

    func testOrdinaryTextOverflowUsesOuterScrollAndKeepsFooterOutsideIt() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeNewOrchestratorForm()
        for index in 0..<12 {
            client.reposResponse.all.append(Repo(id: "extra-\(index)", name: "project-\(index)",
                                                  path: "/Users/alex/GitHub/project-\(index)"))
        }
        await form.load()
        form.name = "x"
        client.spikeResult = .failure(.api(status: 422, code: "preflight_failed",
                                           message: String(repeating: "Repository needs attention. ", count: 12)))
        _ = await form.submit()
        form.name = "🔥"
        form.setAgent("agy")
        client.failNext = .api(status: 500, code: "scan_failed",
                               message: String(repeating: "Could not scan repository. ", count: 10))
        await form.search()
        let host = NSHostingView(rootView: NewOrchestratorView(form: form, onStarted: { _ in }, onCancel: {}))
        host.frame = NSRect(x: 0, y: 0, width: 820, height: 700)
        host.layoutSubtreeIfNeeded()
        func scrolls(in view: NSView) -> [NSScrollView] {
            let own = (view as? NSScrollView).map { [$0] } ?? []
            return own + view.subviews.flatMap(scrolls)
        }
        let views = scrolls(in: host)
        XCTAssertEqual(views.count, 3, "overflowing form needs a last-resort outer scroll view")
        guard let outer = views.first, views.count == 3 else { return }
        XCTAssertGreaterThan(views.last!.convert(views.last!.bounds, to: host).maxY,
                             outer.convert(outer.bounds, to: host).maxY,
                             "Request extends beyond the viewport and must be reachable by scrolling")
        XCTAssertLessThan(outer.convert(outer.bounds, to: host).maxY, host.bounds.height - 30,
                          "footer stays outside the form scroll view")
    }

    func testRequestEditorUsesOverlayScroller() {
        let host = NSHostingView(rootView: RequestEditor(text: .constant("A request")))
        host.frame = NSRect(x: 0, y: 0, width: 500, height: 200)
        host.layoutSubtreeIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        func scrolls(in view: NSView) -> [NSScrollView] {
            let own = (view as? NSScrollView).map { [$0] } ?? []
            return own + view.subviews.flatMap(scrolls)
        }
        guard let scroll = scrolls(in: host).first else { return XCTFail("Request editor scroll view missing") }
        XCTAssertEqual(scroll.scrollerStyle, .overlay)
        XCTAssertTrue(scroll.autohidesScrollers)
        XCTAssertEqual(scroll.verticalScroller?.controlSize, .small)
    }

    func testEmptyChooserExplainsHowToAddRepositories() throws {
        let host = NSHostingView(rootView: RepoChooser(rows: [], selection: .constant([])))
        host.frame = NSRect(x: 0, y: 0, width: 500, height: 240)
        host.layoutSubtreeIfNeeded()
        let image = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: 500, pixelsHigh: 240,
                                     bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true,
                                     isPlanar: false, colorSpaceName: .deviceRGB,
                                     bytesPerRow: 0, bitsPerPixel: 0)!
        host.cacheDisplay(in: host.bounds, to: image)
        let request = VNRecognizeTextRequest()
        try VNImageRequestHandler(cgImage: try XCTUnwrap(image.cgImage), options: [:]).perform([request])
        let visibleText = (request.results ?? []).compactMap { $0.topCandidates(1).first?.string }.joined(separator: " ")
        XCTAssertTrue(visibleText.contains("No repositories found"), visibleText)
        XCTAssertTrue(visibleText.contains("Add a folder or rescan"), visibleText)
    }
    func testMenusFitAtMinimumWindowWidth() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeNewOrchestratorForm()
        await form.load()
        let host = NSHostingView(rootView: NewOrchestratorView(form: form, onStarted: { _ in }, onCancel: {}))
        host.frame = NSRect(x: 0, y: 0, width: 760, height: 860)
        host.layoutSubtreeIfNeeded()
        func popups(in view: NSView) -> [NSPopUpButton] {
            let own = (view as? NSPopUpButton).map { [$0] } ?? []
            return own + view.subviews.flatMap(popups)
        }
        let controls = popups(in: host)
        let paired = controls.filter { [Copy.agent, Copy.model, Copy.advisor, "Advisor model"].contains($0.accessibilityLabel() ?? "") }
        let frames = paired.map { $0.convert($0.bounds, to: host) }.sorted { $0.minY < $1.minY }
        XCTAssertEqual(controls.count, 5)
        XCTAssertEqual(frames.count, 4)
        guard frames.count == 4 else { return }
        for row in [Array(frames[0...1]), Array(frames[2...3])] {
            let columns = row.sorted { $0.minX < $1.minX }
            XCTAssertGreaterThanOrEqual(columns[0].width, 150)
            XCTAssertGreaterThanOrEqual(columns[1].width, 300)
            XCTAssertLessThanOrEqual(columns[1].maxX, 738)
        }
        if let agent = controls.first(where: { $0.accessibilityLabel() == Copy.agent }),
           let codex = agent.itemArray.first(where: { $0.representedObject as? String == "codex" }) {
            agent.select(codex)
            XCTAssertTrue(NSApp.sendAction(agent.action!, to: agent.target, from: agent))
            XCTAssertEqual(form.choice.agent, .codex)
        } else {
            XCTFail("Native Agent popup has no Codex choice")
        }
        func listWidth() -> CGFloat {
            func scrolls(in view: NSView) -> [NSScrollView] {
                let own = (view as? NSScrollView).map { [$0] } ?? []
                return own + view.subviews.flatMap(scrolls)
            }
            guard let list = scrolls(in: host).first else { return 0 }
            return list.convert(list.bounds, to: host).width
        }
        XCTAssertEqual(listWidth(), 716, accuracy: 1)
        host.frame.size.width = 960
        host.layoutSubtreeIfNeeded()
        XCTAssertEqual(listWidth(), 916, accuracy: 1)
    }

    func testNativeWindowGeometryAndCapture() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let m = makeAppModel(client)
        await m.refresh()
        let form = m.makeNewOrchestratorForm()
        await form.load()
        let view = NewOrchestratorView(form: form, onStarted: { _ in }, onCancel: {})
        let host = NSHostingView(rootView: AnyView(view))
        host.frame = NSRect(x: 0, y: 0, width: 820, height: 860)
        let window: NSWindow?
        if ProcessInfo.processInfo.environment["SWARM_NEW_ORCH_SCREENSHOT_DIR"] != nil {
            let native = NSWindow(contentRect: host.frame,
                                  styleMask: [.titled, .closable, .resizable], backing: .buffered, defer: false)
            native.contentView = host
            native.makeKeyAndOrderFront(nil)
            window = native
        } else {
            window = nil
        }
        defer { window?.close() }
        try captureNative(host, name: "normal")
        for index in 0..<12 {
            client.reposResponse.all.append(Repo(id: "extra-\(index)", name: "project-\(index)",
                                                  path: "/Users/alex/GitHub/project-\(index)"))
        }
        await form.load()
        try captureNative(host, name: "twelve-repos")
        client.reposResponse.all = []
        await form.load()
        try captureNative(host, name: "empty")
        form.name = "x"
        client.spikeResult = .failure(.unreachable)
        _ = await form.submit()
        try captureNative(host, name: "error")
        host.rootView = AnyView(view.environment(\.sizeCategory, .accessibilityExtraExtraExtraLarge))
        try captureNative(host, name: "large-text")
    }

    private func captureNative(_ host: NSHostingView<AnyView>, name: String) throws {
        host.layoutSubtreeIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        func scrolls(in view: NSView) -> [NSScrollView] {
            let own = (view as? NSScrollView).map { [$0] } ?? []
            return own + view.subviews.flatMap(scrolls)
        }
        let scrollViews = scrolls(in: host)
        XCTAssertEqual(scrollViews.count, name == "large-text" ? 3 : 2)
        guard scrollViews.count >= (name == "large-text" ? 3 : 2) else { return }
        let list = scrollViews[name == "large-text" ? 1 : 0]
        let editor = scrollViews.last!
        XCTAssertEqual(editor.scrollerStyle, .overlay)
        XCTAssertTrue(editor.autohidesScrollers)
        XCTAssertEqual(editor.verticalScroller?.controlSize, .small)
        XCTAssertEqual(list.bounds.height, name == "twelve-repos" ? 240 : 120,
                       "four rows fit tightly while empty and error states keep a bounded placeholder")
        XCTAssertGreaterThanOrEqual(editor.bounds.height, 100)
        if name == "normal" { XCTAssertGreaterThanOrEqual(editor.bounds.height, 250) }
        if name == "twelve-repos" { XCTAssertGreaterThanOrEqual(editor.bounds.height, 150) }
        XCTAssertLessThanOrEqual(editor.convert(editor.bounds, to: host).maxY, host.bounds.height - 45)
        XCTAssertGreaterThanOrEqual(host.fittingSize.width, 760)
        XCTAssertEqual(host.bounds.width, 820)
        if let dir = ProcessInfo.processInfo.environment["SWARM_NEW_ORCH_SCREENSHOT_DIR"] {
            let image = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: 820, pixelsHigh: 860,
                                         bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true,
                                         isPlanar: false, colorSpaceName: .deviceRGB,
                                         bytesPerRow: 0, bitsPerPixel: 0)!
            host.cacheDisplay(in: host.bounds, to: image)
            try image.representation(using: .png, properties: [:])!
                .write(to: URL(fileURLWithPath: dir).appendingPathComponent("new-orchestrator-\(name).png"))
        }
    }

    func testFormRendersValidInvalidAndFailed() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let m = makeAppModel(client)
        await m.refresh()
        let form = m.makeNewOrchestratorForm()
        await form.load()
        XCTAssertGreaterThanOrEqual(renderedSize(NewOrchestratorView(form: form, onStarted: { _ in }, onCancel: {})).width, 760)
        form.name = "🔥"
        form.setAgent("codex")
        XCTAssertGreaterThanOrEqual(renderedSize(NewOrchestratorView(form: form, onStarted: { _ in }, onCancel: {})).width, 760)
        form.name = "Investigate login crash"
        form.setModel("gpt-6-astra")
        client.spikeResult = .failure(.unreachable)
        _ = await form.submit()
        XCTAssertNotNil(form.failure)
        XCTAssertGreaterThanOrEqual(renderedSize(NewOrchestratorView(form: form, onStarted: { _ in }, onCancel: {})).width, 760)
        XCTAssertGreaterThanOrEqual(RequestEditor.minimumHeight, 100)
        XCTAssertEqual(RepoChooser.visibleHeight(for: 12, maxRows: 6), 180)
        XCTAssertEqual(RepoChooser.visibleHeight(for: 12), 240)
        XCTAssertEqual(RepoChooser.visibleHeight(for: 0), 120)
        XCTAssertEqual(RepoChooser.visibleHeight(for: 3), 90)
        XCTAssertEqual(RepoChooser.visibleHeight(for: 4), 120)
    }
}
