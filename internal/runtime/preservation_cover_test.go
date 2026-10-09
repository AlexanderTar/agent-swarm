package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

func TestPreservationGatesRefuseNonSaveToolsAndEmptyCommands(t *testing.T) {
	if err := PreservationMCPAllowed("swarm_sync"); err != nil {
		t.Fatalf("swarm_sync = %v, want allowed", err)
	}
	if err := PreservationMCPAllowed("swarm_spawn"); err == nil || !strings.Contains(err.Error(), "starts new work") {
		t.Fatalf("swarm_spawn err = %v, want the new-work refusal", err)
	}
	if err := PreservationMCPAllowed("swarm_unheard_of"); err == nil || !strings.Contains(err.Error(), "is not a save tool") {
		t.Fatalf("unknown tool err = %v, want the not-a-save-tool refusal", err)
	}
	if err := PreservationCommandAllowed("   "); !isBadRequest(err, "empty command") {
		t.Fatalf("blank command err = %v", err)
	}
	if err := PreservationCommandAllowed("git status"); err != nil {
		t.Fatalf("git status = %v, want allowed", err)
	}
}

func TestValidatePreservationReadyBlocksOnEachUnsafeManifest(t *testing.T) {
	ctx := context.Background()
	const head = "0123456789abcdef0123456789abcdef01234567"
	type fixture struct {
		manifest    func(opID, agentID string) any // nil -> no manifest recorded
		raw         string                         // overrides manifest when set
		wrongHash   bool
		missingFile bool
		disk        func(path string) (string, error)
		status      string
		statusErr   error
	}
	okDisk := func(string) (string, error) { return head, nil }
	wt := func(observed string) func(opID, agentID string) any {
		return func(opID, agentID string) any {
			return HandoffManifest{OperationID: opID, AgentID: agentID,
				Worktrees: []ManifestWorktree{{ID: "wt_x", Path: "/nowhere/wt_x", ObservedHead: observed}}}
		}
	}
	cases := []struct {
		name string
		f    fixture
		want string
	}{
		{"no manifest recorded", fixture{}, "no manifest recorded"},
		{"manifest file gone", fixture{manifest: wt(head), missingFile: true}, "unreadable"},
		{"hash mismatch", fixture{manifest: wt(head), wrongHash: true}, "hash mismatch"},
		{"not json", fixture{raw: "{{{"}, "is not JSON"},
		{"other operation's manifest", fixture{manifest: func(string, string) any { return HandoffManifest{OperationID: "op_other"} }}, "ownership mismatch"},
		{"worktree unreadable", fixture{manifest: wt(head), disk: func(string) (string, error) { return "", errors.New("not a repo") }}, "worktree /nowhere/wt_x unreadable"},
		{"no observed head", fixture{manifest: wt(""), disk: okDisk}, "has no observed HEAD"},
		{"head moved", fixture{manifest: wt("fedcba9876543210"), disk: okDisk}, "moved under handoff"},
		{"status unreadable", fixture{manifest: wt(head), disk: okDisk, statusErr: errors.New("boom")}, "status unreadable"},
		{"tracked changes", fixture{manifest: wt(head), disk: okDisk, status: " M main.go\n?? scratch.txt\n"}, "uncommitted tracked changes (main.go)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _, _ := newStore(t)
			_, w, wSes := worker(t, s)
			opID := "op_" + strings.ReplaceAll(c.name, " ", "_")
			var path, hash string
			if c.f.manifest != nil || c.f.raw != "" {
				body := []byte(c.f.raw)
				if c.f.raw == "" {
					b, err := json.Marshal(c.f.manifest(opID, w.ID))
					if err != nil {
						t.Fatal(err)
					}
					body = b
				}
				path = filepath.Join(t.TempDir(), "manifest.json")
				if err := os.WriteFile(path, body, 0o600); err != nil {
					t.Fatal(err)
				}
				hash = fmt.Sprintf("%x", sha256.Sum256(body))
				if c.f.wrongHash {
					hash = strings.Repeat("0", 64)
				}
				if c.f.missingFile {
					os.Remove(path)
				}
			}
			now := db.Millis(s.Now())
			if _, err := s.DB.ExecContext(ctx, `INSERT INTO agent_operations
				(id, agent_id, mode, phase, request_key, session_id, generation, manifest_path, manifest_hash, created_at, updated_at)
				VALUES (?, ?, 'handoff', 'preserving', ?, ?, ?, ?, ?, ?, ?)`,
				opID, w.ID, opID, wSes.ID, wSes.Generation, path, hash, now, now); err != nil {
				t.Fatal(err)
			}
			prevHead, prevStatus := readDiskHEAD, readDiskStatus
			t.Cleanup(func() { readDiskHEAD, readDiskStatus = prevHead, prevStatus })
			if c.f.disk != nil {
				readDiskHEAD = c.f.disk
			}
			readDiskStatus = func(string) (string, error) { return c.f.status, c.f.statusErr }

			err := s.ValidatePreservationReady(ctx, opID)
			var ie *items.Error
			if !errors.As(err, &ie) || ie.Code != items.CodeConflict || !strings.Contains(ie.Message, c.want) {
				t.Fatalf("err = %v, want a conflict containing %q", err, c.want)
			}
			op, err := s.getOperation(ctx, opID)
			if err != nil || op.Phase != PhaseBlocked {
				t.Fatalf("operation = %+v err=%v, want blocked (never a fabricated ready)", op, err)
			}
			// A blocked operation is terminal for the gate: asking again is a no-op, not a second block.
			if err := s.ValidatePreservationReady(ctx, opID); err != nil {
				t.Fatalf("second validate = %v, want nil for a decided operation", err)
			}
		})
	}

	t.Run("unknown operation", func(t *testing.T) {
		s, _, _ := newStore(t)
		if err := s.ValidatePreservationReady(ctx, "op_missing"); !conflictOrNotFound(err, items.CodeNotFound) {
			t.Fatalf("err = %v, want not found", err)
		}
	})
}
