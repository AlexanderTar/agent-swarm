package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
)

func TestResolveAgentForModelPrefersEnabledThenFallsBackToAnyKind(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()

	if k, ok := s.resolveAgentForModel(ctx, "fake-1"); !ok || k != Fake {
		t.Fatalf("fake-1 = %v/%v, want fake", k, ok)
	}
	if _, ok := s.resolveAgentForModel(ctx, ""); ok {
		t.Fatal("an empty model id resolved to a kind")
	}
	if _, ok := s.resolveAgentForModel(ctx, "no-such-model"); ok {
		t.Fatal("an unlisted model resolved to a kind")
	}

	// A model only a disabled kind lists still resolves (the caller's
	// preflight, not this lookup, decides whether that kind may run).
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO model_catalog
		(agent_kind, agent_version, models_json, default_model, source, fetched_at, attempted_at)
		VALUES ('codex','1','[{"id":"gpt-x","label":"GPT X","efforts":[],"default_effort":"","effort_encoding":"flag","advisor_capable":false}]','gpt-x','test',1,1)`); err != nil {
		t.Fatal(err)
	}
	if k, ok := s.resolveAgentForModel(ctx, "gpt-x"); !ok || k != Codex {
		t.Fatalf("gpt-x = %v/%v, want codex via the all-kinds fallback", k, ok)
	}

	s.Catalog = nil
	if _, ok := s.resolveAgentForModel(ctx, "fake-1"); ok {
		t.Fatal("resolved a model with no catalog service")
	}
}

func TestRoleDefaultKindModelFallbacks(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	// settings.Put refuses the fake adapter, so write the rows directly.
	put := func(enabled []AgentKind, roles map[Role]settings.RoleDefault) {
		t.Helper()
		for key, v := range map[string]any{"enabled_agents": enabled, "roles": roles} {
			raw, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB.ExecContext(ctx, `INSERT INTO settings (key, value_json, updated_at) VALUES (?, ?, 1)
				ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json`, key, string(raw)); err != nil {
				t.Fatal(err)
			}
		}
	}

	// The role's configured default wins over the first enabled agent.
	put([]AgentKind{Fake}, map[Role]settings.RoleDefault{RoleCoder: {Agent: Fake, Model: "fake-1"}})
	if k, m := s.roleDefaultKindModel(ctx, RoleCoder, "", ""); k != Fake || m != "fake-1" {
		t.Fatalf("configured default = %s/%s, want fake/fake-1", k, m)
	}
	// A configured model the catalog no longer lists falls back to the catalog default.
	put([]AgentKind{Fake}, map[Role]settings.RoleDefault{RoleCoder: {Agent: Fake, Model: "retired-model"}})
	if k, m := s.roleDefaultKindModel(ctx, RoleCoder, "", ""); k != Fake || m != "fake-1" {
		t.Fatalf("retired default = %s/%s, want fake/fake-1", k, m)
	}
	// No default for the role: the first enabled agent; explicit values are kept.
	put([]AgentKind{Fake}, map[Role]settings.RoleDefault{})
	if k, m := s.roleDefaultKindModel(ctx, RoleReviewer, "", ""); k != Fake || m != "fake-1" {
		t.Fatalf("first enabled = %s/%s, want fake/fake-1", k, m)
	}
	if k, m := s.roleDefaultKindModel(ctx, RoleReviewer, Fake, "explicit"); k != Fake || m != "explicit" {
		t.Fatalf("explicit = %s/%s, want fake/explicit", k, m)
	}
	// Nothing enabled at all falls back to the fake adapter.
	put(nil, map[Role]settings.RoleDefault{})
	if k, _ := s.roleDefaultKindModel(ctx, RoleReviewer, "", ""); k != Fake {
		t.Fatalf("nothing enabled = %s, want fake", k)
	}
}

func TestWorkerRoleOverridesSetRestoreAndRefusals(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, w, _ := worker(t, s)
	override := map[Role]settings.RoleDefault{RoleCoder: {Agent: Fake, Model: "fake-1", Reason: "user asked"}}
	badRequest := func(err error) bool {
		var ie *items.Error
		return errors.As(err, &ie) && ie.Code == items.CodeBadRequest
	}

	if err := s.SetWorkerRoleOverrides(ctx, orch.ID, nil); err != nil {
		t.Fatalf("empty overrides = %v, want a no-op", err)
	}
	if err := s.SetWorkerRoleOverrides(ctx, "agt_missing", override); err == nil {
		t.Fatal("overrides on an unknown agent succeeded")
	}
	if err := s.SetWorkerRoleOverrides(ctx, w.ID, override); !badRequest(err) || !strings.Contains(err.Error(), "orchestrators only") {
		t.Fatalf("worker target err = %v, want the orchestrators-only refusal", err)
	}
	// The orchestrator role itself, and roles outside OverridableRoles, are refused wholesale.
	for _, bad := range []Role{RoleOrchestrator, RoleAdvisor, Role("bogus")} {
		err := s.SetWorkerRoleOverrides(ctx, orch.ID, map[Role]settings.RoleDefault{RoleCoder: override[RoleCoder], bad: override[RoleCoder]})
		if !badRequest(err) || !strings.Contains(err.Error(), "must be one of") || strings.Contains(err.Error(), "orchestrator,") {
			t.Fatalf("role %q err = %v", bad, err)
		}
	}
	// A disabled agent fails validation, and nothing is written.
	if err := s.SetWorkerRoleOverrides(ctx, orch.ID, map[Role]settings.RoleDefault{RoleCoder: {Agent: Codex, Model: "gpt-x"}}); err == nil {
		t.Fatal("override to a disabled agent accepted")
	}
	got, err := s.agentByID(ctx, orch.ID)
	if err != nil || len(got.RoleOverrides) != 0 {
		t.Fatalf("overrides after refusals = %v err=%v, want none", got.RoleOverrides, err)
	}

	if err := s.SetWorkerRoleOverrides(ctx, orch.ID, override); err != nil {
		t.Fatal(err)
	}
	got, _ = s.agentByID(ctx, orch.ID)
	if rd := got.RoleOverrides[RoleCoder]; rd.Agent != Fake || rd.Reason != "user asked" {
		t.Fatalf("overrides = %+v", got.RoleOverrides)
	}
	// A second call merges: roles not named stay.
	if err := s.SetWorkerRoleOverrides(ctx, orch.ID, map[Role]settings.RoleDefault{RoleReviewer: {Agent: Fake, Model: "fake-1"}}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.agentByID(ctx, orch.ID)
	if len(got.RoleOverrides) != 2 {
		t.Fatalf("merged overrides = %v, want coder and reviewer", got.RoleOverrides)
	}
	// Restore writes verbatim, and an empty map clears the column.
	if err := s.RestoreRoleOverrides(ctx, orch.ID, override); err != nil {
		t.Fatal(err)
	}
	got, _ = s.agentByID(ctx, orch.ID)
	if len(got.RoleOverrides) != 1 {
		t.Fatalf("restored overrides = %v, want only coder", got.RoleOverrides)
	}
	if err := s.RestoreRoleOverrides(ctx, orch.ID, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = s.agentByID(ctx, orch.ID)
	if len(got.RoleOverrides) != 0 {
		t.Fatalf("cleared overrides = %v", got.RoleOverrides)
	}
	if len(workerOverridableRoles()) != len(OverridableRoles)-1 {
		t.Fatal("workerOverridableRoles must be OverridableRoles minus the orchestrator")
	}
}
