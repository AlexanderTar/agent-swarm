package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

// Batch 3: swarm_control gains the handoff op. Only the controlling
// orchestrator (self or a live ancestor in the same root) may hand an agent
// off; a different root is refused, and so is a same-root agent outside the
// caller's own subtree.
func TestControlHandoffReturnsTheOperation(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	worker := spawnWorker(t, s, seed)
	out, err := s.call(ctx, seed.Caller, "swarm_control",
		`{"target":"`+worker.Name+`","action":"handoff","request_id":"h1"}`)
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		OperationID string `json:"operation_id"`
		Mode        string `json:"mode"`
		Phase       string `json:"phase"`
	}
	json.Unmarshal(mustJSON(out), &res)
	if res.OperationID == "" || res.Mode != "handoff" || res.Phase == "" {
		t.Fatalf("handoff result = %s, want operation_id/mode/phase", mustJSON(out))
	}
}

func TestControlHandoffRefusesAnotherRoot(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	stranger := seedOtherWorkerName(t, s)
	if _, err := s.call(ctx, seed.Caller, "swarm_control",
		`{"target":"`+stranger+`","action":"handoff"}`); err == nil {
		t.Fatal("handoff of another root's agent must be refused")
	} else if !strings.Contains(err.Error(), "outside your subtree") {
		t.Fatalf("err = %v, want the subtree refusal", err)
	}
}

func TestControlHandoffRequiresAnAncestorInTheSameRoot(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	// A coder in the same root is not a controlling orchestrator: even for
	// its sibling worker the handoff must be refused, not just unseen.
	worker := spawnWorker(t, s, seed)
	peer, _, err := s.RT.Spawn(ctx, runtime.SpawnInput{
		ItemKey: seed.TaskKey, Role: runtime.RoleCoder, ParentAgentID: seed.Caller.AgentID,
		Brief: runtime.BriefInput{Objective: "Peer work."},
	})
	if err != nil {
		t.Fatal(err)
	}
	peerSes, err := s.RT.LatestSession(ctx, peer.ID)
	if err != nil {
		t.Fatal(err)
	}
	peerCaller := Caller{SessionID: peerSes.ID, AgentID: peer.ID, AgentName: peer.Name, Role: peer.Role}
	if _, err := s.call(ctx, peerCaller, "swarm_control",
		`{"target":"`+worker.Name+`","action":"handoff"}`); err == nil {
		t.Fatal("a non-orchestrator must not hand off even a same-root agent")
	}
}
