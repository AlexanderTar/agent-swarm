package items_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

func move(t *testing.T, s *items.Store, key string, to items.Status, by items.Actor) error {
	t.Helper()
	_, err := s.Transition(ctx, key, to, by)
	return err
}

func wantStatus(t *testing.T, s *items.Store, key string, want items.Status) {
	t.Helper()
	if got := mustGet(t, s, key).Status; got != want {
		t.Fatalf("%s status = %s, want %s", key, got, want)
	}
}

func wantDenied(t *testing.T, err error, msg string) {
	t.Helper()
	if code(err) != items.CodeTransitionDenied || err.Error() != msg {
		t.Fatalf("err = %v (%s), want transition_denied %q", err, code(err), msg)
	}
}

func setStatus(t *testing.T, s *items.Store, it items.Item, st items.Status) {
	t.Helper()
	exec(t, s.DB, `UPDATE items SET status = ?, updated_at = ? WHERE id = ?`, st, later(s), it.ID)
}

// tree builds EPIC-1 > STORY-1 > TASK-1 (all ready) and returns them.
func tree(t *testing.T, s *items.Store) (e, st, task items.Item) {
	e = mk(t, s, items.Epic, "", "Auth")
	st = mk(t, s, items.Story, e.Key, "Login")
	task = mk(t, s, items.Task, st.Key, "Form")
	for _, it := range []items.Item{e, st, task} {
		setStatus(t, s, it, items.Ready)
	}
	return mustGet(t, s, e.Key), mustGet(t, s, st.Key), mustGet(t, s, task.Key)
}

func TestTaskTransitions(t *testing.T) {
	s := newStore(t)
	e, st, _ := tree(t, s)
	orch := items.Orchestrator("agt_o", e.ID)
	daemon := items.Daemon()
	task := mk(t, s, items.Task, st.Key, "Draft task")

	wantDenied(t, move(t, s, task.Key, items.Ready, daemon), "Couldn't update status. The item remains Draft.")
	if err := move(t, s, task.Key, items.Ready, orch); err != nil {
		t.Fatal(err)
	}
	wantDenied(t, move(t, s, task.Key, items.InProgress, user), "Couldn't update status. The item remains Ready.")
	wantDenied(t, move(t, s, task.Key, items.InProgress, daemon), "Couldn't update status. The item remains Ready.")
	seedCheckpoint(t, s.DB, task, "accepted", 1, later(s), "")
	if err := move(t, s, task.Key, items.InProgress, daemon); err != nil {
		t.Fatal(err)
	}
	wantDenied(t, move(t, s, task.Key, items.Done, user), "Couldn't move TASK-2 to Done. No agent has reported it complete on this task.")
	wantDenied(t, move(t, s, task.Key, items.InReview, daemon), "Couldn't update status. The item remains In progress.")
	// The retry sequence below must come from the SAME builder: per-builder
	// completion (F1) means a different agent's newer attempt never stales
	// this completion -- only the builder's own retry does.
	bAgent, bSes := seedSessionRole(t, s.DB, task, "coder", "running")
	seedCheckpointFor(t, s.DB, task, bAgent, bSes, "completed", 1, later(s))
	wantDenied(t, move(t, s, task.Key, items.Done, orch), "Couldn't update status. The item remains In progress.")
	if err := move(t, s, task.Key, items.InReview, daemon); err != nil {
		t.Fatal(err)
	}
	wantDenied(t, move(t, s, task.Key, items.Done, user), "Couldn't update status. The item remains In review.")

	// review asks for changes: the same builder's new attempt; its own old
	// completion no longer counts
	if err := move(t, s, task.Key, items.InProgress, orch); err != nil {
		t.Fatal(err)
	}
	seedCheckpointFor(t, s.DB, task, bAgent, bSes, "progress", 2, later(s))
	wantDenied(t, move(t, s, task.Key, items.InReview, daemon), "Couldn't update status. The item remains In progress.")
	seedCheckpointFor(t, s.DB, task, bAgent, bSes, "completed", 2, later(s))
	if err := move(t, s, task.Key, items.InReview, daemon); err != nil {
		t.Fatal(err)
	}
	if err := move(t, s, task.Key, items.Done, orch); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, s, task.Key, items.Done)

	wantDenied(t, move(t, s, task.Key, items.Cancelled, user), "Couldn't update status. The item remains Done.")
	wantDenied(t, move(t, s, task.Key, items.Blocked, user), "Couldn't update status. The item remains Done.")
	wantDenied(t, move(t, s, task.Key, items.Ready, orch), "Couldn't update status. The item remains Done.")
	if err := move(t, s, task.Key, items.Ready, user); err != nil {
		t.Fatalf("user reopen: %v", err)
	}
	wantDenied(t, move(t, s, task.Key, items.AwaitingApproval, user), "Only spikes can await approval.")
	if _, err := s.Transition(ctx, task.Key, "doing", user); code(err) != items.CodeBadRequest {
		t.Fatalf("unknown status: %v", err)
	}
	if got, err := s.Transition(ctx, task.Key, items.Ready, user); err != nil || got.Status != items.Ready {
		t.Fatalf("same-status move is a no-op: %v", err)
	}
}

