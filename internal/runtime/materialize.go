package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/workflow"
)

// MaterializeResult is Materialize's result.
type MaterializeResult struct {
	Root    string   // the new top-level item's key
	Created []string // every key created, root first
}

// checkEverySectionApproved is C1/I10 for a spec: every section of the head
// revision needs an approved request whose hash matches.
func (s *Store) checkEverySectionApproved(ctx context.Context, tx *sql.Tx, artifactID string) error {
	var headRev int
	if err := tx.QueryRowContext(ctx, `SELECT head_revision FROM artifacts WHERE id = ?`, artifactID).Scan(&headRev); err != nil {
		return err
	}
	var sectionsJSON string
	if err := tx.QueryRowContext(ctx, `SELECT sections_json FROM artifact_revisions
		WHERE artifact_id = ? AND revision = ?`, artifactID, headRev).Scan(&sectionsJSON); err != nil {
		return err
	}
	var secs []ArtifactSection
	json.Unmarshal([]byte(sectionsJSON), &secs)
	for _, sec := range secs {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests WHERE artifact_id = ? AND section_id = ?
			AND kind = 'approve_section' AND state = 'approved' AND section_sha256 = ?`,
			artifactID, sec.ID, sec.SHA256).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("approval_missing: section %q is not approved at its current revision", sec.Title)
		}
	}
	return nil
}

// checkWholeFileApproved is C1/I10 for a plan or a debug report: the head
// revision needs one approved whole-file request.
func (s *Store) checkWholeFileApproved(ctx context.Context, tx *sql.Tx, artifactID string) error {
	var headRev int
	var kind string
	if err := tx.QueryRowContext(ctx, `SELECT head_revision, kind FROM artifacts WHERE id = ?`,
		artifactID).Scan(&headRev, &kind); err != nil {
		return err
	}
	reqKind := "approve_plan"
	if kind == "debug_report" {
		reqKind = "approve_report"
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests WHERE artifact_id = ? AND kind = ?
		AND state = 'approved' AND artifact_revision = ?`, artifactID, reqKind, headRev).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("approval_missing: the %s is not approved at its current revision", kind)
	}
	return nil
}

// approvedTree re-reads the file on disk and refuses it if it no longer hashes
// to the approved head revision (C2), then decodes that revision's stored
// swarm-tree — never the live file — into a Tree.
func (s *Store) approvedTree(ctx context.Context, tx *sql.Tx, artifactID string) (Tree, error) {
	var path string
	var headRev int
	if err := tx.QueryRowContext(ctx, `SELECT path, head_revision FROM artifacts WHERE id = ?`,
		artifactID).Scan(&path, &headRev); err != nil {
		return Tree{}, err
	}
	var sha, treeJSON string
	if err := tx.QueryRowContext(ctx, `SELECT sha256, COALESCE(tree_json, '') FROM artifact_revisions
		WHERE artifact_id = ? AND revision = ?`, artifactID, headRev).Scan(&sha, &treeJSON); err != nil {
		return Tree{}, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return Tree{}, err
	}
	if sha256Hex(string(body)) != sha {
		return Tree{}, fmt.Errorf("artifact_changed: %s changed after approval. Revise and ask again.", path)
	}
	var tree Tree
	if err := json.Unmarshal([]byte(treeJSON), &tree); err != nil {
		return Tree{}, fmt.Errorf("tree_invalid: %v", err)
	}
	return tree, nil
}

// checkTreeRepos is I10's subset rule. The tree names repos; the item holds ids
// (D49), so the confirmed ids are resolved to names once and the comparison is
// name-to-name.
func (s *Store) checkTreeRepos(ctx context.Context, tx *sql.Tx, spike items.Item, tree Tree) error {
	confirmed := map[string]bool{}
	for _, id := range spike.Repos {
		name, err := s.repoNameTx(ctx, tx, id)
		if err != nil {
			return err
		}
		confirmed[name] = true
	}
	for _, node := range tree.Tasks() {
		for _, name := range node.Repos {
			if !confirmed[name] {
				return fmt.Errorf("tree_invalid: TASK %s uses an unconfirmed repo", node.Ref)
			}
		}
	}
	return nil
}

