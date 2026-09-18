package notify

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db/dbtest"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

// Every §17.5 row is present with its level, title, body and category.
func TestRulesCoverSection175(t *testing.T) {
	want := map[string][4]string{
		"agent.accepted":          {"info", "Task accepted", "{name} started {KEY}: {title}.", "swarm.info"},
		"item.completed":          {"info", "Task completed", "{KEY}: {title} is complete.", "swarm.info"},
		"item.created":            {"info", "Epic ready", "{SPIKE-KEY} produced {ROOT-KEY}: {title}. Start an orchestrator when you're ready.", "swarm.item"},
		"item.created.bug":        {"info", "Bug ready", "{SPIKE-KEY} produced {ROOT-KEY}: {title}. Start an orchestrator when you're ready.", "swarm.item"},
		"agent.queued":            {"info", "Agent queued", "{name} starts when an agent slot becomes available.", "swarm.info"},
		"agent.paused":            {"attention", "Agent paused", "{name} is paused on {KEY}.", "swarm.agent"},
		"agent.interrupted":       {"attention", "Agent stopped", "{name} stopped before {KEY} finished.", "swarm.agent"},
		"agent.retried":           {"info", "Agent retrying", "{name} started attempt {N} on {KEY}.", "swarm.info"},
		"agent.failed":            {"attention", "Agent failed", "{name} couldn't finish {KEY}. Review the error.", "swarm.agent"},
		"agent.crashed":           {"attention", "Agent crashed", "{name} exited unexpectedly on {KEY}.", "swarm.agent"},
		"agent.stale":             {"attention", "No recent activity", "{name} has been quiet for 30 minutes on {KEY}.", "swarm.agent"},
		"agent.undeliverable":     {"attention", "Couldn't deliver messages", "{name} hasn't picked up {N} message(s).", "swarm.agent"},
		"agent.preflight_failed":  {"attention", "Couldn't start agent", "{reason}", "swarm.info"},
		"worktree.retained":       {"attention", "Worktree kept", "The worktree for {ROOT-KEY} has {detail} and was kept.", "swarm.info"},
		"tmux.unknown":            {"attention", "Unknown tmux session", "{name} is running but Swarm has no record of it.", "swarm.info"},
		"request.confirm_repos":   {"action", "Confirm repositories", "{KEY}: {name} proposes {N} repositories{expansion}.", "swarm.approval"},
		"request.close_spike":     {"action", "Close spike?", "{KEY}: {name} found nothing to build ({resolution}).", "swarm.approval"},
		"request.question":        {"action", "Answer needed", "{KEY}: {prompt}", "swarm.question"},
		"request.approve_section": {"action", "Section approval needed", `{KEY}: Review "{section}".`, "swarm.approval"},
		"request.approve_plan":    {"action", "Plan approval needed", "{KEY}: Review the proposed implementation plan.", "swarm.approval"},
		"request.approve_report":  {"action", "Report approval needed", "{KEY}: Review the root cause and fix plan.", "swarm.approval"},
		"request.accept_epic":     {"action", "Epic acceptance needed", "{KEY}: Review completed work and accept the epic.", "swarm.approval"},
		"request.accept_fix":      {"action", "Fix acceptance needed", "{KEY}: Review the fix and accept it.", "swarm.approval"},
	}
	for kind, w := range want {
		r, ok := Rules[kind]
		if !ok {
			t.Errorf("missing rule %q", kind)
			continue
		}
		if r.Level != w[0] || r.Title != w[1] || r.Body != w[2] || r.Category != w[3] {
			t.Errorf("%s = %+v, want %v", kind, r, w)
		}
	}
	if len(Rules) != len(want) {
		t.Errorf("Rules has %d entries, §17.5 has %d", len(Rules), len(want))
	}
}