// 2026-09-22 incident: an agent that skips its mandatory first "accepted"
// checkpoint (skills/swarm/SKILL.md's step 1) leaves the task at Ready
// forever -- acceptedSince never passes, so Ready->InProgress is denied, and
// every later checkpoint.go tryTransition attempt (InProgress->InReview on
// completion) is denied too, generic "remains Ready", even though real,
// verified work landed (three completed checkpoints across three separate
// agent attempts on TASK-106, root cause confirmed against the live DB).
// The daemon's own completed-checkpoint path must self-heal Ready straight to
// InReview when a real completed checkpoint exists -- completedCurrent is
// strictly stronger evidence than the accepted step would have been. A
// direct orchestrator/user call must NOT gain this leniency (daemon-only,
// same as the existing InProgress->InReview case).
func TestCompletedChecksSelfHealsSkippedAcceptedStep(t *testing.T) {
	s := newStore(t)
	e, st, _ := tree(t, s)
	daemon := items.Daemon()
	orch := items.Orchestrator("agt_o", e.ID)
	task := mk(t, s, items.Task, st.Key, "Never accepted")
	if err := move(t, s, task.Key, items.Ready, orch); err != nil {
		t.Fatal(err)
	}

	// No accepted checkpoint ever recorded -- orch/user must still be refused
	// this hop entirely, exactly as before.
	wantDenied(t, move(t, s, task.Key, items.InReview, user), "Couldn't update status. The item remains Ready.")
	wantDenied(t, move(t, s, task.Key, items.InReview, orch), "Couldn't update status. The item remains Ready.")

	seedCheckpoint(t, s.DB, task, "completed", 1, later(s), "")
	if err := move(t, s, task.Key, items.InReview, daemon); err != nil {
		t.Fatalf("daemon must self-heal Ready -> InReview on a real completed checkpoint: %v", err)
	}
	wantStatus(t, s, task.Key, items.InReview)

	if err := move(t, s, task.Key, items.Done, orch); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, s, task.Key, items.Done)
}

func TestBlockAndUnblockRestoresStatus(t *testing.T) {
	s := newStore(t)
	e, st, task := tree(t, s)
	orch := items.Orchestrator("agt_o", e.ID)
	setStatus(t, s, task, items.InReview)
	for _, by := range []items.Actor{user, orch, items.Daemon()} {
		if err := move(t, s, task.Key, items.Blocked, by); err != nil {
			t.Fatalf("%s block: %v", by.Kind, err)
		}
		got := mustGet(t, s, task.Key)
		if got.StatusBeforeBlock != items.InReview {
			t.Fatalf("saved = %q", got.StatusBeforeBlock)
		}
		wantDenied(t, move(t, s, task.Key, items.Ready, by), "Couldn't update status. The item remains Blocked.")
		if err := move(t, s, task.Key, items.InReview, by); err != nil {
			t.Fatalf("%s unblock: %v", by.Kind, err)
		}
		if got := mustGet(t, s, task.Key); got.Status != items.InReview || got.StatusBeforeBlock != "" {
			t.Fatalf("after unblock = %+v", got)
		}
	}
	// stories can be blocked by hand, and derivation leaves them alone while blocked
	if err := move(t, s, st.Key, items.Blocked, user); err != nil {
		t.Fatal(err)
	}
	setStatus(t, s, task, items.Done)
	if err := s.Reconcile(ctx, task.Key); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, s, st.Key, items.Blocked)
	if err := move(t, s, st.Key, items.InReview, user); err != nil { // the saved status
		t.Fatal(err)
	}
	wantStatus(t, s, st.Key, items.Done) // restored, then derived
}

func TestCancelRules(t *testing.T) {
	s := newStore(t)
	e, st, task := tree(t, s)
	orch := items.Orchestrator("agt_o", e.ID)
	wantDenied(t, move(t, s, e.Key, items.Cancelled, orch), "Couldn't update status. The item remains Ready.")
	wantDenied(t, move(t, s, task.Key, items.Cancelled, items.Daemon()), "Couldn't update status. The item remains Ready.")
	if err := move(t, s, task.Key, items.Cancelled, orch); err != nil {
		t.Fatal(err)
	}
	if err := move(t, s, st.Key, items.Cancelled, user); err != nil {
		t.Fatal(err)
	}
	if err := move(t, s, e.Key, items.Cancelled, user); err != nil {
		t.Fatal(err)
	}
	wantDenied(t, move(t, s, task.Key, items.Ready, orch), "Couldn't update status. The item remains Cancelled.")
	if err := move(t, s, task.Key, items.Ready, user); err != nil {
		t.Fatal(err)
	}
}

