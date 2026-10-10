package items_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/workflow"
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
	wantDenied(t, move(t, s, e.Key, items.Done, user), "Finish this epic to mark it Done.")
	wantDenied(t, move(t, s, e.Key, items.Done, daemon), "Finish this epic to mark it Done.")

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
	wantDenied(t, move(t, s, b.Key, items.Done, user), "Finish this fix to mark it Done.")

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
	// an approval followed by an edit to the bug never completes it
	setStatus(t, s, task, items.Done)
	seedCheckpoint(t, s.DB, b, "integrated", 1, later(s), gitJSON)
	s.Reconcile(ctx, b.Key)
	exec(t, s.DB, `UPDATE requests SET state = 'approved' WHERE item_id = ? AND state = 'open'`, b.ID)
	brief := "Also covers the SSO path"
	if _, err := s.Update(ctx, b.Key, items.Patch{Brief: &brief, Revision: mustGet(t, s, b.Key).Revision}, user); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, s, b.Key, items.InProgress)
	if states, _ := acceptRequests(t, s, b); count(states, "accept_fix:approved") != 0 {
		t.Fatalf("after edit of approved: %v", states)
	}
	wantDenied(t, move(t, s, b.Key, items.Done, items.Daemon()), "Finish this fix to mark it Done.")
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
	if _, err := s.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Payments", OriginSpikeID: sp2.ID, Materialized: true}, daemon); err != nil {
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

// TestSpikeProposalIsNotMaterialization is BUG-75: a top-level item the spike's
// orchestrator proposes, or a bug it reports to the board, names the spike as
// origin but is not its materialized root, so the spike stays open.
func TestSpikeProposalIsNotMaterialization(t *testing.T) {
	s := newStore(t)
	sp := mk(t, s, items.Spike, "", "COROS")
	setStatus(t, s, sp, items.InProgress)
	if _, err := s.Create(ctx, items.CreateInput{Type: items.Chore, Title: "Proposed"},
		items.Orchestrator("agt_s", sp.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, items.CreateInput{Type: items.Bug, Title: "Reported", OriginSpikeID: sp.ID},
		items.Daemon()); err != nil {
		t.Fatal(err)
	}
	s.Reconcile(ctx, sp.Key)
	wantStatus(t, s, sp.Key, items.InProgress)
	wantDenied(t, move(t, s, sp.Key, items.Done, items.Daemon()), "This spike reaches Done after materialization.")

	if _, err := s.Create(ctx, items.CreateInput{Type: items.Epic, Title: "COROS", OriginSpikeID: sp.ID,
		Materialized: true}, items.Daemon()); err != nil {
		t.Fatal(err)
	}
	s.Reconcile(ctx, sp.Key)
	wantStatus(t, s, sp.Key, items.Done)
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
	wantDenied(t, err, "Finish this epic to mark it Done.")
	if _, err := s.Update(ctx, e.Key, items.Patch{Status: &done, Revision: e.Revision}, user); code(err) != items.CodeConflict {
		t.Fatalf("stale revision: %v", err)
	}
	evs, _ := s.Events.After(ctx, 0, 100)
	if len(evs) < 2 {
		t.Fatalf("transitions must emit item.changed: %d", len(evs))
	}
}

// Spec C1, C4: cancelling a parent cancels every unfinished descendant,
// nested story -> task included; Done work stays Done; reopening the parent
// leaves the children cancelled.
func TestCancelCascadesToUnfinishedDescendants(t *testing.T) {
	s := newStore(t)
	e, st, task := tree(t, s)
	setStatus(t, s, task, items.InProgress)
	done := mk(t, s, items.Task, st.Key, "Already done")
	setStatus(t, s, done, items.Done)
	blocked := mk(t, s, items.Task, st.Key, "Waiting")
	setStatus(t, s, blocked, items.Blocked)
	st2 := mk(t, s, items.Story, e.Key, "Second") // Draft
	nested := mk(t, s, items.Task, st2.Key, "Nested")
	setStatus(t, s, nested, items.Ready)

	if err := move(t, s, e.Key, items.Cancelled, user); err != nil {
		t.Fatal(err)
	}
	cascaded := []string{st.Key, task.Key, blocked.Key, st2.Key, nested.Key}
	for _, k := range cascaded {
		wantStatus(t, s, k, items.Cancelled)
	}
	wantStatus(t, s, done.Key, items.Done)

	if err := move(t, s, e.Key, items.Ready, user); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, s, e.Key, items.Ready)
	for _, k := range cascaded {
		wantStatus(t, s, k, items.Cancelled)
	}
}

// Spec C2: an orchestrator's story cancel reaches the story's tasks and
// leaves the epic alone.
func TestOrchestratorStoryCancelCascadesToItsTasks(t *testing.T) {
	s := newStore(t)
	e, st, task := tree(t, s)
	if err := move(t, s, st.Key, items.Cancelled, items.Orchestrator("agt_o", e.ID)); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, s, task.Key, items.Cancelled)
	wantStatus(t, s, e.Key, items.Ready)
}