// createTree walks root → children → grandchildren, mapping each ref to its new
// key, then adds every dep edge with AddDepTx (never AddDep, which would open a
// second transaction inside this one and deadlock against itself — R6).
func (s *Store) createTree(ctx context.Context, tx *sql.Tx, spike items.Item, tree Tree, rootType items.Type) (MaterializeResult, error) {
	nameToID := map[string]string{}
	for _, id := range spike.Repos {
		name, err := s.repoNameTx(ctx, tx, id)
		if err != nil {
			return MaterializeResult{}, err
		}
		nameToID[name] = id
	}
	resolveRepos := func(names []string) []string {
		var out []string
		for _, n := range names {
			if id, ok := nameToID[n]; ok {
				out = append(out, id)
			}
		}
		return out
	}
	rootWorkflow, err := resolvedTreeWorkflow(tree.Root)
	if err != nil {
		return MaterializeResult{}, err
	}
	root, err := s.Items.CreateTx(ctx, tx, items.CreateInput{
		Workflow: rootWorkflow,
		Type:     rootType, Title: tree.Root.Title, Brief: tree.Root.Brief,
		Acceptance: tree.Root.Acceptance, Status: items.Draft,
		Repos: spike.Repos, OriginSpikeID: spike.ID,
	}, items.Daemon())
	if err != nil {
		return MaterializeResult{}, err
	}
	keyOf := map[string]string{}
	created := []string{root.Key}
	var walk func(nodes []TreeNode, parentKey string) error
	walk = func(nodes []TreeNode, parentKey string) error {
		for _, n := range nodes {
			var tddExempt string
			if n.TddExempt != nil {
				tddExempt = *n.TddExempt
			}
			resolved, err := resolvedTreeWorkflow(n)
			if err != nil {
				return err
			}
			units := make([]items.Unit, len(n.Units))
			for i, u := range n.Units {
				units[i] = items.Unit{Title: u.Title, Steps: u.Steps}
			}
			role := n.RoleHint
			if resolved != nil && n.Type == "task" {
				role = workflow.RunRole(*resolved)
			}
			it, err := s.Items.CreateTx(ctx, tx, items.CreateInput{
				Workflow: resolved, Steps: n.Steps, Units: units, Solo: n.Solo, Verify: n.Verify,
				Type: items.Type(n.Type), ParentKey: parentKey, Title: n.Title, Brief: n.Brief,
				Acceptance: n.Acceptance, Status: items.Ready, RoleHint: role,
				TddExempt: tddExempt, Repos: resolveRepos(n.Repos),
			}, items.Daemon())
			if err != nil {
				return err
			}
			keyOf[n.Ref] = it.Key
			created = append(created, it.Key)
			if err := walk(n.Children, it.Key); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(tree.Children, root.Key); err != nil {
		return MaterializeResult{}, err
	}
	for _, d := range tree.Deps {
		item, blockedBy := keyOf[d.Item], keyOf[d.BlockedBy]
		if item == "" || blockedBy == "" {
			return MaterializeResult{}, fmt.Errorf("tree_invalid: dependency references an unknown ref")
		}
		if err := s.Items.AddDepTx(ctx, tx, item, blockedBy, items.Daemon()); err != nil {
			return MaterializeResult{}, err
		}
	}
	return MaterializeResult{Root: root.Key, Created: created}, nil
}

// copyArtifacts duplicates each source artifact's approved head revision onto
// the newly materialized root, so the epic or bug carries its spec/plan/report.
func (s *Store) copyArtifacts(ctx context.Context, tx *sql.Tx, rootKey string, sourceIDs ...string) error {
	root, err := s.Items.GetTx(ctx, tx, rootKey)
	if err != nil {
		return err
	}
	for _, srcID := range sourceIDs {
		if srcID == "" {
			continue
		}
		var kind, path string
		var headRev int
		var createdBy sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT kind, path, head_revision, created_by FROM artifacts
			WHERE id = ?`, srcID).Scan(&kind, &path, &headRev, &createdBy); err != nil {
			return err
		}
		var sha, content, sectionsJSON string
		var treeJSON sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT sha256, content, sections_json, tree_json
			FROM artifact_revisions WHERE artifact_id = ? AND revision = ?`, srcID, headRev).
			Scan(&sha, &content, &sectionsJSON, &treeJSON); err != nil {
			return err
		}
		newID := ids.New("art")
		if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts (id, item_id, kind, path, head_revision,
			created_by, created_at) VALUES (?, ?, ?, ?, 1, ?, ?)`,
			newID, root.ID, kind, path, createdBy, db.Millis(s.Now())); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO artifact_revisions
			(artifact_id, revision, sha256, content, sections_json, tree_json, created_at)
			VALUES (?, 1, ?, ?, ?, ?, ?)`, newID, sha, content, sectionsJSON, treeJSON,
			db.Millis(s.Now())); err != nil {
			return err
		}
	}
	return nil
}

