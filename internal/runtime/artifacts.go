package runtime

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

const maxArtifactBytes = 1 << 20 // 1 MB

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// ArtifactResult is RegisterArtifact's result.
type ArtifactResult struct {
	ArtifactID    string
	Revision      int
	Sections      []ArtifactSection
	StaleRequests []string
}

// TreeNode is one node of a swarm-tree block (I10).
type TreeNode struct {
	Ref        string     `json:"ref,omitempty"`
	Type       string     `json:"type"`
	Title      string     `json:"title"`
	Brief      string     `json:"brief"`
	Acceptance []string   `json:"acceptance"`
	RoleHint   string     `json:"role_hint,omitempty"`
	TddExempt  *string    `json:"tdd_exempt,omitempty"`
	Repos      []string   `json:"repos,omitempty"`
	Children   []TreeNode `json:"children,omitempty"`
}

// TreeDep is one swarm-tree dependency edge.
type TreeDep struct {
	Item      string `json:"item"`
	BlockedBy string `json:"blocked_by"`
}

// Tree is the parsed contents of a ```swarm-tree block.
type Tree struct {
	Root     TreeNode   `json:"root"`
	Children []TreeNode `json:"children"`
	Deps     []TreeDep  `json:"deps"`
}

// Tasks returns every task-typed node in the tree, depth-first.
func (t Tree) Tasks() []TreeNode {
	var out []TreeNode
	var walk func([]TreeNode)
	walk = func(nodes []TreeNode) {
		for _, n := range nodes {
			if n.Type == "task" {
				out = append(out, n)
			}
			walk(n.Children)
		}
	}
	walk(t.Children)
	return out
}

// SplitSections splits on "## " headings at the start of a line, ignoring
// headings inside fenced code blocks. A file with none is one section,
// "document".
func SplitSections(md string) []ArtifactSection {
	type mark struct {
		title string
		at    int
	}
	var marks []mark
	var fence string
	offset := 0
	for _, line := range strings.SplitAfter(md, "\n") {
		trimmed := strings.TrimRight(line, "\r\n")
		switch {
		case fence != "" && strings.HasPrefix(strings.TrimSpace(trimmed), fence):
			fence = ""
		case fence == "" && (strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")):
			fence = trimmed[:3]
		case fence == "" && strings.HasPrefix(trimmed, "## ") && !strings.HasPrefix(trimmed, "### "):
			marks = append(marks, mark{strings.TrimSpace(trimmed[3:]), offset})
		}
		offset += len(line)
	}
	if len(marks) == 0 {
		return []ArtifactSection{{ID: "document", Title: "document",
			SHA256: sha256Hex(md), Start: 0, End: len(md)}}
	}
	seen := map[string]int{}
	out := make([]ArtifactSection, 0, len(marks))
	for i, m := range marks {
		end := len(md)
		if i+1 < len(marks) {
			end = marks[i+1].at
		}
		id, _ := ids.Kebab(m.title)
		if id == "" {
			id = "section"
		}
		seen[id]++
		if n := seen[id]; n > 1 {
			id = fmt.Sprintf("%s-%d", id, n)
		}
		out = append(out, ArtifactSection{ID: id, Title: m.title,
			SHA256: sha256Hex(md[m.at:end]), Start: m.at, End: end})
	}
	return out
}

// findSwarmTreeBlocks returns the content of every fenced block whose opening
// line is exactly "```swarm-tree".
func findSwarmTreeBlocks(md string) []string {
	lines := strings.Split(md, "\n")
	var blocks []string
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "```swarm-tree" {
			continue
		}
		var buf []string
		j := i + 1
		for j < len(lines) && strings.TrimSpace(lines[j]) != "```" {
			buf = append(buf, lines[j])
			j++
		}
		blocks = append(blocks, strings.Join(buf, "\n"))
		i = j
	}
	return blocks
}