// Spec E8/E10/decision 4: a chore with a task finishes through accept_fix.
func TestChoreAcceptanceFlow(t *testing.T) {
	s := newStore(t)
	ch := mk(t, s, items.Chore, "", "Bump deps")
	task := mk(t, s, items.Task, ch.Key, "Bump go deps")
	setStatus(t, s, ch, items.Ready)
	seedCheckpoint(t, s.DB, ch, "accepted", 1, later(s), "")
	s.Reconcile(ctx, ch.Key)
	wantStatus(t, s, ch.Key, items.InProgress)

	setStatus(t, s, task, items.Done)
	ckp := seedCheckpoint(t, s.DB, ch, "integrated", 1, later(s), gitJSON)
	s.Reconcile(ctx, ch.Key)
	wantStatus(t, s, ch.Key, items.InReview)
	states, b := acceptRequests(t, s, ch)
	if len(states) != 1 || count(states, "accept_fix:open") != 1 || b.IntegratedCheckpoint != ckp {
		t.Fatalf("requests = %v, binding = %+v", states, b)
	}
	var prompt string
	if err := s.DB.QueryRow(`SELECT prompt FROM requests WHERE item_id = ?`, ch.ID).Scan(&prompt); err != nil {
		t.Fatal(err)
	}
	if prompt != "Review the chore and accept it." {
		t.Fatalf("prompt = %q", prompt)
	}
	wantDenied(t, move(t, s, ch.Key, items.Done, user), "Finish this chore to mark it Done.")
	wantDenied(t, move(t, s, ch.Key, items.Done, items.Daemon()), "Finish this chore to mark it Done.")

	exec(t, s.DB, `UPDATE requests SET state = 'approved' WHERE item_id = ?`, ch.ID)
	s.Reconcile(ctx, ch.Key)
	wantStatus(t, s, ch.Key, items.Done)
}

// Spec E9/E10: a chore needs no child; a declined acceptance goes back to work
// and a fresh integration asks again. An epic with no children still waits.
func TestChoreWithNoTasksReachesDone(t *testing.T) {
	s := newStore(t)
	ch := mk(t, s, items.Chore, "", "Rotate cert")
	setStatus(t, s, ch, items.Ready)
	seedCheckpoint(t, s.DB, ch, "accepted", 1, later(s), "")
	s.Reconcile(ctx, ch.Key)
	wantStatus(t, s, ch.Key, items.InProgress)
	seedCheckpoint(t, s.DB, ch, "integrated", 1, later(s), gitJSON)
	s.Reconcile(ctx, ch.Key)
	wantStatus(t, s, ch.Key, items.InReview)

	exec(t, s.DB, `UPDATE requests SET state = 'changes_requested' WHERE item_id = ?`, ch.ID)
	s.Reconcile(ctx, ch.Key)
	wantStatus(t, s, ch.Key, items.InProgress)
	seedCheckpoint(t, s.DB, ch, "integrated", 1, later(s), gitJSON)
	s.Reconcile(ctx, ch.Key)
	wantStatus(t, s, ch.Key, items.InReview)
	states, _ := acceptRequests(t, s, ch)
	if count(states, "accept_fix:open") != 1 {
		t.Fatalf("requests = %v", states)
	}
	exec(t, s.DB, `UPDATE requests SET state = 'approved' WHERE item_id = ? AND state = 'open'`, ch.ID)
	s.Reconcile(ctx, ch.Key)
	wantStatus(t, s, ch.Key, items.Done)

	e := mk(t, s, items.Epic, "", "Empty epic")
	setStatus(t, s, e, items.Ready)
	seedCheckpoint(t, s.DB, e, "accepted", 1, later(s), "")
	s.Reconcile(ctx, e.Key)
	seedCheckpoint(t, s.DB, e, "integrated", 1, later(s), gitJSON)
	s.Reconcile(ctx, e.Key)
	// CHORE-64: any root, not just a chore, may finish with no tasks.
	wantStatus(t, s, e.Key, items.InReview)
	if states, _ := acceptRequests(t, s, e); count(states, "accept_epic:open") != 1 {
		t.Fatalf("a childless epic with an integration asks for acceptance: %v", states)
	}
}

// Spec decision 6 / E5: BUG-2-style stuck roots recover with one move to Ready.
func TestDraftRootPromotionHonorsEarlierAccepted(t *testing.T) {
	s := newStore(t)
	b := mk(t, s, items.Bug, "", "Crash") // Draft, as materialize used to leave it
	task := mk(t, s, items.Task, b.Key, "Fix")
	seedCheckpoint(t, s.DB, b, "accepted", 1, later(s), "")
	s.Reconcile(ctx, b.Key)
	wantStatus(t, s, b.Key, items.Draft) // the live stuck state
	setStatus(t, s, task, items.Done)
	seedCheckpoint(t, s.DB, b, "integrated", 1, later(s), gitJSON)

	if err := move(t, s, b.Key, items.Ready, user); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, s, b.Key, items.InReview)
	if states, _ := acceptRequests(t, s, b); count(states, "accept_fix:open") != 1 {
		t.Fatalf("requests = %v", states)
	}

	fresh := mk(t, s, items.Epic, "", "Fresh proposal") // no accepted yet
	if err := move(t, s, fresh.Key, items.Ready, user); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, s, fresh.Key, items.Ready)
}

