import AppKit
import SwarmBarKit
import SwiftUI

/// The popover (§16.2): 360 pt wide, fixed header and footer. Each section scrolls on its own so
/// one long list can't push the others out of view, inside a budget that leaves every open
/// section visible without scrolling the popover itself; the outer scroll is the overflow net for
/// four wide-open sections on a short screen.
public struct PopoverView: View {
    @Bindable var model: AppModel
    let openNewOrchestrator: () -> Void
    let openBoardHandoff: (String?) -> Void
    let openSettings: () -> Void

    public init(model: AppModel, openNewOrchestrator: @escaping () -> Void,
                openBoardHandoff: @escaping (String?) -> Void = { _ in },
                openSettings: @escaping () -> Void) {
        self.model = model
        self.openNewOrchestrator = openNewOrchestrator
        self.openBoardHandoff = openBoardHandoff
        self.openSettings = openSettings
    }

    /// The full usable screen height if needed, so the popover never exceeds the desktop.
    private var maxHeight: CGFloat { NSScreen.main?.visibleFrame.height ?? 800 }

    /// How the scrolling room is split between sections. Relative, not absolute: the budget is
    /// shared out among the sections that are actually open, so closing Notifications gives its
    /// room to the others instead of wasting it.
    private static let weights: [AppModel.Section: CGFloat] =
        [.needsYou: 26, .agents: 44, .notifications: 30]

    /// What's left for the scrolling lists themselves. Everything that shares the popover with
    /// them is subtracted first: the popover header and footer (120), every section's own header
    /// plus its spacing and divider (44 each — a closed section still shows its header), the body
    /// padding (24), and the banner or compact note when they're showing. Without this the shares
    /// came out of a budget that still had ~180 pt of chrome in it and Usage fell below the fold,
    /// which was the complaint this was meant to fix.
    private var capBudget: CGFloat {
        let chrome = CGFloat(AppModel.Section.allCases.count) * 44
        let banner: CGFloat = model.banner == nil ? 0 : 76
        let note: CGFloat = model.compactNote == nil ? 0 : 24
        return max(240, maxHeight - 120 - chrome - 24 - banner - note)
    }

    /// The floor keeps every open section worth opening (two usage bars, two agent rows). It can
    /// push the total just past the budget when all four are open at once, which is the one case
    /// the outer scroll view is still there for.
    private func cap(_ section: AppModel.Section) -> CGFloat {
        let open = AppModel.Section.allCases.filter(model.isOpen).reduce(0) { $0 + (Self.weights[$1] ?? 0) }
        guard open > 0 else { return 0 }
        return max(70, capBudget * (Self.weights[section] ?? 0) / open)
    }

    public var body: some View {
        VStack(spacing: 0) {
            header
            Divider()
            ScrollView {
                VStack(alignment: .leading, spacing: 10) {
                    if let banner = model.banner { DaemonBanner(text: banner) { Task { await model.retryConnection() } } }
                    if let note = model.compactNote { Text(note).font(.callout).foregroundStyle(.secondary) }
                    NeedsYouSection(model: model, cap: cap(.needsYou))
                    Divider()
                    AgentsSection(model: model, cap: cap(.agents), openBoardHandoff: openBoardHandoff)
                    Divider()
                    UsageSectionView(model: model)
                    Divider()
                    NotificationsSection(model: model, cap: cap(.notifications))
                }
                .padding(12)
            }
            .frame(maxHeight: maxHeight - 120)
            .fixedSize(horizontal: false, vertical: true)
            .scrollIndicators(.never)
            .scrollBounceBehavior(.basedOnSize)
            Divider()
            footer
        }
        .frame(width: 360)
        .controlSize(.small)
        .glassButtons()
        .onAppear { model.popoverShown() }
        .onDisappear { model.preview.cancel() }
    }

