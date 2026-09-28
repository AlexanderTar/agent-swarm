# Request Images Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the user attach up to 10 images to the New orchestrator request. The daemon stores them under `<SWARM_HOME>/attachments/<ITEM-KEY>/`, lists them in the item brief, and sweeps them away when the root item closes.

**Architecture:** A new `internal/attachments` package owns decoding, validation, storage, brief text and the sweep. `POST /api/spikes` decodes images before creating anything. `Store.StartSpike` saves them after the item exists and rewrites the brief and kickoff objective. The menubar form holds images, converts non-web formats to PNG, and sends base64.

**Tech Stack:** Go 1.x stdlib (`encoding/base64`, `net/http.DetectContentType`, `os`, `path/filepath`), SQLite via `internal/db`, SwiftUI/AppKit (macOS 14).

**Spec:** `docs/specs/2026-09-28-request-images.md` (read it first; copy strings there are exact).

## Global Constraints

- At most 10 images, each at most 10 MiB (`10 << 20` bytes) decoded.
- The daemon accepts PNG, JPEG, GIF and WebP, sniffed from the bytes. The client converts everything else to PNG.
- Directory mode `0700`, file mode `0600`, path `<SWARM_HOME>/attachments/<ITEM-KEY>/NN-<safe-name><ext>`.
- A 400 from validation means no item was created.
- `readJSON` keeps its 1 MiB limit; only `createSpike` reads up to 160 MiB.
- No DB migration.
- Copy strings are verbatim from the spec.
- Worktree `../agent-swarm-request-images`, branch `feat/request-images`. Stage explicit paths; never `git add -A` or `--amend`. Never delete tests.
- No e2e run (`scripts/e2e.sh` uses a fixed port shared with other sessions).

---

### Task 1: `internal/attachments`: Decode, Save, brief text

**Files:**
- Create: `internal/attachments/attachments.go`, `internal/attachments/attachments_test.go`, `internal/attachments/testdata/one.png` (a valid 1×1 PNG)

**Interfaces:**
- Produces:
  ```go
  const MaxCount = 10
  const MaxBytes = 10 << 20
  type Input struct{ Name, Data string }
  type File struct{ Name, Ext string; Body []byte }
  type Saved struct{ Path, Name string }
  func Decode(in []Input) ([]File, error)          // *items.Error{Code: items.CodeBadRequest}
  func Dir(home, key string) string
  func Save(home, key string, files []File) ([]Saved, error)
  func BriefSection(saved []Saved) string
  func FailureSection(count int, err error) string
  func safeName(name string) string
  ```

- [ ] **Step 1: Write the failing tests**

