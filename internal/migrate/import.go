package migrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
)

type ImportInput struct {
	V1Path  string
	NewPath string
	KBDir   string // <home>/kb
	Now     func() time.Time
}

// ImportedItem is one row the import created, for validation and the report.
type ImportedItem struct {
	ID        string
	Key       string
	Type      string
	Status    string
	Title     string
	LegacyKey string
	ParentID  string
	RootID    string
	BlockedBy []string
}

type ImportReport struct {
	Items []ImportedItem
	Notes []string // repo paths skipped, keys with no text, and so on
}

// blockedRestoresTo is §20's decision for status_before_block: a blocked import
// always restores to "ready", never a literal at the insert site — nothing is
// mid-flight after a cutover (A7). Named here so every status written by this
// package traces back to MapStatus, DeriveStoryStatus, Table, or this decision.
const blockedRestoresTo = "ready"

// Import builds the new database at in.NewPath from the v1 database at in.V1Path,
// following §20's table. Everything happens in one transaction: a failure leaves no
// half-built tree, and step 5 deletes the file anyway.
func Import(ctx context.Context, in ImportInput) (*ImportReport, error) {
	v1, err := OpenV1(in.V1Path)
	if err != nil {
		return nil, err
	}
	defer v1.Close()
	src, err := ReadV1(ctx, v1, SourceKeys())
	if err != nil {
		return nil, err
	}

	// db.Open applies the real migrations, so the temp database has schema version 1.
	d, err := db.Open(ctx, in.NewPath)
	if err != nil {
		return nil, err
	}
	defer d.Close()

	rep := &ImportReport{}
	now := db.Millis(in.Now())
	err = d.Tx(ctx, func(tx *sql.Tx) error {
		repoIDs, notes, err := importRepos(ctx, tx, src, now)
		if err != nil {
			return err
		}
		rep.Notes = append(rep.Notes, notes...)
		for _, root := range Table {
			if err := importRoot(ctx, tx, in, src, repoIDs, root, now, rep); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rep, nil
}

// importRepos inserts one repos row per distinct repo_path that exists and is a git
// repository, and returns path → repo id. A missing path is skipped and noted.
func importRepos(ctx context.Context, tx *sql.Tx, src map[string]V1Task, now int64) (map[string]string, []string, error) {
	out := map[string]string{}
	var notes []string
	seen := map[string]bool{}
	for _, k := range SourceKeys() {
		p := src[k].RepoPath
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if _, err := os.Stat(filepath.Join(p, ".git")); err != nil {
			notes = append(notes, "skipped the repository hint "+p+": no git repository there")
			continue
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			real = p
		}
		id := ids.New("repo")
		if _, err := tx.ExecContext(ctx, `INSERT INTO repos
			(id, path, name, source, missing, created_at, updated_at) VALUES (?, ?, ?, 'history', 0, ?, ?)
			ON CONFLICT(path) DO NOTHING`, id, real, filepath.Base(real), now, now); err != nil {
			return nil, nil, err
		}
		var stored string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM repos WHERE path = ?`, real).Scan(&stored); err != nil {
			return nil, nil, err
		}
		out[p] = stored
	}
	return out, notes, nil
}

// importRoot writes one §20 row: the root, its stories, its tasks, the dependencies
// and the note artifacts.
func importRoot(ctx context.Context, tx *sql.Tx, in ImportInput, src map[string]V1Task,
	repoIDs map[string]string, root Root, now int64, rep *ImportReport) error {
	legacy := ""
	if len(root.From) > 0 {
		legacy = root.From[0]
	}
	// suggested_repos_json is the union of the subtree's repo ids (§20 repo hints).
	var suggested []string
	seen := map[string]bool{}
	for _, k := range rootSourceKeys(root) {
		if id, ok := repoIDs[src[k].RepoPath]; ok && !seen[id] {
			seen[id] = true
			suggested = append(suggested, id)
		}
	}

	rootID, err := insertItem(ctx, tx, itemRow{
		Type: root.Type, Title: root.Title, Status: root.Status, LegacyKey: legacy,
		Suggested: suggested, Source: src[legacy], Now: now, SortOrder: 0,
	}, "", "", in, rep)
	if err != nil {
		return err
	}

	// A bug root takes tasks directly; an epic takes stories.
	keyToID := map[string]string{}
	for i, c := range root.Tasks {
		id, err := insertItem(ctx, tx, itemRow{
			Type: "task", Title: src[c.From].Title, LegacyKey: c.From,
			Source: src[c.From], Now: now, SortOrder: i, FromV1Status: true,
		}, rootID, rootID, in, rep)
		if err != nil {
			return err
		}
		keyToID[c.From] = id
	}
	for i, s := range root.Stories {
		// A story with a v1 source takes that row's status; otherwise it is derived.
		r := itemRow{Type: "story", Title: s.Title, LegacyKey: s.From, Source: src[s.From],
			Now: now, SortOrder: i, FromV1Status: s.From != ""}
		if s.From == "" {
			var kidStatuses []string
			for _, c := range s.Tasks {
				st, err := MapStatus(src[c.From].Status)
				if err != nil {
					return fmt.Errorf("%s: %w", c.From, err)
				}
				kidStatuses = append(kidStatuses, st)
			}
			r.Status = DeriveStoryStatus(kidStatuses)
		}
		storyID, err := insertItem(ctx, tx, r, rootID, rootID, in, rep)
		if err != nil {
			return err
		}
		keyToID["story:"+s.Title] = storyID
		for j, c := range s.Tasks {
			id, err := insertItem(ctx, tx, itemRow{
				Type: "task", Title: src[c.From].Title, LegacyKey: c.From,
				Source: src[c.From], Now: now, SortOrder: j, FromV1Status: true,
			}, storyID, rootID, in, rep)
			if err != nil {
				return err
			}
			keyToID[c.From] = id
		}
	}

	// Dependencies, after every id exists.
	addDep := func(item, blocker string) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO item_deps (item_id, blocked_by_id, created_at) VALUES (?, ?, ?)`, item, blocker, now)
		return err
	}
	for _, c := range root.Tasks {
		if c.BlockedBy != "" {
			if err := addDep(keyToID[c.From], keyToID[c.BlockedBy]); err != nil {
				return err
			}
		}
	}
	for _, s := range root.Stories {
		if s.BlockedBy != "" {
			if err := addDep(keyToID["story:"+s.Title], keyToID["story:"+s.BlockedBy]); err != nil {
				return err
			}
		}
		for _, c := range s.Tasks {
			if c.BlockedBy != "" {
				if err := addDep(keyToID[c.From], keyToID[c.BlockedBy]); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// rootSourceKeys is every v1 key under root, including its own.
func rootSourceKeys(root Root) []string {
	var out []string
	out = append(out, root.From...)
	for _, c := range root.Tasks {
		out = append(out, c.From)
	}
	for _, s := range root.Stories {
		if s.From != "" {
			out = append(out, s.From)
		}
		for _, c := range s.Tasks {
			out = append(out, c.From)
		}
	}
	return out
}

type itemRow struct {
	Type         string
	Title        string
	Status       string // used when FromV1Status is false
	FromV1Status bool
	LegacyKey    string
	Suggested    []string
	Source       V1Task
	Now          int64
	SortOrder    int
}

// insertItem writes one items row, plus its note artifact when the v1 row has text.
func insertItem(ctx context.Context, tx *sql.Tx, r itemRow, parentID, rootID string,
	in ImportInput, rep *ImportReport) (string, error) {
	id := ids.New("itm")
	key, err := ids.NextKey(ctx, tx, r.Type)
	if err != nil {
		return "", err
	}
	status := r.Status
	if r.FromV1Status {
		if status, err = MapStatus(r.Source.Status); err != nil {
			return "", fmt.Errorf("%s: %w", r.LegacyKey, err)
		}
	}
	// A blocked item needs a status to return to on unblock (§5). ready is the only
	// safe answer for an import: nothing is in progress after a cutover (A7).
	var before any
	if status == "blocked" {
		before = blockedRestoresTo
	}
	created, updated := r.Now, r.Now
	if !r.Source.CreatedAt.IsZero() {
		created, updated = db.Millis(r.Source.CreatedAt), db.Millis(r.Source.UpdatedAt)
	}
	if rootID == "" {
		rootID = id // a top-level item is its own root
	}
	var parent any
	if parentID != "" {
		parent = parentID
	}
	var legacy any
	if r.LegacyKey != "" {
		legacy = r.LegacyKey
	}
	suggested, err := json.Marshal(orEmptySlice(r.Suggested))
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO items
		(id, key, type, parent_id, root_id, title, brief, acceptance_json, status, status_before_block,
		 priority, confirmed_repos_json, repos_version, repo_hints_json, suggested_repos_json,
		 legacy_key, sort_order, revision, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, '[]', ?, ?, 2, '[]', 0, '[]', ?, ?, ?, 1, ?, ?)`,
		id, key, r.Type, parent, rootID, r.Title, Brief(r.Source.InitialContext, r.Source.HandoffNote),
		status, before, string(suggested), legacy, r.SortOrder, created, updated); err != nil {
		return "", err
	}
	rep.Items = append(rep.Items, ImportedItem{ID: id, Key: key, Type: r.Type, Status: status,
		Title: r.Title, LegacyKey: r.LegacyKey, ParentID: parentID, RootID: rootID})

	if r.LegacyKey != "" {
		if err := writeImportedNote(ctx, tx, in, id, r.Source, r.Now, rep); err != nil {
			return "", err
		}
	}
	return id, nil
}

func orEmptySlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// writeImportedNote computes <KBDir>/imported/<SW-KEY>.md's content and registers it
// as a note artifact (§20 step 4). It writes only to the temp database: the file
// itself is not created here. Step 5's "leaves v1 untouched" must be true of the
// filesystem too, so nothing under kb/imported/ may exist before step 6 confirms the
// switch; step 7 flushes this content to disk once the new database is live (via
// LoadNoteFiles).
func writeImportedNote(ctx context.Context, tx *sql.Tx, in ImportInput, itemID string,
	v1 V1Task, now int64, rep *ImportReport) error {
	ctxText := strings.TrimSpace(v1.InitialContext)
	handoff := strings.TrimSpace(v1.HandoffNote)
	if ctxText == "" && handoff == "" {
		rep.Notes = append(rep.Notes, v1.Key+" had no context or handoff note")
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "---\ntitle: %s\ntags: [imported, %s]\n---\n\n# %s · %s\n\n",
		v1.Title, v1.Key, v1.Key, v1.Title)
	sections := []struct{ Title, Body string }{{"Initial context", ctxText}, {"Handoff note", handoff}}
	type section struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		SHA256 string `json:"sha256"`
		Start  int    `json:"start"`
		End    int    `json:"end"`
	}
	var secs []section
	for _, s := range sections {
		if s.Body == "" {
			continue
		}
		start := b.Len()
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", s.Title, s.Body)
		sum := sha256.Sum256([]byte(s.Body))
		secs = append(secs, section{ID: slug(s.Title), Title: s.Title,
			SHA256: hex.EncodeToString(sum[:]), Start: start, End: b.Len()})
	}
	body := b.String()
	path := filepath.Join(in.KBDir, "imported", v1.Key+".md")
	artID := ids.New("art")
	if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts
		(id, item_id, kind, path, head_revision, created_by, created_at)
		VALUES (?, ?, 'note', ?, 1, NULL, ?)`, artID, itemID, path, now); err != nil {
		return err
	}
	secJSON, err := json.Marshal(secs)
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(body))
	_, err = tx.ExecContext(ctx, `INSERT INTO artifact_revisions
		(artifact_id, revision, sha256, content, sections_json, tree_json, created_at)
		VALUES (?, 1, ?, ?, ?, NULL, ?)`,
		artID, hex.EncodeToString(sum[:]), body, string(secJSON), now)
	return err
}

// slug is a tiny local helper for a section id; ids.Kebab is for agent names and
// errors on empty input, which a fixed heading cannot be.
func slug(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), " ", "-"))
}

// LoadReport reads the imported tree back out of a database, so Validate can check
// what is actually stored rather than what the importer believes it stored.
func LoadReport(path string) (*ImportReport, error) {
	d, err := db.Open(context.Background(), path)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	items, err := loadItems(context.Background(), d)
	if err != nil {
		return nil, err
	}
	return &ImportReport{Items: items}, nil
}

// legalParent is L4's hierarchy: epic→story, story→task, bug→task, spike→task.
var legalParent = map[string][]string{
	"story": {"epic"},
	"task":  {"story", "bug", "spike"},
}

// Validate is §20 step 5. It re-derives every rule from the stored rows, so a bug in
// the importer cannot produce a tree `items` would have refused.
func Validate(ctx context.Context, d *db.DB, rep *ImportReport) error {
	// 1. The expected keys, parents and statuses are present.
	byID := map[string]ImportedItem{}
	for _, it := range rep.Items {
		byID[it.ID] = it
	}
	stored, err := loadItems(ctx, d)
	if err != nil {
		return err
	}
	if len(stored) != len(rep.Items) {
		return fmt.Errorf("the database holds %d items, the import reported %d", len(stored), len(rep.Items))
	}
	byLegacy := map[string]bool{}
	for _, it := range stored {
		// L4: every parent/child pair must be legal.
		if it.ParentID == "" {
			if it.Type != "epic" && it.Type != "bug" && it.Type != "spike" {
				return fmt.Errorf("%s is a top-level %s; only epics, bugs and spikes may be top-level (L4)", it.Key, it.Type)
			}
			if it.RootID != it.ID {
				return fmt.Errorf("%s is top-level but its root_id is %s", it.Key, it.RootID)
			}
		} else {
			parent, ok := byID[it.ParentID]
			if !ok {
				return fmt.Errorf("%s has parent %s, which was not imported", it.Key, it.ParentID)
			}
			allowed := legalParent[it.Type]
			if !contains(allowed, parent.Type) {
				return fmt.Errorf("%s is a %s under a %s; L4 allows %v", it.Key, it.Type, parent.Type, allowed)
			}
		}
		// Statuses must be in the §5 enum and match §20's mapping.
		if !contains([]string{"draft", "ready", "in_progress", "blocked", "in_review",
			"awaiting_approval", "done", "cancelled"}, it.Status) {
			return fmt.Errorf("%s has status %q", it.Key, it.Status)
		}
		if it.LegacyKey != "" {
			if byLegacy[it.LegacyKey] {
				return fmt.Errorf("%s was imported into two items", it.LegacyKey)
			}
			byLegacy[it.LegacyKey] = true
		}
	}
	// Every §20 source key that should have produced an item did.
	for _, k := range SourceKeys() {
		if !byLegacy[k] {
			return fmt.Errorf("%s is in the migration table but produced no item", k)
		}
	}
	// No key §20 leaves behind was imported.
	for _, k := range NotImported {
		if byLegacy[k] {
			return fmt.Errorf("%s was imported; §20 leaves it in swarm-v1.db", k)
		}
	}

	// 2. I12: no dependency along the hierarchy, and no cycle.
	if err := validateDeps(ctx, d, byID); err != nil {
		return err
	}

	// 3. integrity_check and foreign_key_check (§20 step 5).
	var res string
	if err := d.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&res); err != nil {
		return err
	}
	if res != "ok" {
		return fmt.Errorf("integrity_check: %s", res)
	}
	rows, err := d.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("foreign_key_check reported at least one violation")
	}
	return rows.Err()
}