// parentTypesFor is L4's allowed-child-types-by-parent-type table for a
// swarm-tree, mirroring items.allowedParents.
func parentTypesFor(parentType string) []string {
	switch parentType {
	case "epic":
		return []string{"story"}
	case "story", "bug", "spike":
		return []string{"task"}
	}
	return nil
}

// validateTreeShape is L4 (hierarchy) and I12 (no cycle, no hierarchy edge).
func validateTreeShape(t Tree) error {
	if t.Root.Type != "epic" && t.Root.Type != "bug" {
		return fmt.Errorf("tree_invalid: root must be an epic or a bug, got %q", t.Root.Type)
	}
	refs := map[string]bool{}
	parentOf := map[string]string{}
	var walk func(children []TreeNode, parentRef, parentType string) error
	walk = func(children []TreeNode, parentRef, parentType string) error {
		for _, c := range children {
			if c.Ref == "" {
				return errors.New("tree_invalid: every node needs a non-empty ref")
			}
			if refs[c.Ref] {
				return fmt.Errorf("tree_invalid: duplicate ref %q", c.Ref)
			}
			refs[c.Ref] = true
			parentOf[c.Ref] = parentRef
			if !slices.Contains(parentTypesFor(parentType), c.Type) {
				return fmt.Errorf("tree_invalid: a %s can't be under a %s (ref %s)", c.Type, parentType, c.Ref)
			}
			if err := walk(c.Children, c.Ref, c.Type); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(t.Children, "", t.Root.Type); err != nil {
		return err
	}
	isAncestor := func(id, anc string) bool {
		for cur := parentOf[id]; cur != ""; cur = parentOf[cur] {
			if cur == anc {
				return true
			}
		}
		return false
	}
	adj := map[string][]string{}
	for _, d := range t.Deps {
		if !refs[d.Item] {
			return fmt.Errorf("tree_invalid: unknown dep item %q", d.Item)
		}
		if !refs[d.BlockedBy] {
			return fmt.Errorf("tree_invalid: unknown dep blocked_by %q", d.BlockedBy)
		}
		if d.Item == d.BlockedBy || isAncestor(d.Item, d.BlockedBy) || isAncestor(d.BlockedBy, d.Item) {
			return fmt.Errorf("tree_invalid: %s can't depend on its own ancestor or descendant %s", d.Item, d.BlockedBy)
		}
		adj[d.Item] = append(adj[d.Item], d.BlockedBy)
	}
	for start := range adj {
		seen := map[string]bool{start: true}
		queue := append([]string{}, adj[start]...)
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			if cur == start {
				return errors.New("tree_invalid: dependency cycle detected")
			}
			if seen[cur] {
				continue
			}
			seen[cur] = true
			queue = append(queue, adj[cur]...)
		}
	}
	return nil
}

// checkTreeShape is validateTreeShape plus the expected root type (Task 19:
// a feature spike's plan must root an epic, a debug spike's report a bug).
func checkTreeShape(t Tree, rootType items.Type) error {
	if items.Type(t.Root.Type) != rootType {
		return fmt.Errorf("tree_invalid: root must be %s, got %q", rootType, t.Root.Type)
	}
	return validateTreeShape(t)
}

// ParseTree parses and validates one ```swarm-tree block (I10, L4).
func ParseTree(md string) (Tree, error) {
	blocks := findSwarmTreeBlocks(md)
	if len(blocks) == 0 {
		return Tree{}, errors.New("tree_invalid: the plan needs one ```swarm-tree block under \"## Work breakdown\".")
	}
	if len(blocks) > 1 {
		return Tree{}, errors.New("tree_invalid: two swarm-tree blocks; there must be exactly one")
	}
	dec := json.NewDecoder(strings.NewReader(blocks[0]))
	dec.DisallowUnknownFields()
	var tree Tree
	if err := dec.Decode(&tree); err != nil {
		return Tree{}, fmt.Errorf("tree_invalid: %v", err)
	}
	if err := validateTreeShape(tree); err != nil {
		return Tree{}, err
	}
	return tree, nil
}

// sectionsChanged reports the ids whose hash moved between two section lists.
func sectionsChanged(prev, next []ArtifactSection) []string {
	prevHash := map[string]string{}
	for _, sec := range prev {
		prevHash[sec.ID] = sec.SHA256
	}
	var out []string
	for _, sec := range next {
		if prevHash[sec.ID] != sec.SHA256 {
			out = append(out, sec.ID)
		}
	}
	return out
}

// staleApprovals marks stale every open approval request bound to artifactID
// whose section hash is in changed, or, for a whole-file approval
// (approve_plan / approve_report, no section_id), unconditionally — a
// whole-file approval goes stale on any revision.
func (s *Store) staleApprovals(ctx context.Context, tx *sql.Tx, artifactID string, changed []string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, COALESCE(section_id, '') FROM requests
		WHERE artifact_id = ? AND state = 'open'
		AND kind IN ('approve_section','approve_plan','approve_report')`, artifactID)
	if err != nil {
		return nil, err
	}
	type openReq struct{ id, section string }
	var open []openReq
	for rows.Next() {
		var r openReq
		if err := rows.Scan(&r.id, &r.section); err != nil {
			rows.Close()
			return nil, err
		}
		open = append(open, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	changedSet := map[string]bool{}
	for _, c := range changed {
		changedSet[c] = true
	}
	var stale []string
	for _, r := range open {
		if r.section == "" || changedSet[r.section] {
			stale = append(stale, r.id)
		}
	}
	for _, id := range stale {
		if _, err := tx.ExecContext(ctx, `UPDATE requests SET state = 'stale' WHERE id = ?`, id); err != nil {
			return nil, err
		}
		w, err := s.RequestWireTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if _, err := s.Events.Append(ctx, tx, events.RequestResolved, w); err != nil {
			return nil, err
		}
	}
	return stale, nil
}

// RegisterArtifact is swarm_register (register or revise a spec/plan/report/note,
// C2, I10). Only the item's own top-level orchestrator may call it. requestID
// is I11's idempotency key (empty means "no idempotency, just run once").
func (s *Store) RegisterArtifact(ctx context.Context, sessionID, op, itemKey, kind, path, requestID string) (ArtifactResult, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return ArtifactResult{}, fmt.Errorf("artifact: %w", err)
	}
	if len(body) > maxArtifactBytes {
		return ArtifactResult{}, &items.Error{Code: items.CodeBadRequest, Message: "Artifact must be at most 1 MB."}
	}
	sections := SplitSections(string(body))
	var tree *Tree
	if kind == "plan" || kind == "debug_report" {
		t, err := ParseTree(string(body))
		if err != nil {
			return ArtifactResult{}, err
		}
		tree = &t
	}
	var out ArtifactResult
	_, err = IdemTx(ctx, s, sessionID, requestID, "swarm_artifact", &out, func(tx *sql.Tx) error {
		_, a, err := s.sessionAndAgent(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if a.Role != RoleOrchestrator {
			return &items.Error{Code: items.CodeBadRequest, Message: "Only an orchestrator can register an artifact."}
		}
		it, err := s.Items.GetTx(ctx, tx, itemKey)
		if err != nil {
			return err
		}
		if it.RootID != a.RootItemID {
			return &items.Error{Code: items.CodeBadRequest,
				Message: fmt.Sprintf("%s is outside your assignment.", itemKey)}
		}
		var artifactID string
		var revision int
		var prevSectionsJSON string
		err = tx.QueryRowContext(ctx, `SELECT a.id, a.head_revision, r.sections_json FROM artifacts a
			JOIN artifact_revisions r ON r.artifact_id = a.id AND r.revision = a.head_revision
			WHERE a.item_id = ? AND a.path = ?`, it.ID, path).Scan(&artifactID, &revision, &prevSectionsJSON)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			artifactID, revision = ids.New("art"), 1
			if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts
				(id, item_id, kind, path, head_revision, created_by, created_at)
				VALUES (?, ?, ?, ?, 1, ?, ?)`, artifactID, it.ID, kind, path, a.ID, db.Millis(s.Now())); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			revision++
			if _, err := tx.ExecContext(ctx, `UPDATE artifacts SET head_revision = ? WHERE id = ?`,
				revision, artifactID); err != nil {
				return err
			}
		}
		var prevSections []ArtifactSection
		json.Unmarshal([]byte(prevSectionsJSON), &prevSections)

		sectionsJSON, err := json.Marshal(sections)
		if err != nil {
			return err
		}
		var treeJSON sql.NullString
		if tree != nil {
			b, err := json.Marshal(tree)
			if err != nil {
				return err
			}
			treeJSON = sql.NullString{String: string(b), Valid: true}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO artifact_revisions
			(artifact_id, revision, sha256, content, sections_json, tree_json, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, artifactID, revision, sha256Hex(string(body)), string(body),
			string(sectionsJSON), treeJSON, db.Millis(s.Now())); err != nil {
			return err
		}
		stale, err := s.staleApprovals(ctx, tx, artifactID, sectionsChanged(prevSections, sections))
		if err != nil {
			return err
		}
		out = ArtifactResult{ArtifactID: artifactID, Revision: revision, Sections: sections, StaleRequests: stale}
		return nil
	})
	return out, err
}

