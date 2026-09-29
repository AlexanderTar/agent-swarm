import AppKit
import SwarmBarKit
import SwiftUI
import UniformTypeIdentifiers

/// New orchestrator window (§16.3).
public struct NewOrchestratorView: View {
    @Bindable var form: NewOrchestratorForm
    let onStarted: (AgentNode) -> Void
    let onCancel: () -> Void
    @Environment(\.sizeCategory) private var sizeCategory

    public init(form: NewOrchestratorForm, onStarted: @escaping (AgentNode) -> Void, onCancel: @escaping () -> Void) {
        self.form = form
        self.onStarted = onStarted
        self.onCancel = onCancel
    }

    public var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            if form.startedWithUnsavedImages != nil {
                Label(Copy.imagesNotSaved, systemImage: "exclamationmark.triangle.fill")
                    .foregroundStyle(.orange)
                    .padding(.horizontal, 22)
                    .padding(.top, 12)
            }
            GeometryReader { geometry in
                if sizeCategory.isAccessibilityCategory || geometry.size.height < 600 {
                    ScrollView {
                        formContents(maxRows: 4)
                            .background(SubtleScrollerConfig())
                    }
                    .scrollIndicators(.automatic)
                } else {
                    ViewThatFits(in: .vertical) {
                        formContents(maxRows: geometry.size.height >= 700 ? 8 : 6)
                            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
                        formContents(maxRows: 4)
                            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
                        ScrollView {
                            formContents(maxRows: 4)
                                .background(SubtleScrollerConfig())
                        }
                        .scrollIndicators(.automatic)
                    }
                }
            }
            // Once the orchestrator has started (even with unsaved images), the whole form —
            // fields, image strip, Cancel — is inert; only Done stays active.
            .disabled(form.startedWithUnsavedImages != nil)
            Divider()
            HStack {
                if let caption = form.queuedCaption { Text(caption).font(.caption).foregroundStyle(.secondary) }
                Spacer()
                Button(Copy.cancel, action: onCancel).keyboardShortcut(.cancelAction)
                    .disabled(form.startedWithUnsavedImages != nil)
                if let agent = form.startedWithUnsavedImages {
                    Button(Copy.done) { onStarted(agent) }.keyboardShortcut(.defaultAction)
                } else {
                    Button(form.startLabel) {
                        Task { if let agent = await form.submit(), form.startedWithUnsavedImages == nil { onStarted(agent) } }
                    }
                    .keyboardShortcut(.defaultAction)
                    .disabled(!form.canStart)
                }
            }
            .padding(.horizontal, 22)
            .padding(.vertical, 10)
        }
        .frame(minWidth: 760, idealWidth: 820, maxWidth: .infinity,
               minHeight: 700, idealHeight: 790)
        .translucentDialogBackground()
        .background(TranslucentWindowAccessor())
        .glassButtons()
        .task { await form.load() }
    }

    private func formContents(maxRows: Int) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            if let failure = form.failure {
                Label(failure, systemImage: "exclamationmark.triangle.fill").foregroundStyle(.red)
            }
            nameField
            intentField
            reposField(maxRows: maxRows)
            AgentPickerGrid(picker: form.picker)
            VStack(alignment: .leading, spacing: 4) {
                Text(Copy.requestOptional)
                RequestEditor(text: $form.request, onImageData: { form.addImage(data: $0, name: $1) },
                             onImageURLs: { form.addImages(from: $0) })
                RequestImageStrip(form: form)
            }
            .frame(maxHeight: .infinity, alignment: .top)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .padding(.horizontal, 22)
        .padding(.vertical, 12)
    }

    private var nameField: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(Copy.name)
            TextField(Copy.nameOptionalPlaceholder, text: $form.name).textFieldStyle(.roundedBorder).labelsHidden()
            if let error = form.nameError {
                Text(error).font(.caption).foregroundStyle(.red)
            } else if !form.preview.isEmpty {
                Text(form.preview).font(.caption.monospaced()).foregroundStyle(.secondary)
            }
        }
    }

    private var intentField: some View {
        VStack(alignment: .leading, spacing: 4) {
            Picker(Copy.intent, selection: $form.intent) {
                Text(Copy.choreIntent).tag(SpikeIntent.chore)
                Text(Copy.featureSpike).tag(SpikeIntent.feature)
                Text(Copy.debugSpike).tag(SpikeIntent.debug)
            }
            .pickerStyle(.segmented)
            Text(form.intentCaption).font(.caption).foregroundStyle(.secondary)
        }
    }

    private func reposField(maxRows: Int) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(Copy.repositoriesOptional)
            Text(Copy.reposCaption).font(.caption).foregroundStyle(.secondary)
            RepoChooser(rows: form.rows, maxRows: maxRows, selection: Binding(
                get: { Set(form.selection) },
                set: { selected in form.selection = form.rows.map(\.id).filter(selected.contains) }
            ), emptyTitle: emptyRepoTitle)
            HStack {
                Button(Copy.addFolder) { chooseFolder() }
                Spacer()
                Text(form.scanLine).font(.caption).foregroundStyle(.secondary)
                Button(Copy.rescan) { Task { await form.rescan() } }.disabled(!form.connected)
            }
            if let error = form.repoError { Text(error).font(.caption).foregroundStyle(.red) }
            if let notice = form.selectionNotice { Text(notice).font(.caption).foregroundStyle(.secondary) }
            if !form.selectedLine.isEmpty { Text(form.selectedLine).font(.caption) }
        }
    }

    private var emptyRepoTitle: String {
        if form.repoError != nil { return "Repositories unavailable." }
        if form.repos.scanning { return "Scanning repositories…" }
        return "No repositories found."
    }

    private func chooseFolder() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.showsHiddenFiles = true
        guard panel.runModal() == .OK, let url = panel.url else { return }
        Task { await form.addFolder(url.path) }
    }
}

