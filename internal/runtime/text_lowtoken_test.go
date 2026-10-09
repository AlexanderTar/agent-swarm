package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
)

const wantOrchBlock = `## Low-token mode is on
Your user turned on low-token mode for this orchestrator. These rules override the skill steps they name:
- Do small work yourself: at most one research question, config or doc edits touching three files or fewer, and your own plan check instead of spawning a completeness critic.
- Spikes: at most two researchers and no extra research round.
- Advisor: consult only before plan approval and before irreversible steps.
- Batch work into fewer, larger packages (four to five units).
- Keep messages and checkpoint summaries to three sentences. Don't send a finding when your checkpoint already says it.
- Read the lines you need, not whole files. Query graphify if it is built; don't build it for small tasks.
- Call swarm_sync when a notice arrives and before you complete, not on a timer.
- The daemon caps review loops at two rounds, one reviewer per step, and no automatic retries.`

const wantWorkerBlock = `## Low-token mode is on
Your orchestrator runs in low-token mode. These rules override the skill steps they name:
- Keep messages and checkpoint summaries to three sentences.
- Read the lines you need, not whole files. Query graphify if it is built; don't build it for small tasks.
- Call swarm_sync when a notice arrives and before you complete, not on a timer. You get no mid-turn notices, so also call it before any long stretch of work.
- Researchers: answer in eight tool calls or fewer.`

func TestLowTokenBlockText(t *testing.T) {
	if got := LowTokenBlock(RoleOrchestrator, "claude"); got != wantOrchBlock {
		t.Fatalf("orchestrator block:\n%s", got)
	}
	if got := LowTokenBlock(RoleCoder, "muse"); got != wantWorkerBlock {
		t.Fatalf("worker block (muse):\n%s", got)
	}
	muse := LowTokenBlock(RoleOrchestrator, "muse")
	if !strings.Contains(muse, "not on a timer. You get no mid-turn notices, so also call it before any long stretch of work.\n") {
		t.Fatalf("muse orchestrator block lacks the sync sentence:\n%s", muse)
	}
	if strings.Contains(LowTokenBlock(RoleCoder, "claude"), "mid-turn") || strings.Contains(LowTokenBlock(RoleCoder, "claude"), "{muse_sync}") {
		t.Fatal("non-muse worker block must not carry the muse sentence or placeholder")
	}
	if got := LowTokenOnNote(RoleCoder, "claude"); got != "Low-token mode is now on for your orchestrator.\n\n"+LowTokenBlock(RoleCoder, "claude") {
		t.Fatalf("on-note:\n%s", got)
	}
}

func TestStartSessionAppendsLowTokenBlock(t *testing.T) {
	s, _, fa := newStore(t)
	ctx := context.Background()
	orch, w, _ := worker(t, s)
	cases := []struct {
		name      string
		resume    bool
		succMode  string
		wantBlock string
		agent     Agent
	}{
		{"fresh", false, "", wantOrchBlock, orch},
		{"resume", true, "", wantOrchBlock, orch},
		{"successor", false, "handoff", wantOrchBlock, orch},
		{"recovery", false, "recovery", wantOrchBlock, orch},
		{"retry", false, "", LowTokenBlock(RoleCoder, "fake"), w},
	}
	gen := 10
	for _, on := range []bool{true, false} {
		if on {
			mustExec(t, s.DB, `UPDATE agents SET low_token = 1 WHERE id = ?`, orch.ID)
		} else {
			mustExec(t, s.DB, `UPDATE agents SET low_token = 0 WHERE id = ?`, orch.ID)
		}
		for _, c := range cases {
			gen++
			if _, err := s.startSession(ctx, c.agent, 1, gen, c.resume, "prov", c.succMode); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			got := strings.Contains(fa.LastSpec.Kickoff, "## Low-token mode is on")
			if got != on {
				t.Fatalf("%s on=%v: block present=%v\n%s", c.name, on, got, fa.LastSpec.Kickoff)
			}
			if on && !strings.HasSuffix(fa.LastSpec.Kickoff, "\n\n"+c.wantBlock) {
				t.Fatalf("%s: kickoff does not end with the block:\n%s", c.name, fa.LastSpec.Kickoff)
			}
		}
	}
}

func TestMuseResumeGetsBlockAsNote(t *testing.T) {
	s, _, fa := newStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	s.Adapters[Muse] = fa
	mustExec(t, s.DB, `UPDATE agents SET kind = 'muse', low_token = 1 WHERE id = ?`, orch.ID)
	a, err := s.AgentByID(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	var _ adapter.Adapter = fa
	if _, err := s.startSession(ctx, a, 1, 5, true, "prov", ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fa.LastSpec.Kickoff, "## Low-token mode is on") {
		t.Fatal("muse resume drops the kickoff, so the block must not ride on it")
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'assignment_update'
		AND payload_json LIKE '%Low-token mode is now on for your orchestrator.%## Low-token mode is on%'`, orch.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("on-note messages=%d, want 1", n)
	}
	mustExec(t, s.DB, `UPDATE agents SET low_token = 0 WHERE id = ?`, orch.ID)
	if _, err := s.startSession(ctx, a, 1, 6, true, "prov", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'assignment_update'
		AND payload_json LIKE '%Low-token mode is now on%'`, orch.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("off must add no note: n=%d err=%v", n, err)
	}
}
