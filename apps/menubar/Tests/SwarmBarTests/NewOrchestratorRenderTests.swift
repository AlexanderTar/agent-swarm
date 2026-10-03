import AppKit
import SwiftUI
import Vision
import XCTest
@testable import SwarmBarKit
@testable import SwarmBarUI

@MainActor
final class NewOrchestratorRenderTests: XCTestCase {
    /// Offscreen `cacheDisplay` snapshots blank with Liquid Glass prominent buttons
    /// (real windows render them fine — verified with screencapture), so OCR layout
    /// captures pin the bordered style; the glassProminent product default is locked
    /// separately by DialogChromeTests.testProminentStyleMatchesOS.
    override func setUp() {
        super.setUp()
        DialogChrome.prominentStyleOverride = .borderedProminent
    }

    override func tearDown() {
        DialogChrome.prominentStyleOverride = nil
        super.tearDown()
    }

    func testAgentAndAdvisorSelectorsShareAlignedSixColumnRows() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeNewOrchestratorForm()
        await form.load()
        form.picker.setAdvisorAgent("codex")

        for width: CGFloat in [760, 820] {
            let host = NSHostingView(rootView: NewOrchestratorView(form: form, onStarted: { _ in }, onCancel: {}))
            host.frame = NSRect(x: 0, y: 0, width: width, height: 790)
            host.layoutSubtreeIfNeeded()
            let bitmap = try XCTUnwrap(NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: Int(width), pixelsHigh: 790,
                                                        bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true,
                                                        isPlanar: false, colorSpaceName: .deviceRGB,
                                                        bytesPerRow: 0, bitsPerPixel: 0))
            host.cacheDisplay(in: host.bounds, to: bitmap)
            let request = VNRecognizeTextRequest()
            try VNImageRequestHandler(cgImage: try XCTUnwrap(bitmap.cgImage), options: [:]).perform([request])
            let visibleText = (request.results ?? []).compactMap { result -> (String, CGRect)? in
                guard let text = result.topCandidates(1).first?.string else { return nil }
                return (text.trimmingCharacters(in: CharacterSet(charactersIn: " •|")), CGRect(x: result.boundingBox.minX * width,
                                     y: (1 - result.boundingBox.maxY) * 790,
                                     width: result.boundingBox.width * width,
                                     height: result.boundingBox.height * 790))
            }
            func views(_ view: NSView) -> [NSView] { [view] + view.subviews.flatMap(views) }
            let popups = views(host).compactMap { $0 as? NSPopUpButton }
            func popup(_ title: String) throws -> CGRect {
                let control = try XCTUnwrap(popups.first { $0.accessibilityLabel() == title }, "Missing \(title) at \(width) pt")
                return control.convert(control.bounds, to: host)
            }
            func label(_ title: String, nearestY: CGFloat) throws -> CGRect {
                let candidates = visibleText.filter { $0.0 == title }
                return try XCTUnwrap(candidates.min { abs($0.1.midY - nearestY) < abs($1.1.midY - nearestY) }?.1,
                                     "Missing visible \(title) label at \(width) pt: \(visibleText.map(\.0))")
            }
            let agent = try popup(Copy.agent)
            let model = try popup(Copy.model)
            let effort = try popup(Copy.agentEffort)
            let advisor = try popup(Copy.advisor)
            let advisorModel = try popup("Advisor model")
            let advisorEffort = try popup(Copy.advisorEffort)
            for row in [(agent, model, effort), (advisor, advisorModel, advisorEffort)] {
                XCTAssertEqual(row.0.midY, row.1.midY, accuracy: 2)
                XCTAssertEqual(row.0.midY, row.2.midY, accuracy: 2)
            }
            for pair in [(agent, advisor), (model, advisorModel), (effort, advisorEffort)] {
                XCTAssertEqual(pair.0.minX, pair.1.minX, accuracy: 2)
            }
            for (title, control) in [(Copy.agent, agent), (Copy.model, model), (Copy.effort, effort),
                                     (Copy.advisor, advisor), (Copy.model, advisorModel), (Copy.effort, advisorEffort)] {
                let text = try label(title, nearestY: control.midY)
                XCTAssertEqual(text.midY, control.midY, accuracy: 12, "\(title) label must share its picker row")
            }
            let modelLabel = try label(Copy.model, nearestY: model.midY)
            let advisorModelLabel = try label(Copy.model, nearestY: advisorModel.midY)
            let effortLabel = try label(Copy.effort, nearestY: effort.midY)
            let advisorEffortLabel = try label(Copy.effort, nearestY: advisorEffort.midY)
            // OCR boxes jitter a few points with a label's vertical position; the
            // pickers themselves are held to 2 pt above.
            XCTAssertEqual(modelLabel.minX, advisorModelLabel.minX, accuracy: 5)
            XCTAssertEqual(effortLabel.minX, advisorEffortLabel.minX, accuracy: 5)
            XCTAssertLessThanOrEqual(advisorEffort.maxX, width - 22)
        }
    }

    func testNativeEffortRowsAppearForSimulatedPairAndFitNormalWindow() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeNewOrchestratorForm()
        await form.load()
        func controls(width: CGFloat = 820, capture name: String? = nil) -> (labels: Set<String>, editorBottom: CGFloat, editorHeight: CGFloat, effortWidth: CGFloat, scrollCount: Int) {
            let host = NSHostingView(rootView: NewOrchestratorView(form: form, onStarted: { _ in }, onCancel: {}))
            host.frame = NSRect(x: 0, y: 0, width: width, height: 790)
            host.layoutSubtreeIfNeeded()
            if let name, let dir = ProcessInfo.processInfo.environment["SWARM_NEW_ORCH_SCREENSHOT_DIR"],
               let image = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: Int(width), pixelsHigh: 790,
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
        form.picker.setAdvisorAgent("codex")
        let simulated = controls(capture: "820")
        let narrow = controls(width: 760, capture: "760")
        XCTAssertTrue(simulated.0.contains("Agent Effort"))
        XCTAssertTrue(simulated.0.contains("Advisor Effort"))
        XCTAssertLessThanOrEqual(simulated.1, 745, "request editor must stay clear of the footer")
        XCTAssertGreaterThanOrEqual(simulated.editorHeight, 250, "five or more request lines must fit")
        XCTAssertEqual(simulated.scrollCount, 2, "normal window needs only repository and request scrolling")
        XCTAssertLessThanOrEqual(simulated.effortWidth, 320, "effort menu should balance with model menu")
        XCTAssertTrue(narrow.labels.contains(Copy.advisorEffort))
        XCTAssertEqual(narrow.scrollCount, 2)
        XCTAssertGreaterThanOrEqual(narrow.editorHeight, 250)
        form.picker.setAdvisorAgent("none")
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
        XCTAssertGreaterThanOrEqual(views[0].bounds.height, 120, "at least four rows fit")
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
        host.frame = NSRect(x: 0, y: 0, width: 760, height: 700)
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
        func popups(_ view: NSView) -> [NSPopUpButton] {
            let own = (view as? NSPopUpButton).map { [$0] } ?? []
            return own + view.subviews.flatMap(popups)
        }
        for popup in popups(host) {
            XCTAssertLessThanOrEqual(popup.convert(popup.bounds, to: host).maxX, 738,
                                     "error text must not push \(popup.accessibilityLabel() ?? "popup") beyond the side inset")
        }
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
        form.picker.setAgent("agy")
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

    func testRequestEditorInsetsTextFromItsBorder() {
        let host = NSHostingView(rootView: RequestEditor(text: .constant("A request")))
        host.frame = NSRect(x: 0, y: 0, width: 500, height: 200)
        host.layoutSubtreeIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        func scrolls(in view: NSView) -> [NSScrollView] {
            let own = (view as? NSScrollView).map { [$0] } ?? []
            return own + view.subviews.flatMap(scrolls)
        }
        guard let scroll = scrolls(in: host).first else { return XCTFail("Request editor scroll view missing") }
        let frame = scroll.convert(scroll.bounds, to: host)
        // NSHostingView is flipped: minY is the gap between the border and the text area's top.
        XCTAssertGreaterThanOrEqual(host.isFlipped ? frame.minY : host.bounds.height - frame.maxY, 4,
                                    "the first line must not touch the top border")
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
        host.frame = NSRect(x: 0, y: 0, width: 760, height: 790)
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
            XCTAssertGreaterThanOrEqual(columns[0].width, 130)
            XCTAssertGreaterThanOrEqual(columns[1].width, 200)
            XCTAssertLessThanOrEqual(columns[1].maxX, 738)
        }
        if let agent = controls.first(where: { $0.accessibilityLabel() == Copy.agent }),
           let codex = agent.itemArray.first(where: { $0.representedObject as? String == "codex" }) {
            agent.select(codex)
            XCTAssertTrue(NSApp.sendAction(agent.action!, to: agent.target, from: agent))
            XCTAssertEqual(form.picker.choice.agent, .codex)
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
        host.frame = NSRect(x: 0, y: 0, width: 820, height: 790)
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
            let image = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: 820, pixelsHigh: 790,
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
        form.picker.setAgent("codex")
        XCTAssertGreaterThanOrEqual(renderedSize(NewOrchestratorView(form: form, onStarted: { _ in }, onCancel: {})).width, 760)
        form.name = "Investigate login crash"
        form.picker.setModel("gpt-6-astra")
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

    func testRepoPickerHasNoSelectedSummaryLine() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let m = makeAppModel(client)
        await m.refresh()
        let form = m.makeNewOrchestratorForm()
        await form.load()
        form.toggle(form.repos.recent[0])
        form.toggle(form.repos.all[1])
        XCTAssertEqual(form.selection.count, 2)
        let text = try visibleText(host(form))
        XCTAssertFalse(text.contains("Selected:"), text)
    }

    // MARK: images

    /// Plain `Text`/`Button` draw directly in this codebase (no backing `NSView`), so visible
    /// copy is read back with OCR, the same technique `testEmptyChooserExplainsHowToAddRepositories`
    /// and `testAgentAndAdvisorSelectorsShareAlignedSixColumnRows` already use.
    private func visibleText(_ host: NSView, width: CGFloat = 820, height: CGFloat = 790) throws -> String {
        let image = try XCTUnwrap(NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: Int(width), pixelsHigh: Int(height),
                                                   bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true,
                                                   isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0))
        host.cacheDisplay(in: host.bounds, to: image)
        let request = VNRecognizeTextRequest()
        try VNImageRequestHandler(cgImage: try XCTUnwrap(image.cgImage), options: [:]).perform([request])
        return (request.results ?? []).compactMap { $0.topCandidates(1).first?.string }.joined(separator: " ")
    }

    /// A valid 1×1 PNG, same bytes used by NewOrchestratorFormTests.
    private var pngData: Data {
        Data([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
              0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
              0x89, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9C, 0x63, 0xF8, 0x0F, 0x00, 0x00,
              0x01, 0x01, 0x00, 0x05, 0x18, 0xD8, 0x4E, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE,
              0x42, 0x60, 0x82])
    }

    private func host(_ form: NewOrchestratorForm) -> NSHostingView<NewOrchestratorView> {
        let host = NSHostingView(rootView: NewOrchestratorView(form: form, onStarted: { _ in }, onCancel: {}))
        host.frame = NSRect(x: 0, y: 0, width: 820, height: 790)
        host.layoutSubtreeIfNeeded()
        return host
    }

    func testImageStripEmptyStateShowsOnlyAddButton() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeNewOrchestratorForm()
        await form.load()
        let h = host(form)
        // Button copy (any Button in this codebase, not just the footer's) never reaches OCR --
        // verified empirically -- so only the counter, a plain Text, is checked here. The button
        // itself is exercised at the model layer (NewOrchestratorFormTests' image tests).
        XCTAssertFalse(try visibleText(h).contains(Copy.imageCount(0)))
        XCTAssertEqual(form.images.count, 0)
    }

    func testImageStripShowsThumbnailsAndCounter() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeNewOrchestratorForm()
        await form.load()
        form.addImage(data: pngData, name: "a.png")
        form.addImage(data: pngData, name: "b.png")
        form.addImage(data: pngData, name: "c.png")
        XCTAssertEqual(form.images.map(\.name), ["a.png", "b.png", "c.png"])
        let h = host(form)
        XCTAssertTrue(try visibleText(h).contains(Copy.imageCount(3)))
    }

    func testImageStripDisablesAddButtonAtTen() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeNewOrchestratorForm()
        await form.load()
        for i in 0..<10 { form.addImage(data: pngData, name: "\(i).png") }
        // The Add images… button's disabled binding is form.images.count >= 10, exercised at the
        // model layer (testAddImageCapsAtTen); this just confirms the maxed-out strip still renders.
        XCTAssertEqual(form.images.count, 10)
        let h = host(form)
        XCTAssertTrue(try visibleText(h).contains(Copy.imageCount(10)))
    }

    func testImageStripShowsErrorText() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeNewOrchestratorForm()
        await form.load()
        form.addImage(data: Data("text".utf8), name: "n.txt")
        let h = host(form)
        // OCR occasionally misreads a lowercase "i" as "I" in this small caption font; compare
        // case-insensitively rather than assert exact-case text a human never sees compared.
        XCTAssertTrue(try visibleText(h).lowercased().contains(Copy.imageUnsupported("n.txt").lowercased()))
    }

    func testStartedWithUnsavedImagesShowsWarningAndDoneButton() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeNewOrchestratorForm()
        await form.load()
        form.name = "x"
        form.addImage(data: pngData, name: "a.png")
        client.spikeResult = .success(CreateSpikeResponse(
            agent: AgentNode(name: "x", kind: .claude, model: "opus", role: .orchestrator), queued: false, attachmentsFailed: true))
        let created = await form.submit()
        XCTAssertNotNil(created)
        XCTAssertNotNil(form.startedWithUnsavedImages, "the view swaps Start for Done off this flag")
        // Start/Cancel/Done draw through a Liquid Glass material `cacheDisplay` can't rasterize
        // (verified empirically: even the pre-existing Cancel button's text never reaches OCR), so
        // only the warning banner is checked here; the Start-vs-Done swap itself is a one-line
        // `if let agent = form.startedWithUnsavedImages` in NewOrchestratorView, driven by the
        // model-level state already asserted above.
        let h = host(form)
        XCTAssertTrue(try visibleText(h).contains(Copy.imagesNotSaved))
    }

    // MARK: ImagePasteTextView

    private func testPasteboard() -> NSPasteboard { NSPasteboard(name: NSPasteboard.Name("swarm-test-\(UUID().uuidString)")) }

    func testReadSelectionRoutesImageDataToCallbackAndLeavesTextUnchanged() {
        let pboard = testPasteboard()
        pboard.declareTypes([.png], owner: nil)
        pboard.setData(pngData, forType: .png)
        let textView = ImagePasteTextView()
        textView.string = "existing text"
        var received: (Data, String)?
        textView.onImageData = { data, name in received = (data, name) }
        let handled = textView.readSelection(from: pboard, type: .png)
        XCTAssertTrue(handled)
        XCTAssertEqual(received?.0, pngData)
        XCTAssertEqual(textView.string, "existing text")
    }

    func testReadSelectionRoutesImageFileURLToCallbackWithoutInsertingItsPath() throws {
        let url = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString + ".png")
        try pngData.write(to: url)
        defer { try? FileManager.default.removeItem(at: url) }
        let pboard = testPasteboard()
        pboard.writeObjects([url as NSURL])
        let textView = ImagePasteTextView()
        textView.string = ""
        var received: [URL]?
        textView.onImageURLs = { urls in received = urls }
        let handled = textView.readSelection(from: pboard, type: .fileURL)
        XCTAssertTrue(handled)
        XCTAssertEqual(received, [url])
        XCTAssertEqual(textView.string, "", "the path must not land in the request text")
    }

    func testReadSelectionInsertsPlainTextNormally() {
        let pboard = testPasteboard()
        pboard.declareTypes([.string], owner: nil)
        pboard.setString("hello there", forType: .string)
        let textView = ImagePasteTextView()
        textView.string = ""
        var calledBack = false
        textView.onImageData = { _, _ in calledBack = true }
        textView.onImageURLs = { _ in calledBack = true }
        let handled = textView.readSelection(from: pboard, type: .string)
        XCTAssertTrue(handled, "NSTextView's own text insertion still handles this type")
        XCTAssertFalse(calledBack)
        XCTAssertEqual(textView.string, "hello there")
    }

    // MARK: loading-time validation (TASK-486)

    func testNoValidationErrorsWhileCatalogLoads() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        let model = makeAppModel(client)
        await model.refresh()
        client.holdCatalog = true
        let form = model.makeNewOrchestratorForm()
        let h = host(form)
        assertNoPickerErrors(try ocrText(h), "window shown, load not started")
        let load = Task { await form.load() }
        await waitForCall(client, "catalog")
        assertNoPickerErrors(try ocrText(h), "catalog in flight")
        XCTAssertFalse(form.canStart, "no start against an unloaded catalog")
        client.releaseCatalog()
        await load.value
        assertNoPickerErrors(try ocrText(h), "loaded with valid Settings")
    }

    func testGenuineValidationErrorShowsAfterLoad() async throws {
        let client = try MockDaemonClient(fixtures: Fixture.dir)
        var state = try client.stateResult.get()
        state.settings.enabledAgents = [.codex, .agy]
        client.stateResult = .success(state)
        let model = makeAppModel(client)
        await model.refresh()
        let form = model.makeNewOrchestratorForm()
        await form.load()
        XCTAssertTrue(try ocrText(host(form)).contains(Copy.chooseAgent))
    }
}
