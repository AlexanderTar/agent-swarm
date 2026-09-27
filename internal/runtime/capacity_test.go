package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

func TestOperationReason(t *testing.T) {
	for key, want := range map[string]string{
		"capacity:ses_1": "capacity",
		"resume:ses_1":   "resume",
		"h1":             "",
		"":               "",
		"capacityx":      "",
	} {
		if got := OperationReason(key); got != want {
			t.Errorf("OperationReason(%q) = %q, want %q", key, got, want)
		}
	}
}

// spawnRunning spawns a parentless worker on itemKey (or a child when
// parentID is set) and marks its session running. The clock steps a second
// first so created_at strictly orders agents.
func spawnRunning(t *testing.T, s *Store, tm *fakeTmux, itemKey, parentID string) Agent {
	t.Helper()
	ctx := context.Background()
	tm.clk.Advance(time.Second)
	a, queued, err := s.Spawn(ctx, SpawnInput{ItemKey: itemKey, Role: RoleCoder, Kind: Fake, Model: "fake-1",
		ParentAgentID: parentID, Brief: BriefInput{Objective: "work"}})
	if err != nil || queued {
		t.Fatalf("spawn %s: queued=%v err=%v", itemKey, queued, err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'running' WHERE agent_id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	return a
}

// capacityOps maps agent id -> phase for every capacity-keyed operation.
func capacityOps(t *testing.T, s *Store) map[string]string {
	t.Helper()
	rows, err := s.DB.Query(`SELECT agent_id, phase FROM agent_operations WHERE request_key LIKE 'capacity:%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, phase string
		if err := rows.Scan(&id, &phase); err != nil {
			t.Fatal(err)
		}
		out[id] = phase
	}
	return out
}

func latestState(t *testing.T, s *Store, agentID string) SessionState {
	t.Helper()
	ses, err := s.LatestSession(context.Background(), agentID)
	if err != nil {
		t.Fatal(err)
	}
	return ses.State
}

func countKind(s *Store, kind string) int {
	n := 0
	for _, k := range s.Notify.(*fakeNotifier).kinds() {
		if k == kind {
			n++
		}
	}
	return n
}

// Spec scenarios 1 and 2: lowering the limit by N pauses exactly the N
// newest agents, each through a capacity-keyed handoff, and later ticks are
// no-ops.
func TestEnforceCapacityPausesTheNewestAgentsOverTheLimit(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 8)
	seedEpicWithThreeTasks(t, s)
	a := spawnRunning(t, s, tm, "TASK-1", "")
	b := spawnRunning(t, s, tm, "TASK-2", "")
	c := spawnRunning(t, s, tm, "TASK-3", "")
	setLimits(t, s, 1)

	if err := s.EnforceCapacity(ctx); err != nil {
		t.Fatal(err)
	}
	ops := capacityOps(t, s)
	if len(ops) != 2 || ops[b.ID] != "queued" || ops[c.ID] != "queued" {
		t.Fatalf("capacity ops = %v, want b and c queued", ops)
	}
	if st := latestState(t, s, a.ID); st != Running {
		t.Fatalf("oldest agent = %s, want running", st)
	}
	for _, id := range []string{b.ID, c.ID} {
		if st := latestState(t, s, id); st != Interrupted {
			t.Fatalf("paused agent %s = %s, want interrupted", id, st)
		}
	}
	if n := countKind(s, "agent.capacity_paused"); n != 2 {
		t.Fatalf("agent.capacity_paused raised %d times, want 2", n)
	}
	if n := countKind(s, "agent.interrupted"); n != 0 {
		t.Fatalf("agent.interrupted raised %d times, want 0 for a capacity pause", n)
	}
	var note string
	if err := s.DB.QueryRow(`SELECT COALESCE(note, '') FROM agent_operations WHERE agent_id = ?`, b.ID).Scan(&note); err != nil || note != "" {
		t.Fatalf("capacity op note = %q (err %v), want empty", note, err)
	}

	for i := 0; i < 2; i++ {
		if err := s.EnforceCapacity(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var total int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM agent_operations`).Scan(&total); err != nil || total != 2 {
		t.Fatalf("operations after repeat ticks = %d (err %v), want 2", total, err)
	}
}

// Spec scenario 3: a holder still saving its handoff counts toward the
// reduction, so a second tick does not pause an extra agent.
func TestEnforceCapacityCountsInFlightPauses(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 8)
	seedEpicWithThreeTasks(t, s)
	spawnRunning(t, s, tm, "TASK-1", "")
	spawnRunning(t, s, tm, "TASK-2", "")
	c := spawnRunning(t, s, tm, "TASK-3", "")
	cSes, err := s.LatestSession(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	// c's pane is alive, so its handoff parks in preserving (still a holder).
	panes(tm, Pane{Session: cSes.TmuxName, Command: "swarm-fake-agent"})
	tm.env[cSes.TmuxName] = map[string]string{"SWARM_SESSION": cSes.ID}
	setLimits(t, s, 2)

	for i := 0; i < 3; i++ {
		if err := s.EnforceCapacity(ctx); err != nil {
			t.Fatal(err)
		}
	}
	ops := capacityOps(t, s)
	if len(ops) != 1 || ops[c.ID] != "preserving" {
		t.Fatalf("capacity ops = %v, want only c preserving", ops)
	}
}

// Spec scenario 4: each skip rule protects the newest agent P, so the older
// plain worker U is paused instead.
func TestEnforceCapacitySkipRules(t *testing.T) {
	for name, protect := range map[string]func(t *testing.T, s *Store, p Agent, pSes Session){
		"open request": func(t *testing.T, s *Store, p Agent, pSes Session) {
			mustExec(t, s.DB, `INSERT INTO requests (id, kind, is_hitl, agent_id, session_id, item_id, prompt, state, created_at)
				VALUES ('req_1', 'question', 1, ?, ?, ?, 'Which DB?', 'open', 1)`, p.ID, pSes.ID, p.ItemID)
		},
		"unanswered question": func(t *testing.T, s *Store, p Agent, pSes Session) {
			mustExec(t, s.DB, `INSERT INTO messages (id, seq, kind, origin, from_agent_id, to_agent_id, root_item_id, payload_json, state, created_at)
				VALUES ('msg_q', 9001, 'question', 'agent', ?, ?, ?, '{}', 'acked', 1)`, p.ID, p.ID, p.RootItemID)
		},
		// A queued op on a still-running session is not a holder leaving its
		// slot (TestEnforceCapacityCountsHoldersAlreadyLeaving covers those):
		// it only protects P, so U is still paused.
		"operation in flight": func(t *testing.T, s *Store, p Agent, pSes Session) {
			mustExec(t, s.DB, `INSERT INTO agent_operations (id, agent_id, mode, phase, request_key, session_id, generation, created_at, updated_at)
				VALUES ('op_x', ?, 'recover', 'queued', 'k1', ?, ?, 1, 1)`, p.ID, pSes.ID, pSes.Generation)
		},
		"spawning": func(t *testing.T, s *Store, p Agent, pSes Session) {
			mustExec(t, s.DB, `UPDATE sessions SET state = 'spawning' WHERE id = ?`, pSes.ID)
		},
		"cancelled capacity pause of this session": func(t *testing.T, s *Store, p Agent, pSes Session) {
			mustExec(t, s.DB, `INSERT INTO agent_operations (id, agent_id, mode, phase, request_key, session_id, generation, created_at, updated_at)
				VALUES ('op_c', ?, 'handoff', 'cancelled', ?, ?, ?, 1, 1)`, p.ID, "capacity:"+pSes.ID, pSes.ID, pSes.Generation)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, tm, _ := newStore(t)
			ctx := context.Background()
			setLimits(t, s, 8)
			seedEpicWithTwoTasks(t, s)
			u := spawnRunning(t, s, tm, "TASK-1", "")
			p := spawnRunning(t, s, tm, "TASK-2", "")
			pSes, err := s.LatestSession(ctx, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			protect(t, s, p, pSes)
			setLimits(t, s, 1)
			if err := s.EnforceCapacity(ctx); err != nil {
				t.Fatal(err)
			}
			ops := capacityOps(t, s)
			if ops[u.ID] != "queued" {
				t.Fatalf("capacity ops = %v, want the unprotected older worker paused", ops)
			}
			if ph, ok := ops[p.ID]; ok && ph != "cancelled" {
				t.Fatalf("protected agent got capacity op in phase %s", ph)
			}
		})
	}
}

// Spec scenario 4 (orchestrator rule) + "no eligible agent is not an error":
// an orchestrator with a live child is never paused.
func TestEnforceCapacitySkipsOrchestratorWithLiveChildren(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 8)
	seedEpicWithTask(t, s)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.DB, `UPDATE sessions SET state = 'running' WHERE agent_id = ?`, orch.ID)
	child := spawnRunning(t, s, tm, "TASK-1", orch.ID)
	childSes, err := s.LatestSession(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The child waits on the user, so it is protected too.
	mustExec(t, s.DB, `INSERT INTO requests (id, kind, is_hitl, agent_id, session_id, item_id, prompt, state, created_at)
		VALUES ('req_1', 'question', 1, ?, ?, ?, 'Which DB?', 'open', 1)`, child.ID, childSes.ID, child.ItemID)
	setLimits(t, s, 1)
	if err := s.EnforceCapacity(ctx); err != nil {
		t.Fatalf("no eligible agent must not be an error: %v", err)
	}
	if ops := capacityOps(t, s); len(ops) != 0 {
		t.Fatalf("capacity ops = %v, want none", ops)
	}
}

// Spec scenario 5: workers go before orchestrators, even an older worker.
func TestEnforceCapacityPausesWorkersBeforeOrchestrators(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 8)
	seedEpicWithTask(t, s)
	w := spawnRunning(t, s, tm, "TASK-1", "")
	tm.clk.Advance(time.Second)
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.DB, `UPDATE sessions SET state = 'running' WHERE agent_id = ?`, orch.ID)
	setLimits(t, s, 1)
	if err := s.EnforceCapacity(ctx); err != nil {
		t.Fatal(err)
	}
	if ops := capacityOps(t, s); len(ops) != 1 || ops[w.ID] == "" {
		t.Fatalf("capacity ops = %v, want only the (older) worker", ops)
	}
}

