# New orchestrator dialog

## Goal

Make the macOS New orchestrator window readable at a glance. All form controls and actions remain visible together. The repository chooser alone scrolls, shows each discovered main repository once, and uses native macOS row selection.

## Current behavior and cause

- `NewOrchestratorView` fixes its content to 480 pt and wraps the entire form in a `ScrollView`. A resized window leaves broad empty sides while the form stays narrow.
- `RepoPicker.sections` renders Recent, each folder or remote-owner group, and All. A repository can therefore appear several times. The view has no list height cap.
- `internal/repos/walk.go` currently accepts any folder with a `.git` directory. That shape alone is not a reliable main-repository test. The user's discovered list includes worktree-like folders, so classification belongs in backend discovery and API output. Local linked worktrees under `~/GitHub` report a Git common directory under their main checkout; the current `IsRepo` check excludes the common `.git`-file form, but does not prove that every accepted `.git` directory is a primary repository.
- The advisor uses one combined Agent · Model menu. Effort and its note take a separate row. The request editor has a 48 pt minimum.

## Layout sketch

Target window content: **820 pt wide**, **at least 790 pt high as needed for the effort controls**, **760 pt minimum width**. Use 20–24 pt side insets. The content expands with the window. Exact height can grow for validation text or a larger system text size; on a normal 14-inch Mac screen, the default form has no outer scrollbar.

```text
┌─────────────────────────────────────────────────────────────────────────────────────────┐
│ New orchestrator                                                                        │
│                                                                                         │
│ Name                                                                                    │
│ ┌─────────────────────────────────────────────────────────────────────────────────────┐ │
│ │ Name                                                                                │ │
│ └─────────────────────────────────────────────────────────────────────────────────────┘ │
│                                                                                         │
│ Intent     [ Chore            | Feature spike          | Debug spike                 ] │
│            Creates a top-level chore orchestrator for maintenance or general work.      │
│                                                                                         │
│ Repositories (optional)                                               2 selected       │
│ The spike suggests repositories and asks you to confirm them.                          │
│ ┌─────────────────────────────────────────────────────────────────────────────────────┐ │
│ │▌ agent-swarm                   ~/GitHub/agent-swarm                          ↑     │ │
│ │  coffee-hub                    ~/GitHub/coffee-hub                           │     │ │
│ │  endurio                       ~/GitHub/endurio                              │     │ │
│ │  endurio-app                   ~/GitHub/endurio-app                          │     │ │
│ │▌ endurio-chat                  ~/GitHub/endurio-chat                         │     │ │
│ │  endurio-landing               ~/GitHub/endurio-landing                      │     │ │
│ │  go-httpsig                   ~/GitHub/go-httpsig                           │     │ │
│ │  MaxiMint                     ~/GitHub/MaxiMint                             ↓     │ │
│ └─────────────────────────────────────────────────────────────────────────────────────┘ │
│ [Add folder…]                                           Scanned 5m ago  [Rescan]       │
│                                                                                         │
│ Agent      [ Claude             ▾ ]  Model  [ Claude Sonnet 5 (latest)       ▾ ]        │
│ Effort     [ Default (high)     ▾ ]                                                     │
│ Advisor    [ Codex              ▾ ]  Model  [ GPT-6 Astra                 ▾ ]        │
│ Advisor effort [ Default (medium) ▾ ]  (shown for simulated advisor pairings)            │
│                                                                                         │
│ Request (optional)                                                                      │
│ ┌─────────────────────────────────────────────────────────────────────────────────────┐ │
│ │                                                                                     │ │
│ │                                                                                     │ │
│ │                         at least five text lines                                   │ │
│ │                                                                                     │ │
│ │                                                                                     │ │
│ └─────────────────────────────────────────────────────────────────────────────────────┘ │
├─────────────────────────────────────────────────────────────────────────────────────────┤
│ Starts when an agent slot becomes available.                       [Cancel] [Queue…]  │
└─────────────────────────────────────────────────────────────────────────────────────────┘
```

Selected rows use the system selection highlight; no checkbox column. The sketch's `▌` marks a selected row, not a literal UI element. On macOS, plain click selects one row, Command-click adds or removes a row, Shift-click selects a range, and Command-A selects all available rows while the list has focus. Show a short hint below the repository heading if a first-use hint is needed: “Command-click to select multiple repositories.”

## Repository behavior

Use `ReposResponse.all` as the single source for the chooser. Show only currently present, verified primary repositories. Sort by localized name, then canonical path, and deduplicate by canonical path (case-sensitive on a case-sensitive volume; use file-resource identity when available) so distinct checkouts with the same name remain distinct. The secondary text is the shortened **full repository path**, which disambiguates same-name checkouts; remote owner and group headings are omitted. Drop search, Recent, grouping, and group All controls from this dialog. Keep Add folder and Rescan.