/// A native list supplies macOS selection, keyboard range selection, and VoiceOver row focus.
struct RepoChooser: View {
    let rows: [Repo]
    var maxRows = 8
    @Binding var selection: Set<String>
    var emptyTitle = "No repositories found."

    static func visibleHeight(for count: Int, maxRows: Int = 8) -> CGFloat {
        let visibleRows = count == 0 ? 4 : count
        return CGFloat(min(8, max(1, min(maxRows, visibleRows)))) * 30
    }

    var body: some View {
        List(rows, selection: $selection) { repo in
            HStack(spacing: 12) {
                Text(repo.name).lineLimit(1)
                Text(RepoPicker.subtitle(repo))
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
                Spacer(minLength: 0)
            }
            .frame(height: 30, alignment: .leading)
            .contentShape(Rectangle())
            .listRowInsets(EdgeInsets(top: 0, leading: 8, bottom: 0, trailing: 8))
            .accessibilityElement(children: .combine)
            .accessibilityLabel("\(repo.name), \(repo.path)")
            .help(repo.path)
        }
        .listStyle(.plain)
        .scrollContentBackground(.hidden)
        .background(SubtleScrollerConfig())
        .frame(height: Self.visibleHeight(for: rows.count, maxRows: maxRows))
        .overlay {
            if rows.isEmpty {
                VStack(spacing: 4) {
                    Text(emptyTitle).font(.callout)
                    Text("Add a folder or rescan.").font(.caption)
                }
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .padding(16)
            }
        }
        .overlay(RoundedRectangle(cornerRadius: 5).stroke(.separator))
        .accessibilityLabel(Copy.repositoriesOptional)
    }
}

struct RequestEditor: View {
    @Binding var text: String
    static let minimumHeight: CGFloat = 108
    var onImageData: (Data, String) -> Void = { _, _ in }
    var onImageURLs: ([URL]) -> Void = { _ in }

