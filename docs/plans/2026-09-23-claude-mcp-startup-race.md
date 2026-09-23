# Plan: fix Claude's "Unknown skill: swarm" / "no MCP server configured"

Spec: `docs/specs/2026-09-23-claude-mcp-startup-race.md`. Root cause:
`--setting-sources project,local` (added 2026-09-22) excludes the "user"
scope Claude Code needs to see `~/.claude/skills/swarm` and to resolve
`server:swarm` for channels. Fix: also write both as project-scope resources
into the session's own scratch cwd.

All steps run inside this worktree:
`/Users/alexandertar/GitHub/agent-swarm--claude-mcp-startup-race`
(branch `fix/claude-mcp-startup-race`).

## Step 1 — failing test: `.mcp.json` lands in the session cwd

File: `internal/adapter/claude_test.go`.

Add:

```go
// 2026-09-23, root-caused live: --setting-sources project,local (added
// yesterday to keep ~/.claude/CLAUDE.md out) also hides ~/.claude/skills and
// whatever registry the channels feature resolves "server:swarm" against --
// both confirmed by direct reproduction, not a timing race (see the spec).
// A project-scope .mcp.json in the session's own scratch cwd fixes the
// channels side deterministically.
func TestClaudeLaunchWritesProjectScopeMCPConfig(t *testing.T) {
	d := testDeps(t)
	s := claudeSpec(t, d)
	l, err := newClaude(d).Launch(s)
	if err != nil {
		t.Fatal(err)
	}
	_ = l
	b, err := os.ReadFile(filepath.Join(s.Cwd, ".mcp.json"))
	if err != nil {
		t.Fatalf("expected .mcp.json in the session cwd: %v", err)
	}
	var cfg struct {
		Servers map[string]struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("project .mcp.json does not parse: %v\n%s", err, b)
	}
	sw := cfg.Servers["swarm"]
	if sw.Type != "stdio" || sw.Command != s.Bin || strings.Join(sw.Args, " ") != "mcp" {
		t.Fatalf("project .mcp.json swarm entry = %+v", sw)
	}
}
```

Run: `cd /Users/alexandertar/GitHub/agent-swarm--claude-mcp-startup-race &&
go test ./internal/adapter/... -run TestClaudeLaunchWritesProjectScopeMCPConfig -v`
— confirm it fails (no such file).

## Step 2 — failing test: both skills land in the session cwd

Same file, add:

```go
// Same bug, skill side: Skill(swarm) was "Unknown skill: swarm" 100% of the
// time under --setting-sources project,local (verified live, zero-delay
// repro) because ~/.claude/skills is "user" scope. Project-scope copies in
// the session's own cwd fix it without reopening the excluded user scope.
func TestClaudeLaunchWritesProjectScopeSkills(t *testing.T) {
	d := testDeps(t)
	s := claudeSpec(t, d)
	if _, err := newClaude(d).Launch(s); err != nil {
		t.Fatal(err)
	}
	for _, name := range install.SkillNames {
		got, err := os.ReadFile(filepath.Join(s.Cwd, ".claude", "skills", name, "SKILL.md"))
		if err != nil {
			t.Fatalf("expected %s skill reachable in the session cwd: %v", name, err)
		}
		if string(got) != string(install.SkillBody(name)) {
			t.Errorf("%s skill body does not match the embedded source", name)
		}
	}
}

// Resume() shares flags() with Launch(); this must not regress on resume.
func TestClaudeResumeWritesProjectScopeSkillsAndMCP(t *testing.T) {
	d := testDeps(t)
	s := claudeSpec(t, d)
	s.ProviderSessionID = "11111111-2222-4333-8444-555555555555"
	if _, err := newClaude(d).Resume(s); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Cwd, ".mcp.json")); err != nil {
		t.Errorf("resume: expected .mcp.json in the session cwd: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Cwd, ".claude", "skills", "swarm", "SKILL.md")); err != nil {
		t.Errorf("resume: expected the swarm skill in the session cwd: %v", err)
	}
}
```

Add `"github.com/AlexanderTar/agent-swarm/internal/install"` to
`claude_test.go`'s imports.

Run the two new tests — confirm both fail.

