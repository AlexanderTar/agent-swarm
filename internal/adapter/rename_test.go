package adapter

import (
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/kinds"
)

func TestRenameCommand(t *testing.T) {
	cases := map[kinds.AgentKind]string{
		kinds.Codex:  "/rename my-agent",
		kinds.Agy:    "/rename my-agent",
		kinds.Cursor: "/rename my-agent",
		kinds.Muse:   "/name my-agent",
		kinds.Fake:   "/rename my-agent",
	}
	for k, want := range cases {
		a, err := New(k, testDeps(t))
		if err != nil {
			t.Fatalf("%s: %v", k, err)
		}
		r, ok := a.(SessionRenamer)
		if !ok {
			t.Fatalf("%s: does not implement SessionRenamer", k)
		}
		got, ok := r.RenameCommand("my-agent")
		if !ok || got != want {
			t.Errorf("%s: got %q,%v want %q", k, got, ok, want)
		}
	}
}

func TestClaudeHasNoRenameCommand(t *testing.T) {
	a, err := New(kinds.Claude, testDeps(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.(SessionRenamer); ok {
		t.Fatal("claude must not implement SessionRenamer (uses hook sessionTitle)")
	}
}
