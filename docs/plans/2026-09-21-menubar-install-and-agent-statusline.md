# Plan: menubar install never replaces a running bundle + agent status line off

Spec: `docs/specs/2026-09-21-menubar-install-and-agent-statusline.md`.
Work in a sibling worktree, e.g. `git worktree add ../agent-swarm--install-app-quit -b fix/install-app-quit`. Tasks 1-2 are Part A (Makefile), Task 3 is Part B (status line); they touch disjoint files, so commit separately.

## Task 1: failing check (freshness script)
Files: create `scripts/check-app-fresh.sh` (mode 0755). Consumes: running `Swarm` process. Produces: exit 0 when not running or inode matches, exit 1 on mismatch.

```sh
#!/bin/sh
# Fails if the running Swarm executes a different inode than /Applications/Swarm.app has on disk.
pid=$(pgrep -x Swarm) || { echo "Swarm not running"; exit 0; }
run=$(lsof -p "$pid" 2>/dev/null | awk '$4=="txt" {print $8; exit}')
disk=$(ls -i /Applications/Swarm.app/Contents/MacOS/Swarm | awk '{print $1}')
[ "$run" = "$disk" ] && echo "fresh ($run)" || { echo "STALE: running $run, on disk $disk"; exit 1; }
```
1. Write the script.
2. Run `scripts/check-app-fresh.sh` with the current stale process running: expect `STALE`, exit 1 (watch it fail).
3. Also add the dry-run check to run after Task 2: `make -n install-app | awk '/osascript/{q=NR} /rm -rf/{r=NR} /cp -R/{c=NR} /open /{o=NR} END{exit !(q<r && r<c && c<o)}'`. Run it now: exit 1 (no quit/open lines yet).

## Task 2: minimal Makefile fix
File: `Makefile`, replace the `install-app` recipe (currently lines 62-66) with:

```make
install-app: app
	mkdir -p $(APP_DIR)
	-osascript -e 'quit app id "dev.swarm.menubar"'
	@for i in 1 2 3 4 5; do pgrep -x Swarm >/dev/null || break; sleep 1; done; pkill -x Swarm 2>/dev/null; true
	rm -rf $(APP_DIR)/Swarm.app
	cp -R apps/menubar/.build/Swarm.app $(APP_DIR)/Swarm.app
	open $(APP_DIR)/Swarm.app
```
1. Edit the recipe.
2. Run the dry-run check from Task 1 step 3: exit 0.
3. `make install-app`, then `scripts/check-app-fresh.sh`: `fresh (<inode>)`, exit 0.
4. Trigger one notification; confirm the icon shows.
5. Commit with explicit paths (no `git add -A`, no `--amend`):
   `git add Makefile scripts/check-app-fresh.sh docs/specs/2026-09-21-menubar-install-and-agent-statusline.md docs/plans/2026-09-21-menubar-install-and-agent-statusline.md`
   `git commit -m "fix(make): quit Swarm before install-app replaces the bundle"` (end with the Co-Authored-By line from the session attribution).
6. After merge: `git worktree remove ../agent-swarm--install-app-quit`.

## Task 3 (Part B): blank the user status line in agent sessions
Files: `internal/adapter/claude_test.go` (new test), `internal/adapter/claude.go` (`settingsJSON`, line ~25). Consumes: `testDeps`, `claudeSpec` helpers already used by `TestClaudeSettingsJSONTurnsAttributionOffAndListsEveryHook`. Produces: launch settings JSON containing `statusLine` = `{"type":"command","command":"true"}`.

1. Add the failing test after `TestClaudeSettingsJSONTurnsAttributionOffAndListsEveryHook` in `internal/adapter/claude_test.go`:

```go
func TestClaudeSettingsJSONBlanksTheUserStatusLine(t *testing.T) {
	d := testDeps(t)
	s := claudeSpec(t, d)
	l, err := newClaude(d).Launch(s)
	if err != nil {
		t.Fatal(err)
	}
	var path string
	for i, v := range l.Argv {
		if v == "--settings" {
			path = l.Argv[i+1]
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		StatusLine *struct {
			Type    string `json:"type"`
			Command string `json:"command"`
		} `json:"statusLine"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("settings JSON does not parse: %v\n%s", err, b)
	}
	// An object, never null: Claude Code rejects "statusLine": null with a blocking settings-error dialog.
	if cfg.StatusLine == nil || cfg.StatusLine.Type != "command" || cfg.StatusLine.Command != "true" {
		t.Errorf("statusLine must be {type:command, command:true} to blank the user's status line: %s", b)
	}
}
```
2. Run `go test ./internal/adapter/ -run TestClaudeSettingsJSONBlanksTheUserStatusLine -v`: expect FAIL (`statusLine must be ...`).
3. In `settingsJSON` (`internal/adapter/claude.go`), add one key to the `cfg` map, after `"includeCoAuthoredBy": &no,`:

```go
		// Blank the user's global status line (e.g. ccstatusline via npx every 10s)
		// in agent panes. A no-output command, not null: null is rejected by Claude Code.
		"statusLine": map[string]any{"type": "command", "command": "true"},
```
4. Run `go test ./internal/adapter/ -v`: all PASS (the existing settings test is unchanged and still passes).
5. Live check: spawn one agent as usual, then `tmux -L swarm capture-pane -p -t <session> | tail -4`: only the `⏵⏵ bypass permissions on` footer, no `Sonnet 5 high | …` row.
6. Commit with explicit paths (no `git add -A`, no `--amend`):
   `git add internal/adapter/claude.go internal/adapter/claude_test.go`
   `git commit -m "feat(adapter): blank the user status line in Claude agent sessions"` (end with the Co-Authored-By line from the session attribution).

## One-time user actions (not part of the commit)
- `pkill -x Swarm; sleep 1; open /Applications/Swarm.app`
- Only if still blank: `killall NotificationCenter`
- Optional: `lsregister -u ~/GitHub/agent-swarm--menubar-followups/apps/menubar/.build/Swarm.app`
