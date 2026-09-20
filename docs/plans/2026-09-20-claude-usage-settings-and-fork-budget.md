# Implementation Plan: Claude Usage, Settings, Fork Budget, and Generalist Verification

## Overview
This plan implements the four fixes in `docs/specs/2026-09-20-claude-usage-settings-and-fork-budget.md` in strict TDD order:
1. Claude Menubar Usage Indicator (`five_hour` priority).
2. Settings Model, Wire, and UI (`maxConcurrentSubagents` + UI revert fix).
3. Fork Budget Gate in Hook Handler (`Agent`, `Fork`, `claude` bash calls).
4. Generalist Checkpoint Verification Gate (`verifyOK` replacing `tddOK`).

---

## Phase 1: Fix Claude Menubar Usage Indicator

### Step 1.1: Test for 5h Priority
**File**: `internal/usage/claude_test.go`
Add a unit test ensuring that when `anthropic-ratelimit-unified-representative-claim` is `seven_day`, `HeadlineID` remains `"five_hour"` when `"five_hour"` meter exists.

Run:
```bash
go test -v -run TestClaudeHeadlinePrioritizesFiveHour ./internal/usage/...
```
Verify failure.

### Step 1.2: Implement 5h Priority in `internal/usage/claude.go`
In `claudeSnapshotFromHeaders(h http.Header)`:
```go
hasFiveHour := false
for _, m := range meters {
    if m.ID == "five_hour" {
        hasFiveHour = true
        break
    }
}
if hasFiveHour {
    headline = "five_hour"
} else if headline != "five_hour" && headline != "seven_day" && len(meters) > 0 {
    headline = meters[0].ID
}
```
Run:
```bash
go test -v ./internal/usage/...
```
Verify pass.

---

## Phase 2: Fix Settings Wire, Model, and UI

### Step 2.1: Add `maxConcurrentSubagents` to Wire Model
**File**: `apps/menubar/Sources/SwarmBarKit/Wire.swift`
In `public struct Settings`:
```swift
public var maxConcurrentSubagents: Int = 3
```
In `CodingKeys`:
```swift
case maxConcurrentSubagents = "max_concurrent_subagents"
```
In `public static let defaults`:
```swift
maxConcurrentSubagents: 3
```

### Step 2.2: Simplify Limit Enum and UI Field
**Files**:
- `apps/menubar/Sources/SwarmBarKit/SettingsModel.swift`:
  Simplify `Limit` enum to only `.subagents` and `.pauseDeadline`:
  ```swift
  public enum Limit: CaseIterable, Sendable {
      case subagents, pauseDeadline

      public var range: ClosedRange<Int> {
          switch self {
          case .subagents: return 1...16
          case .pauseDeadline: return 30...600
          }
      }
  }
  ```
  Map `.subagents` to `settings.maxConcurrentSubagents` in `value` and `store`.
  Update `overLimit` to count active subagents under any parent against `.subagents`.
- `apps/menubar/Sources/SwarmBarUI/SettingsView.swift`:
  In `LimitsTab`, display only one subagent limit field:
  ```swift
  LimitField(model: model, title: Copy.maxSubagents, limit: .subagents)
  Text(Copy.subagentsLimitCaption).font(.caption).foregroundStyle(.secondary)
  ```
  In `LimitField.commit()`, do not immediately reset `text` if `model.value(limit)` has not yet updated.
- `apps/menubar/Sources/SwarmBarKit/Copy.swift`:
  Add `public static let maxSubagents = "Max concurrent subagents"`
  Add `public static let subagentsLimitCaption = "Maximum subagents each parent agent may run concurrently."`

### Step 2.3: Verification
```bash
cd apps/menubar && swift test
```
Verify all Swift tests pass.

---

## Phase 3: Disable Native Forks and Subagents Completely in Hooks