const gitJSONAB = `[{"repo":"a","branch":"epic/epic-1-auth","sha":"aaa1111","dirty":false},` +
	`{"repo":"b","branch":"epic/epic-1-auth","sha":"bbb2222","dirty":false},` +
	`{"repo":"a","branch":"epic/epic-1-auth","sha":"aaa1111","dirty":false}]`

// finishRoot builds an in_review root of typ (an epic or bug with a done task, or a
// chore with none) whose newest integrated checkpoint names repos a and b, and returns it with that
// checkpoint id. The open accept request is still open.
func finishRoot(t *testing.T, s *items.Store, typ items.Type) (items.Item, string) {
	t.Helper()
	var root items.Item
	if typ == items.Epic {
		e, _, task := tree(t, s)
		setStatus(t, s, task, items.Done)
		s.Reconcile(ctx, task.Key)
		root = e
	} else {
		root = mk(t, s, typ, "", "Root")
		if typ == items.Bug { // a bug needs a finished task; a chore may have none
			task := mk(t, s, items.Task, root.Key, "Fix")
			setStatus(t, s, task, items.Done)
		}
		setStatus(t, s, root, items.Ready)
	}
	seedCheckpoint(t, s.DB, root, "accepted", 1, later(s), "")
	s.Reconcile(ctx, root.Key)
	ckp := seedCheckpoint(t, s.DB, root, "integrated", 1, later(s), gitJSONAB)
	s.Reconcile(ctx, root.Key)
	wantStatus(t, s, root.Key, items.InReview)
	for _, r := range []string{"a", "b"} {
		exec(t, s.DB, `INSERT OR IGNORE INTO repos (id, name, path, default_branch, source, created_at, updated_at)
			VALUES (?, ?, ?, 'main', 'manual', 1, 1)`, "repo_"+r, r, "/tmp/"+r)
	}
	return mustGet(t, s, root.Key), ckp
}

func approveFinish(t *testing.T, s *items.Store, it items.Item, merge string) string {
	t.Helper()
	var id string
	if err := s.DB.QueryRow(`SELECT id FROM requests WHERE item_id = ? AND state = 'open'`, it.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	b := `binding_json`
	if merge != "" {
		b = `json_set(binding_json, '$.merge', '` + merge + `')`
	}
	exec(t, s.DB, `UPDATE requests SET state = 'approved', agent_id = NULL, responded_at = ?, binding_json = `+b+` WHERE id = ?`, later(s), id)
	return id
}

func insertMerge(t *testing.T, s *items.Store, it items.Item, ckp, repo, state string) {
	t.Helper()
	exec(t, s.DB, `INSERT INTO item_merges (id, item_id, integrated_checkpoint, repo, repo_id, kind, base, head, state, created_at)
		VALUES (?, ?, ?, ?, ?, 'local', 'main', 'epic/epic-1-auth', ?, ?)`,
		"mrg_"+repo+"_"+ckp, it.ID, ckp, repo, "repo_"+repo, state, later(s))
}

func TestFinishStatusRule(t *testing.T) {
	cases := []struct {
		name  string
		merge string
		rows  map[string]string
		want  items.Status
	}{
		{"pre-0022 approval", "", nil, items.Done},
		{"auto, no rows", "auto", nil, items.InReview},
		{"auto, one of two merged", "auto", map[string]string{"a": "merged", "b": "open"}, items.InReview},
		{"auto, both merged", "auto", map[string]string{"a": "merged", "b": "merged"}, items.Done},
		{"manual, both merged", "manual", map[string]string{"a": "merged", "b": "merged"}, items.Done},
		{"auto, one closed", "auto", map[string]string{"a": "closed"}, items.InProgress},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			e, ckp := finishRoot(t, s, items.Epic)
			approveFinish(t, s, e, c.merge)
			for repo, st := range c.rows {
				insertMerge(t, s, e, ckp, repo, st)
			}
			s.Reconcile(ctx, e.Key)
			s.Reconcile(ctx, e.Key)
			wantStatus(t, s, e.Key, c.want)
			var n int
			s.DB.QueryRow(`SELECT COUNT(*) FROM requests WHERE item_id = ? AND kind = 'accept_epic'
				AND json_extract(binding_json, '$.integrated_checkpoint') = ?`, e.ID, ckp).Scan(&n)
			if n != 1 {
				t.Fatalf("accept_epic rows for the checkpoint = %d, want 1", n)
			}
		})
	}
}

func TestFinishOlderCheckpointRowsDoNotCount(t *testing.T) {
	s := newStore(t)
	ch, old := finishRoot(t, s, items.Chore)
	exec(t, s.DB, `UPDATE requests SET state = 'changes_requested' WHERE item_id = ?`, ch.ID)
	insertMerge(t, s, ch, old, "a", "merged")
	insertMerge(t, s, ch, old, "b", "merged")
	s.Reconcile(ctx, ch.Key)
	wantStatus(t, s, ch.Key, items.InProgress)
	seedCheckpoint(t, s.DB, ch, "integrated", 1, later(s), gitJSONAB)
	s.Reconcile(ctx, ch.Key)
	wantStatus(t, s, ch.Key, items.InReview)
	approveFinish(t, s, mustGet(t, s, ch.Key), "auto")
	s.Reconcile(ctx, ch.Key)
	wantStatus(t, s, ch.Key, items.InReview)
}

