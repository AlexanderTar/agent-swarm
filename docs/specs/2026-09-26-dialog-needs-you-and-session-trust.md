# Spec: surface dialog-blocked panes in Needs you, and pre-trust every session's workspace

## Context

**Incident.** Three Claude worker panes sat for 2-5 days on Claude's
folder-trust dialog ("Accessing workspace: ~/.swarm/work/<agent> … Is this a
project you created or one you trust? ❯ No, exit / Yes, I trust this
folder"): `s4-r5-g-vectors` (2026-09-21), `green-merge-11` (09-22) and
`full-go-api-migration-orchestrator-6` (09-24). Two more hit it too:
`independent-review-task-reviewer-7` (09-23) and
`s13-2b-eval-date-fix-reviewer` (09-24). That is 5 of about 350 Claude spawns.
Nobody noticed, because nothing reached Needs you.

**Chain, with evidence (`main` @ 3aa22c7):**

1. **The dialog wording matched.** `claudeTrust`/`claudeTrustYes`
   (`internal/adapter/claude.go:213-214`) match the `sessions.failure_text`
   saved for all three.
2. **The keys were sent once and never again.** `watchStartup`
   (`internal/runtime/agents.go:1270-1284`) sent `Down Enter` once and set
   `answered[i] = true`.
3. **The keys never landed.** The saved pane still shows `❯ No, exit` selected.
   The daemon logs neither the key send nor the failure (zero lines for these
   agents in `~/.swarm/logs/daemon.err.log`). That leaves two candidate causes:
   - a copy-mode swallow, which b02463f/8dad80b fixed on 09-26, after all five
     incidents;
   - keys arriving before the TUI read input.

   Either way, nothing retried.
4. **The session failed but the pane stayed.** The capture didn't change for
   30 s, so `failSession` (`agents.go:1173`) marked the session `failed` at
   start + 31 s in all three cases.
5. **The notification was unreadable.** `failSession` raised
   `agent.preflight_failed` with `reason` = the first non-blank *raw* line,
   an ANSI-colored `────` rule. The notification body was that rule; it was
   read and dismissed.
6. **Nothing ever killed the pane.**
   - The reconcile reaper deliberately leaves live panes of
     failed/crashed/cancelled sessions running (`reconcile.go:186-201`,
     P0-crash-1 "let a human inspect it").
   - The parent's `swarm_control cancel` then marked the agent `finished`, but
     `Cancel` only kills *live* sessions (`agents.go:1377`).
   - The user's `Ack` never kills (`agents.go:1617`).
7. **No request row existed, and a failed session couldn't hold one.**
   `withdrawOrphanedRequests` (`reconcile.go:238`) closes any HITL row whose
   newest session is terminal.

**Related gaps:**

- **Raw-capture matching in reconcile.** Its prompt auto-answer
  (`reconcile.go:822-834`) matches `PromptPatterns` against the *raw* `-e`
  capture (`spawn/tmux.go:174`). `watchStartup` strips ANSI first, and the
  `stripANSI` comment (`agents.go:1219`) shows per-word escapes break
  multi-word matches.
- **Answered-once flag.** `markPromptAnswered` (`reconcile.go:750`) is
  once-only per session and title.
- **codex 0.157.0 renamed its trust dialog.** The new wording is "Trust this
  folder?" (probe C2). `codexTrust` still matches only "Do you trust the
  contents of this directory?", so codex's auto-answer no longer fires.
- **codex trust is written to the wrong file.** `Codex.TrustFolder` writes
  `~/.codex/config.toml`, but spawned codex runs with a per-launch
  `CODEX_HOME` (`codex.go:53`) that has no `config.toml`. The write is dead
  for every spawned session.

**The user's rule:** Needs you shows *anything that requires the user's input*.
A pane blocked on a startup, trust or permission dialog that the daemon
didn't clear is exactly that.

**Affected repo:** agent-swarm only. Worktree: `../agent-swarm-dialog-needs-you`,
branch `feat/dialog-needs-you` from `origin/main` 3aa22c7.

**Collision warnings:**
- `internal/runtime/reconcile.go` and `agents.go` are hot files; rebase before
  each batch.
- The two untracked 2026-09-21 claude-idle-wake specs in the primary checkout
  also touch reconcile's idle path. Don't merge their edits into this branch.

## Locked decisions

User decisions D1-D8 below were locked 2026-09-26, after Q1 and Q2 (batch 1
review). They amend decision 1 and resolve Q1; nothing here reopens them.

1. **Trust is injected per session, for every agent kind.**
   - There is no parent-dir or global trust grant: no `~/.swarm/work` entry, no
     home-dir entry.
   - Each launch starts with its own workspace (`Spec.Cwd`, the neutral work
     dir or a git worktree) already trusted.
   - **D1 (claude, resolves Q1):** per-session write into the REAL
     `~/.claude.json`: `projects[<exact abs workspace path>]` (plus its
     `filepath.EvalSymlinks` realpath if different) `.hasTrustDialogAccepted =
     true`, written just before launch, under Claude's own `<file>.lock`
     mkdir lock (verified live, Task 14a), atomic write (temp file in the
     same dir + rename, preserving the existing file's mode), touching no
     other key. No global or parent (`~/.swarm/work`) grant. The isolated
     `CLAUDE_CONFIG_DIR` approach (P-C2) stays rejected: login copy/rotation
     risk, transcripts move, and it adds nothing once `--setting-sources
     project,local` is already in play.
   - **D5 (codex):** per-launch trust entry in `$CODEX_HOME/config.toml`,
     where `CODEX_HOME` is `internal/adapter.CodexHomeDir(home, agentID)`
     (agent-keyed since `main@8a610d7`, not session-keyed) -- the `-c`
     override does not work (P-X2). The entry must survive `Resume` (same
     `CODEX_HOME`, so a merge-not-replace write, like `writeCodexTrust`
     already does). codex 0.157's new "Trust this folder?" dialog (distinct
     from the older "Do you trust the contents of this directory?", still
     current in some installs) is added to `StartupDialogs`/`PromptPatterns`;
     the existing `codexTrust` pattern's auto-answer never matches it.
   - **D6 (agy):** per-launch `settings.json` copy with the session's
     workspace added to `trustedWorkspaces`; every other entry of the real
     `antigravity-cli/` dir stays symlinked (so login/auth is kept).
   - **D7 (cursor, muse):** `cursor.go`'s `--trust --workspace <cwd>` and
     `muse.go`'s `--yolo --trust-workspace` already avoid the dialog and are
     unchanged. A regression test asserts both flags on every `Launch` and
     `Resume`. Detect-only dialog patterns are added so an unexpected dialog
     (a flag silently stops working, or a future version adds a new one)
     still surfaces as a Needs-you row, batch 1's escalation machinery.
2. **`failSession` kills the pane.** `failure_text` keeps the last 40 lines
   (unchanged).
3. **`CLAUDE_CODE_SANDBOXED` is not used.** No other undocumented trust-skip
   env var (`IS_DEMO`, `CLAUBBIT`, `CLAUDE_BG_WORKSPACE_TRUSTED`) is used
   either.
4. **codex, agy, cursor and muse must also be free of workspace-trust
   dialogs.**
5. **The Needs-you row reuses request kind `prompt`.**
   - No schema change, no new kind, no UI change.
   - web `logic/inbox.ts:10` and menubar `AppModel.swift:47` already treat
     `prompt` like question/blocker.
   - Its row is the generic row (spec 2.2.2 of
     `2026-09-25-needs-you-and-child-approval-routing.md`), and a click opens
     the agent's terminal (`requestTarget`).
6. **The row resolves automatically, "answered via terminal".** It closes when
   the dialog leaves the pane. There are no answer buttons.
7. **Only live sessions are eligible** (`spawning` or `running`). Panes of
   finished or acknowledged agents are killed, not surfaced.
8. **D2 (cleanup):** the per-session Claude trust entry (D1) is removed once
   that session's agent is deleted or reclaimed. (As built: this codebase
   has no hard-delete path for `agents`/`sessions` rows at all -- confirmed,
   `grep -n "DELETE FROM agents\|DELETE FROM sessions"` matches nothing --
   so "deleted" never applies today; "reclaimed" means `state IN
   ('finished', 'acknowledged')`, checked by `forgetFinishedClaudeTrust`
   every reconcile tick, rate-limited per session id so an already-handled
   session is never re-checked.) Only entries whose path is
   Swarm-owned are ever touched -- under `<swarm home>/work` or `<swarm
   home>/worktrees` (`filepath.Clean` + prefix match against both roots,
   checked against both the literal and realpath keys D1 may have written.
   The user's own projects (any other path in `~/.claude.json`) are never
   read, matched, or removed. Reuses the existing, currently-uncalled
   `Adapter.ForgetFolder(ctx, path)`; `Claude.ForgetFolder` is implemented for
   the first time to delete both keys via the same lock/atomic-write
   protocol as D1's write. Idempotent: a path with no entry is a no-op, and
   the reclaim sweep may call it more than once for the same agent.
9. **D3 (swarm install):** verifies `~/.claude.json` exists and is writable,
   and that the installed Claude version is one where the trust key was
   verified live (2.1.283 or later; an older or undetectable version gets a
   warning, not a failure -- install never blocks on this). It also prunes
   stale Swarm-owned entries: any `projects[...]` key that is Swarm-owned
   (D2's predicate) and no longer exists on disk. Never adds a
   parent/global grant.
   - **Implementation note (as built, 2026-09-26):** `swarm install` is a
     standalone CLI command with no DB handle, same as D4's doctor check
     below, so "no longer exists on disk" is the only staleness test
     implemented -- a plain `<home>/work/<n>` directory is never deleted by
     anything in this codebase today (confirmed: only `MkdirAll` sites for
     that path), so in practice this prune only ever fires for a
     `git worktree remove`d worktree. A work-dir's trust entry is instead
     cleaned up by D2's reconcile-time hook the moment its owning agent
     finishes, which needs no directory deletion to trigger. An earlier
     draft of this decision additionally described pruning entries
     "belonging to a terminal-session work dir" as an install-time DB-based
     check; that was never implemented (same no-DB reason as D4's deferred
     WARN) and is removed here rather than left to describe code that does
     not exist.
10. **D4 (swarm doctor):** a new "Claude trust" check:
    - **FAIL** when `~/.claude.json` doesn't exist or isn't writable, or the
      installed Claude version is untested (older than 2.1.283, or the
      version can't be determined).
    - **WARN**, with a count, when stale Swarm-owned entries exist (D3's same
      predicate; doctor never deletes, only reports -- `swarm install`
      prunes).
    - **WARN** when a live Claude session's workspace (a `running` or
      `spawning` session whose agent kind is Claude) has no matching
      `projects[...]` entry at all (D1's write may have failed, or raced a
      concurrent Claude rewrite).
    - **PASS** otherwise, with the count of Swarm-owned entries.
    - **Implementation note (flagged, not decided here):** `swarm doctor` is
      a standalone CLI command with no DB handle (`cmd/swarm/commands.go`
      builds `install.Doctor` from `Config` + `execx.Runner` only); the
      live-session WARN needs either a new daemon HTTP endpoint or a direct
      read-only DB open from the `install` package. Everything else in D4 is
      implemented; this one WARN is deliberately left out pending that
      choice, rather than picked unilaterally.
11. **D8 (codex hardening, from batch-1 review):**
    - `setupEnv` calls `os.Chtimes(codexHome, now, now)` right after
      `os.MkdirAll` succeeds (best-effort; an error is logged, not returned).
      This closes the sweep race window `reclaimCodexHomes`'s `snapshotAt`
      guard already narrows: without it, a `CODEX_HOME` created a long time
      ago (first spawn) and only *resumed* now keeps its original mtime
      forever, so a slow DB snapshot race is still theoretically possible on
      a very old, frequently-resumed agent. Refreshing the mtime on every
      `setupEnv` call (including Resume) makes "this dir was touched after
      `snapshotAt`" true for any agent actually in use right now.
    - `reclaimOldCodexLaunchHomes` becomes a true one-time job per daemon
      run: a `sync.Once` field on `Store`, not a per-`Reconcile`-tick call.
      It already self-limits in effect (nothing is left to remove after the
      first successful pass), but it still pays a DB query and an
      `os.ReadDir` every 5 s tick forever; `sync.Once` removes that cost.

## Trust mechanism per kind (probe evidence)

**Probe setup:**
- Scratch dirs under the session scratchpad, isolated tmux socket
  `-L dialogprobe`, never `-L swarm`.
- `work-*` are plain dirs. `wt-a` is a `git worktree add` of scratch repo
  `repo`.
- Launches carried no prompt, so no model calls.
- Transcripts are summarised here; `P-*` ids are reused in the plan's probe
  fixtures.

| Kind | Version | Mechanism (chosen) | Evidence | Can the dialog still appear? |
|---|---|---|---|---|
| claude | 2.1.283 | Before launch, write `projects[<cwd>]` and `projects[<realpath cwd>]` = `{hasTrustDialogAccepted: true}` (merged into any existing entry) into the user's `~/.claude.json`. This is the same write Claude makes when the user picks "Yes, I trust this folder". It happens under `~/.claude.json.lock`. **D1: confirmed live, P-C5.** | **P-C1:** `--dangerously-skip-permissions` alone still shows the dialog. **P-C2:** a per-launch `CLAUDE_CONFIG_DIR` suppresses trust but brings up the first-run theme picker and, once `.claude.json` is copied in, the "Bypass Permissions mode" accept dialog. It also moves settings, skills, transcripts (read by `advisor/transcript.go:341`) and the keychain credential slot, so it is rejected. **P-C3:** an exact-path entry in the config Claude reads suppresses the dialog for both `work-a` and worktree `wt-a`. This ran against a scratch-`HOME` *copy* of `~/.claude.json`, never the real file, so the mechanism is proven but the real-file write is not. **P-C4 (negative):** same config, a path not listed, and the dialog appears. There is no CLI flag (`claude --help`: only `-p` skips trust) and no settings key: Claude reads trust only from `projects[...]` in the global config (`DTe`/`oS`/`iS` in the binary). **P-C5 (Task 14a, live, 2026-09-26):** a scratch `HOME` (`tmux -L dialogprobe`, never `-L swarm`) seeded with a *copy* (never the real file) of the real `~/.claude.json` reaches the actual "Is this a project you created or one you trust?" dialog for a fresh scratch workspace. Accepting it: (1) creates `<HOME>/.claude.json.lock` as a **directory** (`mkdir` lock, confirming the spec's assumption -- no separate file-based lock library), held only for the instant of the write, then removed; (2) writes `projects["<abs scratch work path>"] = {..., "hasTrustDialogAccepted": true, ...}` alongside Claude's other own per-project fields (`allowedTools`, `mcpServers`, etc.) -- confirming the exact key name on 2.1.283, the version actually installed here. | Yes, if Claude changes its trust key or path normalisation, or if the entry is lost to a concurrent rewrite. The retry and the Needs-you row cover this. |
| codex | 0.157.0 | Per-launch `$CODEX_HOME/config.toml`, where `$CODEX_HOME` is `internal/adapter.CodexHomeDir(home, agentID)` (agent-keyed, `<home>/cx/<hash(agentID)>`, `main@8a610d7`) with `[projects."<cwd>"] trust_level = "trusted"`, plus the realpath if it differs. A merge, not a replace, so it survives `Resume` against the same `CODEX_HOME`. | **P-X1:** a fresh `CODEX_HOME` with `--dangerously-bypass-approvals-and-sandbox` shows "Folder access … Trust this folder?" for both `cx-work` and `wt-a`. **P-X2:** `-c 'projects."<path>".trust_level="trusted"'` does **not** suppress it, for either the worktree path or the repo root. **P-X3:** the per-launch `config.toml` entry suppresses it for `cx-work`, and for `wt-a` keyed by either the worktree path or the repo root. | Yes, on a version change. The dialog pattern "Trust this folder\?" (added here) with auto-answer `Enter` (option 1 is preselected), plus the retry and the row, cover it. |
| agy | 1.2.11 | Per-launch `agy-home/.gemini/antigravity-cli/` becomes a real directory. Every entry of the real `~/.gemini/antigravity-cli/` is symlinked except `settings.json`, which is a per-launch copy with `trustedWorkspaces` ∪ {`<cwd>`}. Today the whole directory is a single symlink (`agy.go:54-63`). | **P-A1:** baseline outside home shows "Do you trust the contents of this project?". **P-A2:** the per-launch layout suppresses it for `agy-work` and `wt-a`, and the account still shows (auth intact). **P-A3 (negative):** an unlisted `work-b` shows the dialog. `agy --help` has no trust flag. | Yes, on a version change. The existing `agyTrust` pattern, plus the retry and the row, cover it. |
| cursor | 2026.09.26 | Already done: `--trust --workspace <cwd>` (`cursor.go:39-40`). No change. | **P-U1:** `--yolo --trust --approve-mcps` shows no dialog in fresh `cur-work` or in `wt-a`. **P-U2 (negative):** a fresh dir without flags shows "▶ [a] Trust this workspace / [q] Quit". | Only if the flag is removed. Add a detect-only pattern so a regression reaches Needs you. |
| muse | 1.4.0 | Already done: `--yolo --trust-workspace` on Launch and Resume (`muse.go:32,329`). No change. | **P-M1:** the flags show no dialog in fresh `mu-fresh` or in `wt-a`. **P-M2 (negative):** without flags, "Do you trust this workspace? … > 1 Trust and continue". | Only if the flag changes. Add a detect-only pattern. |

## Design

### A. Startup dialogs (`watchStartup`)

Per `StartupDialogs()` index `i`, track `dialogState{firstSeen, lastSent time.Time; sends int; reqID string}`
instead of `answered[i] bool`.

Each 500 ms poll, on `plain := stripANSI(capture)`:

- **When the dialog matches** (including `Require`):
  - `Fail` dialogs are unchanged: `failSession`.
  - If `len(Keys) > 0`, `sends < dialogMaxSends` (3), and either `sends == 0`
    or `now - lastSent >= dialogRetryEvery` (5 s): send the keys, `sends++`,
    and log `startup: %s: sent %v for dialog %d (send %d)`.
  - If `now - firstSeen >= dialogEscalateAfter` (15 s) and `reqID == ""`:
    `reqID = OpenDialogPrompt(ses.ID, title)` and log
    `startup: %s: dialog %q still visible after %s, opened %s`.
  - While any dialog row is open, push `stallDeadline` and `ceiling` forward.
    A human-blocked pane never fails; it waits for the user.
- **When the dialog no longer matches:**
  - If `reqID != ""`: `ResolveDialogPrompt(ses.ID, title)`.
  - Reset this dialog's state.
- **Unchanged:** Idle or Busy moves the session to `running`. Resolve any open
  dialog rows first.

`title` comes from a new `Dialog.Title` field. Every existing `Dialog` gets one,
matching its `PromptMatcher` twin (copy table below).

### B. Live-session prompts (`resolveAlive`)

- **Defer a `Spawning` session only while its `watchStartup` is running.**
  REVISED 2026-09-26 (Batch 1 review, e88e1bd): an in-memory
  `activeWatchStartup` set (`setWatchStartupActive` / `hasActiveWatchStartup`)
  marks sessions a live `watchStartup` goroutine owns. Reconcile skips dialog
  matching and resolving only for those, which prevents double key presses.
  A `Spawning` session with no active `watchStartup` (for example after a
  daemon restart, since nothing re-attaches the goroutine) is taken over by
  reconcile: retry, escalate and resolve, like any live session.
- **Match on `stripANSI(capture)`.** Change both `m.Match` and `m.Require` in
  `reconcile.go:824-827`.
- **Replace the once-only flag.** `markPromptAnswered(sessionID, title) bool`
  becomes `promptTick(sessionID, title string, now time.Time) promptStep` over
  a `map[string]*dialogState` (same mutex).
  - `promptStep` is `{Send bool; Escalate bool}`, with the same
    retry/escalate rules and constants as A.
  - Detect-only matchers (`Action == ""`) never send, but still escalate.
    Today `Action == ""` is skipped entirely (`reconcile.go:823`).
- **Clear state when nothing matches.** If no matcher matched this tick, then
  for every `(session, title)` with state: `ResolveDialogPrompt`, and delete the
  state.
  - This runs **outside** the `if !idle` block (`reconcile.go:821`), so a pane
    that went idle still closes its row.
  - If `OpenDialogPrompt` errors, reset `reqID` to `""` so the next tick
    retries the escalation.
- **Human-blocked sessions don't raise no-ack or stale.** A session with an
  escalated dialog (`reqID` set in `promptState`) takes the `waiting` early
  return. It skips `notifyNoAck` (`reconcile.go:856`) and the stale check, but
  the `sessions.waiting` column is *not* set. Otherwise the parent
  orchestrator would get a no_ack relay ("swarm_control cancel if genuinely
  stuck") while the user's row is open, and a cancel would kill the pane and
  withdraw the row.
  - **Helper:** `func (s *Store) hasEscalatedPrompt(ctx context.Context, sessionID string, ad adapter.Adapter) (bool, error)`.
    REVISED 2026-09-26 (e88e1bd): it is DB-backed, not in-memory. It returns
    true when the session has an open `prompt` request whose title is one of
    the adapter's dialog titles (the union of `PromptPatterns` and
    `StartupDialogs`). That way an escalation opened by `watchStartup`, or one
    left over from before a daemon restart, still suppresses no-ack.

### C. Request rows

```go
// requests.go
// OpenDialogPrompt opens (or returns the already-open) prompt row for a
// dialog visible in this session's pane. Dedupe key: (session_id, prompt=title, state=open).
func (s *Store) OpenDialogPrompt(ctx context.Context, sessionID, title string) (Request, bool /*created*/, error)

// ResolveDialogPrompt closes the open prompt row(s) of this session whose
// prompt == title, as answered via "terminal". No-op when none is open.
func (s *Store) ResolveDialogPrompt(ctx context.Context, sessionID, title string) error
```

- **`OpenDialogPrompt`:**
  - Runs `SELECT id FROM requests WHERE session_id=? AND kind='prompt' AND state='open' AND prompt=?`.
  - If a row exists, it returns it with `created=false`.
  - Otherwise it inserts with the same SQL as `AskPrompt` (`requests.go:697-701`),
    so `finishOpen` raises `request.prompt` exactly once per row.
- **`ResolveDialogPrompt`** uses `ResolvePrompt(id, "terminal")`.
- **Permission-hook rows are safe.** Rows created by the `PermissionRequest`
  hook have `prompt = command` or `"Permission requested"`, which never equals a
  dialog title, so neither function touches them.
- **Tool-call resolution is fine.** `ResolveSessionPrompts` with an empty
  command (PostToolUse) also closes dialog rows. A tool call proves the dialog
  is gone.

### D. Reaper, `failSession` and the notification

- **Reaper** (`reconcile.go:186-201`): for a pane not in `known` whose tmux
  name belongs to an agent in `finished` or `acknowledged` state, kill it,
  live or dead.
  - New query: `finishedAgentTmuxNames(ctx) (map[string]bool, error)`, as
    `SELECT DISTINCT s.tmux_name FROM sessions s JOIN agents a ON a.id = s.agent_id WHERE a.state IN ('finished','acknowledged')`.
  - Log `reconcile: killed pane %s of finished agent %s`.
  - Live panes of terminal sessions whose agent is still active are kept
    (P0-crash-1). `Cancel` and `Ack` need no change: the reaper catches their
    panes within one tick (5 s).
- **`failSession`:** after the transaction, `s.Tmux.Kill(ctx, ses.TmuxName)`;
  on error, log it and don't return it.
- **Notification reason:** `firstReadableLine(pane string) string` returns the
  first line of `stripANSI(pane)` that, after `TrimSpace`, contains at least
  one letter or digit (`unicode.IsLetter || unicode.IsDigit`). It is capped at
  120 runes with `…`. The `reason` format is unchanged:
  `"<line> Couldn't start agent."`.

### E. Per-session trust (all kinds)

- **Interface:** `Adapter.TrustFolder(ctx, path)` is removed. Its only caller
  is `agents.go:1031`, and it can't be per-session: it takes no session.
  Each adapter's `Launch`/`Resume` does its own trust from `Spec.Cwd` and
  `Spec.SessionID`.
- **Unchanged:** `ForgetFolder` (it has no runtime caller).

```go
// claude.go
// trustClaudeWorkspace marks cwd (and its realpath) trusted in Claude's
// global config, merging into any existing project entry, under Claude's own
// lock directory. It is idempotent and never rewrites the file when the
// entries already exist.
func (c *Claude) trustClaudeWorkspace(cwd string) error

// codex.go (inside setupEnv, after codexHome exists)
func writeCodexTrust(codexHome, cwd string) error // merges [projects."<cwd>"] (+ realpath) into codexHome/config.toml

// agy.go (inside setupEnv, replacing the whole-dir antigravity-cli symlink)
func linkAgyCLIDir(real, dst, cwd string) error // per-entry symlinks, settings.json copy with cwd added to trustedWorkspaces
```

**Claude write protocol (`trustClaudeWorkspace`):**
1. **Best-effort lock.** The lock-dir name is inferred from the binary's
   proper-lockfile code (`${file}.lock`), not verified live. The real guard
   against losing an update is step 4's re-read-compare.
   `os.Mkdir(<UserHome>/.claude.json.lock, 0o755)`. On `EEXIST`, retry every
   100 ms for up to 2 s. A lock dir older than 10 s is stale: remove it once
   and retry.
2. Read `~/.claude.json`. If both keys already have
   `hasTrustDialogAccepted: true`, release the lock and return.
3. Otherwise, unmarshal into `map[string]json.RawMessage`. Set
   `projects[key]["hasTrustDialogAccepted"] = true` and keep every other field
   of every entry.
4. Re-read the file. If its bytes changed since step 2, go back to step 2, up
   to 3 times. After that, log and give up; the dialog auto-answer is the
   fallback.
5. Write with `writeFileAtomic(path, out, <existing mode, default 0o600>)`,
   then `os.Remove` the lock dir.

Failure never blocks a spawn: log `claude: pre-trust %s: %v` and continue.

**agy layout (`linkAgyCLIDir`):**
- If `dst` is a symlink (a legacy agy-home on Resume), leave it as it is and
  log. Otherwise create `dst`.
- For each entry of `real` except `settings.json`: symlink it, unless `dst`
  already has that name.
- Write `settings.json` as the real file's JSON with `trustedWorkspaces` =
  existing ∪ {`cwd`}. agy stores paths exactly as given
  (`ForgetFolder` comment, `agy.go:370`), so this is not the realpath.
- If the real file is missing or doesn't parse, write
  `{"trustedWorkspaces":["<cwd>"]}`.

## Screens

The Needs-you row is the existing generic row. Nothing changes on screen:

```
┌ Needs you ─────────────────────────────────────────────┐
│ ● STORY-29 · Merge the green branch                    │  line 1: item key · item title
│   green-merge-11                                        │  line 2: agent name
│   Waiting for your input                                │  line 3: C.needsYouMessage / Copy.needsYouMessage
└─────────────────────────────────────────────────────────┘
  click → opens green-merge-11's terminal (requestTarget: terminal_agent = the agent itself for kind prompt)
  pane gone (tmux_alive false) → existing "unavailable" hint, unchanged
```

**Deliberately not on screen:** the dialog title (rows never show prompt text,
spec 2.2.2), answer buttons, and a new color or icon. The Questions filter
already includes `prompt`.

## User-facing copy

**Dialog titles.** Each becomes the `requests.prompt` value, so it appears in
the notification and in `swarm_read`.

| Adapter | Match | Title | Keys |
|---|---|---|---|
| claude | `Is this a project you created or one you trust\?` + Require `Yes, I trust this folder` | `Trust this project` | `Down`, `Enter` |
| claude | `I am using this for local development` | `Confirm local development` | `Enter` |
| codex | `Do you trust the contents of this directory\?` | `Trust this directory` | `Enter` |
| codex (new, 0.157) | `Trust this folder\?` + Require `Trust and continue` | `Trust this folder` | `Enter` |
| codex | `codexRetire` (existing) | `Keep the current model` | `Down`, `Enter` |
| codex | `codexHookTrust` (existing, `Fail`) | `Hook sandbox approval` | `Fail` |
| agy | `Do you trust the contents of this project\?` | `Trust this project` | `Enter` |
| cursor (new, detect-only) | `Trust this workspace` + Require `\[q\] Quit` | `Trust this workspace` | none |
| muse (new, detect-only) | `Do you trust this workspace\?` | `Trust this workspace` | none |

**Notifications:**
- The existing `request.prompt` template is unchanged: title "Approval needed",
  body `{KEY}: {name} is waiting on approval: {prompt}`.
- Example: `STORY-29: green-merge-11 is waiting on approval: Trust this project`.
- `agent.preflight_failed` is unchanged in template. The body becomes readable,
  e.g. `Loading… Couldn't start agent.` instead of an ANSI `────` rule.
- Needs-you row line 3 stays `Waiting for your input` (existing
  `needsYouMessage`, web and menubar).

**Doctor copy (D4, `Check{Name: "Claude trust", ...}`):**
- FAIL, not writable/missing: `"~/.claude.json is missing or not writable. Claude sessions will hit the trust dialog. Run swarm install."`
- FAIL, untested version: `"Claude <version> is older than 2.1.283, where the trust-dialog key was verified. Update Claude, then run swarm doctor again."`
- FAIL, version undetectable: `"Couldn't determine the installed Claude version. Update Claude, then run swarm doctor again."`
- WARN, stale entries (count > 0): `"<n> stale Swarm-owned entries in ~/.claude.json. Run swarm install to prune them."`
- WARN, live session missing entry: `"<agent name> is running but its workspace isn't trusted in ~/.claude.json yet."`
- PASS: `"~/.claude.json is writable, Claude <version>, <n> Swarm-owned entries."`

**Install copy (D3, printed the same way as other `swarm install` steps):**
- Not writable/missing: `"~/.claude.json is missing or not writable: Claude sessions will hit the trust dialog. Fix its permissions, then run swarm install again."`
- Untested version: `"Claude <version> is older than 2.1.283: the trust-dialog key was verified on 2.1.283+. Continuing, but sessions may still hit the trust dialog."`
- Prune summary (count > 0): `"Removed <n> stale Swarm-owned entries from ~/.claude.json."`
- Prune summary (count == 0): no line printed (install stays quiet when there is nothing to do, matching every other install step).

**Log lines (exact format strings):**
- `startup: %s: sent %v for dialog %q (send %d of 3)`
- `startup: %s: dialog %q still visible after 15s, opened %s`
- `reconcile: %s: sent %v for prompt %q (send %d of 3)`
- `reconcile: %s: prompt %q still visible after 15s, opened %s`
- `reconcile: killed pane %s of finished agent %s`
- `failSession: kill %s: %v`
- `claude: pre-trust %s: %v`

There are no new i18n keys and no new error or empty states.

## Types and constants

```go
// adapter/adapter.go
type Dialog struct {
	Match   *regexp.Regexp
	Keys    []string
	Fail    bool
	Require *regexp.Regexp
	Title   string // new: Needs-you prompt text when this dialog outlives its auto-answer
}

// runtime/agents.go
const (
	dialogRetryEvery    = 5 * time.Second
	dialogMaxSends      = 3
	dialogEscalateAfter = 15 * time.Second
)

// runtime/reconcile.go (Store field in model.go replaces promptAnswered map[string]bool)
type dialogState struct {
	firstSeen, lastSent time.Time
	sends               int
	reqID               string
}
type promptStep struct{ Send, Escalate bool }
func (s *Store) promptTick(sessionID, title string, hasKeys bool, now time.Time) promptStep
func (s *Store) clearPromptState(sessionID, title string)
func firstReadableLine(pane string) string
func (s *Store) finishedAgentTmuxNames(ctx context.Context) (map[string]bool, error)
```

There are no DB model changes. `requests.kind = 'prompt'` already exists, with
`is_hitl = 1`.

## File list

**Changed, batch 1 (runtime):**
- `internal/adapter/adapter.go`: add `Dialog.Title`.
- `internal/adapter/claude.go`, `codex.go`, `agy.go`: add `Title` to existing
  `Dialog`s.
- `internal/runtime/agents.go`: `watchStartup` state machine, `failSession`
  kill, `firstReadableLine`.
- `internal/runtime/reconcile.go`: `resolveAlive` (strip ANSI, defer a
  spawning session only while `hasActiveWatchStartup`, `promptTick`, escalate,
  resolve), the DB-backed `hasEscalatedPrompt`, reaper, `finishedAgentTmuxNames`.
- `internal/runtime/model.go`: the `promptAnswered` field becomes
  `promptState map[string]*dialogState`.
- `internal/runtime/requests.go`: `OpenDialogPrompt`, `ResolveDialogPrompt`.
- Tests: `internal/runtime/agents_test.go`, `reconcile_test.go`,
  `requests_test.go`.

**Changed, batch 2 (trust):**
- `internal/adapter/adapter.go`: remove `TrustFolder` from the interface and
  from `base`.
- `internal/runtime/agents.go`: remove `_ = ad.TrustFolder(ctx, cwd)`.
- `internal/adapter/claude.go`: `trustClaudeWorkspace`, called from
  `Launch` and `Resume`.
- `internal/adapter/codex.go`:
  - remove `TrustFolder`;
  - add `writeCodexTrust` in `setupEnv`;
  - add the new `codexTrustFolder` pattern for `Dialog` and `PromptMatcher`.
- `internal/adapter/agy.go`: `linkAgyCLIDir` in `setupEnv`.
- `internal/adapter/cursor.go`, `muse.go`: detect-only `PromptMatcher`s (for
  muse, replacing `return nil // TODO(probe)`).
- Tests: `internal/adapter/claude_test.go`, `codex_test.go`, `agy_test.go`,
  `cursor_test.go`, `muse_test.go`, `adapter_test.go`.
- Fixtures (captured pane text): under each kind's existing
  `internal/adapter/testdata/<kind>/` directory (the convention already used
  by `pane-dialog-trust.txt` etc.), not a new shared `testdata/trust/`.

**Changed, batch 2b (D2-D4, D8):**
- `internal/adapter/claude.go`: `Claude.ForgetFolder` (D2), removing both keys
  under the same lock/atomic-write protocol as `trustClaudeWorkspace`.
- `internal/runtime/reconcile.go`: a Swarm-owned-path predicate
  (`swarmOwnedWorkspace`, shared by D2's cleanup and D3's prune) and the D2
  cleanup hook, wherever an agent's rows/dirs are deleted or reclaimed;
  `os.Chtimes` in `codex.go`'s `setupEnv` (D8) and the `sync.Once` guard
  around `reclaimOldCodexLaunchHomes` (D8), plus the `Store` field it needs in
  `model.go`.
- `internal/install/claude.go` or a new `internal/install/claude_trust.go`:
  the D3 install-time check/prune (writability, version gate, stale-entry
  prune).
- `internal/install/doctor.go`: the D4 "Claude trust" check, wired into
  `Doctor.Checks`.
- Tests: `internal/runtime/reconcile_test.go` (D2, D8), `internal/adapter/codex_test.go`
  (D8's Chtimes/sync.Once), `internal/install/claude_test.go`, `internal/install/doctor_test.go`.

**Reused unchanged:**
- `ResolvePrompt`, `finishOpen`, `ResolveSessionPrompts`, `withdrawOrphanedRequests`;
- notify templates;
- web `NeedsYou`, `inbox.ts`; menubar `NeedsYouRow`, `Notifier`;
- `Spawner.Keys` (copy-mode guard).

**Tests ported, not deleted:**
- `adapter_test.go:66`: base `TrustFolder` no-op call. Drop only that call,
  since the method no longer exists; keep the `ForgetFolder` assertion.
- `codex_test.go:177` `TestCodexTrustFolderIsAStructuredIdempotentEdit` is
  rewritten as `TestCodexLaunchWritesPerLaunchTrustIdempotently`, with the same
  structured-merge and idempotence assertions, now against
  `<launch>/codex-home/config.toml`.
- `reconcile_test.go:1818` `TestPromptPatternAutoAnswersOncePerSessionAndOpensNoRequest`
  is renamed `TestPromptPatternAutoAnswersOnceWhenTheDialogClears`. Its
  assertion (one send, no row) holds when the dialog clears after the first
  send; a new test covers escalation.

**Deleted:** `Codex.TrustFolder` (dead: spawned codex never reads
`~/.codex/config.toml`). Its test is ported as above.

## Verification

**Command order (in the worktree):**
1. `go build ./... && go vet ./...`
2. `go test ./internal/adapter/... ./internal/runtime/... ./internal/notifyrules/...`
3. `go test ./...`
4. `cd web && pnpm test -- NeedsYou inbox` (unchanged UI; regression only)

**Scenarios with the fake tmux (unit):**
- **Happy path:** the dialog clears after the first send. One send, no row, the
  session reaches `running`.
- **Keys swallowed:** the dialog is still visible. Sends at t = 0, 5 and 10 s;
  a row opens at 15 s; there is exactly one `request.prompt` notification; the
  session stays `spawning` past 30 s and past the 10 min ceiling.
- **User answers in the terminal:** the dialog disappears, the row becomes
  `answered`/`terminal`, and the session reaches `running`.
- **Detect-only dialog** (muse/cursor pattern): zero sends, a row at 15 s.
- **Unknown dialog / stall:** the existing 30 s `failSession`. The pane is
  killed and the notification body is the first readable line.
- **Live session:** the prompt appears mid-run. Retries, then a row, then it
  resolves when the prompt clears. A `Spawning` session is left to reconcile's
  twin only while its `watchStartup` is active; after a daemon restart,
  reconcile takes it over.
- **Reaper:** a live pane of an `acknowledged` agent with a failed session is
  killed, while one belonging to an `active` agent's crashed session is kept.
- **Dedupe:** two escalations of the same (session, title) produce one row.

**Live, after `make install-daemon`:**
- Spawn one claude, codex and agy worker each. Each pane reaches its idle
  prompt with no dialog captured (`tmux -L swarm capture-pane -p -t <name>`
  never shows a trust line).
- `jq '.projects["<cwd>"]' ~/.claude.json` shows `hasTrustDialogAccepted: true`.
- `cat ~/.swarm/run/launch/<ses>/codex-home/config.toml` shows the trust entry.
- `ls -la ~/.swarm/run/launch/<ses>/agy-home/.gemini/antigravity-cli/` shows a
  real `settings.json` and symlinks for everything else.
- **Row path, live:** spawn a muse worker. In its pane, run
  `/exit`, then start `muse` bare in the same dir. This is a manual,
  non-Swarm-launched dialog in a Swarm-owned pane; reconcile sees the
  detect-only muse pattern. Expect a Needs-you row within 20 s ("Waiting for
  your input"; the notification reads
  `<KEY>: <agent> is waiting on approval: Trust this workspace`). Answer `1` in
  the terminal; the row disappears within 5 s. If Swarm treats the exited
  process as crashed first, rely on the unit scenarios above; no test-only
  env switch is added.

## Explicitly out of scope

- Answer buttons on board or menubar rows.
- Recognising unknown dialogs by stall heuristics (they still fail and are
  killed).
- The codex `SUN_LEN` failure: `~/.swarm/run/launch/<ses>/codex-home/app-server-control/app-server-control.sock`
  is 122 bytes, over macOS's 104, so codex 0.157 exits with "app server did
  not become ready" (probe P-X0). This is a separate spec.
- Removing agy's trust entries (agy persists them today too, and D2/D3 are
  Claude-only: agy has no separate global config file this touches).
- Pruning the user's own (non-Swarm-owned) entries in `~/.claude.json`. D2/D3
  only ever touch Swarm-owned paths (under `<swarm home>/work` or
  `<swarm home>/worktrees`); the 308 pre-existing per-dir entries a real user
  has accumulated by hand are never read or matched.
- Changes to web or menubar code.

## Open questions

Q1 and Q2 were resolved 2026-09-26 by decisions D1-D8 above; kept here for
history.

- **Q1 (resolved by D1):** the only Claude mechanism that verifiably
  suppresses the dialog without moving auth or state is an exact-path entry
  in the user's `~/.claude.json`. That is the same per-path write Claude
  makes on "Yes", done per session under Claude's lock. **Decided:** allow
  this per-session exact-path write; it is not a global or parent grant. Q1
  also covers cleanup, since a per-session write needs a per-session
  teardown: that half is D2/D3/D4.
- **Q2 (resolved by D6, non-blocking):** re-probe the agy per-entry symlink
  layout after an agy self-update, in case new top-level files land
  per-session. **Decided:** yes, as a one-line check in the live
  verification (Task 14's live steps).