### Step 3.1: Add Tests for Disabling Native Forks and Subagents
**File**: `internal/hook/handler_test.go`
Add tests in `handler_test.go`:
- Test that `in.ToolName == "Agent"` is blocked unconditionally in `PreToolUse`.
- Test that `in.ToolName == "Task"` is blocked unconditionally in `PreToolUse`.
- Test that `in.ToolName == "Fork"` and `"fork"` are blocked unconditionally in `PreToolUse`.
- Test that `in.ToolName == "invoke_subagent"` is blocked unconditionally in `PreToolUse`.
- Test that `in.Command` running `claude ...` (or `claude -p`, `claude --fork`) in `Bash` is blocked unconditionally.
- Test that `SessionStart` with `in.Source == "fork"` is blocked.
- Test that `swarm_spawn` continues to work (subject to `max_concurrent_subagents`).

Run:
```bash
go test -v -run "Test.*Fork.*|Test.*NativeSubagent.*" ./internal/hook/...
```
Verify failure.

### Step 3.2: Implement Fork & Native Subagent Blocking in `internal/hook/handler.go`
1. In `case "PreToolUse":`:
   ```go
   isNativeFork := in.ToolName == "Agent" ||
       in.ToolName == "Task" ||
       in.ToolName == "Fork" ||
       in.ToolName == "fork" ||
       in.ToolName == "invoke_subagent" ||
       in.ToolName == "subagent" ||
       in.ToolName == "dispatch_agent" ||
       in.ToolName == "spawn_agent"

   if isNativeFork {
       return adapter.HookDecision{
           Block:  true,
           Reason: "[swarm] Native forks and subagents are disabled. Use swarm_spawn to delegate work to Swarm-managed agents, or execute tasks sequentially in this session.",
       }, nil
   }

   if in.Command != "" && isClaudeCLI(in.Command) {
       return adapter.HookDecision{
           Block:  true,
           Reason: "[swarm] Nested agent invocations via shell are disabled. Use swarm_spawn to delegate work.",
       }, nil
   }
   ```
2. In `case "SessionStart":`:
   ```go
   if in.Source == "fork" {
       return adapter.HookDecision{
           Block:  true,
           Reason: "[swarm] Forked sessions are disabled. Work must run within the assigned Swarm session.",
       }, nil
   }
   ```
3. For `swarm_spawn`, enforce `max_concurrent_subagents` budget against DB-tracked child agents.

Run:
```bash
go test -v ./internal/hook/...
```
Verify pass.

---

## Phase 4: Generalist Verification Gate

### Step 4.1: Write Tests for Generalist Verification
**File**: `internal/runtime/checkpoint_test.go`
Update `TestTDDGate` (renaming to `TestVerificationGate`):
- Verification with red-only test run (`Phase: "red", OK: false`): passes!
- Verification with green test run (`Phase: "green", OK: true`): passes!
- Verification with build command (`Cmd: "make build", OK: true`): passes!
- No verification entries (`nil` or `[]`): fails with `verifyMissing`!

Run:
```bash
go test -v -run TestVerificationGate ./internal/runtime/...
```
Verify failure.

### Step 4.2: Implement `verifyOK` in `internal/runtime/checkpoint.go`
1. Replace `tddMissing` with:
   ```go
   const verifyMissing = "Verification evidence missing: record what was run to verify this work before completing."
   ```
2. Replace `tddOK` with:
   ```go
   func verifyOK(prior, now []Verify) bool {
       all := append(append([]Verify{}, prior...), now...)
       if len(all) == 0 {
           return false
       }
       for _, v := range all {
           if strings.TrimSpace(v.Cmd) != "" {
               return true
           }
       }
       return false
   }
   ```
3. In `WriteCheckpoint`:
   ```go
   if in.Kind == CompletedCkp && it.TddExempt == "" {
       gated := slices.Contains(gatedRoles, a.Role)
       if !gated && a.Role == RoleOrchestrator && s.changedFiles(ctx, in.Git) > 0 {
           gated = true
       }
       if gated {
           prior, err := s.priorVerify(ctx, tx, a.ID, ses.Attempt)
           if err != nil {
               return err
           }
           if !verifyOK(prior, in.Verification) {
               return errors.New(verifyMissing)
           }
       }
   }
   ```

Run:
```bash
go test -v ./internal/runtime/...
```
Verify all runtime tests pass.

---

## Phase 5: End-to-End Verification
1. Run entire Go test suite:
   ```bash
   go test ./...
   ```
2. Run entire Swift test suite:
   ```bash
   cd apps/menubar && swift test
   ```
3. Commit and prepare merge.