func TestStoriesAreDerived(t *testing.T) {
	s := newStore(t)
	_, st, t1 := tree(t, s)
	t2 := mk(t, s, items.Task, st.Key, "Second")
	setStatus(t, s, t2, items.Ready)

	wantDenied(t, move(t, s, st.Key, items.InProgress, user), "Couldn't update status. The item remains Ready.")
	wantDenied(t, move(t, s, st.Key, items.Done, user), "Couldn't move STORY-1 to Done. Complete all child tasks and their checkpoints first.")

	all := []items.Status{items.Draft, items.Ready, items.InProgress, items.Blocked, items.InReview, items.AwaitingApproval, items.Done, items.Cancelled}
	oracle := func(kids []items.Status, cur items.Status) items.Status {
		n, done, fin, review, moved := len(kids), 0, 0, 0, 0
		for _, k := range kids {
			switch k {
			case items.Done:
				done++
				fin++
			case items.Cancelled:
				fin++
			case items.InReview:
				review++
			}
			if k != items.Draft && k != items.Ready {
				moved++
			}
		}
		switch {
		case fin == n && done > 0:
			return items.Done
		case fin == n:
			return cur
		case fin+review == n:
			return items.InReview
		case moved > 0:
			return items.InProgress
		}
		return items.Ready
	}
	for _, a := range all {
		for _, b := range all {
			for _, start := range []items.Status{items.Ready, items.InProgress, items.InReview, items.Done} {
				setStatus(t, s, st, start)
				setStatus(t, s, t1, a)
				setStatus(t, s, t2, b)
				if err := s.Reconcile(ctx, t1.Key); err != nil {
					t.Fatal(err)
				}
				want := oracle([]items.Status{a, b}, start)
				if got := mustGet(t, s, st.Key).Status; got != want {
					t.Errorf("children %s,%s from %s: story = %s, want %s", a, b, start, got, want)
				}
			}
		}
	}
	// named cases the oracle encodes
	setStatus(t, s, st, items.InProgress)
	setStatus(t, s, t1, items.Done)
	setStatus(t, s, t2, items.Done)
	s.Reconcile(ctx, t1.Key)
	wantStatus(t, s, st.Key, items.Done) // last child finishing marks the story done
	if err := move(t, s, t2.Key, items.Ready, user); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, s, st.Key, items.InProgress) // reopening a child reopens the story
	setStatus(t, s, st, items.Draft)
	setStatus(t, s, t1, items.InProgress)
	s.Reconcile(ctx, t1.Key)
	wantStatus(t, s, st.Key, items.Draft) // draft stories aren't derived
}

func TestNewChildReopensDoneStory(t *testing.T) {
	s := newStore(t)
	_, st, task := tree(t, s)
	setStatus(t, s, task, items.Done)
	s.Reconcile(ctx, task.Key)
	wantStatus(t, s, st.Key, items.Done)
	mk(t, s, items.Task, st.Key, "Follow-up")
	wantStatus(t, s, st.Key, items.InProgress)
}

type binding struct {
	ItemRevision         int             `json:"item_revision"`
	IntegratedCheckpoint string          `json:"integrated_checkpoint"`
	Git                  json.RawMessage `json:"git"`
}