## Step 3 — minimal implementation

File: `internal/adapter/claude.go`.

1. Add import `"github.com/AlexanderTar/agent-swarm/internal/install"`.
2. In `flags()`, right after `mcpPath, err := c.d.writeLaunchFile(...)`
   succeeds, call the new helper:

```go
	if err := writeProjectSwarmConfig(s.Cwd, mcp); err != nil {
		return nil, err
	}
```

3. Add the helper function (see spec's "API / behavior changes" section for
   the exact body — copy it verbatim, including the doc comment).

## Step 4 — verify

```bash
cd /Users/alexandertar/GitHub/agent-swarm--claude-mcp-startup-race
go test ./internal/adapter/... -v -run TestClaude
go build ./...
go vet ./...
```

All `TestClaude*` tests pass, including the two new ones and every
pre-existing one (`TestClaudeLaunchArgv`, `TestClaudeMCPConfigJSON`,
`TestClaudeMCPConfigIsIsolated`, `TestClaudeResumeUsesTheProviderID`, etc. —
argv is untouched, so none of these should need changes).

## Step 5 — manual end-to-end re-check (the same probe that found the bug)

On an isolated tmux socket (never `swarm`), from a throwaway project dir:

```bash
SOCK=swarm-test-verify
tmux -L $SOCK kill-server 2>/dev/null
PROBE=$(mktemp -d)
# Build the fixed binary and run it once to populate PROBE via flags()'s
# real code path is overkill for a manual check; instead mirror flags()'s
# argv shape directly, pointing --mcp-config at a throwaway file and
# reusing this fix's own output: after `go test` in step 4 passes, any
# TestClaudeLaunchWritesProjectScopeMCPConfig run already proves the file
# contents; this step is about Claude Code's actual acceptance of them.
# So: copy the two skill files and a real claude-mcp.json-shaped .mcp.json
# into $PROBE by hand (same shape as claude-mcp.json), then:
tmux -L $SOCK new-session -d -s probe -c "$PROBE" -x 220 -y 60 -- \
  claude -n probe --model claude-sonnet-4-5 --dangerously-skip-permissions \
  --strict-mcp-config --mcp-config "$PROBE/claude-mcp.json" \
  --setting-sources project,local \
  --dangerously-load-development-channels server:swarm \
  -- "Say hello, then use the Skill tool to invoke the skill named swarm."
# handle the trust dialog (Down, Enter) and dev-channels dialog (Enter),
# then capture-pane and confirm: no "no MCP server configured with that
# name" line, and Skill(swarm) reports "Successfully loaded skill".
tmux -L $SOCK kill-server
```

This mirrors the exact probe already run live during the investigation
(documented in the spec) which found the fix works; re-running it here is
belt-and-suspenders confirmation against the built fix, not new discovery.

## Step 6 — commit

```bash
cd /Users/alexandertar/GitHub/agent-swarm--claude-mcp-startup-race
git add internal/adapter/claude.go internal/adapter/claude_test.go \
  docs/specs/2026-09-23-claude-mcp-startup-race.md \
  docs/plans/2026-09-23-claude-mcp-startup-race.md
git commit -m "$(cat <<'EOF'
fix(adapter/claude): make the swarm skill and channel name reachable under --setting-sources project,local

Root cause (not a startup race -- confirmed by direct, zero-delay
reproduction): --setting-sources project,local, added 2026-09-22 to keep the
user's global ~/.claude/CLAUDE.md out of agent sessions, also excludes the
"user" scope Claude Code needs to see ~/.claude/skills/swarm and to resolve
server:swarm for the experimental channels feature. Skill(swarm) always
failed with "Unknown skill: swarm" and the channel banner always showed "no
MCP server configured with that name", regardless of timing.

Fix: write a project-scope .mcp.json and both swarm skill files into the
session's own scratch cwd (~/.swarm/work/<agent-name> -- always empty at
spawn, never a real git worktree, confirmed live). Both resources become
visible under "project" scope without reopening the excluded "user" scope.
EOF
)"
git status --short
```

Do NOT merge, push, or touch the primary checkout
(`/Users/alexandertar/GitHub/agent-swarm`).