func TestRenderSubstitutesAndRefusesAMissingArgument(t *testing.T) {
	r, err := Render("agent.paused", map[string]string{"name": "login-form-coder", "KEY": "TASK-101"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Body != "login-form-coder is paused on TASK-101." {
		t.Fatalf("body = %q", r.Body)
	}
	if _, err := Render("agent.paused", map[string]string{"name": "x"}); err == nil {
		t.Fatal("a missing argument must be an error, not a literal {KEY} in a banner")
	}
	if _, err := Render("nope.kind", nil); err == nil {
		t.Fatal("an unknown kind is an error")
	}
}

func TestRaiseWritesTheRowAndPublishesTheEvent(t *testing.T) {
	d := dbtest.Open(t)
	at := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	ev := events.New(d, func() time.Time { return at })
	s := &Service{DB: d, Events: ev, Now: func() time.Time { return at }, Log: func(string, ...any) {}}
	ctx := context.Background()
	if err := s.Raise(ctx, nil, runtime.NotifyInput{Kind: "agent.paused", AgentName: "coder-1",
		ItemKey: "TASK-1", Args: map[string]string{"name": "coder-1", "KEY": "TASK-1"}}); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx, true, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Title != "Agent paused" || list[0].Level != "attention" {
		t.Fatalf("list = %+v", list)
	}
	evs, _ := ev.After(ctx, 0, 10)
	var payload json.RawMessage
	for _, e := range evs {
		if e.Type == events.NotificationCreated {
			payload = e.Payload
		}
	}
	var wire map[string]any
	json.Unmarshal(payload, &wire)
	for _, k := range []string{"id", "level", "kind", "title", "body", "agent_name", "item_key",
		"request_id", "read_at", "created_at"} {
		if _, ok := wire[k]; !ok {
			t.Errorf("notification.created is missing %q: %s", k, payload)
		}
	}
}

func TestDedupWithinThirtySeconds(t *testing.T) {
	d := dbtest.Open(t)
	at := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	s := &Service{DB: d, Events: events.New(d, func() time.Time { return at }),
		Now: func() time.Time { return at }, Log: func(string, ...any) {}}
	ctx := context.Background()
	in := runtime.NotifyInput{Kind: "agent.stale", AgentName: "coder-1", ItemKey: "TASK-1",
		Args: map[string]string{"name": "coder-1", "KEY": "TASK-1"}}
	s.Raise(ctx, nil, in)
	at = at.Add(29 * time.Second) // D-note: time.Time has no Advance; brief typo, fixed.
	s.Raise(ctx, nil, in)
	if n, _ := s.Unread(ctx); n != 1 {
		t.Fatalf("unread = %d, want 1 (deduplicated)", n)
	}
	at = at.Add(2 * time.Second)
	s.Raise(ctx, nil, in)
	if n, _ := s.Unread(ctx); n != 2 {
		t.Fatalf("unread = %d, want 2 after the window", n)
	}
}

func TestReadAllAndMarkRead(t *testing.T) {
	d := dbtest.Open(t)
	at := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	s := &Service{DB: d, Events: events.New(d, func() time.Time { return at }),
		Now: func() time.Time { return at }, Log: func(string, ...any) {}}
	ctx := context.Background()
	for _, k := range []string{"agent.paused", "agent.crashed"} {
		s.Raise(ctx, nil, runtime.NotifyInput{Kind: k, AgentName: "c", ItemKey: "TASK-1",
			Args: map[string]string{"name": "c", "KEY": "TASK-1"}})
		at = at.Add(time.Minute)
	}
	list, _ := s.List(ctx, true, 10)
	if err := s.MarkRead(ctx, list[0].ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Unread(ctx); n != 1 {
		t.Fatalf("unread = %d", n)
	}
	n, err := s.ReadAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("ReadAll = %d", n)
	}
	if u, _ := s.Unread(ctx); u != 0 {
		t.Fatalf("unread after read-all = %d", u)
	}
}

// The body never leaks an unsubstituted placeholder into a banner.
func TestNoRuleBodyEscapesWithBraces(t *testing.T) {
	for kind := range Rules {
		args := map[string]string{}
		for _, ph := range placeholders(Rules[kind].Body) {
			args[ph] = "X"
		}
		r, err := Render(kind, args)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if strings.ContainsAny(r.Body, "{}") {
			t.Errorf("%s body still has braces: %q", kind, r.Body)
		}
	}
}