func acceptRequests(t *testing.T, s *items.Store, it items.Item) (states map[string]string, open binding) {
	t.Helper()
	rows, err := s.DB.Query(`SELECT id, kind, state, binding_json FROM requests WHERE item_id = ? AND kind IN ('accept_epic', 'accept_fix')`, it.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	states = map[string]string{}
	for rows.Next() {
		var id, kind, state, b string
		rows.Scan(&id, &kind, &state, &b)
		states[id] = kind + ":" + state
		if state == "open" {
			json.Unmarshal([]byte(b), &open)
		}
	}
	return states, open
}

func count(states map[string]string, v string) int {
	n := 0
	for _, s := range states {
		if s == v {
			n++
		}
	}
	return n
}

const gitJSON = `[{"repo":"agent-swarm","branch":"epic/epic-1-auth","sha":"abc1234","dirty":false}]`

func TestEpicAcceptanceFlow(t *testing.T) {
	s := newStore(t)
	e, st, task := tree(t, s)
	daemon := items.Daemon()

	seedCheckpoint(t, s.DB, e, "accepted", 1, later(s), "")
	s.Reconcile(ctx, e.Key)
	wantStatus(t, s, e.Key, items.InProgress)

	seedCheckpoint(t, s.DB, e, "integrated", 1, later(s), gitJSON) // before the child finished
	setStatus(t, s, task, items.Done)
	s.Reconcile(ctx, task.Key)
	wantStatus(t, s, st.Key, items.Done)
	wantStatus(t, s, e.Key, items.InProgress)

	ckp := seedCheckpoint(t, s.DB, e, "integrated", 1, later(s), gitJSON)
	s.Reconcile(ctx, e.Key)
	s.Reconcile(ctx, e.Key) // opens exactly once
	wantStatus(t, s, e.Key, items.InReview)
	states, b := acceptRequests(t, s, e)
	if len(states) != 1 || count(states, "accept_epic:open") != 1 {
		t.Fatalf("requests = %v", states)
	}
	if b.IntegratedCheckpoint != ckp || b.ItemRevision != mustGet(t, s, e.Key).Revision || string(b.Git) != gitJSON {
		t.Fatalf("binding = %+v", b)
	}
	wantDenied(t, move(t, s, e.Key, items.Done, user), "Accept this epic to mark it Done.")
	wantDenied(t, move(t, s, e.Key, items.Done, daemon), "Accept this epic to mark it Done.")

	exec(t, s.DB, `UPDATE requests SET state = 'approved' WHERE item_id = ?`, e.ID)
	s.Reconcile(ctx, e.Key)
	wantStatus(t, s, e.Key, items.Done)

	// reopen clears the old acceptance and doesn't jump back
	if err := move(t, s, e.Key, items.Ready, user); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, s, e.Key, items.Ready)
	states, _ = acceptRequests(t, s, e)
	if count(states, "accept_epic:stale") != 1 {
		t.Fatalf("reopen: requests = %v", states)
	}
	// the board only invalidates its inbox on request.*, so staling must be announced
	resolved := eventsOfType(t, s, events.RequestResolved)
	if len(resolved) != 1 {
		t.Fatalf("reopen: request.resolved = %v", resolved)
	}
	if got, want := resolved[0], (map[string]string{"id": onlyKey(t, states), "kind": "accept_epic",
		"item": e.Key, "state": "stale"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("reopen: payload = %v, want %v", got, want)
	}
}

// Cancelling a root in review must resolve its open accept request, not orphan it.
func TestCancelStalesTheOpenAcceptRequest(t *testing.T) {
	s := newStore(t)
	b := mk(t, s, items.Bug, "", "Crash")
	task := mk(t, s, items.Task, b.Key, "Fix")
	setStatus(t, s, b, items.InProgress)
	setStatus(t, s, task, items.Done)
	seedCheckpoint(t, s.DB, b, "integrated", 1, later(s), gitJSON)
	s.Reconcile(ctx, b.Key)
	wantStatus(t, s, b.Key, items.InReview)

	if err := move(t, s, b.Key, items.Cancelled, user); err != nil {
		t.Fatal(err)
	}
	states, _ := acceptRequests(t, s, b)
	if count(states, "accept_fix:open") != 0 || count(states, "accept_fix:stale") != 1 {
		t.Fatalf("cancel: requests = %v", states)
	}
	resolved := eventsOfType(t, s, events.RequestResolved)
	if len(resolved) != 1 || resolved[0]["item"] != b.Key || resolved[0]["kind"] != "accept_fix" ||
		resolved[0]["state"] != "stale" || resolved[0]["id"] != onlyKey(t, states) {
		t.Fatalf("cancel: request.resolved = %v", resolved)
	}
}

func onlyKey(t *testing.T, m map[string]string) string {
	t.Helper()
	if len(m) != 1 {
		t.Fatalf("want one request, got %v", m)
	}
	for k := range m {
		return k
	}
	return ""
}

func TestAcceptRequestsGoStale(t *testing.T) {
	s := newStore(t)
	b := mk(t, s, items.Bug, "", "Crash")
	task := mk(t, s, items.Task, b.Key, "Fix")
	setStatus(t, s, b, items.InProgress)
	setStatus(t, s, task, items.Done)
	seedCheckpoint(t, s.DB, b, "integrated", 1, later(s), gitJSON)
	s.Reconcile(ctx, b.Key)
	wantStatus(t, s, b.Key, items.InReview)
	wantDenied(t, move(t, s, b.Key, items.Done, user), "Accept this fix to mark it Done.")

	// a newer integration replaces the request
	second := seedCheckpoint(t, s.DB, b, "integrated", 1, later(s), gitJSON)
	s.Reconcile(ctx, b.Key)
	wantStatus(t, s, b.Key, items.InReview)
	states, open := acceptRequests(t, s, b)
	if count(states, "accept_fix:stale") != 1 || count(states, "accept_fix:open") != 1 || open.IntegratedCheckpoint != second {
		t.Fatalf("after newer integration: %v %+v", states, open)
	}

	// a revision change stales it and sends the bug back until the next integration
	cur := mustGet(t, s, b.Key)
	title := "Crash on login"
	if _, err := s.Update(ctx, b.Key, items.Patch{Title: &title, Revision: cur.Revision}, user); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, s, b.Key, items.InProgress)
	if states, _ := acceptRequests(t, s, b); count(states, "accept_fix:stale") != 2 || count(states, "accept_fix:open") != 0 {
		t.Fatalf("after edit: %v", states)
	}
	seedCheckpoint(t, s.DB, b, "integrated", 1, later(s), gitJSON)
	s.Reconcile(ctx, b.Key)
	wantStatus(t, s, b.Key, items.InReview)

	// changes requested sends it back without a new request
	exec(t, s.DB, `UPDATE requests SET state = 'changes_requested' WHERE item_id = ? AND state = 'open'`, b.ID)
	s.Reconcile(ctx, b.Key)
	wantStatus(t, s, b.Key, items.InProgress)
	if states, _ := acceptRequests(t, s, b); count(states, "accept_fix:open") != 0 {
		t.Fatalf("after change request: %v", states)
	}
	seedCheckpoint(t, s.DB, b, "integrated", 1, later(s), gitJSON)
	s.Reconcile(ctx, b.Key)
	wantStatus(t, s, b.Key, items.InReview)

	// a reopened child stales it too
	if err := move(t, s, task.Key, items.Ready, user); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, s, b.Key, items.InProgress)
	if states, _ := acceptRequests(t, s, b); count(states, "accept_fix:open") != 0 {
		t.Fatalf("after reopen: %v", states)
	}
	// an approval bound to an old revision never completes the bug
	setStatus(t, s, task, items.Done)
	seedCheckpoint(t, s.DB, b, "integrated", 1, later(s), gitJSON)
	s.Reconcile(ctx, b.Key)
	exec(t, s.DB, `UPDATE requests SET state = 'approved' WHERE item_id = ? AND state = 'open'`, b.ID)
	exec(t, s.DB, `UPDATE items SET revision = revision + 1 WHERE id = ?`, b.ID)
	s.Reconcile(ctx, b.Key)
	wantStatus(t, s, b.Key, items.InProgress)
	wantDenied(t, move(t, s, b.Key, items.Done, items.Daemon()), "Accept this fix to mark it Done.")
}