```go
package attachments

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

func png(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/one.png")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func in(name string, b []byte) Input { return Input{Name: name, Data: base64.StdEncoding.EncodeToString(b)} }

func badRequest(t *testing.T, err error, want string) {
	t.Helper()
	var ie *items.Error
	if !errors.As(err, &ie) || ie.Code != items.CodeBadRequest || ie.Message != want {
		t.Fatalf("err = %v, want bad_request %q", err, want)
	}
}

func TestDecodeAcceptsPNGAndSniffsExtension(t *testing.T) {
	files, err := Decode([]Input{in("Login bug.JPG", png(t))})
	if err != nil || len(files) != 1 || files[0].Ext != ".png" || files[0].Name != "Login bug.JPG" {
		t.Fatalf("Decode = %+v, %v", files, err)
	}
}

func TestDecodeRefusals(t *testing.T) {
	var eleven []Input
	for range 11 {
		eleven = append(eleven, in("a.png", png(t)))
	}
	_, err := Decode(eleven)
	badRequest(t, err, "At most 10 images.")

	_, err = Decode([]Input{in("notes.png", []byte("just text, not an image"))})
	badRequest(t, err, `Image "notes.png" is not a PNG, JPEG, GIF or WebP.`)

	_, err = Decode([]Input{{Name: "x.png", Data: "%%%"}})
	badRequest(t, err, `Image "x.png" is not valid base64.`)

	big := append(png(t), make([]byte, MaxBytes)...)
	_, err = Decode([]Input{in("big.png", big)})
	badRequest(t, err, `Image "big.png" is larger than 10 MB.`)
}

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"Login bug.png":        "login-bug.png",
		"../../etc/passwd":     "etc-passwd",
		"   ":                  "image",
		strings.Repeat("a", 90): strings.Repeat("a", 60),
	} {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSaveWritesPrivateNumberedFiles(t *testing.T) {
	home := t.TempDir()
	files, _ := Decode([]Input{in("Login bug.png", png(t)), in("second.png", png(t))})
	saved, err := Save(home, "SPIKE-12", files)
	if err != nil || len(saved) != 2 {
		t.Fatalf("Save = %+v, %v", saved, err)
	}
	want := filepath.Join(home, "attachments", "SPIKE-12", "01-login-bug.png")
	if saved[0].Path != want || saved[0].Name != "Login bug.png" {
		t.Fatalf("saved[0] = %+v, want path %s", saved[0], want)
	}
	if fi, _ := os.Stat(Dir(home, "SPIKE-12")); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(saved[1].Path); fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v", fi.Mode().Perm())
	}
}

func TestSaveFailureLeavesNoDir(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "attachments"), nil, 0o600); err != nil { // a file blocks the dir
		t.Fatal(err)
	}
	files, _ := Decode([]Input{in("a.png", png(t))})
	if _, err := Save(home, "SPIKE-1", files); err == nil {
		t.Fatal("Save succeeded under a file")
	}
}

func TestBriefSections(t *testing.T) {
	got := BriefSection([]Saved{{Path: "/h/attachments/SPIKE-12/01-login-bug.png", Name: "Login bug.png"}})
	want := "## Attachments\n\nThe user attached these images to the request. Open each one with your file or image tool before you plan, and pass the paths that matter to children in their briefs.\n\n- /h/attachments/SPIKE-12/01-login-bug.png (Login bug.png)"
	if got != want {
		t.Errorf("BriefSection =\n%s\nwant\n%s", got, want)
	}
	got = FailureSection(3, errors.New("disk full"))
	want = "## Attachments\n\nThe user attached 3 images, but Swarm could not save them (disk full). Ask the user to share them another way."
	if got != want {
		t.Errorf("FailureSection = %q", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/attachments/`
Expected: FAIL (package has no non-test Go files / undefined: Decode).

Create `testdata/one.png` first:

```bash
mkdir -p internal/attachments/testdata && printf '\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82' > internal/attachments/testdata/one.png
```

- [ ] **Step 3: Implement**