    var body: some View {
        // Inset inside the border: flush against it, the first line's ascenders are clipped.
        ImagePasteTextEditor(text: $text, onImageData: onImageData, onImageURLs: onImageURLs)
            .frame(minHeight: Self.minimumHeight, maxHeight: .infinity)
            .padding(.vertical, 6)
            .padding(.horizontal, 4)
            .border(.separator)
    }
}

/// Wraps `ImagePasteTextView` in a scroll view configured the way the rest of the app's text areas
/// are (overlay scroller, small, autohiding) — set directly here rather than through the
/// `SubtleScrollerConfig` sibling-view trick `TextEditor` needs, since this scroll view is ours.
private struct ImagePasteTextEditor: NSViewRepresentable {
    @Binding var text: String
    var onImageData: (Data, String) -> Void
    var onImageURLs: ([URL]) -> Void

    func makeCoordinator() -> Coordinator { Coordinator(text: $text) }

    func makeNSView(context: Context) -> NSScrollView {
        let textView = ImagePasteTextView()
        textView.delegate = context.coordinator
        textView.string = text
        textView.isEditable = true
        textView.isSelectable = true
        textView.isRichText = false
        textView.drawsBackground = false
        textView.isVerticallyResizable = true
        textView.isHorizontallyResizable = false
        textView.autoresizingMask = [.width]
        textView.textContainer?.widthTracksTextView = true
        textView.font = .systemFont(ofSize: NSFont.systemFontSize)
        textView.onImageData = onImageData
        textView.onImageURLs = onImageURLs
        textView.registerForDraggedTypes([.fileURL, .tiff, .png])

        let scrollView = NSScrollView()
        scrollView.documentView = textView
        scrollView.hasVerticalScroller = true
        scrollView.hasHorizontalScroller = false
        scrollView.drawsBackground = false
        scrollView.scrollerStyle = .overlay
        scrollView.autohidesScrollers = true
        scrollView.verticalScroller?.controlSize = .small
        return scrollView
    }

    func updateNSView(_ scrollView: NSScrollView, context: Context) {
        guard let textView = scrollView.documentView as? ImagePasteTextView else { return }
        context.coordinator.text = $text
        textView.onImageData = onImageData
        textView.onImageURLs = onImageURLs
        textView.isEditable = context.environment.isEnabled
        if textView.string != text { textView.string = text }
        scrollView.scrollerStyle = .overlay
        scrollView.autohidesScrollers = true
        scrollView.verticalScroller?.controlSize = .small
    }

    final class Coordinator: NSObject, NSTextViewDelegate {
        var text: Binding<String>
        init(text: Binding<String>) { self.text = text }
        func textDidChange(_ notification: Notification) {
            guard let textView = notification.object as? NSTextView, text.wrappedValue != textView.string else { return }
            text.wrappedValue = textView.string
        }
    }
}

/// An `NSTextView` that hands an image — pasted from the clipboard, dropped as a file, or a
/// pasted/dropped image file's URL — to the New orchestrator form instead of inserting it as text.
/// Plain text paste and drop still land in the text normally: `NSTextView` owns paste and drop
/// itself once it is first responder, so interception has to happen at this level, not with
/// `.onPasteCommand`/`.onDrop` on a SwiftUI wrapper (those never see it — verified empirically).
final class ImagePasteTextView: NSTextView {
    var onImageData: (Data, String) -> Void = { _, _ in }
    var onImageURLs: ([URL]) -> Void = { _ in }

    private static let imageTypes: [NSPasteboard.PasteboardType] = [.tiff, .png]

    override var readablePasteboardTypes: [NSPasteboard.PasteboardType] {
        Self.imageTypes + [.fileURL] + super.readablePasteboardTypes
    }

    override func readSelection(from pboard: NSPasteboard, type: NSPasteboard.PasteboardType) -> Bool {
        handle(pboard, type: type) || super.readSelection(from: pboard, type: type)
    }

    override func draggingEntered(_ sender: NSDraggingInfo) -> NSDragOperation {
        canHandle(sender.draggingPasteboard) ? .copy : super.draggingEntered(sender)
    }

