package runtime

import (
	"context"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
)

// Rows cached before default_effort was filled hold "" — the read-time fill must cover them.
const claudeEffortCatalog = `[
	{"id":"claude-sonnet-5-5","aliases":["sonnet"],"label":"Claude Sonnet 5.5","efforts":["low","medium","high","xhigh","max"],"default_effort":"","effort_encoding":"flag","advisor_capable":true},
	{"id":"claude-opus-4-5","aliases":["opus"],"label":"Claude Opus 4.5","efforts":["low","medium"],"default_effort":"","effort_encoding":"flag","advisor_capable":true},
	{"id":"claude-haiku-4-5","aliases":["haiku"],"label":"Claude Haiku 4.5","efforts":[],"default_effort":"","effort_encoding":"flag","advisor_capable":false}
]`

func claudeEffortStore(t *testing.T) (*Store, *adapter.Fake) {
	t.Helper()
	s, _ := newStoreWithFallback(t)
	if _, err := s.DB.ExecContext(context.Background(),
		`UPDATE model_catalog SET models_json = ? WHERE agent_kind = 'claude'`, claudeEffortCatalog); err != nil {
		t.Fatal(err)
	}
	seedEpicWithTask(t, s)
	return s, s.Adapters[Claude].(*adapter.Fake)
}

func rowEffort(t *testing.T, s *Store, id string) string {
	t.Helper()
	var e string
	if err := s.DB.QueryRowContext(context.Background(), `SELECT COALESCE(effort, '') FROM agents WHERE id = ?`, id).Scan(&e); err != nil {
		t.Fatal(err)
	}
	return e
}

var effortCases = []struct{ model, want string }{
	{"sonnet", "high"}, // high is supported
	{"opus", "medium"}, // high is not: the top supported level
	{"haiku", ""},      // no efforts: never pass --effort
}

// A Claude agent with no effort set must still launch with a concrete level,
// and the agent row must store it.
func TestSpawnPassesAndStoresAConcreteClaudeEffort(t *testing.T) {
	for _, c := range effortCases {
		t.Run(c.model, func(t *testing.T) {
			s, fa := claudeEffortStore(t)
			a, _, err := s.Spawn(context.Background(), SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Claude,
				Model: c.model, Brief: BriefInput{Objective: "do it"}})
			if err != nil {
				t.Fatal(err)
			}
			if fa.LastSpec.Effort != c.want {
				t.Errorf("Spec.Effort = %q, want %q", fa.LastSpec.Effort, c.want)
			}
			if got := rowEffort(t, s, a.ID); got != c.want {
				t.Errorf("row effort = %q, want %q", got, c.want)
			}
		})
	}
}

// A row that already has no effort (spawned before this fix) is resolved at the
// Spec seam on resume, wake and replacement too.
func TestRelaunchOfAnEmptyEffortRowPassesTheResolvedEffort(t *testing.T) {
	for _, c := range effortCases {
		t.Run(c.model, func(t *testing.T) {
			s, fa := claudeEffortStore(t)
			ctx := context.Background()
			a, _, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-1", Role: RoleCoder, Kind: Claude,
				Model: c.model, Brief: BriefInput{Objective: "do it"}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET effort = '' WHERE id = ?`, a.ID); err != nil {
				t.Fatal(err)
			}
			a.Effort = ""
			if _, err := s.startSession(ctx, a, 1, 2, true, "prov", "resume"); err != nil {
				t.Fatal(err)
			}
			if fa.LastSpec.Effort != c.want {
				t.Errorf("resume Spec.Effort = %q, want %q", fa.LastSpec.Effort, c.want)
			}
		})
	}
}

// Only Claude is resolved: cursor's bare "default" slug level and other kinds keep their behaviour.
func TestResolveLaunchEffortLeavesOtherKindsAlone(t *testing.T) {
	s, _ := claudeEffortStore(t)
	ctx := context.Background()
	if got := s.resolveLaunchEffort(ctx, Claude, "sonnet", "max"); got != "max" {
		t.Errorf("explicit effort = %q, want max", got)
	}
	if got := s.resolveLaunchEffort(ctx, Claude, "unknown-model", ""); got != "" {
		t.Errorf("unknown model = %q, want empty", got)
	}
	if got := s.resolveLaunchEffort(ctx, Codex, "gpt-6-astra", ""); got != "" {
		t.Errorf("codex = %q, want empty", got)
	}
	if got := s.resolveLaunchEffort(ctx, Cursor, "gpt-5.3-codex", "default"); got != "default" {
		t.Errorf("cursor bare default = %q, want default", got)
	}
}