```go
// Package attachments stores the images a user attaches to a New orchestrator
// request (spec docs/specs/2026-09-28-request-images.md). The directory
// <SWARM_HOME>/attachments/<ITEM-KEY>/ is the only record; Sweep removes it once
// the root item is Done, Cancelled or gone.
package attachments

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

const (
	MaxCount = 10
	MaxBytes = 10 << 20
)

type Input struct{ Name, Data string }

type File struct {
	Name string // original name, display only
	Ext  string
	Body []byte
}

type Saved struct{ Path, Name string }

var exts = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}

func bad(format string, a ...any) error {
	return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(format, a...)}
}

// Decode validates every image before anything is created; it never touches disk.
func Decode(in []Input) ([]File, error) {
	if len(in) > MaxCount {
		return nil, bad("At most 10 images.")
	}
	out := make([]File, 0, len(in))
	for _, a := range in {
		if base64.StdEncoding.DecodedLen(len(a.Data)) > MaxBytes+3 {
			return nil, bad("Image %q is larger than 10 MB.", a.Name)
		}
		body, err := base64.StdEncoding.DecodeString(a.Data)
		if err != nil {
			return nil, bad("Image %q is not valid base64.", a.Name)
		}
		if len(body) > MaxBytes {
			return nil, bad("Image %q is larger than 10 MB.", a.Name)
		}
		ext, ok := exts[http.DetectContentType(body)]
		if !ok {
			return nil, bad("Image %q is not a PNG, JPEG, GIF or WebP.", a.Name)
		}
		out = append(out, File{Name: a.Name, Ext: ext, Body: body})
	}
	return out, nil
}

func Dir(home, key string) string { return filepath.Join(home, "attachments", key) }

var unsafeRun = regexp.MustCompile(`[^a-z0-9.-]+`)

func safeName(name string) string {
	s := strings.Trim(unsafeRun.ReplaceAllString(strings.ToLower(name), "-"), "-.")
	if r := []rune(s); len(r) > 60 {
		s = string(r[:60])
	}
	if s == "" {
		return "image"
	}
	return s
}

// Save writes NN-<safe-name><ext>; any failure removes the whole directory.
func Save(home, key string, files []File) (saved []Saved, err error) {
	dir := Dir(home, key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(dir)
		}
	}()
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	for i, f := range files {
		base := strings.TrimSuffix(safeName(f.Name), filepath.Ext(safeName(f.Name)))
		if base == "" {
			base = "image"
		}
		p := filepath.Join(dir, fmt.Sprintf("%02d-%s%s", i+1, base, f.Ext))
		if err := os.WriteFile(p, f.Body, 0o600); err != nil {
			return nil, err
		}
		saved = append(saved, Saved{Path: p, Name: f.Name})
	}
	return saved, nil
}

func BriefSection(saved []Saved) string {
	var b strings.Builder
	b.WriteString("## Attachments\n\nThe user attached these images to the request. Open each one with your file or image tool before you plan, and pass the paths that matter to children in their briefs.\n")
	for _, s := range saved {
		fmt.Fprintf(&b, "\n- %s (%s)", s.Path, s.Name)
	}
	return b.String()
}

func FailureSection(count int, err error) string {
	return fmt.Sprintf("## Attachments\n\nThe user attached %d images, but Swarm could not save them (%v). Ask the user to share them another way.", count, err)
}
```

`TestSafeName` is the contract: `"Login bug.png"` → `login-bug.png`, `"../../etc/passwd"` → `etc-passwd`.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/attachments/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/attachments/attachments.go internal/attachments/attachments_test.go internal/attachments/testdata/one.png docs/plans/2026-09-28-request-images.md
git commit -m "feat(attachments): decode, store and describe request images"
```

---

### Task 2: Sweep, plus the daemon loop

**Files:**
- Modify: `internal/attachments/attachments.go`
- Create: `internal/attachments/sweep_test.go`
- Modify: `cmd/swarm/daemon.go` (loop list near line 362)

**Interfaces:**
- Consumes: `Dir` from Task 1.
- Produces: `func Sweep(ctx context.Context, db *sql.DB, home string) (removed []string, err error)`, and `func SweepLoop(ctx context.Context, db *sql.DB, home string, every time.Duration, log func(string, ...any))`, which runs Sweep immediately and then on every tick.

- [ ] **Step 1: Failing test.** Use the repo's DB test helper. Check `internal/db/dbtest` for the constructor, and `internal/items` tests for how items are inserted with a status. Create items `SPIKE-1` (done), `SPIKE-2` (cancelled) and `SPIKE-3` (in_progress), plus dirs for all three and for `SPIKE-9` (no item). After `Sweep`, only `SPIKE-3` remains and `removed` is `[SPIKE-1 SPIKE-2 SPIKE-9]` (sorted). An absent `attachments` dir returns `nil, nil`.

```go
func TestSweepRemovesClosedAndMissingOnly(t *testing.T) {
	d := dbtest.Open(t) // use the actual helper name from internal/db/dbtest
	home := t.TempDir()
	for key, status := range map[string]string{"SPIKE-1": "done", "SPIKE-2": "cancelled", "SPIKE-3": "in_progress"} {
		insertItem(t, d, key, status) // small helper in this test file: INSERT the minimum NOT NULL columns of items
	}
	for _, k := range []string{"SPIKE-1", "SPIKE-2", "SPIKE-3", "SPIKE-9"} {
		os.MkdirAll(Dir(home, k), 0o700)
	}
	removed, err := Sweep(context.Background(), d.DB, home)
	if err != nil || !slices.Equal(removed, []string{"SPIKE-1", "SPIKE-2", "SPIKE-9"}) {
		t.Fatalf("Sweep = %v, %v", removed, err)
	}
	if _, err := os.Stat(Dir(home, "SPIKE-3")); err != nil {
		t.Fatal("open item's attachments were removed")
	}
}
```

- [ ] **Step 2:** `go test ./internal/attachments/ -run Sweep`. Expect FAIL (undefined: Sweep).

- [ ] **Step 3: Implement**

```go
// Sweep removes a key's attachments once its root item is Done, Cancelled or
// gone. ponytail: reopening an item after a sweep leaves it without images.
func Sweep(ctx context.Context, db *sql.DB, home string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(home, "attachments"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var status string
		err := db.QueryRowContext(ctx, `SELECT status FROM items WHERE key = ?`, e.Name()).Scan(&status)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return removed, err
		}
		if err == nil && status != string(items.Done) && status != string(items.Cancelled) {
			continue
		}
		if err := os.RemoveAll(Dir(home, e.Name())); err != nil {
			return removed, err
		}
		removed = append(removed, e.Name())
	}
	return removed, nil
}