func TestEpicManualMoves(t *testing.T) {
	s := newStore(t)
	e, _, _ := tree(t, s)
	orch := items.Orchestrator("agt_o", e.ID)
	wantDenied(t, move(t, s, e.Key, items.InProgress, orch), "Couldn't update status. The item remains Ready.")
	wantDenied(t, move(t, s, e.Key, items.InProgress, items.Daemon()), "Couldn't update status. The item remains Ready.")
	seedCheckpoint(t, s.DB, e, "accepted", 1, later(s), "")
	if err := move(t, s, e.Key, items.InProgress, items.Daemon()); err != nil {
		t.Fatal(err)
	}
	wantDenied(t, move(t, s, e.Key, items.InReview, items.Daemon()), "Couldn't update status. The item remains In progress.")
	setStatus(t, s, e, items.InReview)
	if err := move(t, s, e.Key, items.InProgress, orch); err != nil {
		t.Fatal(err)
	}
	draft := mk(t, s, items.Epic, "", "Draft epic")
	if err := move(t, s, draft.Key, items.Ready, user); err != nil {
		t.Fatal(err)
	}
}

func TestSpikeTransitions(t *testing.T) {
	s := newStore(t)
	daemon := items.Daemon()
	sp := mk(t, s, items.Spike, "", "Offline mode")
	wantDenied(t, move(t, s, sp.Key, items.InProgress, daemon), "Couldn't update status. The item remains Draft.")
	seedCheckpoint(t, s.DB, sp, "accepted", 1, later(s), "")
	s.Reconcile(ctx, sp.Key)
	wantStatus(t, s, sp.Key, items.InProgress)

	req := seedRequest(t, s.DB, sp, "approve_section", "open")
	s.Reconcile(ctx, sp.Key)
	wantStatus(t, s, sp.Key, items.AwaitingApproval)
	wantDenied(t, move(t, s, sp.Key, items.InProgress, daemon), "Couldn't update status. The item remains Awaiting approval.")
	exec(t, s.DB, `UPDATE requests SET state = 'approved' WHERE id = ?`, req)
	s.Reconcile(ctx, sp.Key)
	wantStatus(t, s, sp.Key, items.InProgress)
	wantDenied(t, move(t, s, sp.Key, items.AwaitingApproval, daemon), "Couldn't update status. The item remains In progress.")

	for _, by := range []items.Actor{user, daemon, items.Orchestrator("agt_s", sp.ID)} {
		wantDenied(t, move(t, s, sp.Key, items.Done, by), "This spike reaches Done after materialization.")
	}

	// close_spike approved → done, with no new item
	close := seedRequest(t, s.DB, sp, "close_spike", "open")
	s.Reconcile(ctx, sp.Key)
	wantStatus(t, s, sp.Key, items.AwaitingApproval)
	exec(t, s.DB, `UPDATE requests SET state = 'approved' WHERE id = ?`, close)
	s.Reconcile(ctx, sp.Key)
	wantStatus(t, s, sp.Key, items.Done)

	// reopening stales the close approval and doesn't bounce back
	if err := move(t, s, sp.Key, items.Ready, user); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, s, sp.Key, items.Ready)
	var state string
	s.DB.QueryRow(`SELECT state FROM requests WHERE id = ?`, close).Scan(&state)
	if state != "stale" {
		t.Fatalf("close request = %s", state)
	}

	// materialization is the other way to done
	sp2 := mk(t, s, items.Spike, "", "Payments")
	setStatus(t, s, sp2, items.InProgress)
	if _, err := s.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Payments", OriginSpikeID: sp2.ID}, daemon); err != nil {
		t.Fatal(err)
	}
	wantDenied(t, move(t, s, sp2.Key, items.Done, user), "This spike reaches Done after materialization.")
	if err := move(t, s, sp2.Key, items.Done, daemon); err != nil {
		t.Fatal(err)
	}
	sp3 := mk(t, s, items.Spike, "", "Dropped")
	if err := move(t, s, sp3.Key, items.Cancelled, user); err != nil {
		t.Fatal(err)
	}
}

