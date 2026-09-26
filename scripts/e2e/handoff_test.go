//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Batch 3 cycle 5: worker handoff end to end on an isolated assignment with
// the fake adapter (never a live swarm). Dirty tracked work plus an untracked
// file plus an outside-git spec plus an unfinished unit go into the handoff;
// out come a signed WIP commit, the manifest bundle with artifact snapshots,
// a dead predecessor token, exactly one fresh session on the same agent
// (same id, name and paths), a recovery read, and continued work.
func TestScenarioHandoffWorker(t *testing.T) {
	h := newHarness(t)
	_, keyID := throwawayGPGKey(t)
	epic := h.materializedEpic(t)
	orch := h.startOrchestrator(t, epic)
	h.mustTool(t, orch, "swarm_checkpoint", map[string]any{"kind": "accepted", "summary": "starting"})
	task := h.firstTask(t, epic)

	repoID, repoPath := e2eRepo(t, h, "repo-handoff")
	gitOut(t, repoPath, "config", "user.signingkey", keyID)
	gitOut(t, repoPath, "config", "commit.gpgsign", "true")
	h.confirmRepo(t, epic, repoID)
	wtOut := h.mustTool(t, orch, "swarm_worktree", map[string]any{
		"op": "create", "repo": repoID, "branch": "task/handoff",
	})
	wtID, _ := wtOut["worktree_id"].(string)
	wtPath, _ := wtOut["path"].(string)
	if wtID == "" || wtPath == "" {
		t.Fatalf("swarm_worktree create returned %+v", wtOut)
	}

	// Outside-git spec: registered in the artifact registry, never committed.
	specPath := filepath.Join(t.TempDir(), "handoff-plan.md")
	if err := os.WriteFile(specPath, []byte("# handoff plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.mustTool(t, orch, "swarm_artifact", map[string]any{
		"op": "register", "item": task, "kind": "spec", "path": specPath,
	})

	spawnOut := h.mustTool(t, orch, "swarm_spawn", map[string]any{
		"item": task, "role": "coder", "agent": "fake", "model": "fake-1",
		"override_reason": "e2e harness pins the fake adapter",
		"brief":           map[string]any{"objective": "Do unit 1 then unit 2."},
		"worktrees":       []map[string]any{{"worktree": wtID, "mode": "rw"}},
	})
	coder, _ := spawnOut["agent"].(string)
	if coder == "" {
		t.Fatalf("swarm_spawn returned %+v", spawnOut)
	}
	h.mustTool(t, coder, "swarm_checkpoint", map[string]any{"kind": "accepted", "summary": "starting"})
	if !h.waitForSessionState(t, coder, "running", 5*time.Second) {
		t.Fatalf("coder session = %s, never reached running", h.sessionState(t, coder))
	}

	// Unfinished unit: progress recorded, nothing completed.
	h.mustTool(t, coder, "swarm_checkpoint", map[string]any{
		"kind": "progress", "summary": "unit 1 done, unit 2 next", "next": []string{"finish unit 2"},
	})

	// Dirty tracked work plus one untracked file.
	if err := os.WriteFile(filepath.Join(wtPath, "owned.go"), []byte("package handoff\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "scratch.go"), []byte("package scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, wtPath, "add", "owned.go")
	gitOut(t, wtPath, "commit", "-q", "-m", "WIP: unit 1")
	wipSHA := strings.TrimSpace(gitOut(t, wtPath, "rev-parse", "HEAD"))
	if out := gitOut(t, wtPath, "verify-commit", "HEAD"); !strings.Contains(out, "Good signature") {
		t.Fatalf("WIP commit is not signed: %s", out)
	}
	if st := gitOut(t, wtPath, "status", "--porcelain"); !strings.Contains(st, "?? scratch.go") {
		t.Fatalf("untracked file must survive preservation, status = %q", st)
	}

	// Cooperative order: pause first so the session is pausing, request the
	// replacement while the predecessor is still live (the walk parks in
	// preserving instead of killing it), then claim the handoff checkpoint,
	// which binds the manifest while the operation is pending.
	since := time.Now()
	h.pause(t, coder, "session")
	if !h.waitForSessionState(t, coder, "pause_requested", 5*time.Second) {
		t.Fatalf("coder session = %s, want pause_requested", h.sessionState(t, coder))
	}
	oldToken := h.sessionToken(t, coder)
	status, raw, err := h.do(http.MethodPost, "/api/agents/"+coder+"/handoff", map[string]any{"request_id": "req-handoff-1"})
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusAccepted {
		t.Fatalf("POST handoff status = %d %s, want 202", status, raw)
	}
	var accepted struct {
		OperationID string `json:"operation_id"`
		Mode        string `json:"mode"`
		Phase       string `json:"phase"`
	}
	if err := json.Unmarshal(raw, &accepted); err != nil {
		t.Fatalf("handoff response %s: %v", raw, err)
	}
	if accepted.OperationID == "" || accepted.Mode != "handoff" {
		t.Fatalf("handoff response = %s, want operation_id/mode", raw)
	}
	if accepted.Phase != "preserving" {
		t.Fatalf("handoff phase = %q, want preserving (the cooperative window)", accepted.Phase)
	}
	h.mustTool(t, coder, "swarm_checkpoint", map[string]any{
		"kind": "handoff", "summary": "unit 1 saved, unit 2 next",
		"next": []string{"finish unit 2"},
		"git":  []map[string]any{{"repo": repoID, "branch": "task/handoff", "sha": wipSHA, "dirty": false}},
	})

	// The walk runs through reconcile ticks: wait for the operation to leave
	// the coordinator (success lands on 404 = no replacement in progress).
	deadline := time.Now().Add(30 * time.Second)
	for {
		s, _, err := h.do(http.MethodGet, "/api/agents/"+coder+"/replacement", nil)
		if err != nil {
			t.Fatal(err)
		}
		if s == http.StatusNotFound {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("replacement never finished within 30s")
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Signed WIP + bundle: the manifest names the operation, agent,
	// predecessor and generation, and snapshots the outside-git spec.
	var agentID string
	if err := h.db(t).QueryRow(`SELECT id FROM agents WHERE name = ?`, coder).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(h.home, "handoffs", agentID, accepted.OperationID, "manifest.json")
	rawMan, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("handoff bundle missing at %s: %v", manifestPath, err)
	}
	var manifest struct {
		SchemaVersion int    `json:"schema_version"`
		OperationID   string `json:"operation_id"`
		AgentID       string `json:"agent_id"`
		Predecessor   string `json:"predecessor_session_id"`
		Generation    int    `json:"generation"`
	}
	if err := json.Unmarshal(rawMan, &manifest); err != nil {
		t.Fatalf("manifest is not JSON: %v", err)
	}
	if manifest.OperationID != accepted.OperationID || manifest.AgentID != agentID || manifest.Predecessor == "" {
		t.Fatalf("manifest identity = %+v, want op %s agent %s with a predecessor", manifest, accepted.OperationID, agentID)
	}
	snaps, err := filepath.Glob(filepath.Join(h.home, "handoffs", agentID, accepted.OperationID, "artifacts", "*"))
	if err != nil || len(snaps) == 0 {
		t.Fatalf("handoff bundle has no artifact snapshots: %v %v", snaps, err)
	}

	// Old token dead: the predecessor token file is revoked and its bearer
	// no longer authenticates.
	var predID string
	if err := h.db(t).QueryRow(`SELECT session_id FROM agent_operations WHERE id = ?`, accepted.OperationID).Scan(&predID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(h.home, "run", "tokens", predID)); !os.IsNotExist(err) {
		t.Fatalf("predecessor token file still present for %s", predID)
	}
	mcpBody, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}})
	req, _ := http.NewRequest(http.MethodPost, h.url+"/mcp", strings.NewReader(string(mcpBody)))
	req.Header.Set("Authorization", "Bearer "+oldToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("predecessor bearer still authenticates after handoff")
	}

	// One fresh session on the same agent: same id/row and name, same
	// worktree reservation, exactly one new generation.
	var sessions int
	if err := h.db(t).QueryRow(`SELECT COUNT(*) FROM sessions WHERE agent_id = ?`, agentID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 2 {
		t.Fatalf("sessions for %s = %d, want exactly 2 (predecessor plus one successor)", coder, sessions)
	}
	var succID string
	var succGen int
	if err := h.db(t).QueryRow(`SELECT id, generation FROM sessions WHERE agent_id = ? ORDER BY generation DESC LIMIT 1`,
		agentID).Scan(&succID, &succGen); err != nil {
		t.Fatal(err)
	}
	if succID == predID {
		t.Fatal("no successor session started")
	}
	if succGen != manifest.Generation+1 {
		t.Fatalf("successor generation = %d, want manifest generation %d + 1", succGen, manifest.Generation)
	}
	var kept int
	if err := h.db(t).QueryRow(`SELECT COUNT(*) FROM worktree_reservations WHERE worktree_id = ? AND agent_id = ? AND released_at IS NULL`,
		wtID, agentID).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 1 {
		t.Fatal("successor lost the worktree reservation")
	}
	if !h.waitForRelay(t, orch, "handoff", since, 10*time.Second) {
		t.Fatal("no handoff relay to the parent for the handed-off coder")
	}

	// Recovery read: the successor booted and took its first sync (the
	// fake agent's boot sync consumes the one-shot recovery bundle, by
	// design), so the durable assignment still resolves and the full
	// recovery read replays the predecessor's evidence.
	var firstSyncAt int64
	if err := h.db(t).QueryRow(`SELECT COALESCE(first_sync_at, 0) FROM sessions WHERE id = ?`,
		succID).Scan(&firstSyncAt); err != nil {
		t.Fatal(err)
	}
	if firstSyncAt == 0 {
		t.Fatal("successor never took its first sync")
	}
	syncOut := h.mustTool(t, coder, "swarm_sync", map[string]any{})
	assignment, _ := syncOut["assignment"].(map[string]any)
	if assignment["item"] != task || assignment["name"] != coder {
		t.Fatalf("successor assignment = %+v, want item %s agent %s", assignment, task, coder)
	}
	readOut := h.mustTool(t, coder, "swarm_read", map[string]any{
		"recovery": map[string]any{"agent": coder, "limit": 10},
	})
	rec, _ := readOut["recovery"].(map[string]any)
	sawHandoff := false
	for _, c := range rec["checkpoints"].([]any) {
		cp, _ := c.(map[string]any)
		if cp["kind"] != "handoff" {
			continue
		}
		gits, _ := cp["git"].([]any)
		if len(gits) == 0 {
			continue
		}
		g, _ := gits[0].(map[string]any)
		if g["sha"] == wipSHA {
			sawHandoff = true
		}
	}
	if !sawHandoff {
		t.Fatalf("recovery checkpoints %+v, want the handoff checkpoint naming WIP %s", rec["checkpoints"], wipSHA)
	}
	sawSpec := false
	for _, a := range rec["artifacts"].([]any) {
		art, _ := a.(map[string]any)
		if art["kind"] == "spec" && art["path"] == specPath {
			sawSpec = true
		}
	}
	if !sawSpec {
		t.Fatalf("recovery artifacts %+v, want the outside-git spec %s", rec["artifacts"], specPath)
	}
	// Worktree paths are reservation-scoped, not owner-scoped: the
	// successor keeps the same tree through its unreleased reservation,
	// proven by the kept==1 check above, not by this owner-scoped list.

	// Work continues on the same assignment, naming the predecessor.
	h.mustTool(t, coder, "swarm_checkpoint", map[string]any{
		"kind": "progress", "summary": "successor continuing unit 2", "next": []string{"finish unit 2"},
	})
	if got := h.itemStatus(t, task); got != "in_progress" {
		t.Fatalf("task status = %s, want in_progress", got)
	}
}