func TestFinishDaemonDoneDeniedUntilMerged(t *testing.T) {
	for typ, msg := range map[items.Type]string{
		items.Epic:  "Finish this epic to mark it Done.",
		items.Chore: "Finish this chore to mark it Done.",
		items.Bug:   "Finish this fix to mark it Done.",
	} {
		t.Run(string(typ), func(t *testing.T) {
			s := newStore(t)
			it, ckp := finishRoot(t, s, typ)
			approveFinish(t, s, it, "auto")
			wantDenied(t, move(t, s, it.Key, items.Done, items.Daemon()), msg)
			insertMerge(t, s, it, ckp, "a", "merged")
			insertMerge(t, s, it, ckp, "b", "merged")
			if err := move(t, s, it.Key, items.Done, items.Daemon()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFinishApprovalTx(t *testing.T) {
	s := newStore(t)
	e, ckp := finishRoot(t, s, items.Epic)
	if _, ok, err := s.FinishApprovalTx(ctx, s.DB, e.ID); err != nil || ok {
		t.Fatalf("open request: ok = %v, err = %v", ok, err)
	}
	reqID := approveFinish(t, s, e, "auto")
	agentID, _ := seedSession(t, s.DB, e, "running")
	exec(t, s.DB, `UPDATE requests SET agent_id = ? WHERE id = ?`, agentID, reqID)
	fa, ok, err := s.FinishApprovalTx(ctx, s.DB, e.ID)
	if err != nil || !ok {
		t.Fatalf("ok = %v, err = %v", ok, err)
	}
	if fa.RequestID != reqID || fa.AgentID != agentID || fa.Merge != "auto" || fa.CheckpointID != ckp || string(fa.Git) != gitJSONAB {
		t.Fatalf("fa = %+v", fa)
	}
	// TASK-457: blocking and unblocking moves the revision but keeps the approval
	if err := move(t, s, e.Key, items.Blocked, user); err != nil {
		t.Fatal(err)
	}
	if err := move(t, s, e.Key, items.InReview, user); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, s, e.Key, items.InReview)
	if _, ok, err := s.FinishApprovalTx(ctx, s.DB, e.ID); err != nil || !ok {
		t.Fatalf("after block round trip: ok = %v, err = %v", ok, err)
	}
	acc := []string{"Login works with SSO"}
	if _, err := s.Update(ctx, e.Key, items.Patch{Acceptance: &acc, Revision: mustGet(t, s, e.Key).Revision}, user); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.FinishApprovalTx(ctx, s.DB, e.ID); err != nil || ok {
		t.Fatalf("after acceptance edit: ok = %v, err = %v", ok, err)
	}
}

func TestFinishApprovalStaledByWaiverEdit(t *testing.T) {
	s := newStore(t)
	e, _ := finishRoot(t, s, items.Epic)
	approveFinish(t, s, e, "auto")
	orch := items.Orchestrator("agt_o", e.ID)
	if _, err := s.Update(ctx, e.Key, items.Patch{Revision: e.Revision,
		Waive: []items.WaiveInput{{Gate: items.WaivableGates[0], Reason: "flaky CI"}}}, orch); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.FinishApprovalTx(ctx, s.DB, e.ID); err != nil || ok {
		t.Fatalf("after waiver: ok = %v, err = %v", ok, err)
	}
	wantStatus(t, s, e.Key, items.InProgress)
}

func forceTo(it items.Item, to items.Status, reason string) items.Patch {
	return items.Patch{Revision: it.Revision, Status: &to, OverrideReason: reason}
}

func TestOverrideForcesWorkflowTaskDoneAndCancelsItsWorkflow(t *testing.T) {
	s := newStore(t)
	e, st, task := tree(t, s)
	orch := items.Orchestrator("agt_o", e.ID)
	setWorkflowJSON(t, s.DB, task)
	setStatus(t, s, task, items.InReview)
	agentID, _ := seedSession(t, s.DB, task, "running")
	exec(t, s.DB, `INSERT INTO workflows (id, item_id, root_item_id, owner_agent_id, state, worktrees_json, created_at, updated_at)
		VALUES ('wfl_1', ?, ?, ?, 'running', '[]', ?, ?)`, task.ID, task.RootID, agentID, later(s), later(s))
	exec(t, s.DB, `INSERT INTO workflow_runs (id, workflow_id, step_id, round, role, agent_id, state, created_at)
		VALUES ('wfr_1', 'wfl_1', 'build', 1, 'coder', ?, 'active', ?)`, agentID, later(s))
	task = mustGet(t, s, task.Key)

	// Without a reason the workflow still owns Done.
	_, err := s.Update(ctx, task.Key, items.Patch{Revision: task.Revision, Status: ptr(items.Done)}, orch)
	wantDenied(t, err, task.Key+" is finished by its workflow. It moves to Done when the workflow succeeds; use swarm_workflow resume to accept or fail it.")

	got, err := s.Update(ctx, task.Key, forceTo(task, items.Done, "shipped by hand"), orch)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != items.Done || got.Override == nil || got.Override.Status != items.Done ||
		got.Override.Reason != "shipped by hand" || got.Override.Agent != "agt_o" || got.Override.At.IsZero() {
		t.Fatalf("task = %s %+v", got.Status, got.Override)
	}
	var wfState, runState string
	if err := s.DB.QueryRow(`SELECT state FROM workflows WHERE id = 'wfl_1'`).Scan(&wfState); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow(`SELECT state FROM workflow_runs WHERE id = 'wfr_1'`).Scan(&runState); err != nil {
		t.Fatal(err)
	}
	if wfState != "cancelled" || runState != "cancelled" {
		t.Fatalf("workflow = %s, run = %s, want both cancelled", wfState, runState)
	}
	wantStatus(t, s, st.Key, items.Done) // the only child finished, so the story follows
	evs, err := s.Events.After(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, ev := range evs {
		if ev.Type == events.ItemOverridden {
			seen = true
			p := string(ev.Payload)
			if !strings.Contains(p, task.Key) || !strings.Contains(p, "done") || !strings.Contains(p, "shipped by hand") {
				t.Fatalf("item.overridden payload = %s", p)
			}
		}
	}
	if !seen {
		t.Fatal("no item.overridden event")
	}
}

func ptr[T any](v T) *T { return &v }

func TestOverrideStoryDoneSurvivesReconcileUntilANormalTransition(t *testing.T) {
	s := newStore(t)
	e, st, t1 := tree(t, s)
	t2 := mk(t, s, items.Task, st.Key, "Second")
	setStatus(t, s, t2, items.Ready)
	orch := items.Orchestrator("agt_o", e.ID)

	got, err := s.Update(ctx, st.Key, forceTo(st, items.Done, "split into another epic"), orch)
	if err != nil || got.Status != items.Done || got.Override == nil {
		t.Fatalf("override story = %s %+v, %v", got.Status, got.Override, err)
	}
	if err := s.Reconcile(ctx, t1.Key); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, s, st.Key, items.Done) // two unfinished children would otherwise pull it back
	if mustGet(t, s, st.Key).Override == nil {
		t.Fatal("reconcile must not clear the override")
	}
	// A later normal transition (the user reopening it) clears the override and
	// hands the story back to derivation.
	if err := move(t, s, st.Key, items.Ready, user); err != nil {
		t.Fatal(err)
	}
	after := mustGet(t, s, st.Key)
	if after.Override != nil || after.Status != items.Ready {
		t.Fatalf("after reopen = %s %+v, want ready and no override", after.Status, after.Override)
	}
}

func TestOverrideReopenDoneToReadyAndAnyNonCancelledSource(t *testing.T) {
	s := newStore(t)
	e, _, task := tree(t, s)
	orch := items.Orchestrator("agt_o", e.ID)
	for _, from := range []items.Status{items.Draft, items.Ready, items.InProgress, items.InReview, items.Done} {
		for _, to := range []items.Status{items.Ready, items.InProgress, items.InReview, items.Done} {
			if from == to {
				continue
			}
			setStatus(t, s, task, from)
			cur := mustGet(t, s, task.Key)
			got, err := s.Update(ctx, task.Key, forceTo(cur, to, "orchestrator call"), orch)
			if err != nil || got.Status != to {
				t.Errorf("%s -> %s: status = %s, err = %v", from, to, got.Status, err)
			}
		}
	}
	setStatus(t, s, task, items.Cancelled)
	cur := mustGet(t, s, task.Key)
	if _, err := s.Update(ctx, task.Key, forceTo(cur, items.Ready, "x"), orch); code(err) != items.CodeTransitionDenied {
		t.Fatalf("override out of cancelled err = %v, want transition_denied", err)
	}
}

func TestOverrideRefusals(t *testing.T) {
	s := newStore(t)
	e, st, task := tree(t, s)
	orch := items.Orchestrator("agt_o", e.ID)
	worker := items.Actor{Kind: items.ActorAgent, AgentID: "agt_w", Role: "coder", RootID: e.RootID}
	const only = "Only an orchestrator can waive gates or override status."
	for name, c := range map[string]struct {
		by    items.Actor
		it    items.Item
		to    items.Status
		why   string
		want  string
		wcode string
	}{
		"worker":        {worker, task, items.Done, "x", only, items.CodeBadRequest},
		"user":          {user, task, items.Done, "x", only, items.CodeBadRequest},
		"empty reason":  {orch, task, items.Done, "  ", "Give a reason (1–300 characters).", items.CodeBadRequest},
		"long reason":   {orch, task, items.Done, strings.Repeat("x", 301), "Give a reason (1–300 characters).", items.CodeBadRequest},
		"root done":     {orch, e, items.Done, "x", "A root reaches Done only through its finish question.", items.CodeBadRequest},
		"root progress": {orch, e, items.InProgress, "x", "Roots follow their own lifecycle; only a task or story can be overridden.", items.CodeBadRequest},
		"blocked":       {orch, task, items.Blocked, "x", "An override can set ready, in_progress, in_review or done.", items.CodeBadRequest},
		"cancelled":     {orch, task, items.Cancelled, "x", "An override can set ready, in_progress, in_review or done.", items.CodeBadRequest},
	} {
		_, err := s.Update(ctx, c.it.Key, forceTo(c.it, c.to, c.why), c.by)
		if err == nil || err.Error() != c.want || code(err) != c.wcode {
			t.Errorf("%s: err = %v (%s), want %q", name, err, code(err), c.want)
		}
	}
	// A reason with no status has nothing to override.
	_, err := s.Update(ctx, st.Key, items.Patch{Revision: st.Revision, OverrideReason: "x"}, orch)
	if err == nil || err.Error() != "override_reason needs a status to force." {
		t.Fatalf("reason without status err = %v", err)
	}
	if mustGet(t, s, task.Key).Override != nil || mustGet(t, s, e.Key).Override != nil {
		t.Fatal("a refused override must not write")
	}
	// Outside the orchestrator's tree.
	other := mk(t, s, items.Epic, "", "Other")
	os := mk(t, s, items.Story, other.Key, "OS")
	_, err = s.Update(ctx, os.Key, forceTo(os, items.Done, "x"), orch)
	if err == nil || err.Error() != os.Key+" is outside "+e.Key+"." {
		t.Fatalf("outside tree err = %v", err)
	}
}

func TestOverrideReasonWithAnAllowedMoveRecordsNoOverride(t *testing.T) {
	s := newStore(t)
	e, _, task := tree(t, s)
	orch := items.Orchestrator("agt_o", e.ID)
	setStatus(t, s, task, items.InReview)
	cur := mustGet(t, s, task.Key)
	got, err := s.Update(ctx, task.Key, forceTo(cur, items.InProgress, "send it back"), orch)
	if err != nil || got.Status != items.InProgress || got.Override != nil {
		t.Fatalf("allowed move = %s %+v, %v; want a plain transition", got.Status, got.Override, err)
	}
}

func TestLaterTransitionClearsATaskOverride(t *testing.T) {
	s := newStore(t)
	e, _, task := tree(t, s)
	orch := items.Orchestrator("agt_o", e.ID)
	got, err := s.Update(ctx, task.Key, forceTo(task, items.InProgress, "work started offline"), orch)
	if err != nil || got.Override == nil || got.Status != items.InProgress {
		t.Fatalf("force in_progress = %s %+v, %v", got.Status, got.Override, err)
	}
	if err := move(t, s, task.Key, items.Cancelled, orch); err != nil {
		t.Fatal(err)
	}
	if after := mustGet(t, s, task.Key); after.Override != nil || after.Status != items.Cancelled {
		t.Fatalf("after cancel = %s %+v", after.Status, after.Override)
	}
}

// resolveFixture: a Draft bug, a Done chore, and an in-tree task.
func resolveFixture(t *testing.T) (s *items.Store, bug, chore items.Item) {
	t.Helper()
	s = newStore(t)
	bug = mk(t, s, items.Bug, "", "Broken")
	chore = mk(t, s, items.Chore, "", "Fix it")
	setStatus(t, s, chore, items.Done)
	return s, mustGet(t, s, bug.Key), mustGet(t, s, chore.Key)
}

func resolve(s *items.Store, it items.Item, by string, a items.Actor) error {
	cur, err := s.Get(ctx, it.Key)
	if err != nil {
		return err
	}
	_, err = s.Update(ctx, it.Key, items.Patch{Status: ptr(items.Done), ResolvedBy: &by, Revision: cur.Revision}, a)
	return err
}

func TestResolvedByAllowed(t *testing.T) {
	for _, from := range []items.Status{items.Draft, items.Ready, items.InProgress, items.InReview, items.Blocked} {
		s, bug, chore := resolveFixture(t)
		child := mk(t, s, items.Task, bug.Key, "Child")
		if from != items.Draft {
			setStatus(t, s, bug, from)
		}
		if err := resolve(s, bug, chore.Key, user); err != nil {
			t.Fatalf("from %s: %v", from, err)
		}
		got := mustGet(t, s, bug.Key)
		if got.Status != items.Done || got.ResolvedBy != chore.Key {
			t.Fatalf("from %s: status %s resolved_by %q", from, got.Status, got.ResolvedBy)
		}
		wantStatus(t, s, child.Key, items.Cancelled)
		evs, err := s.Events.After(ctx, 0, 1000)
		if err != nil {
			t.Fatal(err)
		}
		var seen bool
		for _, ev := range evs {
			if ev.Type == events.ItemResolved {
				seen = true
				p := string(ev.Payload)
				if !strings.Contains(p, chore.Key) || !strings.Contains(p, `"actor"`) {
					t.Fatalf("payload = %s", p)
				}
			}
		}
		if !seen {
			t.Fatal("no item.resolved event")
		}
	}
}

func TestResolvedByStalesAcceptRequests(t *testing.T) {
	s, bug, chore := resolveFixture(t)
	req := seedRequest(t, s.DB, bug, "accept_fix", "open")
	if err := resolve(s, bug, chore.Key, user); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := s.DB.QueryRow(`SELECT state FROM requests WHERE id = ?`, req).Scan(&state); err != nil || state != "stale" {
		t.Fatalf("request state = %q, %v", state, err)
	}
}

func TestResolvedByOrchestratorOfAnotherRoot(t *testing.T) {
	s, bug, chore := resolveFixture(t)
	if err := resolve(s, bug, chore.Key, items.Orchestrator("agt_o", chore.ID)); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, s, bug.Key, items.Done)
	// the exemption is for this transition only
	other := mk(t, s, items.Bug, "", "Other")
	setStatus(t, s, other, items.InProgress) // a Draft/Ready foreign root takes title/brief edits
	_, err := s.Update(ctx, other.Key, items.Patch{Title: ptr("x"), Revision: other.Revision}, items.Orchestrator("agt_o", chore.ID))
	if err == nil {
		t.Fatal("orchestrator edited another root")
	}
}

func TestResolvedByRefused(t *testing.T) {
	s, bug, chore := resolveFixture(t)
	notDone := mk(t, s, items.Chore, "", "Open chore")
	spike := mk(t, s, items.Spike, "", "Spike")
	setStatus(t, s, spike, items.Done)
	story := mk(t, s, items.Story, mk(t, s, items.Epic, "", "E").Key, "S")
	setStatus(t, s, story, items.Done)
	task := mk(t, s, items.Task, bug.Key, "T")
	var closed []items.Item
	for _, st := range []items.Status{items.Done, items.Cancelled, items.AwaitingApproval} {
		c := mk(t, s, items.Bug, "", "Closed "+string(st))
		setStatus(t, s, c, st)
		closed = append(closed, c)
	}
	worker := items.Actor{Kind: items.ActorAgent, AgentID: "agt_w", Role: "coder", RootID: bug.ID}
	cases := []struct {
		name string
		it   items.Item
		by   string
		a    items.Actor
	}{
		{"missing", bug, "CHORE-999", user},
		{"self", bug, bug.Key, user},
		{"target not done", bug, notDone.Key, user},
		{"target spike", bug, spike.Key, user},
		{"target non-root", bug, story.Key, user},
		{"subject non-root", task, chore.Key, user},
		{"subject spike", mk(t, s, items.Spike, "", "S2"), chore.Key, user},
		{"from done", closed[0], chore.Key, user},
		{"from cancelled", closed[1], chore.Key, user},
		{"from awaiting approval", closed[2], chore.Key, user},
		{"worker", bug, chore.Key, worker},
		{"daemon", bug, chore.Key, items.Daemon()},
	}
	for _, c := range cases {
		if err := resolve(s, c.it, c.by, c.a); err == nil {
			t.Errorf("%s: want refusal", c.name)
		}
	}
	wantStatus(t, s, bug.Key, items.Draft)
	// resolved_by needs status done
	cur := mustGet(t, s, bug.Key)
	if _, err := s.Update(ctx, bug.Key, items.Patch{ResolvedBy: &chore.Key, Revision: cur.Revision}, user); err == nil {
		t.Error("resolved_by without status done accepted")
	}
	if _, err := s.Update(ctx, bug.Key, items.Patch{Status: ptr(items.Ready), ResolvedBy: &chore.Key, Revision: cur.Revision}, user); err == nil {
		t.Error("resolved_by with status ready accepted")
	}
}

func crossRootFixture(t *testing.T, from items.Status) (s *items.Store, foreign, story items.Item, orch items.Actor) {
	t.Helper()
	s = newStore(t)
	mine := mk(t, s, items.Chore, "", "Mine")
	foreign = mk(t, s, items.Epic, "", "Theirs")
	story = mk(t, s, items.Story, foreign.Key, "Their story")
	if from != items.Draft {
		setStatus(t, s, foreign, from)
	}
	return s, mustGet(t, s, foreign.Key), mustGet(t, s, story.Key), items.Orchestrator("agt_o", mine.ID)
}

func crossRootEvents(t *testing.T, s *items.Store) []string {
	t.Helper()
	evs, err := s.Events.After(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, ev := range evs {
		if ev.Type == events.ItemCrossRootEdit {
			out = append(out, string(ev.Payload))
		}
	}
	return out
}

func TestCrossRootEditsOnDraftAndReadyRoots(t *testing.T) {
	for _, from := range []items.Status{items.Draft, items.Ready} {
		s, foreign, story, orch := crossRootFixture(t, from)
		task, err := s.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: story.Key, Title: "Do it", Workflow: &workflow.Spec{Template: "tdd-reviewed"}}, orch)
		if err != nil {
			t.Fatalf("%s: create: %v", from, err)
		}
		if _, err := s.Update(ctx, task.Key, items.Patch{Brief: ptr("why"), Title: ptr("Do it now"),
			Acceptance: &[]string{"works"}, Revision: task.Revision}, orch); err != nil {
			t.Fatalf("%s: update: %v", from, err)
		}
		if _, err := s.Update(ctx, foreign.Key, items.Patch{Brief: ptr("context"), Revision: foreign.Revision}, orch); err != nil {
			t.Fatalf("%s: update root: %v", from, err)
		}
		evs := crossRootEvents(t, s)
		if len(evs) != 3 || !strings.Contains(evs[0], `"actor":"agt_o"`) || !strings.Contains(evs[0], `"actor_root"`) {
			t.Fatalf("%s: cross-root events = %v", from, evs)
		}
	}
}

func TestCrossRootEditsRefused(t *testing.T) {
	s, foreign, story, orch := crossRootFixture(t, items.Ready)
	task := mk(t, s, items.Task, story.Key, "Existing")
	cases := map[string]func() error{
		"status change": func() error {
			_, err := s.Update(ctx, task.Key, items.Patch{Status: ptr(items.Cancelled), Revision: task.Revision}, orch)
			return err
		},
		"priority": func() error {
			_, err := s.Update(ctx, task.Key, items.Patch{Priority: ptr(1), Revision: task.Revision}, orch)
			return err
		},
		"transition": func() error { _, err := s.Transition(ctx, task.Key, items.Ready, orch); return err },
	}
	for name, f := range cases {
		if err := f(); err == nil {
			t.Errorf("%s: want refusal", name)
		}
	}
	// a live orchestrator in the foreign root closes the door
	seedSessionRole(t, s.DB, foreign, "orchestrator", "running")
	if _, err := s.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: story.Key, Title: "x", Workflow: &workflow.Spec{Template: "tdd-reviewed"}}, orch); err == nil {
		t.Error("create under a root with a live orchestrator accepted")
	}
	if _, err := s.Update(ctx, task.Key, items.Patch{Brief: ptr("x"), Revision: task.Revision}, orch); err == nil {
		t.Error("update under a root with a live orchestrator accepted")
	}
	if got := crossRootEvents(t, s); len(got) != 0 {
		t.Fatalf("events for refused edits: %v", got)
	}
}

