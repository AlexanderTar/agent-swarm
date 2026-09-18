package runtime

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os/exec"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// seedSectionApproval writes a spike, an artifact with one section and an open
// approve_section request, without needing Task 18's registrar. The sha256 is the
// real hash of the section body, because Approve compares it and a placeholder
// would make every approval a conflict.
func seedSectionApproval(t *testing.T, s *Store) (Request, Session, Artifact) {
	t.Helper()
	ctx := context.Background()
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Design the flow",
		Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	it, err := s.Items.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	const body = "The flow is three screens.\n"
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
	artID, now := ids.New("art"), db.Millis(s.Now())
	sections := fmt.Sprintf(`[{"id":"overview","heading":"Overview","sha256":%q,"approval_state":"none"}]`, sum)
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO artifacts
		(id, item_id, kind, path, head_revision, created_by, created_at)
		VALUES (?, ?, 'spec', 'docs/specs/flow.md', 1, ?, ?)`, artID, it.ID, a.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO artifact_revisions
		(artifact_id, revision, sha256, content, sections_json, created_at)
		VALUES (?, 1, ?, ?, ?, ?)`, artID, sum, "## Overview\n"+body, sections, now); err != nil {
		t.Fatal(err)
	}
	reqID := ids.New("req")
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO requests
		(id, kind, agent_id, session_id, item_id, artifact_id, section_id, section_sha256,
		 prompt, state, artifact_revision, created_at)
		VALUES (?, 'approve_section', ?, ?, ?, ?, 'overview', ?, 'Approve the overview.', 'open', 1, ?)`,
		reqID, a.ID, ses.ID, it.ID, artID, sum, now); err != nil {
		t.Fatal(err)
	}
	// The real flow reaches awaiting_approval through Ask + ReconcileTx; this
	// helper writes the request directly (Task 18's registrar doesn't exist yet),
	// so it forces the same status by hand.
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = 'awaiting_approval', updated_at = ?
		WHERE id = ?`, now, it.ID); err != nil {
		t.Fatal(err)
	}
	req, err := s.RequestByID(ctx, reqID)
	if err != nil {
		t.Fatal(err)
	}
	// There is no ArtifactByID in the ledger and this task does not add one: the
	// helper already knows every value it just wrote, so it fills the struct.
	art := Artifact{ID: artID, ItemID: it.ID, Kind: "spec", Path: "docs/specs/flow.md",
		HeadRevision: 1, Revision: 1,
		Sections: []ArtifactSection{{ID: "overview", Title: "Overview", SHA256: sum}}}
	return req, ses, art
}

func seedEpicWithTask(t *testing.T, s *Store) items.Item {
	t.Helper()
	ctx := context.Background()
	ep, err := s.Items.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Build it"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	story, err := s.Items.Create(ctx, items.CreateInput{Type: items.Story, ParentKey: ep.Key,
		Title: "Build the thing"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: story.Key,
		Title: "Write the failing test"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	// `ready` directly, not through Transition: the daemon's Ready->InProgress hop
	// needs an accepted checkpoint, which these tests are not about.
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = 'ready' WHERE id IN (?, ?, ?)`,
		ep.ID, story.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	return ep
}

func seedEpicWithTwoTasks(t *testing.T, s *Store) items.Item {
	t.Helper()
	ctx := context.Background()
	ep, err := s.Items.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Build it"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	story, err := s.Items.Create(ctx, items.CreateInput{Type: items.Story, ParentKey: ep.Key,
		Title: "Build the thing"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	t1, err := s.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: story.Key,
		Title: "Write the failing test"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	t2, err := s.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: story.Key,
		Title: "Implement the fix"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = 'ready' WHERE id IN (?, ?, ?, ?)`,
		ep.ID, story.ID, t1.ID, t2.ID); err != nil {
		t.Fatal(err)
	}
	return ep
}

func seedEpicWithThreeTasks(t *testing.T, s *Store) items.Item {
	t.Helper()
	ctx := context.Background()
	ep, err := s.Items.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Build it"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	story, err := s.Items.Create(ctx, items.CreateInput{Type: items.Story, ParentKey: ep.Key,
		Title: "Build the thing"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	t1, err := s.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: story.Key,
		Title: "Write the failing test"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	t2, err := s.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: story.Key,
		Title: "Implement the fix"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	t3, err := s.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: story.Key,
		Title: "Refactor"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = 'ready' WHERE id IN (?, ?, ?, ?, ?)`,
		ep.ID, story.ID, t1.ID, t2.ID, t3.ID); err != nil {
		t.Fatal(err)
	}
	return ep
}

// startSessionForTest exposes the unexported startSession to other test files
// in the package (Task 15: TDD evidence survives a pause into a new generation).
func (s *Store) startSessionForTest(ctx context.Context, a Agent, attempt, generation int) (Session, error) {
	return s.startSession(ctx, a, attempt, generation, false, "")
}

func gitRepoNoSigning(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "t@example.invalid"},
		{"config", "user.name", "T"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dir
}