// Batch 3 cycle 5, orchestrator variant: the handoff replaces only the
// orchestrator's session while a child keeps running; a child that finishes
// mid-replacement is relayed to the parent exactly once.
func TestScenarioHandoffOrchestratorRelay(t *testing.T) {
	h := newHarness(t)
	t.Cleanup(func() { h.setMaxConcurrentAgents(t, 200) })
	epic := h.materializedEpic(t)
	orch := h.startOrchestrator(t, epic)
	h.mustTool(t, orch, "swarm_checkpoint", map[string]any{"kind": "accepted", "summary": "starting"})
	task := h.firstTask(t, epic)
	coder := h.spawn(t, orch, task, "coder")
	if !h.waitForSessionState(t, coder, "running", 5*time.Second) {
		t.Fatalf("coder session = %s, never reached running", h.sessionState(t, coder))
	}
	h.setMaxConcurrentAgents(t, 1)

	// No admission slot for the successor (cap 1, two live agents): the
	// orchestrator replacement parks in queued, provably mid-replacement
	// while the child finishes below.
	since := time.Now()
	status, raw, err := h.do(http.MethodPost, "/api/agents/"+orch+"/handoff", map[string]any{"request_id": "req-orch-handoff-1"})
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusAccepted {
		t.Fatalf("POST handoff status = %d %s, want 202", status, raw)
	}
	var accepted struct {
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal(raw, &accepted); err != nil {
		t.Fatalf("handoff response %s: %v", raw, err)
	}
	// No pause first: the handoff itself asks the orchestrator to preserve.
	// It saves, then the walk stops it and parks in queued for a slot.
	if !h.waitForSessionState(t, orch, "pause_requested", 5*time.Second) {
		t.Fatalf("orch session = %s, want pause_requested", h.sessionState(t, orch))
	}
	h.mustTool(t, orch, "swarm_checkpoint", map[string]any{"kind": "handoff", "summary": "orchestrator saved"})
	queued := false
	for i := 0; i < 40; i++ {
		s, body, err := h.do(http.MethodGet, "/api/agents/"+orch+"/replacement", nil)
		if err != nil {
			t.Fatal(err)
		}
		if s != http.StatusOK {
			t.Fatalf("replacement status = %d %s, want the parked operation", s, body)
		}
		var cur struct {
			Phase string `json:"phase"`
		}
		if err := json.Unmarshal(body, &cur); err != nil {
			t.Fatal(err)
		}
		if cur.Phase == "queued" {
			queued = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !queued {
		t.Fatal("orchestrator replacement never parked in queued")
	}

	// The live child finishes mid-replacement with legacy evidence.
	h.mustTool(t, coder, "swarm_checkpoint", map[string]any{
		"kind": "completed", "summary": "unit done",
		"git":          []map[string]any{{"repo": "chat", "branch": "task/one", "sha": h.headSHA(t)}},
		"verification": []map[string]any{{"cmd": "go test ./...", "ok": true}},
	})
	if !h.waitForRelay(t, orch, "completed", since, 10*time.Second) {
		t.Fatal("no completed relay to the handed-off orchestrator")
	}
	// Settle past reconcile ticks, then the completion must appear exactly
	// once: one relay row, never doubled by the replacement walk.
	time.Sleep(6 * time.Second)
	var orchID string
	if err := h.db(t).QueryRow(`SELECT id FROM agents WHERE name = ?`, orch).Scan(&orchID); err != nil {
		t.Fatal(err)
	}
	var relays int
	if err := h.db(t).QueryRow(`SELECT COUNT(*) FROM messages WHERE kind = 'relay' AND to_agent_id = ?
		AND payload_json LIKE '%"event":"completed"%' AND created_at >= ?`,
		orchID, since.UnixMilli()).Scan(&relays); err != nil {
		t.Fatal(err)
	}
	if relays != 1 {
		t.Fatalf("completed relays to %s = %d, want exactly 1", orch, relays)
	}

	// Open the admission slot: the successor starts on the same agent and
	// its first sync still serves the child completion.
	h.setMaxConcurrentAgents(t, 200)
	if !h.waitForSessionState(t, orch, "running", 30*time.Second) {
		t.Fatalf("orch session = %s, want the successor running", h.sessionState(t, orch))
	}
	s, _, err := h.do(http.MethodGet, "/api/agents/"+orch+"/replacement", nil)
	if err != nil {
		t.Fatal(err)
	}
	if s != http.StatusNotFound {
		t.Fatalf("replacement status = %d, want 404 (operation done)", s)
	}
}

// handoffNoPrePause drives the main user path (menubar/CLI POST /handoff on a
// running agent, no pause first): the walk must move the session to
// pause_requested and park in preserving with the pane alive, and replace the
// session only after the agent's own handoff checkpoint.
func handoffNoPrePause(t *testing.T, h *harness, name, requestID string) (opID string) {
	t.Helper()
	status, raw, err := h.do(http.MethodPost, "/api/agents/"+name+"/handoff", map[string]any{"request_id": requestID})
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusAccepted {
		t.Fatalf("POST handoff status = %d %s, want 202", status, raw)
	}
	var accepted struct {
		OperationID string `json:"operation_id"`
		Phase       string `json:"phase"`
	}
	if err := json.Unmarshal(raw, &accepted); err != nil {
		t.Fatalf("handoff response %s: %v", raw, err)
	}
	if accepted.Phase != "preserving" {
		t.Fatalf("handoff phase = %q, want preserving (the predecessor must get to save)", accepted.Phase)
	}
	if got := h.sessionState(t, name); got != "pause_requested" {
		t.Fatalf("%s session = %s, want pause_requested (not killed)", name, got)
	}
	var sessions int
	if err := h.db(t).QueryRow(`SELECT COUNT(*) FROM sessions s JOIN agents a ON a.id = s.agent_id
		WHERE a.name = ?`, name).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	// Past a reconcile tick the walk still waits for the save.
	time.Sleep(6 * time.Second)
	if got := h.sessionState(t, name); got != "pause_requested" && got != "quiescing" {
		t.Fatalf("%s session = %s before its handoff checkpoint, want still preserving", name, got)
	}
	h.mustTool(t, name, "swarm_checkpoint", map[string]any{"kind": "handoff", "summary": "saved for handoff"})
	deadline := time.Now().Add(40 * time.Second)
	for {
		s, _, err := h.do(http.MethodGet, "/api/agents/"+name+"/replacement", nil)
		if err != nil {
			t.Fatal(err)
		}
		if s == http.StatusNotFound {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("replacement never finished within 40s")
		}
		time.Sleep(500 * time.Millisecond)
	}
	var phase, manifest string
	if err := h.db(t).QueryRow(`SELECT phase, COALESCE(manifest_path, '') FROM agent_operations WHERE id = ?`,
		accepted.OperationID).Scan(&phase, &manifest); err != nil {
		t.Fatal(err)
	}
	if phase != "succeeded" || manifest == "" {
		t.Fatalf("operation phase = %s manifest = %q, want succeeded with a bound manifest", phase, manifest)
	}
	var after int
	if err := h.db(t).QueryRow(`SELECT COUNT(*) FROM sessions s JOIN agents a ON a.id = s.agent_id
		WHERE a.name = ?`, name).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != sessions+1 {
		t.Fatalf("%s sessions = %d, want exactly one successor over %d", name, after, sessions)
	}
	return accepted.OperationID
}

func TestScenarioHandoffWorkerNoPrePause(t *testing.T) {
	h := newHarness(t)
	epic := h.materializedEpic(t)
	orch := h.startOrchestrator(t, epic)
	h.mustTool(t, orch, "swarm_checkpoint", map[string]any{"kind": "accepted", "summary": "starting"})
	coder := h.spawn(t, orch, h.firstTask(t, epic), "coder")
	h.mustTool(t, coder, "swarm_checkpoint", map[string]any{"kind": "accepted", "summary": "starting"})
	if !h.waitForSessionState(t, coder, "running", 5*time.Second) {
		t.Fatalf("coder session = %s, never reached running", h.sessionState(t, coder))
	}
	handoffNoPrePause(t, h, coder, "req-nopause-worker")
	if !h.waitForSessionState(t, coder, "running", 30*time.Second) {
		t.Fatalf("coder successor = %s, want running", h.sessionState(t, coder))
	}
}

func TestScenarioHandoffOrchestratorNoPrePause(t *testing.T) {
	h := newHarness(t)
	epic := h.materializedEpic(t)
	orch := h.startOrchestrator(t, epic)
	h.mustTool(t, orch, "swarm_checkpoint", map[string]any{"kind": "accepted", "summary": "starting"})
	coder := h.spawn(t, orch, h.firstTask(t, epic), "coder")
	if !h.waitForSessionState(t, coder, "running", 5*time.Second) {
		t.Fatalf("coder session = %s, never reached running", h.sessionState(t, coder))
	}
	handoffNoPrePause(t, h, orch, "req-nopause-orch")
	if !h.waitForSessionState(t, orch, "running", 30*time.Second) {
		t.Fatalf("orch successor = %s, want running", h.sessionState(t, orch))
	}
	// Orchestrator handoff replaces only its own session: the child kept running.
	if got := h.sessionState(t, coder); got != "running" {
		t.Fatalf("child session = %s, want still running through the orchestrator handoff", got)
	}
}