func SweepLoop(ctx context.Context, db *sql.DB, home string, every time.Duration, log func(string, ...any)) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := Sweep(ctx, db, home); err != nil && ctx.Err() == nil {
			log("attachments sweep: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
```

Wire it in `cmd/swarm/daemon.go`'s `loops` list, after `ReclaimWorktreesLoop`:

```go
func(ctx context.Context) { attachments.SweepLoop(ctx, dm.db.DB, cfg.Home, 10*time.Minute, cfg.Log) },
```

(Check the actual field for the `*db.DB` on `dm`: `dm.db` is used by `pruneLoop`. Pass its embedded `*sql.DB`.)

- [ ] **Step 4:** `go test ./internal/attachments/ && go build ./...` → PASS.
- [ ] **Step 5: Commit** `internal/attachments/attachments.go internal/attachments/sweep_test.go cmd/swarm/daemon.go` with the message `feat(attachments): sweep closed items' images every 10 minutes`.

---

### Task 3: API and runtime wiring

**Files:**
- Modify: `internal/httpapi/server.go` (add `readJSONLimit` next to `readJSON`, line ~309)
- Modify: `internal/httpapi/runtime.go` (`spikeRequestBody` line ~324, `spikeResponseWire` line ~243)
- Modify: `internal/httpapi/spawn.go` (`createSpike`, line 32)
- Modify: `internal/runtime/model.go` (`SpikeInput`, line 125)
- Modify: `internal/runtime/agents.go` (`StartSpike`, lines ~299-400)
- Test: `internal/httpapi/spawn_test.go` (or wherever the `createSpike` tests live; `grep -rn '/api/spikes' internal/httpapi/*_test.go`), `internal/runtime/agents_test.go`

**Interfaces:**
- Consumes: `attachments.Decode`, `Save`, `BriefSection`, `FailureSection`.
- Produces:
  - wire `attachments: [{name, data}]` in and `attachments_failed` out;
  - `runtime.SpikeInput.Attachments []attachments.File`;
  - `runtime.SpikeInput.AttachmentsFailed *bool`: an out-parameter set to true when Save failed. It avoids changing StartSpike's signature, which has 224 call sites.

- [ ] **Step 1: Failing tests.** Copy the existing happy-path `createSpike` test. Use a fake kind that passes preflight, as the neighbouring tests do.
  - (a) Post two base64 `one.png` images. Assert 200. The files `<home>/attachments/<key>/01-*.png` and `02-*.png` exist. The item brief (`GET` the item, or `s.Items.Get`) ends with the `BriefSection` text. The spawned agent's brief or kickoff objective contains the first path.
  - (b) Post 11 images. Assert 400 with message `At most 10 images.`, and the items count is unchanged.
  - (c) Post `"just text"` as base64. Assert 400 with `not a PNG, JPEG, GIF or WebP`, and no item.
  - (d) Replay the same `request_id` with images. The response is identical and there is still one directory with two files.
  - (e) Make `<home>/attachments` a regular file. Assert 200, `attachments_failed: true`, and the item brief contains `could not save them`.
  - (f) A body of 1.5 MiB with images is accepted (it proves the raised limit). A 2 MiB body to a different route (for example `POST /api/items`) is still refused, keeping readJSON's limit.
- [ ] **Step 2:** `go test ./internal/httpapi/ -run Spike`. Expect FAIL.
- [ ] **Step 3: Implement.**

`server.go`:

```go
// readJSONLimit is readJSON with a caller-chosen cap (createSpike carries images).
func readJSONLimit(r *http.Request, v any, limit int64) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, limit))
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return apiErr(http.StatusBadRequest, "bad_request", "Invalid JSON body.")
	}
	return nil
}

func readJSON(r *http.Request, v any) error { return readJSONLimit(r, v, 1<<20) }
```

`runtime.go`:

```go
type attachmentBody struct {
	Name string `json:"name"`
	Data string `json:"data"`
}
// in spikeRequestBody:
	Attachments []attachmentBody `json:"attachments"`
// in spikeResponseWire:
	AttachmentsFailed bool `json:"attachments_failed,omitempty"`
```

`spawn.go` `createSpike`:

```go
	var body spikeRequestBody
	if err := readJSONLimit(r, &body, 160<<20); err != nil {
		s.writeErr(w, err)
		return
	}
	in := make([]attachments.Input, len(body.Attachments))
	for i, a := range body.Attachments {
		in[i] = attachments.Input{Name: a.Name, Data: a.Data}
	}
	files, err := attachments.Decode(in)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.idempotent(w, r, body.RequestID, "POST /api/spikes", http.StatusOK, func(ctx context.Context) (any, error) {
		var failed bool
		key, a, queued, err := s.RT.StartSpike(ctx, runtime.SpikeInput{ /* existing fields */,
			Attachments: files, AttachmentsFailed: &failed})
		...
		return spikeResponseWire{Item: itemW, Agent: agentW, Queued: queued, AttachmentsFailed: failed}, nil
	})
```

`model.go` `SpikeInput`:

```go
	Attachments       []attachments.File
	AttachmentsFailed *bool // out: set when the images could not be saved
```

`agents.go` `StartSpike`: right after `it, err := s.Items.Create(...)` succeeds, and before the fallback and preflight code:

```go
	if len(in.Attachments) > 0 {
		section := ""
		if saved, err := attachments.Save(s.Home, it.Key, in.Attachments); err != nil {
			section = attachments.FailureSection(len(in.Attachments), err)
			if in.AttachmentsFailed != nil {
				*in.AttachmentsFailed = true
			}
		} else {
			section = attachments.BriefSection(saved)
		}
		in.Request = strings.TrimSpace(in.Request + "\n\n" + section)
		if _, err := s.Items.Update(ctx, it.Key, items.Patch{Brief: &in.Request}, items.Daemon()); err != nil {
			return "", Agent{}, false, err
		}
	}
```

`in.Request` then flows unchanged into `Brief:` (preflight-failed agent row) and `Objective:` (RenderBrief). Check with `go list -deps` that `internal/attachments` doesn't import `runtime` (it imports only `items`), so there's no cycle.

- [ ] **Step 4:** `go test ./internal/httpapi/ ./internal/runtime/ ./internal/attachments/`, then `go test ./...`. All PASS.
- [ ] **Step 5: Commit** the changed files with the message `feat(api): images on POST /api/spikes land in the item brief`.

---

### Task 4: Swift wire and form model

**Files:**
- Modify: `apps/menubar/Sources/SwarmBarKit/Wire.swift` (line ~658 `CreateSpikeBody`, ~680 `CreateSpikeResponse`)
- Modify: `apps/menubar/Sources/SwarmBarKit/NewOrchestratorForm.swift`
- Modify: `apps/menubar/Sources/SwarmBarKit/Copy.swift` (near line 98)
- Modify: `apps/menubar/Sources/SwarmBarKit/HTTPDaemonClient.swift` (`createSpike`: pass `timeout: 60`)
- Test: `apps/menubar/Tests/SwarmBarTests/NewOrchestratorFormTests.swift` (find the existing form tests file with `grep -rln 'NewOrchestratorForm(' Tests`)

**Interfaces:**
- Produces:
  ```swift
  public struct AttachmentPayload: Codable, Sendable, Equatable { public var name: String; public var data: String }
  // CreateSpikeBody: public var attachments: [AttachmentPayload]?  (nil when empty)
  // CreateSpikeResponse: public var attachmentsFailed: Bool?  (CodingKey "attachments_failed")
  public struct RequestImage: Identifiable, Equatable { public let id: UUID; public var name: String; public var data: Data }
  // NewOrchestratorForm:
  public private(set) var images: [RequestImage]
  public private(set) var imageError: String?
  public private(set) var startedWithUnsavedImages: AgentNode?
  public func addImages(from urls: [URL])
  public func addImage(data: Data, name: String)
  public func removeImage(_ id: UUID)
  static func uploadable(_ data: Data) -> Data?   // web formats as-is, else PNG via NSBitmapImageRep, nil if unreadable
  ```
  The thumbnail is derived in the view (`NSImage(data:)`), not stored, so `RequestImage` stays `Equatable`.

Copy (exact):

```swift
public static let addImages = "Add images…"
public static let removeImage = "Remove %@"
public static let imageCount = "%d of 10"
public static let imageTooMany = "Up to 10 images."
public static let imageTooLarge = "%@ is larger than 10 MB."
public static let imageUnsupported = "%@ is not an image Swarm can attach."
public static let imagesNotSaved = "Started, but the images could not be saved. Share them with the orchestrator another way."
public static let done = "Done"   // only if Copy has no `done` already
```

- [ ] **Step 1: Failing tests** (XCTest, in the existing form tests file, using its fake client):
  - `addImage(data: pngData, name: "a.png")` gives `images.count == 1` and `data == pngData` (PNG kept as is).
  - `addImage` with TIFF data from `NSBitmapImageRep(...).tiffRepresentation` gives stored data starting with the PNG magic `89 50 4E 47`.
  - `addImage(data: Data("text".utf8), name: "n.txt")` gives `imageError == "n.txt is not an image Swarm can attach."` and count 0.
  - 11 adds give `imageError == "Up to 10 images."` and count 10.
  - A PNG of 10 MiB + 1 byte gives `imageError == "big.png is larger than 10 MB."`.
  - `removeImage(images[0].id)` gives count 0 and clears `imageError`.
  - `submit()` with one image: the fake client's received body has `attachments == [AttachmentPayload(name: "a.png", data: pngData.base64EncodedString())]`.
  - Fake response `attachmentsFailed: true`: `submit()` returns the agent and `startedWithUnsavedImages != nil`.
  - An encoding test: `CreateSpikeBody` without images encodes no `attachments` key.
- [ ] **Step 2:** `cd apps/menubar && swift test --filter NewOrchestratorForm`. Expect FAIL.
- [ ] **Step 3: Implement.** Sniff the magic bytes:
  - PNG `89 50 4E 47`
  - JPEG `FF D8 FF`
  - GIF `47 49 46 38`
  - WebP `52 49 46 46 ?? ?? ?? ?? 57 45 42 50`

  Anything else: `NSBitmapImageRep(data:)?.representation(using: .png, properties: [:])`. Check the 10 MiB limit (`10 << 20`) on the uploadable bytes. `addImages(from:)` reads each URL with `Data(contentsOf:)` and uses `lastPathComponent` as the name. `body()` maps the images to `AttachmentPayload`, or nil when there are none. In `submit()`, after success: `if created.attachmentsFailed == true { startedWithUnsavedImages = created.agent }`.
- [ ] **Step 4:** `swift test` (all). Expect PASS.
- [ ] **Step 5: Commit** the Swift files with the message `feat(menubar): request images in the New orchestrator form model`.

---

### Task 5: Swift view: thumbnail strip, picker, paste and drop, warning

**Files:**
- Modify: `apps/menubar/Sources/SwarmBarUI/NewOrchestratorView.swift` (`formContents`, around lines 69-72; the Start button around line 47)
- Test: `apps/menubar/Tests/SwarmBarTests/NewOrchestratorRenderTests.swift`

**Interfaces:**
- Consumes: the form API from Task 4.
- Produces: `struct RequestImageStrip: View` (form-bound), rendered under `RequestEditor`.

- [ ] **Step 1: Failing render tests.**
  - `RequestImageStrip` with 0 images: the accessibility tree has a button labelled "Add images…" and no text "0 of 10".
  - With 3 images: 3 elements labelled "Remove a.png" (and so on), plus the text "3 of 10".
  - With 10 images: the "Add images…" button is disabled.
  - With `imageError` set: the error text is visible.
  - `NewOrchestratorView` with `startedWithUnsavedImages` set: the text `Copy.imagesNotSaved` and a "Done" button, and no Start button.

  Follow the existing render tests' style: `NSHostingView` plus an accessibility or subview walk.
- [ ] **Step 2:** `swift test --filter NewOrchestratorRender`. Expect FAIL.
- [ ] **Step 3: Implement.**
  - Strip: `ScrollView(.horizontal) { HStack(spacing: 8) { ForEach(form.images) { thumbnail } } }`.
  - Each thumbnail: 48×48, `.aspectRatio(contentMode: .fill)`, clipped with `RoundedRectangle(cornerRadius: 6)`, with a 14pt `xmark.circle.fill` button overlaid at the top right. The button has `.accessibilityLabel(String(format: Copy.removeImage, image.name))` and `.help(image.name)`.
  - Then `Button(Copy.addImages)`, which opens an `NSOpenPanel` (`allowedContentTypes: [.image]`, `allowsMultipleSelection = true`) and passes the result to `form.addImages(from:)`. It is disabled at 10 images.
  - Then `Text(String(format: Copy.imageCount, form.images.count))`, shown only when the count is above 0.
  - The error line shows `form.imageError` in `.red`.
  - Paste: on `RequestEditor`, `.onPasteCommand(of: [.image, .fileURL])`. Load each provider's data for `.image` (via `NSImage`, then TIFF data) or the URL for `.fileURL`, and call `form.addImage` / `addImages`. Plain text paste must still reach the TextEditor, so only intercept when the pasteboard has no string.
  - Drop: `.onDrop(of: [.fileURL, .image], isTargeted: nil)` on the editor.
  - Warning: when `form.startedWithUnsavedImages` is set, show `Label(Copy.imagesNotSaved, systemImage: "exclamationmark.triangle.fill").foregroundStyle(.orange)` above the form. The Start button is replaced by `Button(Copy.done) { onStarted(agent) }`. The existing Start action becomes `if let agent = await form.submit(), form.startedWithUnsavedImages == nil { onStarted(agent) }`.
- [ ] **Step 4:** `swift test` (all). Expect PASS. Also run `swift build -c release`.
- [ ] **Step 5: Commit** the view and tests with the message `feat(menubar): attach images to the New orchestrator request`.

---

### Task 6: Per-kind image probe and docs

**Files:**
- Modify: `internal/attachments/attachments.go` (package doc)

- [ ] **Step 1:** For each kind (claude, codex, agy, cursor, muse), start the CLI on an **isolated tmux socket** (`tmux -L imgprobe`), never `-L swarm`. Use a scratch cwd and the same flags Swarm's adapter passes (see each adapter's argv doc comment). Do not modify the user's real config. Prompt: `Open the image at <abs path to a PNG with the word "SWARM" on it> with your file or image tool and tell me the word in it.` Record per kind: `sees image | path only (reason)`.
- [ ] **Step 2:** Add the results table to the package doc comment. If a kind can't view images, name it and say it still gets the paths.
- [ ] **Step 3:** `gofmt -l .`, `go vet ./...`, `go test ./...`, `cd apps/menubar && swift test`. All clean.
- [ ] **Step 4: Commit** with the message `docs(attachments): record which agent kinds can view images`.

## Final (coordinator)

Opus review, then merge origin/main, push to `HEAD:main`, back up the DB, run `make install-daemon`, kickstart, check health, run `make install-app`, and remove the worktree.