// relayMaterialized tells the spike's own orchestrator that materialization
// finished, so its next swarm_sync carries the new root's key.
func (s *Store) relayMaterialized(ctx context.Context, tx *sql.Tx, a Agent, spikeKey string, out MaterializeResult) error {
	body, err := json.Marshal(map[string]any{"event": "materialized", "agent": a.Name,
		"item": spikeKey, "root": out.Root})
	if err != nil {
		return err
	}
	_, err = s.enqueue(ctx, tx, Message{Kind: "relay", Origin: "daemon",
		ToAgentID: a.ID, RootItemID: a.RootItemID, Payload: body})
	return err
}

// notifyItemCreated raises §17.5's item.created (or item.created.bug for a
// debug spike's bug root).
func (s *Store) notifyItemCreated(ctx context.Context, tx *sql.Tx, spike items.Item, rootKey, rootTitle string, rootType items.Type) error {
	kind := "item.created"
	if rootType == items.Bug {
		kind = "item.created.bug"
	} else if rootType == items.Chore {
		kind = "item.created.chore"
	}
	return s.notify(ctx, tx, NotifyInput{Kind: kind, ItemKey: rootKey,
		Args: map[string]string{"SPIKE-KEY": spike.Key, "ROOT-KEY": rootKey, "title": rootTitle}})
}

// Materialize turns an approved spike into an epic (feature) or a bug (debug),
// all inside one transaction (§8.2): every check must pass before anything is
// created, and any failure rolls the whole tree back.
// requestID is I11's idempotency key, scoped to the calling MCP session
// (empty means "no idempotency, just run once").
func (s *Store) Materialize(ctx context.Context, sessionID, spikeKey, specID, planID, reportID, requestID string) (MaterializeResult, error) {
	var out MaterializeResult
	_, err := IdemTx(ctx, s, sessionID, requestID, "swarm_materialize", &out, func(tx *sql.Tx) error {
		_, a, err := s.sessionAndAgent(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		spike, err := s.Items.GetTx(ctx, tx, spikeKey)
		if err != nil {
			return err
		}
		if a.Role != RoleOrchestrator || a.ItemID != spike.ID {
			return &items.Error{Code: items.CodeBadRequest, Message: "Only the spike's orchestrator can materialize it."}
		}
		treeSource := planID
		rootType := items.Epic
		if spike.SpikeIntent == "debug" {
			if reportID == "" {
				return errors.New("A debug spike materializes from its report.")
			}
			treeSource, rootType = reportID, items.Bug
		} else if spike.SpikeIntent == "chore" {
			if planID == "" {
				return errors.New("A chore spike materializes from its plan.")
			}
			treeSource, rootType = planID, items.Chore
		} else if specID == "" || planID == "" {
			return errors.New("A feature spike materializes from its spec and plan.")
		}
		if specID != "" {
			if err := s.checkEverySectionApproved(ctx, tx, specID); err != nil {
				return err
			}
		}
		if err := s.checkWholeFileApproved(ctx, tx, treeSource); err != nil {
			return err
		}
		tree, err := s.approvedTree(ctx, tx, treeSource) // also re-hashes the file on disk (C2)
		if err != nil {
			return err
		}
		if err := checkTreeShape(tree, rootType); err != nil {
			return err
		}
		if err := s.checkTreeRepos(ctx, tx, spike, tree); err != nil {
			return err
		}
		out, err = s.createTree(ctx, tx, spike, tree, rootType)
		if err != nil {
			return err
		}
		if err := s.copyArtifacts(ctx, tx, out.Root, specID, planID, reportID); err != nil {
			return err
		}
		if _, err := s.Items.TransitionTx(ctx, tx, spikeKey, items.Done, items.Daemon()); err != nil {
			return err
		}
		if err := s.relayMaterialized(ctx, tx, a, spikeKey, out); err != nil {
			return err
		}
		return s.notifyItemCreated(ctx, tx, spike, out.Root, tree.Root.Title, rootType)
	})
	return out, err
}

func resolvedTreeWorkflow(n TreeNode) (*workflow.Spec, error) {
	if n.Workflow == nil {
		return nil, nil
	}
	resolved, err := workflow.Resolve(*n.Workflow, n.TddExempt != nil)
	if err != nil {
		return nil, err
	}
	return &resolved, nil
}