// TestCompletedCurrentIsPerAgent reproduces the cross-agent attempt bug (spec
// B5/Context): completedCurrent used to take MAX(attempt) across every
// checkpoint on the item, mixing each agent's own independent attempt
// counter. A workflow task's reviewer cycles through its own review
// attempts on a totally different counter than the builder's -- that must
// never mask or invalidate the builder's own completed checkpoint.
func TestCompletedCurrentIsPerAgent(t *testing.T) {
	s := newStore(t)
	_, st, _ := tree(t, s)
	daemon := items.Daemon()

	// Positive case: the reviewer's own attempt counter (5) is way ahead of
	// the coder's (1) -- must not stop the coder's completed@1 from counting.
	taskA := mk(t, s, items.Task, st.Key, "Batched fix")
	setWorkflowJSON(t, s.DB, taskA)
	setStatus(t, s, taskA, items.InProgress)
	coderAgent, coderSes := seedSessionRole(t, s.DB, taskA, "coder", "running")
	seedCheckpointFor(t, s.DB, taskA, coderAgent, coderSes, "completed", 1, later(s))
	reviewerAgent, reviewerSes := seedSessionRole(t, s.DB, taskA, "reviewer", "running")
	seedCheckpointFor(t, s.DB, taskA, reviewerAgent, reviewerSes, "progress", 5, later(s))
	if err := move(t, s, taskA.Key, items.InReview, daemon); err != nil {
		t.Fatalf("the reviewer's unrelated attempt counter must not block the coder's own completed: %v", err)
	}
	wantStatus(t, s, taskA.Key, items.InReview)

	// Negative case: the SAME coder later posts a non-completed checkpoint at
	// a higher attempt -- its own stale completed@1 must stop counting.
	taskB := mk(t, s, items.Task, st.Key, "Batched fix, retried")
	setWorkflowJSON(t, s.DB, taskB)
	setStatus(t, s, taskB, items.InProgress)
	coderAgent2, coderSes2 := seedSessionRole(t, s.DB, taskB, "coder", "running")
	seedCheckpointFor(t, s.DB, taskB, coderAgent2, coderSes2, "completed", 1, later(s))
	seedCheckpointFor(t, s.DB, taskB, coderAgent2, coderSes2, "progress", 2, later(s))
	wantDenied(t, move(t, s, taskB.Key, items.InReview, daemon),
		"Couldn't update status. The item remains In progress.")
}