// Spec scenario 12: a refused replacement is logged and skipped; the next
// candidate is paused and the tick does not fail.
func TestEnforceCapacitySkipsARefusedReplacement(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 8)
	seedEpicWithThreeTasks(t, s)
	spawnRunning(t, s, tm, "TASK-1", "")
	older := spawnRunning(t, s, tm, "TASK-2", "")
	newest := spawnRunning(t, s, tm, "TASK-3", "")
	mustExec(t, s.DB, `CREATE TRIGGER reject_op BEFORE INSERT ON agent_operations
		WHEN NEW.agent_id = '`+newest.ID+`' BEGIN SELECT RAISE(ABORT, 'injected'); END`)
	setLimits(t, s, 2)
	if err := s.EnforceCapacity(ctx); err != nil {
		t.Fatalf("tick failed: %v", err)
	}
	if ops := capacityOps(t, s); len(ops) != 1 || ops[older.ID] != "queued" {
		t.Fatalf("capacity ops = %v, want the next candidate paused", ops)
	}
}

// Spec scenario 6: raising the limit resumes a capacity-paused agent before
// a queued spawn takes the slot, in one Reconcile tick.
func TestReconcileResumesCapacityPausedBeforeQueuedSpawns(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 8)
	seedEpicWithThreeTasks(t, s)
	a := spawnRunning(t, s, tm, "TASK-1", "")
	b := spawnRunning(t, s, tm, "TASK-2", "")
	setLimits(t, s, 1)
	if err := s.EnforceCapacity(ctx); err != nil {
		t.Fatal(err)
	}
	if ops := capacityOps(t, s); ops[b.ID] != "queued" {
		t.Fatalf("setup: capacity ops = %v", ops)
	}
	tm.clk.Advance(time.Second)
	c, queued, err := s.Spawn(ctx, SpawnInput{ItemKey: "TASK-3", Role: RoleCoder, Kind: Fake, Model: "fake-1",
		Brief: BriefInput{Objective: "new"}})
	if err != nil || !queued {
		t.Fatalf("setup: spawn C queued=%v err=%v", queued, err)
	}
	aSes, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	panes(tm, Pane{Session: aSes.TmuxName, Command: "swarm-fake-agent"})
	tm.env[aSes.TmuxName] = map[string]string{"SWARM_SESSION": aSes.ID}

	setLimits(t, s, 2)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if ops := capacityOps(t, s); ops[b.ID] != "succeeded" {
		t.Fatalf("capacity ops = %v, want b succeeded (resumed first)", ops)
	}
	got, err := s.Agent(ctx, c.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != AgentQueued {
		t.Fatalf("queued spawn C = %s, want still queued", got.State)
	}
}