    private var header: some View {
        HStack(spacing: 6) {
            Text(Copy.appTitle).font(.headline)
            LiveBadge(connected: model.connected)
            Spacer(minLength: 4)
            if model.connected {
                Text(model.activeLine)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .monospacedDigit()
            }
            IconButton("gearshape", help: Copy.settings, action: openSettings)
            Menu {
                Button(Copy.quit) { NSApp.terminate(nil) }
            } label: {
                Image(systemName: "ellipsis")
            }
            .menuStyle(.borderlessButton)
            .menuIndicator(.hidden)
            .frame(width: 22)
            .help(Copy.more)
            .accessibilityLabel(Copy.more)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 6)
    }

    private var footer: some View {
        HStack {
            PopoverFooterSplitButton(openNewOrchestrator: openNewOrchestrator,
                                     openBoardHandoff: openBoardHandoff,
                                     connected: model.connected)
            Spacer()
            IconButton("square.grid.2x2", help: Copy.openBoard) { model.openBoard() }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
    }
}

/// The footer's split button: ONE boxed control — a "New orchestrator" label
/// segment plus a chevron menu segment sharing a single rounded container with
/// a hairline divider between them. Deliberately NOT one Menu with a
/// primaryAction: that renders a single segmented control whose chevron cell
/// drew its pressed pill ~3 pt below the label segment with the menu open
/// (screenshot 02), which no modifier on the Menu can fix. Nor a popup bezel:
/// hiding its indicator collapses it to 14 pt (misaligned), and under the
/// popover's glass buttons the bezel rasterizes as a blank white box (both
/// verified with key-window captures). So the box is drawn by the container
/// itself: both segments are borderless controls with the same fixed height,
/// and the container supplies the glass/material chrome plus the clip that
/// keeps each segment's hover/pressed tint inside its own half.
struct PopoverFooterSplitButton: View {
    let openNewOrchestrator: () -> Void
    let openBoardHandoff: (String?) -> Void
    let connected: Bool

    /// The fixed segment height. Both segments declare it (the label's content
    /// frame, the chevron popup's representable frame) and the container hugs
    /// them, so the row is 22 pt because the segments are — never the other
    /// way round. The chevron is a real NSPopUpButton sized to its 28 pt
    /// segment (a SwiftUI Menu's popup keeps its intrinsic ~14 pt and centers
    /// itself, leaving dead hit area — measured), so popup, segment, and hit
    /// area are all the same 28×22.
    static let rowHeight: CGFloat = 22
    private static let cornerRadius: CGFloat = 7
    private static let chevronWidth: CGFloat = 28

    @State private var labelHovered = false
    @State private var chevronHovered = false

    var body: some View {
        HStack(spacing: 0) {
            Button(action: openNewOrchestrator) {
                Label(Copy.newOrchestrator, systemImage: "plus")
                    .padding(.horizontal, 8)
                    .frame(height: Self.rowHeight)
                    .contentShape(Rectangle())
            }
            // Borderless, not a custom style: a custom ButtonStyle renders drawn
            // (no NSView, unmeasurable), while .borderless stays an AppKit
            // button with no bezel of its own — the container's box is the
            // only chrome, and the popover root's glass style does not
            // double-draw it. Its pressed highlight is AppKit-bounded.
            .buttonStyle(.borderless)
            .background(Color.primary.opacity(labelHovered ? 0.08 : 0),
                        in: UnevenRoundedRectangle(topLeadingRadius: Self.cornerRadius,
                                                   bottomLeadingRadius: Self.cornerRadius))
            .onHover { labelHovered = $0 }
            .help(Copy.newOrchestrator)
            HairlineDivider()
                .frame(width: 1, height: Self.rowHeight - 6)
            SplitChevronPopUp(openBoardHandoff: openBoardHandoff, enabled: connected)
                .frame(width: Self.chevronWidth, height: Self.rowHeight)
                .background(Color.primary.opacity(chevronHovered ? 0.08 : 0),
                            in: UnevenRoundedRectangle(bottomTrailingRadius: Self.cornerRadius,
                                                       topTrailingRadius: Self.cornerRadius))
                .onHover { chevronHovered = $0 }
                .help(Copy.moreStartOptions)
                .accessibilityLabel(Copy.moreStartOptions)
        }
        .background {
            SplitBoxBackground()
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        // Clip first so the glass edge/shadow drawn by the chrome keeps its
        // own shape; the segments' hover tints already carry their own
        // half-rounded shapes.
        .clipShape(RoundedRectangle(cornerRadius: Self.cornerRadius))
        .modifier(SplitBoxChrome(cornerRadius: Self.cornerRadius))
        .controlSize(.small)
        .disabled(!connected)
    }
}

/// The chevron segment: a borderless pull-down NSPopUpButton filling its
/// 28×22 slot, showing only a centered chevron glyph. A SwiftUI Menu renders
/// a ~14 pt popup centered in the slot (measured), so its top/bottom edges
/// and trailing strip hit-test as hosting-view no-ops while looking
/// clickable; this owns its whole frame, so slot, popup, and hit area match.
private struct SplitChevronPopUp: NSViewRepresentable {
    var openBoardHandoff: (String?) -> Void
    var enabled: Bool

