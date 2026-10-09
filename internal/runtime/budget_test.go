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
