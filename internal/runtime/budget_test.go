package runtime

import (
	"context"
	"database/sql"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
	"github.com/AlexanderTar/agent-swarm/internal/kinds"
)

func sampleRow(t *testing.T, s *Store, sessionID string) (tokens, window sql.NullInt64) {
	t.Helper()
	if err := s.DB.QueryRowContext(context.Background(),
		`SELECT context_tokens, context_window FROM sessions WHERE id = ?`, sessionID).Scan(&tokens, &window); err != nil {
		t.Fatal(err)
	}
	return
}

func TestRecordContextSample(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Sample", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tok, win := sampleRow(t, s, ses.ID); tok.Valid || win.Valid {
		t.Fatalf("fresh session already sampled: %v %v", tok, win)
	}
	w := 200000
	if err := s.RecordContextSample(ctx, ses.ID, 93123, &w); err != nil {
		t.Fatal(err)
	}
	if tok, win := sampleRow(t, s, ses.ID); tok.Int64 != 93123 || win.Int64 != 200000 {
		t.Fatalf("row = %v/%v, want 93123/200000", tok, win)
	}
	// a nil window keeps the existing one
	if err := s.RecordContextSample(ctx, ses.ID, 95000, nil); err != nil {
		t.Fatal(err)
	}
	if tok, win := sampleRow(t, s, ses.ID); tok.Int64 != 95000 || win.Int64 != 200000 {
		t.Fatalf("row = %v/%v, want 95000/200000", tok, win)
	}
}

// museContextFake is a Muse-kind adapter whose session log reader returns a canned sample.
type museContextFake struct {
	*adapter.Fake
	sample adapter.ContextSample
	ok     bool
	gotID  string
}

func (m *museContextFake) SessionContext(providerSessionID string) (adapter.ContextSample, bool) {
	m.gotID = providerSessionID
	return m.sample, m.ok
}

func TestMuseIdleRecordsSample(t *testing.T) {
	s, tm, fa := newStore(t)
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "MuseCtx", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET kind = 'muse' WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET provider_session_id = 'prov-1', state = 'running', context_window = 200000 WHERE id = ?`, ses.ID); err != nil {
		t.Fatal(err)
	}
	fake := &museContextFake{Fake: fa, sample: adapter.ContextSample{Tokens: 157990}, ok: true}
	s.Adapters[kinds.Muse] = fake
	panes(tm, Pane{Session: ses.TmuxName})
	if err := s.WakeDue(ctx); err != nil {
		t.Fatal(err)
	}
	if fake.gotID != "prov-1" {
		t.Fatalf("reader got provider session %q, want prov-1", fake.gotID)
	}
	// the launch limit already in context_window survives a window-less sample
	if tok, win := sampleRow(t, s, ses.ID); tok.Int64 != 157990 || win.Int64 != 200000 {
		t.Fatalf("row = %v/%v, want 157990/200000", tok, win)
	}
}

func TestStartSessionSetsNativeCapForLowTokenOrchestratorsOnly(t *testing.T) {
	s, _, fa := newStore(t)
	ctx := context.Background()
	orch, w, _ := worker(t, s)
	for _, k := range []AgentKind{Claude, Codex, Muse, Cursor, Agy} {
		s.Adapters[k] = fa
	}
	want := map[AgentKind]int{Claude: 200000, Codex: 150000, Muse: 200000, Cursor: 200000, Agy: 0}
	gen := 20
	for kind, cap := range want {
		mustExec(t, s.DB, `UPDATE agents SET kind = ? WHERE id IN (?, ?)`, string(kind), orch.ID, w.ID)
		for _, tc := range []struct {
			name  string
			on    bool
			agent Agent
			want  int
		}{
			{"orchestrator on", true, orch, cap},
			{"orchestrator off", false, orch, 0},
			{"worker under low-token orchestrator", true, w, 0},
		} {
			mustExec(t, s.DB, `UPDATE agents SET low_token = ? WHERE id = ?`, tc.on, orch.ID)
			a, err := s.AgentByID(ctx, tc.agent.ID)
			if err != nil {
				t.Fatal(err)
			}
			gen++
			ses, err := s.startSession(ctx, a, 1, gen, false, "prov", "")
			if err != nil {
				t.Fatalf("%s %s: %v", kind, tc.name, err)
			}
			if fa.LastSpec.LowTokenCap != tc.want {
				t.Errorf("%s %s: LowTokenCap = %d, want %d", kind, tc.name, fa.LastSpec.LowTokenCap, tc.want)
			}
			var win sql.NullInt64
			if err := s.DB.QueryRowContext(ctx, `SELECT context_window FROM sessions WHERE id = ?`, ses.ID).Scan(&win); err != nil {
				t.Fatal(err)
			}
			wantWin := kind == Muse && tc.want > 0
			if win.Valid != wantWin || (wantWin && win.Int64 != 200000) {
				t.Errorf("%s %s: context_window = %v, want 200000 only for a capped muse launch", kind, tc.name, win)
			}
		}
	}
}

// budgetFixture is a running low-token orchestrator (fake kind: backstop 300k)
// with one running child worker.
func budgetFixture(t *testing.T) (s *Store, orch, child Agent, orchSes Session) {
	t.Helper()
	s, tm, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 8)
	seedEpicWithTask(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.DB, `UPDATE sessions SET state = 'running' WHERE agent_id = ?`, orch.ID)
	mustExec(t, s.DB, `UPDATE agents SET low_token = 1 WHERE id = ?`, orch.ID)
	child = spawnRunning(t, s, tm, "TASK-1", orch.ID)
	orchSes, err = s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, orch, child, orchSes
}

func strikes(t *testing.T, s *Store, sessionID string) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow(`SELECT context_strikes FROM sessions WHERE id = ?`, sessionID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func budgetOps(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.DB.Query(`SELECT request_key FROM agent_operations WHERE request_key LIKE 'budget:%' ORDER BY request_key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	return keys
}