// TestLegacyCompletedCurrentIsPerBuilder is the legacy-task remainder of the
// cross-agent attempt bug (F1/TASK-242): the Workflow==nil branch compared a
// completed checkpoint against the item-wide MAX(attempt), so agent A's
// attempt-2 accepted (then cancelled) masked agent B's completed at attempt
// 1. The legacy comparison must be per-builder: the latest relevant builder
// completion against THAT builder's own latest attempt.
func TestLegacyCompletedCurrentIsPerBuilder(t *testing.T) {
	daemon := items.Daemon()

	// Cancelled predecessor at attempt 2 must not mask the successor's
	// completed at attempt 1 (TASK-242, no workflow_json on either task).
	s := newStore(t)
	_, st, _ := tree(t, s)
	task := mk(t, s, items.Task, st.Key, "Legacy fix")
	setStatus(t, s, task, items.InProgress)
	aAgent, aSes := seedSessionRole(t, s.DB, task, "coder", "running")
	exec(t, s.DB, `UPDATE sessions SET attempt = 2, state = 'cancelled' WHERE id = ?`, aSes)
	seedCheckpointFor(t, s.DB, task, aAgent, aSes, "accepted", 2, later(s))
	bAgent, bSes := seedSessionRole(t, s.DB, task, "coder", "running")
	seedCheckpointFor(t, s.DB, task, bAgent, bSes, "completed", 1, later(s))
	if err := move(t, s, task.Key, items.InReview, daemon); err != nil {
		t.Fatalf("cancelled attempt-2 must not mask completed attempt-1: %v", err)
	}
	if err := move(t, s, task.Key, items.Done, daemon); err != nil {
		t.Fatalf("done after the successor completion: %v", err)
	}
	wantStatus(t, s, task.Key, items.Done)

	// Same-agent stale: the same builder's own later attempt voids its
	// earlier completion.
	s2 := newStore(t)
	_, st2, _ := tree(t, s2)
	stale := mk(t, s2, items.Task, st2.Key, "Legacy retry")
	setStatus(t, s2, stale, items.InProgress)
	cAgent, cSes := seedSessionRole(t, s2.DB, stale, "coder", "running")
	seedCheckpointFor(t, s2.DB, stale, cAgent, cSes, "completed", 1, later(s2))
	seedCheckpointFor(t, s2.DB, stale, cAgent, cSes, "progress", 2, later(s2))
	wantDenied(t, move(t, s2, stale.Key, items.InReview, daemon),
		"Couldn't update status. The item remains In progress.")

	// Reviewer-only completion is not a builder completion.
	s3 := newStore(t)
	_, st3, _ := tree(t, s3)
	rev := mk(t, s3, items.Task, st3.Key, "Legacy review only")
	setStatus(t, s3, rev, items.InProgress)
	rAgent, rSes := seedSessionRole(t, s3.DB, rev, "reviewer", "running")
	seedCheckpointFor(t, s3.DB, rev, rAgent, rSes, "completed", 1, later(s3))
	wantDenied(t, move(t, s3, rev.Key, items.InReview, daemon),
		"Couldn't update status. The item remains In progress.")

	// Tied timestamps: two builders complete at the same instant -- the
	// completion still counts.
	s4 := newStore(t)
	_, st4, _ := tree(t, s4)
	tied := mk(t, s4, items.Task, st4.Key, "Legacy tie")
	setStatus(t, s4, tied, items.InProgress)
	at := later(s4)
	dAgent, dSes := seedSessionRole(t, s4.DB, tied, "coder", "running")
	seedCheckpointFor(t, s4.DB, tied, dAgent, dSes, "completed", 1, at)
	eAgent, eSes := seedSessionRole(t, s4.DB, tied, "coder", "running")
	seedCheckpointFor(t, s4.DB, tied, eAgent, eSes, "completed", 1, at)
	if err := move(t, s4, tied.Key, items.InReview, daemon); err != nil {
		t.Fatalf("tied builder completions must count: %v", err)
	}

	// Reopened item: after Done -> Ready and a same-agent retry, the
	// pre-reopen completion is stale against the agent's own new attempt.
	s5 := newStore(t)
	_, st5, _ := tree(t, s5)
	re := mk(t, s5, items.Task, st5.Key, "Legacy reopened")
	setStatus(t, s5, re, items.InProgress)
	fAgent, fSes := seedSessionRole(t, s5.DB, re, "coder", "running")
	seedCheckpointFor(t, s5.DB, re, fAgent, fSes, "completed", 1, later(s5))
	if err := move(t, s5, re.Key, items.InReview, daemon); err != nil {
		t.Fatal(err)
	}
	if err := move(t, s5, re.Key, items.Done, daemon); err != nil {
		t.Fatal(err)
	}
	if err := move(t, s5, re.Key, items.Ready, user); err != nil {
		t.Fatal(err)
	}
	seedCheckpointFor(t, s5.DB, re, fAgent, fSes, "progress", 2, later(s5))
	setStatus(t, s5, re, items.InProgress)
	wantDenied(t, move(t, s5, re.Key, items.InReview, daemon),
		"Couldn't update status. The item remains In progress.")
}