// Spec scenario 7: among capacity-paused agents, the earliest pause resumes
// first, by created_at, not by updated_at (which every phase swap bumps).
func TestResumeOperationsResumesTheEarliestPauseFirst(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	setLimits(t, s, 8)
	seedEpicWithThreeTasks(t, s)
	spawnRunning(t, s, tm, "TASK-1", "")
	b := spawnRunning(t, s, tm, "TASK-2", "")
	c := spawnRunning(t, s, tm, "TASK-3", "")
	setLimits(t, s, 2)
	if err := s.EnforceCapacity(ctx); err != nil { // pauses c
		t.Fatal(err)
	}
	tm.clk.Advance(time.Minute)
	setLimits(t, s, 1)
	if err := s.EnforceCapacity(ctx); err != nil { // pauses b, later
		t.Fatal(err)
	}
	// c's row was touched last: updated_at order would pick b.
	mustExec(t, s.DB, `UPDATE agent_operations SET updated_at = updated_at + 3600000 WHERE agent_id = ?`, c.ID)
	setLimits(t, s, 2)
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatal(err)
	}
	ops := capacityOps(t, s)
	if ops[c.ID] != "succeeded" || ops[b.ID] != "queued" {
		t.Fatalf("capacity ops = %v, want c (earliest pause) resumed, b still queued", ops)
	}
}

