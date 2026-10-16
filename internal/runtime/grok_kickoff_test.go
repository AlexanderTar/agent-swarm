package runtime

import (
	"context"
	"strings"
	"testing"
)

func TestKindKickoffNote(t *testing.T) {
	note := KindKickoffNote(Grok)
	for _, want := range []string{"superpowers", "/fork"} {
		if !strings.Contains(note, want) {
			t.Errorf("grok kickoff note %q lacks %q", note, want)
		}
	}
	if !strings.Contains(note, "never") && !strings.Contains(note, "Never") {
		t.Errorf("grok kickoff note must forbid /fork: %q", note)
	}
	// grok 1.0.60 has no skill tool: agents load a skill by reading its SKILL.md.
	if !strings.Contains(note, "read_file") || !strings.Contains(note, "SKILL.md") {
		t.Errorf("grok kickoff note must say to read SKILL.md with read_file: %q", note)
	}
	// Swarm runs grok with GROK_HOME=<swarm home>/grok, so the plugins live
	// there, not under ~/.grok.
	if !strings.Contains(note, "$GROK_HOME/installed-plugins/") || strings.Contains(note, "~/.grok") {
		t.Errorf("grok kickoff note must point at $GROK_HOME/installed-plugins, not ~/.grok: %q", note)
	}
	if strings.Contains(note, "with the skill tool") {
		t.Errorf("grok has no skill tool: %q", note)
	}
	for _, k := range []AgentKind{Claude, Codex, Agy, Cursor, Muse, Fake} {
		if got := KindKickoffNote(k); got != "" {
			t.Errorf("KindKickoffNote(%s) = %q, want none", k, got)
		}
	}
}

func TestStartContextDropped(t *testing.T) {
	if !StartContextDropped(Grok) {
		t.Error("grok drops SessionStart and UserPromptSubmit context")
	}
	for _, k := range []AgentKind{Claude, Codex, Agy, Cursor, Muse, Fake} {
		if StartContextDropped(k) {
			t.Errorf("%s keeps SessionStart context", k)
		}
	}
}

// A grok kickoff carries the note; the graphify hint (which SessionStart would
// say, but its context never reaches grok) is built per agent for the kickoff.
func TestGrokKickoffCarriesNoteAndGraphifyHint(t *testing.T) {
	s, _, fa := newStore(t)
	ctx := context.Background()
	s.Adapters[Grok] = fa
	seedCatalog(t, s, "grok", "1.0.60", `[{"id":"grok-4.7","label":"Grok 4.7","efforts":["low","high"],"default_effort":"high","effort_encoding":"flag","advisor_capable":false}]`, "grok-4.7")
	setEnabled(t, s, Grok)
	seedEpicWithTask(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Grok, Model: "grok-4.7"})
	if err != nil {
		t.Fatal(err)
	}
	if got := fa.LastSpec.Kickoff; !strings.Contains(got, KindKickoffNote(Grok)) {
		t.Fatalf("kickoff lacks the grok note:\n%s", got)
	}
	// With a worktree owned by the agent, the hint names it.
	wt := t.TempDir()
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO repos (id, name, path, default_branch, source, created_at, updated_at)
		VALUES ('repo_1', 'r', ?, 'main', 'manual', 1, 1)`, wt); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO worktrees (id, repo_id, path, branch, base_ref, base_sha, state, owner_agent_id, root_item_id, created_at)
		SELECT 'wt_1', 'repo_1', ?, 'b', 'main', 'abc1234', 'active', id, root_item_id, 1 FROM agents WHERE id = ?`, wt, orch.ID); err != nil {
		t.Fatal(err)
	}
	hint, err := s.GraphifyHintFor(ctx, orch.ID)
	if err != nil || !strings.Contains(hint, wt) || !strings.Contains(hint, "graphify extract") {
		t.Fatalf("hint = %q", hint)
	}
	if none, err := s.GraphifyHintFor(ctx, ""); none != "" || err != nil {
		t.Fatal("no agent, no hint")
	}
}