// ArtifactMarkdown reads one artifact revision's whole text, or one section of
// it. revision 0 means the artifact's head revision (C2: an older revision is
// still readable after the file on disk moved on).
func (s *Store) ArtifactMarkdown(ctx context.Context, artifactID string, revision int, sectionID string) (Artifact, string, error) {
	var art Artifact
	var created int64
	err := s.DB.QueryRowContext(ctx, `SELECT item_id, kind, path, head_revision, COALESCE(created_by, ''), created_at
		FROM artifacts WHERE id = ?`, artifactID).Scan(&art.ItemID, &art.Kind, &art.Path, &art.HeadRevision,
		&art.CreatedBy, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return art, "", &items.Error{Code: items.CodeNotFound, Message: "Unknown artifact."}
	}
	if err != nil {
		return art, "", err
	}
	art.ID = artifactID
	art.CreatedAt = db.FromMillis(created)
	if revision <= 0 {
		revision = art.HeadRevision
	}
	art.Revision = revision
	var content, sectionsJSON string
	err = s.DB.QueryRowContext(ctx, `SELECT content, sections_json FROM artifact_revisions
		WHERE artifact_id = ? AND revision = ?`, artifactID, revision).Scan(&content, &sectionsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return art, "", &items.Error{Code: items.CodeNotFound, Message: "Unknown revision."}
	}
	if err != nil {
		return art, "", err
	}
	json.Unmarshal([]byte(sectionsJSON), &art.Sections)
	if sectionID == "" {
		return art, content, nil
	}
	for _, sec := range art.Sections {
		if sec.ID == sectionID {
			return art, content[sec.Start:sec.End], nil
		}
	}
	return art, "", &items.Error{Code: items.CodeNotFound, Message: "Unknown section."}
}

// ArtifactsFor lists every artifact registered on itemID, each at its head
// revision.
func (s *Store) ArtifactsFor(ctx context.Context, itemID string) ([]Artifact, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, kind, path, head_revision, COALESCE(created_by, ''), created_at
		FROM artifacts WHERE item_id = ? ORDER BY created_at`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Artifact
	for rows.Next() {
		var art Artifact
		var created int64
		if err := rows.Scan(&art.ID, &art.Kind, &art.Path, &art.HeadRevision, &art.CreatedBy, &created); err != nil {
			return nil, err
		}
		art.ItemID = itemID
		art.Revision = art.HeadRevision
		art.CreatedAt = db.FromMillis(created)
		out = append(out, art)
	}
	return out, rows.Err()
}