    override func prepareForDragOperation(_ sender: NSDraggingInfo) -> Bool {
        canHandle(sender.draggingPasteboard) || super.prepareForDragOperation(sender)
    }

    override func performDragOperation(_ sender: NSDraggingInfo) -> Bool {
        let pboard = sender.draggingPasteboard
        if let urls = imageFileURLs(pboard) {
            onImageURLs(urls)
            return true
        }
        for type in Self.imageTypes {
            if let data = pboard.data(forType: type) {
                onImageData(data, "Dropped image.png")
                return true
            }
        }
        return super.performDragOperation(sender)
    }

    /// True (and the callback fired) when `type` is an image or an image file's URL — nothing is
    /// inserted as text in that case. False, unhandled, for anything else (plain text included).
    @discardableResult
    private func handle(_ pboard: NSPasteboard, type: NSPasteboard.PasteboardType) -> Bool {
        if Self.imageTypes.contains(type), let data = pboard.data(forType: type) {
            onImageData(data, "Pasted image.png")
            return true
        }
        if type == .fileURL, let urls = imageFileURLs(pboard) {
            onImageURLs(urls)
            return true
        }
        return false
    }

    private func canHandle(_ pboard: NSPasteboard) -> Bool {
        imageFileURLs(pboard) != nil || Self.imageTypes.contains { pboard.data(forType: $0) != nil }
    }

    private func imageFileURLs(_ pboard: NSPasteboard) -> [URL]? {
        guard let urls = pboard.readObjects(forClasses: [NSURL.self], options: [.urlReadingFileURLsOnly: true]) as? [URL] else { return nil }
        let images = urls.filter { (try? $0.resourceValues(forKeys: [.contentTypeKey]))?.contentType?.conforms(to: .image) == true }
        return images.isEmpty ? nil : images
    }
}

/// Thumbnail strip under the request editor (spec Screens): 0 images shows only the Add images…
/// button; the counter is hidden until the first image; the button disables at 10.
struct RequestImageStrip: View {
    @Bindable var form: NewOrchestratorForm

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 8) {
                if !form.images.isEmpty {
                    ScrollView(.horizontal) {
                        HStack(spacing: 8) {
                            ForEach(form.images) { image in thumbnail(image) }
                        }
                    }
                    .scrollIndicators(.never)
                    .fixedSize(horizontal: false, vertical: true)
                }
                Button(Copy.addImages) { addImages() }
                    .accessibilityLabel(Copy.addImages)
                    .disabled(form.images.count >= 10)
                if !form.images.isEmpty {
                    Text(Copy.imageCount(form.images.count)).font(.caption).foregroundStyle(.secondary)
                }
                Spacer(minLength: 0)
            }
            if let error = form.imageError { Text(error).font(.caption).foregroundStyle(.red) }
        }
    }

    private func thumbnail(_ image: RequestImage) -> some View {
        ZStack(alignment: .topTrailing) {
            Group {
                if let nsImage = NSImage(data: image.data) {
                    Image(nsImage: nsImage).resizable().aspectRatio(contentMode: .fill)
                } else {
                    Color.secondary.opacity(0.2)
                }
            }
            .frame(width: 48, height: 48)
            .clipShape(RoundedRectangle(cornerRadius: 6))
            Button { form.removeImage(image.id) } label: {
                Image(systemName: "xmark.circle.fill").font(.system(size: 14)).foregroundStyle(.white, .black.opacity(0.6))
            }
            .buttonStyle(.plain)
            .offset(x: 4, y: -4)
            .accessibilityLabel(Copy.removeImage(image.name))
            .help(image.name)
        }
        .frame(width: 48, height: 48)
    }

    private func addImages() {
        let panel = NSOpenPanel()
        panel.allowedContentTypes = [.image]
        panel.allowsMultipleSelection = true
        panel.canChooseFiles = true
        panel.canChooseDirectories = false
        guard panel.runModal() == .OK else { return }
        form.addImages(from: panel.urls)
    }
}