func TestRecordContextSampleCountsStrikesForLowTokenOrchestratorsOnly(t *testing.T) {
	s, _, child, ses := budgetFixture(t)
	ctx := context.Background()
	back := BackstopTokens("fake")
	for i, tc := range []struct{ tokens, want int }{{back, 1}, {back + 5, 2}, {back - 1, 0}, {back, 1}} {
		if err := s.RecordContextSample(ctx, ses.ID, tc.tokens, nil); err != nil {
			t.Fatal(err)
		}
		if got := strikes(t, s, ses.ID); got != tc.want {
			t.Fatalf("sample %d (%d tokens): strikes = %d, want %d", i, tc.tokens, got, tc.want)
		}
	}
	// A worker is never counted.
	cSes, _ := s.LatestSession(ctx, child.ID)
	if err := s.RecordContextSample(ctx, cSes.ID, back*2, nil); err != nil {
		t.Fatal(err)
	}
	if got := strikes(t, s, cSes.ID); got != 0 {
		t.Fatalf("worker strikes = %d, want 0", got)
	}
	// Mode off: no strikes.
	mustExec(t, s.DB, `UPDATE agents SET low_token = 0 WHERE id = ?`, ses.AgentID)
	mustExec(t, s.DB, `UPDATE sessions SET context_strikes = 0 WHERE id = ?`, ses.ID)
	if err := s.RecordContextSample(ctx, ses.ID, back, nil); err != nil {
		t.Fatal(err)
	}
	if got := strikes(t, s, ses.ID); got != 0 {
		t.Fatalf("mode-off strikes = %d, want 0", got)
	}
}

func TestBackstopTokens(t *testing.T) {
	for kind, want := range map[string]int{"claude": 300000, "codex": 225000, "muse": 300000, "cursor": 300000, "agy": 300000} {
		if got := BackstopTokens(kind); got != want {
			t.Errorf("BackstopTokens(%s) = %d, want %d", kind, got, want)
		}
	}
}