    func makeNSView(context: Context) -> SplitChevronSlotView {
        SplitChevronSlotView(frame: NSRect(x: 0, y: 0, width: 28, height: 22),
                             onPick: openBoardHandoff, enabled: enabled)
    }

    func updateNSView(_ slot: SplitChevronSlotView, context: Context) {
        slot.button.onPick = openBoardHandoff
        slot.button.isEnabled = enabled
    }
}

/// The chevron's 28×22 layout slot. A plain view, so SwiftUI sizes it to the
/// proposed frame exactly (like SplitDividerView); the popup fills it in
/// layout, no matter what size its cell prefers — the cell sizing itself to
/// its content and centering with dead margins is the bug this replaces.
private final class SplitChevronSlotView: NSView {
    let button: SplitChevronPopUpButton

    init(frame: NSRect, onPick: ((String?) -> Void)?, enabled: Bool) {
        button = SplitChevronPopUpButton(frame: NSRect(origin: .zero, size: frame.size), onPick: onPick)
        button.controlSize = .small
        button.isEnabled = enabled
        super.init(frame: frame)
        addSubview(button)
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { nil }

    override func layout() {
        super.layout()
        button.frame = bounds
    }

    override var intrinsicContentSize: NSSize { NSSize(width: 28, height: 22) }
}

/// The split control's chevron popup. Internal (not private) so the layout
/// test can find it by class; cf. TranslucentWindowAccessor.AccessorView.
final class SplitChevronPopUpButton: NSPopUpButton {
    var onPick: ((String?) -> Void)?

    init(frame: NSRect, onPick: ((String?) -> Void)?) {
        self.onPick = onPick
        super.init(frame: frame, pullsDown: true)
        isBordered = false
        setAccessibilityLabel(Copy.moreStartOptions)
        contentTintColor = .labelColor
        let menu = NSMenu()
        // Index 0 is the pull-down title item: AppKit uses it as the button
        // face and hides it in the opened menu, so it carries only the
        // chevron glyph. With a single item the menu opened empty and the
        // action was unreachable (measured: item isHidden, no menu window).
        let titleItem = NSMenuItem(title: "", action: nil, keyEquivalent: "")
        titleItem.image = SplitChevronPopUpButton.chevronImage()
        menu.addItem(titleItem)
        let item = NSMenuItem(title: Copy.orchestrateBoardItemMenu,
                              action: #selector(fire), keyEquivalent: "")
        item.target = self
        menu.addItem(item)
        self.menu = menu
        showChevron()
    }

    /// The chevron glyph at label weight: the default symbol config renders
    /// larger and brighter than the small label beside it (measured in the
    /// after-shot), so pin point size/weight and let contentTintColor match
    /// the label's tint, disabled dimming included.
    static func chevronImage() -> NSImage? {
        let config = NSImage.SymbolConfiguration(pointSize: 11, weight: .semibold)
        return NSImage(systemSymbolName: "chevron.down", accessibilityDescription: nil)?
            .withSymbolConfiguration(config)
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { nil }

    /// Re-apply after each item sync so the button keeps showing the glyph.
    override func synchronizeTitleAndSelectedItem() {
        super.synchronizeTitleAndSelectedItem()
        showChevron()
    }

    private func showChevron() {
        imagePosition = .imageOnly
        image = itemArray.first?.image
        (cell as? NSPopUpButtonCell)?.arrowPosition = .noArrow
    }

    @objc private func fire() { onPick?(nil) }
}

/// The split control's box: Liquid Glass shaped to its own rounded rect on
/// macOS 26, the popover-weight regular material with a separator stroke below.
/// Never an opaque fill, so the popover material shows through.
private struct SplitBoxChrome: ViewModifier {
    var cornerRadius: CGFloat

    func body(content: Content) -> some View {
        if #available(macOS 26, *) {
            content.glassEffect(.regular, in: RoundedRectangle(cornerRadius: cornerRadius))
        } else {
            content
                .background(.regularMaterial, in: RoundedRectangle(cornerRadius: cornerRadius))
                .overlay(RoundedRectangle(cornerRadius: cornerRadius)
                    .strokeBorder(Color(nsColor: .separatorColor)))
        }
    }
}

/// The 1 pt vertical hairline between the split segments. A hand-drawn NSView
/// rather than an NSBox separator: a separator box overrides the representable
/// sizing to a 1×5 box with a 1×1 line (measured), while a plain view honors
/// the frame exactly. A real NSView either way, so the layout test can measure
/// it instead of eyeballing.
private struct HairlineDivider: NSViewRepresentable {
    func makeNSView(context: Context) -> SplitDividerView { SplitDividerView(frame: .zero) }