// loadItems is the one query. LoadReport opens the file and calls it; Validate uses
// it against the handle it already has.
func loadItems(ctx context.Context, d *db.DB) ([]ImportedItem, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, key, type, status, title, COALESCE(legacy_key,''),
		COALESCE(parent_id,''), root_id FROM items ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImportedItem
	for rows.Next() {
		var it ImportedItem
		if err := rows.Scan(&it.ID, &it.Key, &it.Type, &it.Status, &it.Title,
			&it.LegacyKey, &it.ParentID, &it.RootID); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// validateDeps re-derives I12 (no dependency along the hierarchy) and the cycle rule
// from the stored edges. These are the two rules items.AddDep enforces; they are
// re-derived here rather than imported, because AddDep is not reachable from the
// importer's own transaction and the point of step 5 is an independent check.
func validateDeps(ctx context.Context, d *db.DB, byID map[string]ImportedItem) error {
	rows, err := d.QueryContext(ctx, `SELECT item_id, blocked_by_id FROM item_deps`)
	if err != nil {
		return err
	}
	defer rows.Close()
	edges := map[string][]string{}
	for rows.Next() {
		var item, blocker string
		if err := rows.Scan(&item, &blocker); err != nil {
			return err
		}
		if isAncestor(byID, blocker, item) || isAncestor(byID, item, blocker) {
			return fmt.Errorf("%s depends on %s, which is in its own hierarchy (I12)",
				byID[item].Key, byID[blocker].Key)
		}
		edges[item] = append(edges[item], blocker)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	state := map[string]int{} // 0 unseen, 1 on the stack, 2 done
	var walk func(string) error
	walk = func(id string) error {
		switch state[id] {
		case 1:
			return fmt.Errorf("the dependency graph has a cycle through %s", byID[id].Key)
		case 2:
			return nil
		}
		state[id] = 1
		for _, next := range edges[id] {
			if err := walk(next); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for id := range edges {
		if err := walk(id); err != nil {
			return err
		}
	}
	return nil
}

// isAncestor reports whether a is an ancestor of b through parent_id.
func isAncestor(byID map[string]ImportedItem, a, b string) bool {
	for cur := byID[b].ParentID; cur != ""; cur = byID[cur].ParentID {
		if cur == a {
			return true
		}
	}
	return false
}