func TestEnforceContextBudgetHandsOffAfterTwoSamplesDespiteLiveChildren(t *testing.T) {
	s, orch, _, ses := budgetFixture(t)
	ctx := context.Background()
	back := BackstopTokens("fake")
	if err := s.RecordContextSample(ctx, ses.ID, back, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.EnforceContextBudget(ctx); err != nil {
		t.Fatal(err)
	}
	if ops := budgetOps(t, s); len(ops) != 0 {
		t.Fatalf("one sample triggered a handoff: %v", ops)
	}
	if err := s.RecordContextSample(ctx, ses.ID, back, nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ { // idempotent across ticks
		if err := s.EnforceContextBudget(ctx); err != nil {
			t.Fatal(err)
		}
	}
	ops := budgetOps(t, s)
	if len(ops) != 1 || ops[0] != "budget:"+ses.ID {
		t.Fatalf("budget ops = %v, want exactly budget:%s", ops, ses.ID)
	}
	var mode string
	if err := s.DB.QueryRow(`SELECT mode FROM agent_operations WHERE request_key = ? AND agent_id = ?`, "budget:"+ses.ID, orch.ID).Scan(&mode); err != nil || mode != "handoff" {
		t.Fatalf("mode = %q err=%v, want handoff", mode, err)
	}
}

func TestEnforceContextBudgetExclusions(t *testing.T) {
	for name, block := range map[string]func(t *testing.T, s *Store, orch, child Agent, ses Session){
		"mode off": func(t *testing.T, s *Store, orch, _ Agent, _ Session) {
			mustExec(t, s.DB, `UPDATE agents SET low_token = 0 WHERE id = ?`, orch.ID)
		},
		"open request": func(t *testing.T, s *Store, orch, _ Agent, ses Session) {
			mustExec(t, s.DB, `INSERT INTO requests (id, kind, is_hitl, agent_id, session_id, item_id, prompt, state, created_at)
				VALUES ('req_1', 'question', 1, ?, ?, ?, 'Which DB?', 'open', 1)`, orch.ID, ses.ID, orch.ItemID)
		},
		"operation in flight on the orchestrator": func(t *testing.T, s *Store, orch, _ Agent, ses Session) {
			mustExec(t, s.DB, `INSERT INTO agent_operations (id, agent_id, mode, phase, request_key, session_id, generation, created_at, updated_at)
				VALUES ('op_x', ?, 'recover', 'queued', 'k1', ?, ?, 1, 1)`, orch.ID, ses.ID, ses.Generation)
		},
		"operation in flight on a direct child": func(t *testing.T, s *Store, _, child Agent, _ Session) {
			cSes, err := s.LatestSession(context.Background(), child.ID)
			if err != nil {
				t.Fatal(err)
			}
			mustExec(t, s.DB, `INSERT INTO agent_operations (id, agent_id, mode, phase, request_key, session_id, generation, created_at, updated_at)
				VALUES ('op_c', ?, 'handoff', 'preserving', 'k2', ?, ?, 1, 1)`, child.ID, cSes.ID, cSes.Generation)
		},
		"unanswered question": func(t *testing.T, s *Store, orch, _ Agent, _ Session) {
			mustExec(t, s.DB, `INSERT INTO messages (id, seq, kind, origin, from_agent_id, to_agent_id, root_item_id, payload_json, state, created_at)
				VALUES ('msg_q', 9001, 'question', 'agent', ?, ?, ?, '{}', 'acked', 1)`, orch.ID, orch.ID, orch.RootItemID)
		},
		"second trigger in the same session": func(t *testing.T, s *Store, orch, _ Agent, ses Session) {
			mustExec(t, s.DB, `INSERT INTO agent_operations (id, agent_id, mode, phase, request_key, session_id, generation, created_at, updated_at)
				VALUES ('op_b', ?, 'handoff', 'cancelled', ?, ?, ?, 1, 1)`, orch.ID, "budget:"+ses.ID, ses.ID, ses.Generation)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, orch, child, ses := budgetFixture(t)
			ctx := context.Background()
			block(t, s, orch, child, ses)
			back := BackstopTokens("fake")
			for i := 0; i < 2; i++ {
				if err := s.RecordContextSample(ctx, ses.ID, back, nil); err != nil {
					t.Fatal(err)
				}
			}
			before := len(budgetOps(t, s))
			if err := s.EnforceContextBudget(ctx); err != nil {
				t.Fatal(err)
			}
			if after := len(budgetOps(t, s)); after != before {
				t.Fatalf("budget ops went %d -> %d, want no new handoff", before, after)
			}
		})
	}
}

func TestReconcileRunsTheBudgetPass(t *testing.T) {
	s, _, _, ses := budgetFixture(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := s.RecordContextSample(ctx, ses.ID, BackstopTokens("fake"), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if ops := budgetOps(t, s); len(ops) != 1 {
		t.Fatalf("budget ops after Reconcile = %v, want one", ops)
	}
}

func TestBudgetHandoffGivesTheSuccessorTheBudgetNote(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	mustExec(t, s.DB, `UPDATE agents SET low_token = 1 WHERE id = ?`, orch.ID)
	mustExec(t, s.DB, `UPDATE sessions SET state = 'running' WHERE agent_id = ?`, orch.ID)
	ses, err := s.LatestSession(ctx, orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	panes(tm)
	for i := 0; i < 2; i++ {
		if err := s.RecordContextSample(ctx, ses.ID, 312400, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EnforceContextBudget(ctx); err != nil {
		t.Fatal(err)
	}
	const want = "Your predecessor was handed off because its context passed 312k tokens (low-token budget 300k). " +
		"Recover from checkpoints and artifacts; don't re-read what they already cover."
	var note string
	if err := s.DB.QueryRowContext(ctx, `SELECT note FROM agent_operations WHERE request_key = ?`, "budget:"+ses.ID).Scan(&note); err != nil {
		t.Fatal(err)
	}
	if note != want {
		t.Fatalf("op note = %q, want %q", note, want)
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE to_agent_id = ? AND kind = 'assignment_update' AND payload_json LIKE ?`,
		orch.ID, "%passed 312k tokens (low-token budget 300k)%").Scan(&n); err != nil || n != 1 {
		t.Fatalf("successor notes = %d err=%v, want 1", n, err)
	}
	got, err := s.AgentByID(ctx, orch.ID)
	if err != nil || got.LowToken == nil || !*got.LowToken {
		t.Fatalf("low_token override lost: %v %v", got.LowToken, err)
	}
}
