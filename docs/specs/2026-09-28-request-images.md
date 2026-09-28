# Spec: images on the New orchestrator request

## Context

The user wants to attach images and screenshots to the request in the menubar's
New orchestrator dialog, and have the orchestrator (and its children) see them.
Today the dialog sends `request` as plain text (`CreateSpikeBody.request`,
`apps/menubar/Sources/SwarmBarKit/Wire.swift:658`) to `POST /api/spikes`
(`internal/httpapi/spawn.go:32`). The runtime stores it as the item's `Brief` and
passes it as the kickoff `Objective` (`internal/runtime/agents.go:303`, `:397`).

Worktrees are per agent and per workflow step, and are removed when the step
finishes, so their lifetime is shorter than the orchestrator's need. Attachments
therefore live in a central store tied to the root item.

Affected repo: agent-swarm only. There is no collision with other in-flight work.

## Locked decisions

1. **Storage:** central store at `<SWARM_HOME>/attachments/<ITEM-KEY>/`, with the directory
   at mode `0700` and each file at `0600`. The directory is the source of truth: no DB table,
   no migration.
2. **Delivery:** the item brief gets an "Attachments" section listing absolute paths. The
   images are not copied into worktrees and not inlined into prompts.
3. **Cleanup:** a daemon sweep (on startup and every 10 minutes) deletes a key's folder once
   the root item is Done or Cancelled, or no longer exists. If the item is reopened after the
   sweep, its images are gone.
4. **Upload:** base64 inside the same JSON create body. One request and one `request_id`. A
   replay of an existing `request_id` returns the item and writes nothing.
5. **Limits:** at most 10 images, each at most 10 MiB decoded. The daemon accepts PNG, JPEG,
   GIF and WebP, sniffed from the bytes with `http.DetectContentType`; the client's claim is
   ignored. The client converts HEIC, TIFF and pasted clipboard images to PNG before sending.
6. **Scope:** attachments only at creation, and only from the New orchestrator dialog.
7. **Agent kinds:** all five get the paths. Claude (Read) and Codex (`view_image`) view images.
   agy, Cursor and Muse are probed during implementation. The result for each goes into the
   `attachments` package doc, and a kind that can't view images still gets the paths.

## DB models

None. Item `brief` (existing column) carries the Attachments section.

## Model / API types

HTTP (`internal/httpapi/runtime.go` `spikeRequestBody`):

```go
Attachments []attachmentBody `json:"attachments"`

type attachmentBody struct {
    Name string `json:"name"` // original file name, display only
    Data string `json:"data"` // standard base64
}
```

`createSpike` decodes this body with a 160 MiB `MaxBytesReader`. `readJSON` stays at 1 MiB
for every other route. Add `readJSONLimit(r, v, limit int64)`.

New package `internal/attachments`:

```go
const MaxCount = 10
const MaxBytes = 10 << 20

type Input struct{ Name, Data string }
type File struct {
    Name string // sanitized original name
    Ext  string // ".png" | ".jpg" | ".gif" | ".webp"
    Body []byte
}
type Saved struct{ Path, Name string } // Path absolute; Name is the original name

// Decode validates count, base64, size and sniffed type. It never touches disk.
func Decode(in []Input) ([]File, error) // error is *items.Error CodeBadRequest with the copy below
func Dir(home, key string) string       // <home>/attachments/<key>
// Save writes NN-<safe-name><ext> (NN = 01..10); a partial write removes the dir.
func Save(home, key string, files []File) ([]Saved, error)
func BriefSection(saved []Saved) string
func FailureSection(count int, err error) string
// Sweep removes <home>/attachments/<key> when the item is Done, Cancelled or missing.
func Sweep(ctx context.Context, db *sql.DB, home string) (removed []string, err error)
```

Safe name: lower-case the name. Every run of characters outside `[a-z0-9.-]` becomes `-`.
Trim `-` and `.` from both ends and cap the name at 60 runes; if nothing is left, use `image`.
The extension always comes from the sniffed type.

Runtime (`internal/runtime`, the spike input struct used by `CreateSpike`): add
`Attachments []attachments.File`. Flow in the create path:

1. The handler calls `attachments.Decode` **before** creating anything. A 400 means nothing
   was created.
2. The item is created as today. A `request_id` replay returns early and writes nothing.
3. `attachments.Save(s.Home, it.Key, files)`:
   - On success, brief = request + "\n\n" + `BriefSection`.
   - On failure, brief = request + "\n\n" + `FailureSection`, and the create response sets
     `attachments_failed: true`.
4. `s.Items.Update(ctx, it.Key, items.Patch{Brief: &brief}, …)`. The kickoff `Objective` uses
   the same combined text.

Response (`POST /api/spikes`): the existing body plus `"attachments_failed": bool`
(omitted when false). Swift `CreateSpikeResult` gains `attachmentsFailed: Bool?`.

Swift (`Wire.swift`):

```swift
public struct AttachmentPayload: Codable, Sendable, Equatable { public var name: String; public var data: String }
// CreateSpikeBody gains: public var attachments: [AttachmentPayload]? (omitted when empty)
```

`NewOrchestratorForm` gains:

```swift
public struct RequestImage: Identifiable, Equatable { public let id: UUID; public var name: String; public var data: Data; public var thumbnail: NSImage }
public private(set) var images: [RequestImage]
public private(set) var imageError: String?
public func addImages(from urls: [URL])
public func addImage(data: Data, name: String)
public func removeImage(_ id: UUID)
```

`data` holds the bytes to upload. `addImage` keeps PNG, JPEG, GIF and WebP bytes as they are (sniffed by magic bytes). Anything
`NSImage` can read is converted to PNG. Anything else, or an 11th image, or a file over 10 MiB,
sets `imageError` with the copy below.

## Screens

New orchestrator dialog, Request area (unchanged above it):

```
Request (optional)
┌──────────────────────────────────────────────────────────────┐
│ text editor (6pt vertical / 4pt horizontal inset)           │
│  ⌘V pastes an image; dropping an image file adds it         │
└──────────────────────────────────────────────────────────────┘
┌────┐ ┌────┐ ┌────┐
│ 🖼 ✕│ │ 🖼 ✕│ │ 🖼 ✕│  [Add images…]        3 of 10
└────┘ └────┘ └────┘
⚠ Screenshot 2.heic is larger than 10 MB.        (red, only when imageError)
```

- Thumbnails are 48×48pt, aspect-fill, 6pt corner radius. The ✕ is a 14pt circle at the top
  right. The tooltip shows the original name.
- The strip is a single horizontal row that scrolls when full.
- Empty state: only the `Add images…` button. The counter is hidden at 0.
- At 10 images the button is disabled.
- Not on screen: previews larger than thumbnails, reordering, captions.

## User-facing copy

Swift `Copy`:
- `addImages` = "Add images…"
- `removeImage` = "Remove %@"
- `imageCount` = "%d of 10"
- `imageTooMany` = "Up to 10 images."
- `imageTooLarge` = "%@ is larger than 10 MB."
- `imageUnsupported` = "%@ is not an image Swarm can attach."
- `imagesNotSaved` = "Started, but the images could not be saved. Share them with the orchestrator another way."

Daemon 400 messages (`bad_request`):
- "At most 10 images."
- "Image %q is larger than 10 MB."
- "Image %q is not a PNG, JPEG, GIF or WebP."
- "Image %q is not valid base64."

Brief section (`BriefSection`):

```
## Attachments

The user attached these images to the request. Open each one with your file or image tool before you plan, and pass the paths that matter to children in their briefs.

- /Users/…/.swarm/attachments/SPIKE-12/01-login-bug.png (Login bug.png)
```

`FailureSection`:

```
## Attachments

The user attached 3 images, but Swarm could not save them (<error>). Ask the user to share them another way.
```

## File list

Create:
- `internal/attachments/attachments.go`, `attachments_test.go`, `sweep_test.go`, plus a test PNG under `testdata/`.

Change:
- `internal/httpapi/runtime.go`: the body field.
- `internal/httpapi/spawn.go`: decode with the limit and call Decode.
- `internal/httpapi/server.go`: `readJSONLimit`.
- `internal/runtime/agents.go`: save, brief and objective.
- `cmd/swarm/daemon.go`: the sweep loop at startup and every 10 minutes.
- `apps/menubar/Sources/SwarmBarKit/Wire.swift`, `NewOrchestratorForm.swift`, `Copy.swift`.
- `apps/menubar/Sources/SwarmBarUI/NewOrchestratorView.swift`: the strip, paste and drop.
- Tests beside each.

Reused unchanged: `items.Store.Update`, `RenderBrief`, and the request_id replay logic.

Deleted: nothing.

## Verification

1. `go test ./internal/attachments/ ./internal/httpapi/ ./internal/runtime/`, then `go test ./...`, `go vet ./...`, `gofmt -l .`
2. `cd apps/menubar && swift test`
3. Scenarios, each a test:
   - happy path: 2 PNGs → files exist at `0600`, dir `0700`, brief lists both paths, kickoff objective includes them;
   - 11 images → 400 "At most 10 images.", no item created;
   - a text file renamed `.png` → 400 "not a PNG, JPEG, GIF or WebP", no item;
   - bad base64 → 400;
   - 10 MiB + 1 byte → 400;
   - request_id replay with images → the same item, no second write;
   - Save fails (home not writable) → item created, brief has FailureSection, `attachments_failed: true`;
   - sweep: Done, Cancelled and missing keys removed; an in-progress key kept;
   - Swift: paste PNG data → 1 image; add a HEIC URL → converted to PNG; an 11th add → `imageTooMany`; remove → gone; submit body carries base64; `attachmentsFailed` shows `imagesNotSaved`.
4. Live probe per kind (claude, codex, agy, cursor, muse) on isolated tmux sockets: an agent is
   told to open a PNG by absolute path and describe it. Record which kinds can view it.
5. Deploy: `make install-daemon`, kickstart, health, `make install-app`. Manual check: attach a
   screenshot, start, and see the orchestrator open it.

## Explicitly out of scope

- Adding images after creation (board, `swarm_send`, CLI).
- Images on items created outside the dialog.
- Copying images into worktrees or git.
- Non-image files (PDF, logs).
- Reopened items getting their images back.