// Spec scenario 8: Resume with the pool full records a resume-keyed handoff
// instead of launching; a repeat is a no-op; a freed slot starts the
// successor in resume mode with no agent.retried.
func TestResumeWhenFullQueuesForAFreeSlot(t *testing.T) {
	s, _, fa := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	if err := s.SetSessionState(ctx, wSes.ID, Paused); err != nil {
		t.Fatal(err)
	}
	setLimits(t, s, 1) // the orchestrator holds the only slot
	sessions := func() int {
		var n int
		if err := s.DB.QueryRow(`SELECT COUNT(*) FROM sessions WHERE agent_id = ?`, w.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	for i := 0; i < 2; i++ {
		got, err := s.Resume(ctx, w.Name, "", "")
		if err != nil {
			t.Fatalf("resume #%d: %v", i+1, err)
		}
		if got.ID != w.ID {
			t.Fatalf("resume returned %s", got.Name)
		}
	}
	var phase string
	var n int
	if err := s.DB.QueryRow(`SELECT phase, (SELECT COUNT(*) FROM agent_operations WHERE agent_id = ?)
		FROM agent_operations WHERE request_key = ?`, w.ID, "resume:"+wSes.ID).Scan(&phase, &n); err != nil {
		t.Fatal(err)
	}
	if phase != "queued" || n != 1 {
		t.Fatalf("resume op = %s (ops %d), want one queued", phase, n)
	}
	if got := sessions(); got != 1 {
		t.Fatalf("sessions = %d, want 1 (no launch while full)", got)
	}

	setLimits(t, s, 2)
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatal(err)
	}
	if got := sessions(); got != 2 {
		t.Fatalf("sessions = %d, want 2 after a slot freed", got)
	}
	if !strings.Contains(fa.LastSpec.Kickoff, ResumeAddition) {
		t.Fatalf("successor kickoff is not resume mode:\n%s", fa.LastSpec.Kickoff)
	}
	if n := countKind(s, "agent.retried"); n != 0 {
		t.Fatalf("agent.retried raised %d times for a queued resume, want 0", n)
	}
}

// Spec scenario 9: Resume with room behaves as before: an immediate new
// generation and no operation row.
func TestResumeWithRoomStartsAtOnce(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	if err := s.SetSessionState(ctx, wSes.ID, Paused); err != nil {
		t.Fatal(err)
	}
	setLimits(t, s, 8)
	if _, err := s.Resume(ctx, w.Name, "", ""); err != nil {
		t.Fatal(err)
	}
	var ops int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM agent_operations WHERE agent_id = ?`, w.ID).Scan(&ops); err != nil || ops != 0 {
		t.Fatalf("operations = %d (err %v), want 0", ops, err)
	}
	ses, err := s.LatestSession(ctx, w.ID)
	if err != nil || ses.Generation != wSes.Generation+1 {
		t.Fatalf("latest generation = %d (err %v), want %d", ses.Generation, err, wSes.Generation+1)
	}
}

// A queued resume is not a handoff: the paused session's own handoff
// checkpoint is not re-gated, so a dirty tracked tree (which the direct
// Resume never checks either) does not block it.
func TestQueuedResumeSkipsTheHandoffGate(t *testing.T) {
	s, _, fa := newStore(t)
	ctx := context.Background()
	_, w, wSes := worker(t, s)
	now := db.Millis(s.Now())
	it, err := s.Items.Get(ctx, "TASK-1")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.DB, `INSERT INTO repos (id, name, path, default_branch, source, created_at, updated_at)
		VALUES ('repo_r', 'repo1', '/tmp/repo1', 'main', 'manual', ?, ?)`, now, now)
	mustExec(t, s.DB, `INSERT INTO worktrees (id, repo_id, path, branch, base_ref, base_sha, state, owner_agent_id, root_item_id, created_at)
		VALUES ('wt_r', 'repo_r', '/tmp/resume-wt', 'b', 'main', 'deadbee', 'active', ?, ?, ?)`, w.ID, it.RootID, now)
	oldHEAD, oldStatus := readDiskHEAD, readDiskStatus
	readDiskHEAD = func(string) (string, error) { return "deadbee", nil }
	readDiskStatus = func(string) (string, error) { return " M main.go\n", nil }
	t.Cleanup(func() { readDiskHEAD, readDiskStatus = oldHEAD, oldStatus })
	mustExec(t, s.DB, `INSERT INTO checkpoints (id, session_id, agent_id, item_id, kind, attempt, summary, daemon_written, created_at)
		VALUES ('ckp_pause', ?, ?, ?, 'handoff', 1, 'paused', 1, ?)`, wSes.ID, w.ID, w.ItemID, now)
	if err := s.SetSessionState(ctx, wSes.ID, Paused); err != nil {
		t.Fatal(err)
	}
	setLimits(t, s, 1) // the orchestrator holds the only slot

	if _, err := s.Resume(ctx, w.Name, "", ""); err != nil {
		t.Fatal(err)
	}
	var phase string
	if err := s.DB.QueryRow(`SELECT phase FROM agent_operations WHERE request_key = ?`,
		"resume:"+wSes.ID).Scan(&phase); err != nil || phase != "queued" {
		t.Fatalf("resume op = %s (err %v), want queued", phase, err)
	}
	setLimits(t, s, 2)
	if err := s.ResumeOperations(ctx); err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, w.ID)
	if err != nil || ses.Generation != wSes.Generation+1 {
		t.Fatalf("latest generation = %d (err %v), want %d", ses.Generation, err, wSes.Generation+1)
	}
	if !strings.Contains(fa.LastSpec.Kickoff, ResumeAddition) {
		t.Fatalf("successor kickoff is not resume mode:\n%s", fa.LastSpec.Kickoff)
	}
}

// A holder already leaving its slot for another reason (a user Pause in
// flight, a user handoff still preserving) counts toward the reduction:
// pausing another agent for it would only flap that agent back once the
// leaver lets go.
func TestEnforceCapacityCountsHoldersAlreadyLeaving(t *testing.T) {
	for name, leave := range map[string]func(t *testing.T, s *Store, a Agent, aSes Session){
		"pause requested": func(t *testing.T, s *Store, a Agent, aSes Session) {
			mustExec(t, s.DB, `UPDATE sessions SET state = 'pause_requested' WHERE id = ?`, aSes.ID)
		},
		"quiescing": func(t *testing.T, s *Store, a Agent, aSes Session) {
			mustExec(t, s.DB, `UPDATE sessions SET state = 'quiescing' WHERE id = ?`, aSes.ID)
		},
		"stopping": func(t *testing.T, s *Store, a Agent, aSes Session) {
			mustExec(t, s.DB, `UPDATE sessions SET state = 'stopping' WHERE id = ?`, aSes.ID)
		},
		"user handoff preserving": func(t *testing.T, s *Store, a Agent, aSes Session) {
			mustExec(t, s.DB, `INSERT INTO agent_operations (id, agent_id, mode, phase, request_key, session_id, generation, created_at, updated_at)
				VALUES ('op_h', ?, 'handoff', 'preserving', 'h1', ?, ?, 1, 1)`, a.ID, aSes.ID, aSes.Generation)
		},
		"user recover requested": func(t *testing.T, s *Store, a Agent, aSes Session) {
			mustExec(t, s.DB, `INSERT INTO agent_operations (id, agent_id, mode, phase, request_key, session_id, generation, created_at, updated_at)
				VALUES ('op_r', ?, 'recover', 'requested', 'r1', ?, ?, 1, 1)`, a.ID, aSes.ID, aSes.Generation)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, tm, _ := newStore(t)
			ctx := context.Background()
			setLimits(t, s, 8)
			seedEpicWithThreeTasks(t, s)
			a := spawnRunning(t, s, tm, "TASK-1", "")
			spawnRunning(t, s, tm, "TASK-2", "")
			spawnRunning(t, s, tm, "TASK-3", "")
			aSes, err := s.LatestSession(ctx, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			leave(t, s, a, aSes)
			setLimits(t, s, 2)
			if err := s.EnforceCapacity(ctx); err != nil {
				t.Fatal(err)
			}
			if ops := capacityOps(t, s); len(ops) != 0 {
				t.Fatalf("capacity ops = %v, want none (a is already leaving)", ops)
			}
		})
	}
}