    func updateNSView(_ view: SplitDividerView, context: Context) {}
}

/// The split control's shared container marker: a pass-through background view
/// filling the HStack, so the layout test can measure the one box both
/// segments sit in. Pure SwiftUI chrome (glass/material) is never measurable —
/// deleting it leaves layout tests green — so this view is the structural
/// assertion, and it must only exist as this control's background.
private struct SplitBoxBackground: NSViewRepresentable {
    func makeNSView(context: Context) -> SplitBoxContainerView { SplitBoxContainerView(frame: .zero) }

    func updateNSView(_ view: SplitBoxContainerView, context: Context) {}
}

/// The split control's container. Internal (not private) so the layout test
/// can find it by class; cf. TranslucentWindowAccessor.AccessorView.
/// Pass-through for clicks: the segments beneath keep their own hit areas.
final class SplitBoxContainerView: NSView {
    override func hitTest(_ point: NSPoint) -> NSView? { nil }
}

/// The split control's divider. Internal (not private) so the layout test can
/// find it by class; cf. TranslucentWindowAccessor.AccessorView.
final class SplitDividerView: NSView {
    override func draw(_ dirtyRect: NSRect) {
        // Tertiary label, not separatorColor: the separator sat at 35,35,35
        // on a 33,36,35 box in the after-shot — invisible inside the
        // glass/material chrome. Tertiary label is drawn for legibility on
        // materials while staying a hairline.
        NSColor.tertiaryLabelColor.setFill()
        NSRect(x: bounds.midX - 0.5, y: 0, width: 1, height: bounds.height).fill()
    }
}

/// "● Live" next to the app title: green while the daemon stream is up, red when it isn't.
struct LiveBadge: View {
    let connected: Bool

    var body: some View {
        HStack(spacing: 3) {
            StateDot(connected ? .green : .red)
            Text(connected ? Copy.live : Copy.offline)
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .help(connected ? Copy.live : Copy.offline)
        .accessibilityElement(children: .combine)
        .accessibilityLabel(connected ? Copy.live : Copy.offline)
    }
}

struct DaemonBanner: View {
    let text: String
    let retry: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Label(text, systemImage: "exclamationmark.triangle.fill")
                .foregroundStyle(.orange)
                .fixedSize(horizontal: false, vertical: true)
            HStack {
                Spacer()
                Button(Copy.retryConnection, action: retry)
            }
        }
        .padding(8)
        .background(Color.orange.opacity(0.12), in: RoundedRectangle(cornerRadius: 6))
    }
}