// Fix round 1, R2: completedCurrent's workflow branch counts "build roles"
// -- any role except reviewer/ui_reviewer/orchestrator, not just
// coder/debugger/mechanical -- so a design-reviewed template's designer step
// can finish too.
func TestCompletedCurrentCountsDesignerOnAWorkflowTask(t *testing.T) {
	s := newStore(t)
	_, st, _ := tree(t, s)
	daemon := items.Daemon()

	task := mk(t, s, items.Task, st.Key, "Design the flow")
	setWorkflowJSON(t, s.DB, task)
	setStatus(t, s, task, items.InProgress)
	designerAgent, designerSes := seedSessionRole(t, s.DB, task, "designer", "running")
	seedCheckpointFor(t, s.DB, task, designerAgent, designerSes, "completed", 1, later(s))
	if err := move(t, s, task.Key, items.InReview, daemon); err != nil {
		t.Fatalf("a designer's completed on a workflow task must count: %v", err)
	}
	wantStatus(t, s, task.Key, items.InReview)
}

func TestWorkflowSucceededGatesDone(t *testing.T) {
	s := newStore(t)
	_, _, task := tree(t, s)
	setWorkflowJSON(t, s.DB, task)
	daemon := items.Daemon()
	// No workflows row: Done is engine-only.
	err := move(t, s, task.Key, items.Done, daemon)
	if code(err) != items.CodeTransitionDenied || !strings.Contains(err.Error(), "finished by its workflow") {
		t.Fatalf("Done without a workflow row: err = %v, want transition_denied finished-by-workflow", err)
	}
	agentID, _ := seedSession(t, s.DB, task, "running")
	exec(t, s.DB, `INSERT INTO workflows (id, item_id, root_item_id, owner_agent_id, state, worktrees_json, created_at, updated_at)
		VALUES ('wfl_1', ?, ?, ?, 'succeeded', '[]', ?, ?)`,
		task.ID, task.RootID, agentID, later(s), later(s))
	if err := move(t, s, task.Key, items.Done, daemon); err != nil {
		t.Fatalf("Done with a succeeded workflow: err = %v, want nil", err)
	}
	wantStatus(t, s, task.Key, items.Done)
}

func TestWorkflowFailedOrCancelledResetsToReady(t *testing.T) {
	for _, state := range []string{"failed", "cancelled", "running"} {
		s := newStore(t)
		_, _, task := tree(t, s)
		setWorkflowJSON(t, s.DB, task)
		daemon := items.Daemon()
		agentID, _ := seedSession(t, s.DB, task, "running")
		exec(t, s.DB, `INSERT INTO workflows (id, item_id, root_item_id, owner_agent_id, state, worktrees_json, created_at, updated_at)
			VALUES ('wfl_1', ?, ?, ?, ?, '[]', ?, ?)`,
			task.ID, task.RootID, agentID, state, later(s), later(s))
		setStatus(t, s, task, items.InProgress)
		err := move(t, s, task.Key, items.Ready, daemon)
		if state == "running" {
			if code(err) != items.CodeTransitionDenied {
				t.Fatalf("state %s: Ready reset err = %v, want transition_denied", state, err)
			}
		} else {
			if err != nil {
				t.Fatalf("state %s: Ready reset err = %v, want nil", state, err)
			}
			wantStatus(t, s, task.Key, items.Ready)
		}
		// Done stays engine-only for every non-succeeded state.
		setStatus(t, s, task, items.InProgress)
		if err := move(t, s, task.Key, items.Done, daemon); code(err) != items.CodeTransitionDenied ||
			!strings.Contains(err.Error(), "finished by its workflow") {
			t.Fatalf("state %s: Done err = %v, want transition_denied finished-by-workflow", state, err)
		}
	}
}

func TestPatchStatusGoesThroughTransition(t *testing.T) {
	s := newStore(t)
	e := mk(t, s, items.Epic, "", "E")
	ready := items.Ready
	got, err := s.Update(ctx, e.Key, items.Patch{Status: &ready, Revision: e.Revision}, user)
	if err != nil || got.Status != items.Ready || got.Revision != e.Revision+1 {
		t.Fatalf("patch = %+v, %v", got, err)
	}
	done := items.Done
	_, err = s.Update(ctx, e.Key, items.Patch{Status: &done, Revision: got.Revision}, user)
	wantDenied(t, err, "Accept this epic to mark it Done.")
	if _, err := s.Update(ctx, e.Key, items.Patch{Status: &done, Revision: e.Revision}, user); code(err) != items.CodeConflict {
		t.Fatalf("stale revision: %v", err)
	}
	evs, _ := s.Events.After(ctx, 0, 100)
	if len(evs) < 2 {
		t.Fatalf("transitions must emit item.changed: %d", len(evs))
	}
}