func TestCrossRootEditsRefusedOnOpenRoot(t *testing.T) {
	for _, from := range []items.Status{items.InProgress, items.InReview, items.Done} {
		s, _, story, orch := crossRootFixture(t, from)
		if _, err := s.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: story.Key, Title: "x", Workflow: &workflow.Spec{Template: "tdd-reviewed"}}, orch); err == nil {
			t.Errorf("%s: create accepted", from)
		}
		if _, err := s.Update(ctx, story.Key, items.Patch{Brief: ptr("x"), Revision: story.Revision}, orch); err == nil {
			t.Errorf("%s: update accepted", from)
		}
	}
}

func TestResolvedByLandedCheck(t *testing.T) {
	const git = `[{"repo":"agent-swarm","branch":"bug-9/integration","sha":"112d3dfc"}]`
	for _, c := range []struct {
		name    string
		git     string // "" seeds no integrated checkpoint
		hookErr error
		refused bool
		called  bool
	}{
		{"unmerged sha refused", git, errors.New("112d3df is not on agent-swarm's main"), true, true},
		{"landed sha resolves", git, nil, false, true},
		{"no integrated checkpoint resolves", "", errors.New("must not run"), false, false},
		{"integrated with no git resolves", "[]", errors.New("must not run"), false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, bug, chore := resolveFixture(t)
			if c.git != "" {
				seedCheckpoint(t, s.DB, bug, "integrated", 1, later(s), c.git)
			}
			var called bool
			s.CheckLanded = func(_ context.Context, _ *sql.Tx, root items.Item, gotGit []byte) error {
				called = true
				if root.Key != bug.Key || string(gotGit) != c.git {
					t.Fatalf("hook got %s %s", root.Key, gotGit)
				}
				return c.hookErr
			}
			err := resolve(s, bug, chore.Key, user)
			if (err != nil) != c.refused || called != c.called {
				t.Fatalf("err = %v, hook called = %v", err, called)
			}
			if c.refused {
				if !strings.Contains(err.Error(), "112d3df") {
					t.Fatalf("err = %v", err)
				}
				if mustGet(t, s, bug.Key).Status == items.Done {
					t.Fatal("refused root went Done")
				}
			}
		})
	}
}

func TestResolvedByAbandonUnmerged(t *testing.T) {
	s, bug, chore := resolveFixture(t)
	seedCheckpoint(t, s.DB, bug, "integrated", 1, later(s), `[{"repo":"agent-swarm","branch":"b","sha":"112d3dfc"}]`)
	s.CheckLanded = func(context.Context, *sql.Tx, items.Item, []byte) error { return errors.New("112d3df is not on main") }
	by := chore.Key
	cur := mustGet(t, s, bug.Key)
	if _, err := s.Update(ctx, bug.Key, items.Patch{Status: ptr(items.Done), ResolvedBy: &by, Revision: cur.Revision}, user); err == nil {
		t.Fatal("unmerged close was not refused")
	}
	if _, err := s.Update(ctx, bug.Key, items.Patch{Status: ptr(items.Done), ResolvedBy: &by, AbandonUnmerged: true, Revision: cur.Revision}, user); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, s, bug.Key, items.Done)
	evs, err := s.Events.After(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Type == events.ItemResolved {
			if !strings.Contains(string(ev.Payload), `"abandon_unmerged":"true"`) {
				t.Fatalf("payload = %s", ev.Payload)
			}
			return
		}
	}
	t.Fatal("no item.resolved event")
}