The backend returns all matches for an empty query with a limit of 100,000, so removing search does not truncate the list. Make backend discovery classify a path as a primary repository by Git metadata, not the existence or type of `.git` alone: resolve its Git directory and common directory (including a `.git` file or a `.git/commondir` link), verify that the path is the worktree root, and reject a linked checkout whose common directory belongs to another root. Apply the same check when returning stored rows from `/api/repos`, so an old worktree record cannot reappear before the next scan. Main repositories stay eligible even when they own linked worktrees. A separate clone with its own Git directory and common directory is a legitimate repository; a matching remote or suggestive folder name is not proof that it is a worktree. Omit missing paths from this chooser: they are not currently discovered, and their Git identity cannot be checked. If a selected row disappears after rescan, remove that ID from selection and show a concise notice.

The chooser shows `min(8, count)` rows at a stable row height of about 30–32 pt, with a subtle border and overlay scrollbar. Preserve keyboard selection and VoiceOver row names including the full path. Avoid a second nested scroll view for the editor; size it for at least five lines and allow its normal internal scrolling for longer requests.

## Agent and advisor rows

Use two aligned rows with columns: role label (about 72 pt), Agent menu (at least 150 pt), Model label (about 52 pt), Model menu (remaining width, at least 300 pt). The row width must fit inside the 760 pt minimum window width with side insets. Keep picker labels explicit for VoiceOver even if the visual text is provided by the grid.

Advisor Agent offers the enabled agents plus “No advisor.” Changing its agent picks the Settings advisor model when compatible, otherwise the first advisor-capable model from that agent's catalog. Advisor Model lists only models that can advise (preserve Claude's current advisor-only filter). When “No advisor” is selected, disable the Model menu and show an em dash. Persist `AdvisorChoice.none` or `.pair(agent, model)`.

Show a primary Effort menu whenever the chosen model supports effort. Its initial value comes from Settings, and a changed agent or model normalizes an unsupported level to that model's default. Show an Advisor Effort menu when an advisor is selected and advice runs as a separate simulated sub-agent: every pairing except an advisor-capable Claude model advising a Claude primary agent. Normalize its initial Settings value and every agent/model change against the chosen advisor model. Hide it when the advisor model offers no effort levels or “No advisor” is selected. The spike body carries the selected efforts. Native Claude-to-Claude advice uses the built-in mechanism, so the spike body omits advisor effort even if Settings holds a value; the backend also discards it for native mode from any caller. Simulated advice retains the selected advisor effort for the separately launched advisor. For a slug-encoded advisor model, the backend resolves the selected effort to its catalog launch ID before starting that separate process; flag-encoded advisors receive their usual effort flag.

## Window sizing and states

Set the window's default size high enough to show the applicable Effort rows and at least five Request lines, enforce a 760 pt minimum width, and let the view fill available horizontal space. The footer stays pinned. The normal form must fit without outer scrolling at default system text size; the repository list is capped at eight rows. If display height or accessibility text size prevents that, reduce visible repository rows before allowing a last-resort outer scroll. Do not clip validation, connection, or submission errors. Empty, scanning, and failed repository states retain a bounded chooser area so the rest of the form does not jump.

Use the existing `SubtleScrollerConfig` (`Components.swift`): overlay, auto-hiding, small scroller. Apply it to the repository list and any last-resort outer scroll. Inactive scrollbars must not reserve a wide gutter. Keep native system colors, controls, focus ring, and selection appearance.

## Evidence and acceptance

Apple's [Lists and tables guidance](https://developer.apple.com/design/human-interface-guidelines/lists-and-tables) treats row selection as a standard list interaction. Apple's [SwiftUI List documentation](https://developer.apple.com/documentation/SwiftUI/List) says keyboard-and-pointer platforms can select multiple rows without edit mode; [AppKit NSTableView](https://developer.apple.com/documentation/appkit/nstableview/allowsmultipleselection) explicitly supports multiple row selection. Apple's [focus and selection guidance](https://developer.apple.com/design/human-interface-guidelines/focus-and-selection/) supports the native selected-row highlight. Use SwiftUI `List(selection:)` if it meets the row-height and scrollbar constraints; otherwise wrap `NSTableView` with `allowsMultipleSelection` and `allowsEmptySelection`.

Accept when: (1) a normal window displays the name, intent, up to eight repos, both agent rows, applicable Effort menus, five-line request, and footer together; (2) widening the window widens the form; (3) every present main repo appears once and linked worktrees and missing paths do not; (4) Command/Shift/keyboard selection works without checkboxes; (5) changing advisor agent updates its model and effort menus and “No advisor” produces the existing no-advisor payload; (6) the native Claude-to-Claude advisor body and persisted runtime row have no advisor effort, while simulated pairings carry a selected valid effort to the separate advisor; (7) selected repo IDs survive refresh when still present; (8) focus, VoiceOver, empty, and error states remain usable.
