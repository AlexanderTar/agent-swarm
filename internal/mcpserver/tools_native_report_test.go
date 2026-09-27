package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

func nativeReportMCPFixture(t *testing.T, kind runtime.AgentKind) (*Server, Caller, string, string, string) {
	t.Helper()
	s := newTestServer(t)
	ctx := context.Background()
	key, agent, _, err := s.RT.StartSpike(ctx, runtime.SpikeInput{Name: "Native report MCP", Intent: "feature", Kind: runtime.Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.RT.LatestSession(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RT.DB.ExecContext(ctx, `UPDATE agents SET kind = ? WHERE id = ?`, kind, agent.ID); err != nil {
		t.Fatal(err)
	}
	caller := Caller{SessionID: ses.ID, AgentID: agent.ID, AgentName: agent.Name, Role: runtime.RoleOrchestrator, SpikeOrchestrator: true}
	path := filepath.Join(t.TempDir(), "spec.md")
	if err := os.WriteFile(path, []byte("# Spec\n\n## Data model\n\nRows.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	artifactOut, err := s.call(ctx, caller, "swarm_artifact", `{"op":"register","item":"`+key+`","kind":"spec","path":"`+path+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	var artifact struct {
		ArtifactID string `json:"artifact_id"`
		Sections   []struct {
			ID string `json:"id"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(mustJSON(artifactOut), &artifact); err != nil || len(artifact.Sections) != 1 {
		t.Fatalf("artifact = %+v, err = %v", artifact, err)
	}
	askOut, err := s.call(ctx, caller, "swarm_ask", `{"kind":"approval","prompt":"Create rows.","artifact":"`+artifact.ArtifactID+`","section":"`+artifact.Sections[0].ID+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	var ask struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(mustJSON(askOut), &ask); err != nil || ask.RequestID == "" {
		t.Fatalf("ask = %+v, err = %v", ask, err)
	}
	return s, caller, key, path, ask.RequestID
}

func TestNativeAnswerReportsThroughMCPSurface(t *testing.T) {
	for _, kind := range []runtime.AgentKind{runtime.Cursor, runtime.Muse} {
		t.Run(string(kind), func(t *testing.T) {
			for _, tc := range []struct{ name, decision, answer, state, comment string }{
				{"approve", "approve", "Approve", "approved", ""},
				{"changes", "request_changes", "Request changes: tighten scope", "changes_requested", "tighten scope"},
				{"typed comment", "request_changes", "Please tighten scope", "changes_requested", "Please tighten scope"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					s, caller, _, _, ref := nativeReportMCPFixture(t, kind)
					args, _ := json.Marshal(map[string]string{"kind": "native_answer", "ref": ref, "decision": tc.decision, "answer_text": tc.answer})
					out, err := s.call(context.Background(), caller, "swarm_ask", string(args))
					if err != nil {
						t.Fatal(err)
					}
					var result struct {
						State string `json:"state"`
					}
					json.Unmarshal(mustJSON(out), &result)
					if result.State != tc.state {
						t.Fatalf("MCP state = %s, want %s", result.State, tc.state)
					}
					req, err := s.RT.RequestByID(context.Background(), ref)
					if err != nil || req.ResponseText != tc.comment {
						t.Fatalf("request = %+v, err = %v", req, err)
					}
					wire, err := s.RT.RequestWireByID(context.Background(), ref)
					if err != nil || wire.ApprovalEvidence == nil || *wire.ApprovalEvidence != runtime.EvidenceAgentReported {
						t.Fatalf("evidence = %v, err = %v", wire.ApprovalEvidence, err)
					}
					if _, err := s.call(context.Background(), caller, "swarm_ask", string(args)); err == nil {
						t.Fatal("duplicate MCP answer accepted")
					}
				})
			}
			for _, tc := range []struct{ name, answer string }{{"cancelled", ""}, {"blank", "   "}} {
				t.Run(tc.name, func(t *testing.T) {
					s, caller, _, _, ref := nativeReportMCPFixture(t, kind)
					args, _ := json.Marshal(map[string]string{"kind": "native_answer", "ref": ref, "decision": "approve", "answer_text": tc.answer})
					if _, err := s.call(context.Background(), caller, "swarm_ask", string(args)); err == nil {
						t.Fatal("empty MCP answer accepted")
					}
					req, _ := s.RT.RequestByID(context.Background(), ref)
					if req.State != "open" {
						t.Fatalf("state after cancellation = %s", req.State)
					}
				})
			}
			t.Run("stale", func(t *testing.T) {
				s, caller, key, path, ref := nativeReportMCPFixture(t, kind)
				if err := os.WriteFile(path, []byte("# Spec\n\n## Data model\n\nRevised rows.\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if _, err := s.call(context.Background(), caller, "swarm_artifact", `{"op":"revise","item":"`+key+`","kind":"spec","path":"`+path+`"}`); err != nil {
					t.Fatal(err)
				}
				_, err := s.call(context.Background(), caller, "swarm_ask", `{"kind":"native_answer","ref":"`+ref+`","decision":"approve","answer_text":"Approve"}`)
				if err == nil || !strings.Contains(err.Error(), "stale") {
					t.Fatalf("stale MCP answer err = %v", err)
				}
				req, _ := s.RT.RequestByID(context.Background(), ref)
				if req.State == "approved" {
					t.Fatal("stale MCP answer approved")
				}
			})
		})
	}
}
