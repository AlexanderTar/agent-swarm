package runtime

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// realUniqueViolation runs a genuine duplicate insert through the SQLite
// driver and returns the raw error, so the mapping test pins the driver's
// actual shape instead of a hand-built imitation.
func realUniqueViolation(t *testing.T) error {
	t.Helper()
	raw, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`CREATE TABLE workflows (id TEXT PRIMARY KEY, item_id TEXT);
CREATE UNIQUE INDEX workflows_one_live ON workflows(item_id);`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO workflows(id, item_id) VALUES('a','x')`); err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`INSERT INTO workflows(id, item_id) VALUES('b','x')`)
	if err == nil {
		t.Fatal("expected a unique violation, got nil")
	}
	return err
}

func TestWorkflowsOneLiveErrMapsUniqueViolation(t *testing.T) {
	mapped := workflowsOneLiveErr(realUniqueViolation(t), "TASK-1")
	var apiErr *items.Error
	if !errors.As(mapped, &apiErr) {
		t.Fatalf("mapped = %#v, want *items.Error", mapped)
	}
	if apiErr.Code != items.CodeBadRequest || apiErr.Message != "TASK-1 already has a running workflow." {
		t.Fatalf("mapped = %+v, want bad-request already-running refusal", apiErr)
	}
}

func TestWorkflowsOneLiveErrPassesThroughUnrelatedErrors(t *testing.T) {
	if got := workflowsOneLiveErr(nil, "TASK-1"); got != nil {
		t.Fatalf("nil = %v, want nil", got)
	}
	// Same message text, but not a SQLite constraint error: only the
	// database's own typed failure may become the friendly refusal.
	plain := fmt.Errorf("UNIQUE constraint failed: workflows.item_id")
	if got := workflowsOneLiveErr(plain, "TASK-1"); got != plain {
		t.Fatalf("plain error mapped to %#v, want passthrough", got)
	}
}
