package migrate_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/migrate"
)

// fullV1 builds a v4 fixture holding every key §20 names, with the statuses the live
// database had on 2026-09-18, plus rows that must stay behind.
func fullV1(t *testing.T) string {
	t.Helper()
	status := map[string]string{
		"SW-256": "blocked", "SW-355": "blocked", "SW-356": "blocked",
		"SW-357": "backlog", "SW-359": "blocked", "SW-361": "blocked",
		"SW-649": "done", "SW-650": "done",
	}
	inProgress := map[string]bool{}
	for n := 653; n <= 660; n++ {
		inProgress[fmt.Sprintf("SW-%d", n)] = true
	}
	for n := 662; n <= 670; n++ {
		inProgress[fmt.Sprintf("SW-%d", n)] = true
	}
	inProgress["SW-648"] = true

	var rows [][]any
	for _, k := range migrate.SourceKeys() {
		s := "ready"
		if v, ok := status[k]; ok {
			s = v
		} else if inProgress[k] {
			s = "in_progress"
		}
		repo := "/fake/repos/endurio-chat"
		rows = append(rows, row(k, "v1 title for "+k, s, repo, "context for "+k, "", "",
			"2026-09-01 10:00:00", "2026-09-02 11:00:00"))
	}
	// Rows §20 leaves behind.
	rows = append(rows, row("SW-638", "Stays behind", "ready", "", "", "", "", "2026-08-01 10:00:00", "2026-08-01 10:00:00"))
	rows = append(rows, row("SW-1", "Archived", "archived", "", "", "", "", "2026-01-01 10:00:00", "2026-01-01 10:00:00"))
	return newV1DB(t, rows...)
}

func importInto(t *testing.T, v1 string) (*migrate.ImportReport, string, string) {
	t.Helper()
	dir := t.TempDir()
	newPath := filepath.Join(dir, "swarm-v2.tmp.db")
	kb := filepath.Join(dir, "kb")
	// fullV1's repo_path is /fake/repos/endurio-chat, which does not exist, so the
	// repos rows are skipped and reported. The repo case has its own test below,
	// with a real git directory inside t.TempDir() (S-5: never a real path).
	rep, err := migrate.Import(context.Background(), migrate.ImportInput{
		V1Path: v1, NewPath: newPath, KBDir: kb,
		Now: func() time.Time { return time.Unix(1700000000, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	return rep, newPath, kb
}

// §23.2 scenario 18: "a v4 fixture DB gives exactly the §20 tree".
func TestImportProducesExactlyTheSpecTree(t *testing.T) {
	rep, newPath, _ := importInto(t, fullV1(t))
	d, err := db.Open(context.Background(), newPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var epics, stories, tasks, bugs int
	if err := d.QueryRow(`SELECT
		(SELECT count(*) FROM items WHERE type='epic'),
		(SELECT count(*) FROM items WHERE type='story'),
		(SELECT count(*) FROM items WHERE type='task'),
		(SELECT count(*) FROM items WHERE type='bug')`).Scan(&epics, &stories, &tasks, &bugs); err != nil {
		t.Fatal(err)
	}
	if epics != 9 || bugs != 1 {
		t.Errorf("epics = %d (want 9), bugs = %d (want 1)", epics, bugs)
	}
	// 3 + 3 + 1 + 2 + 6 + 4 = 19 stories; 36 + 10 + 8 + 3 = 57 tasks. SW-710 (Table
	// row 2, added mid-session after this brief's test numbers were written for the
	// older 9-row table) is a childless EPIC root, so it adds to the epic count, not
	// the task count.
	if stories != 19 || tasks != 57 {
		t.Errorf("stories = %d (want 19), tasks = %d (want 57)", stories, tasks)
	}

	// The SW-673 epic, its three stories and their 36 tasks.
	var epicID, epicKey, epicStatus, legacy string
	if err := d.QueryRow(`SELECT id, key, status, COALESCE(legacy_key,'') FROM items WHERE title = ?`,
		"Migrate the coach agent from Eve to ADK Go (endurio-chat)").Scan(&epicID, &epicKey, &epicStatus, &legacy); err != nil {
		t.Fatal(err)
	}
	if epicStatus != "ready" || legacy != "SW-673" {
		t.Errorf("epic = %s/%s, want ready/SW-673", epicStatus, legacy)
	}
	if !strings.HasPrefix(epicKey, "EPIC-") {
		t.Errorf("epic key = %q", epicKey)
	}
	var kids int
	if err := d.QueryRow(`SELECT count(*) FROM items WHERE parent_id = ? AND type='story'`, epicID).Scan(&kids); err != nil {
		t.Fatal(err)
	}
	if kids != 3 {
		t.Errorf("SW-673 stories = %d, want 3", kids)
	}
	var subtasks int
	if err := d.QueryRow(`SELECT count(*) FROM items WHERE root_id = ? AND type='task'`, epicID).Scan(&subtasks); err != nil {
		t.Fatal(err)
	}
	if subtasks != 36 {
		t.Errorf("SW-673 tasks = %d, want 36", subtasks)
	}

	// The story chain: PR B blocked by PR A, PR C by PR B.
	for _, pair := range [][2]string{
		{"PR B: ai-SDK completion and the unified tool registry", "PR A: Qdrant vector store"},
		{"PR C: Go ADK service, MCP bridge and Eve removal", "PR B: ai-SDK completion and the unified tool registry"},
	} {
		var n int
		if err := d.QueryRow(`SELECT count(*) FROM item_deps
			WHERE item_id = (SELECT id FROM items WHERE title = ?)
			  AND blocked_by_id = (SELECT id FROM items WHERE title = ?)`, pair[0], pair[1]).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%q is not blocked by %q", pair[0], pair[1])
		}
	}

	// The telemetry chain: 7 edges across 8 tasks.
	var chainEdges int
	if err := d.QueryRow(`SELECT count(*) FROM item_deps d
		JOIN items i ON i.id = d.item_id
		WHERE i.root_id = (SELECT id FROM items WHERE title = ?) AND i.type = 'task'`,
		"Restore end-to-end OpenObserve telemetry in endurio-chat").Scan(&chainEdges); err != nil {
		t.Fatal(err)
	}
	if chainEdges != 7 {
		t.Errorf("telemetry chain edges = %d, want 7", chainEdges)
	}

	// A7: in_progress becomes ready.
	var sw653 string
	if err := d.QueryRow(`SELECT status FROM items WHERE legacy_key = 'SW-653'`).Scan(&sw653); err != nil {
		t.Fatal(err)
	}
	if sw653 != "ready" {
		t.Errorf("SW-653 status = %q, want ready (A7)", sw653)
	}
	// backlog becomes draft.
	var sw357 string
	if err := d.QueryRow(`SELECT status FROM items WHERE legacy_key = 'SW-357'`).Scan(&sw357); err != nil {
		t.Fatal(err)
	}
	if sw357 != "draft" {
		t.Errorf("SW-357 status = %q, want draft", sw357)
	}
	// done stays done.
	var sw649 string
	if err := d.QueryRow(`SELECT status FROM items WHERE legacy_key = 'SW-649'`).Scan(&sw649); err != nil {
		t.Fatal(err)
	}
	if sw649 != "done" {
		t.Errorf("SW-649 status = %q, want done", sw649)
	}
	// A blocked root keeps a restorable previous status.
	var ragStatus, before string
	if err := d.QueryRow(`SELECT status, COALESCE(status_before_block,'') FROM items WHERE legacy_key = 'SW-359'`).
		Scan(&ragStatus, &before); err != nil {
		t.Fatal(err)
	}
	if ragStatus != "blocked" || before != "ready" {
		t.Errorf("SW-359 = %s/%s, want blocked/ready", ragStatus, before)
	}

	// Rows §20 leaves behind are not here.
	for _, k := range append([]string{"SW-1"}, migrate.NotImported...) {
		var n int
		if err := d.QueryRow(`SELECT count(*) FROM items WHERE legacy_key = ?`, k).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s was imported; §20 leaves it in swarm-v1.db", k)
		}
	}

	// A pure container root carries no legacy_key (this plan's decision).
	var containerLegacy sql.NullString
	if err := d.QueryRow(`SELECT legacy_key FROM items WHERE title = ?`, "Indoor trainer workout export").
		Scan(&containerLegacy); err != nil {
		t.Fatal(err)
	}
	if containerLegacy.Valid {
		t.Errorf("legacy_key = %q; a spec-authored container root has none", containerLegacy.String)
	}
	// Its stories do carry theirs.
	var storyLegacy string
	if err := d.QueryRow(`SELECT legacy_key FROM items WHERE title = ?`, "App side (endurio-app)").
		Scan(&storyLegacy); err != nil {
		t.Fatal(err)
	}
	if storyLegacy != "SW-362" {
		t.Errorf("story legacy_key = %q, want SW-362", storyLegacy)
	}

	if len(rep.Items) != epics+stories+tasks+bugs {
		t.Errorf("report has %d items, database has %d", len(rep.Items), epics+stories+tasks+bugs)
	}
}

// §20: a task's title comes from the v1 row; a root's and a sourced story's come
// from the table.
func TestImportKeepsV1TaskTitlesAndRewritesRootAndStoryTitles(t *testing.T) {
	_, newPath, _ := importInto(t, fullV1(t))
	d, err := db.Open(context.Background(), newPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var taskTitle string
	if err := d.QueryRow(`SELECT title FROM items WHERE legacy_key = 'SW-674'`).Scan(&taskTitle); err != nil {
		t.Fatal(err)
	}
	if taskTitle != "v1 title for SW-674" {
		t.Errorf("task title = %q, want the v1 title", taskTitle)
	}
	var storyTitle string
	if err := d.QueryRow(`SELECT title FROM items WHERE legacy_key = 'SW-355'`).Scan(&storyTitle); err != nil {
		t.Fatal(err)
	}
	if storyTitle != "COROS provider adapter" {
		t.Errorf("story title = %q, want §20's rewritten title", storyTitle)
	}
}

// §20: the complete text goes to kb/imported/<SW-KEY>.md and is linked as a note.
func TestImportWritesTheFullTextAndRegistersANoteArtifact(t *testing.T) {
	_, newPath, kb := importInto(t, fullV1(t))
	p := filepath.Join(kb, "imported", "SW-674.md")
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "context for SW-674") {
		t.Errorf("the initial_context is missing:\n%s", body)
	}
	d, err := db.Open(context.Background(), newPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var kind, path string
	var head int
	var createdBy sql.NullString
	if err := d.QueryRow(`SELECT a.kind, a.path, a.head_revision, a.created_by FROM artifacts a
		JOIN items i ON i.id = a.item_id WHERE i.legacy_key = 'SW-674'`).Scan(&kind, &path, &head, &createdBy); err != nil {
		t.Fatal(err)
	}
	if kind != "note" || path != p || head != 1 {
		t.Errorf("artifact = %s/%s/%d", kind, path, head)
	}
	if createdBy.Valid {
		t.Error("created_by must be NULL: no agent wrote an imported note")
	}
	var revs int
	if err := d.QueryRow(`SELECT count(*) FROM artifact_revisions`).Scan(&revs); err != nil {
		t.Fatal(err)
	}
	if revs == 0 {
		t.Error("no artifact_revisions row")
	}
}

// §20's "Repo hints": a repo that exists becomes a repos row and the root's
// suggested set; nothing is confirmed (L25, I13).
func TestImportCreatesRepoRowsForExistingReposAndConfirmsNothing(t *testing.T) {
	// Build a real git repo in the temp dir and point the fixture at it.
	repo := filepath.Join(t.TempDir(), "endurio-chat")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	rows := [][]any{
		row("SW-359", "RAG", "blocked", repo, "ctx", "", "", "2026-09-01 10:00:00", "2026-09-01 10:00:00"),
	}
	for _, k := range migrate.SourceKeys() {
		if k == "SW-359" {
			continue
		}
		rows = append(rows, row(k, "v1 "+k, "ready", "/fake/missing", "ctx", "", "",
			"2026-09-01 10:00:00", "2026-09-01 10:00:00"))
	}
	dir := t.TempDir()
	newPath := filepath.Join(dir, "swarm-v2.tmp.db")
	rep, err := migrate.Import(context.Background(), migrate.ImportInput{
		V1Path: newV1DB(t, rows...), NewPath: newPath, KBDir: filepath.Join(dir, "kb"),
		Now: func() time.Time { return time.Unix(1700000000, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(context.Background(), newPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var repoCount int
	if err := d.QueryRow(`SELECT count(*) FROM repos`).Scan(&repoCount); err != nil {
		t.Fatal(err)
	}
	if repoCount != 1 {
		t.Fatalf("repos = %d, want 1 (only the one that exists)", repoCount)
	}
	var source, path string
	if err := d.QueryRow(`SELECT source, path FROM repos`).Scan(&source, &path); err != nil {
		t.Fatal(err)
	}
	if source != "history" {
		t.Errorf("source = %q, want history", source)
	}
	var suggested, confirmed, hints string
	if err := d.QueryRow(`SELECT suggested_repos_json, confirmed_repos_json, repo_hints_json
		FROM items WHERE legacy_key = 'SW-359'`).Scan(&suggested, &confirmed, &hints); err != nil {
		t.Fatal(err)
	}
	if suggested == "[]" {
		t.Error("the root's suggested_repos_json is empty")
	}
	if confirmed != "[]" || hints != "[]" {
		t.Errorf("confirmed = %s, hints = %s; nothing is confirmed until the user confirms it (L25)", confirmed, hints)
	}
	var mentioned bool
	for _, n := range rep.Notes {
		mentioned = mentioned || strings.Contains(n, "/fake/missing")
	}
	if !mentioned {
		t.Errorf("the missing repo path was not reported: %v", rep.Notes)
	}
}

// §20 step 5: the tree is validated independently of the code that built it.
func TestValidateAcceptsTheImportAndRejectsATamperedTree(t *testing.T) {
	_, newPath, _ := importInto(t, fullV1(t))
	rep, err := migrate.LoadReport(newPath) // reads the tree back out of the database
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(context.Background(), newPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := migrate.Validate(context.Background(), d, rep); err != nil {
		t.Fatalf("a clean import must validate: %v", err)
	}

	// L4: a task directly under an epic is refused.
	var epicID string
	if err := d.QueryRow(`SELECT id FROM items WHERE type='epic' LIMIT 1`).Scan(&epicID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE items SET parent_id = ? WHERE legacy_key = 'SW-674'`, epicID); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Validate(context.Background(), d, rep); err == nil {
		t.Fatal("want an error: a task's parent must be a story or a bug (L4)")
	}
}

// I12: a dependency along the hierarchy is refused.
func TestValidateRejectsADependencyAlongTheHierarchy(t *testing.T) {
	_, newPath, _ := importInto(t, fullV1(t))
	d, err := db.Open(context.Background(), newPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	rep, err := migrate.LoadReport(newPath)
	if err != nil {
		t.Fatal(err)
	}
	var task, parent string
	if err := d.QueryRow(`SELECT id, parent_id FROM items WHERE legacy_key = 'SW-674'`).Scan(&task, &parent); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO item_deps (item_id, blocked_by_id, created_at) VALUES (?, ?, 0)`, task, parent); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Validate(context.Background(), d, rep); err == nil {
		t.Fatal("want an error: a task cannot depend on its own story (I12)")
	}
}

// The importer is deterministic in shape: two runs give the same keys and tree.
func TestImportIsDeterministicInKeysAndOrder(t *testing.T) {
	v1 := fullV1(t)
	var runs [2][]string
	for i := range runs {
		rep, _, _ := importInto(t, v1)
		for _, it := range rep.Items {
			runs[i] = append(runs[i], it.Key+" "+it.Type+" "+it.Status+" "+it.Title)
		}
		sort.Strings(runs[i])
	}
	if strings.Join(runs[0], "\n") != strings.Join(runs[1], "\n") {
		t.Error("two imports of the same fixture produced different trees")
	}
}

// Requirement 1: one transaction for the whole import. SW-361 (a v1-sourced story
// in "Health platform workout ingestion", the second-to-last root Table processes)
// is given a v1 status with no §20 mapping ("review" is deliberately absent from
// statusMap), so MapStatus fails only after every earlier root — including the
// 36-task SW-673 epic — has already been inserted inside the same transaction. A
// failure that late must still roll back everything: zero rows from this import may
// exist in the target database afterward.
func TestImportIsAtomicOnFailure(t *testing.T) {
	var rows [][]any
	for _, k := range migrate.SourceKeys() {
		s := "ready"
		if k == "SW-361" {
			s = "review" // has no §20 mapping; MapStatus fails loudly
		}
		rows = append(rows, row(k, "v1 title for "+k, s, "", "context for "+k, "", "",
			"2026-09-01 10:00:00", "2026-09-02 11:00:00"))
	}
	v1 := newV1DB(t, rows...)
	dir := t.TempDir()
	newPath := filepath.Join(dir, "swarm-v2.tmp.db")
	_, err := migrate.Import(context.Background(), migrate.ImportInput{
		V1Path: v1, NewPath: newPath, KBDir: filepath.Join(dir, "kb"),
		Now: func() time.Time { return time.Unix(1700000000, 0).UTC() },
	})
	if err == nil {
		t.Fatal("want an error: SW-361's status has no §20 mapping")
	}
	d, err := db.Open(context.Background(), newPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var items, deps, artifacts, revisions int
	if err := d.QueryRow(`SELECT
		(SELECT count(*) FROM items),
		(SELECT count(*) FROM item_deps),
		(SELECT count(*) FROM artifacts),
		(SELECT count(*) FROM artifact_revisions)`).Scan(&items, &deps, &artifacts, &revisions); err != nil {
		t.Fatal(err)
	}
	if items != 0 || deps != 0 || artifacts != 0 || revisions != 0 {
		t.Errorf("partial import left rows behind: items=%d item_deps=%d artifacts=%d artifact_revisions=%d",
			items, deps, artifacts, revisions)
	}
}

// Regression test for the exact defect this task hit and reported: a top-level item
// whose type is neither epic nor bug (originally caused by table.go's SW-710 row
// being typed TASK; fixed there, but Validate's own guard against this shape must
// stay covered so a future table.go regression is caught here too, independent of
// what any particular row happens to be typed today).
func TestValidateRejectsATopLevelItemThatIsNotAnEpicOrBug(t *testing.T) {
	_, newPath, _ := importInto(t, fullV1(t))
	d, err := db.Open(context.Background(), newPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	rep, err := migrate.LoadReport(newPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE items SET type = 'task'
		WHERE id = (SELECT id FROM items WHERE type = 'epic' AND parent_id IS NULL LIMIT 1)`); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Validate(context.Background(), d, rep); err == nil {
		t.Fatal("want an error: a top-level task is illegal (L4 allows only epic and bug)")
	}
}

// Validate's own item count must match what is actually stored, independent of
// whatever the importer's in-memory report claims.
func TestValidateRejectsAReportThatDoesNotMatchStoredCount(t *testing.T) {
	_, newPath, _ := importInto(t, fullV1(t))
	d, err := db.Open(context.Background(), newPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	rep, err := migrate.LoadReport(newPath)
	if err != nil {
		t.Fatal(err)
	}
	rep.Items = rep.Items[:len(rep.Items)-1]
	if err := migrate.Validate(context.Background(), d, rep); err == nil {
		t.Fatal("want an error: the report no longer matches the stored row count")
	}
}

// Two items must never share a legacy_key: that would mean the same v1 row was
// imported twice.
func TestValidateRejectsADuplicateLegacyKey(t *testing.T) {
	_, newPath, _ := importInto(t, fullV1(t))
	d, err := db.Open(context.Background(), newPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	rep, err := migrate.LoadReport(newPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE items SET legacy_key = 'SW-674' WHERE legacy_key = 'SW-675'`); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Validate(context.Background(), d, rep); err == nil {
		t.Fatal("want an error: SW-674 is now on two items")
	}
}

// §20's "Everything else stays only in swarm-v1.db" list must never appear as a
// legacy_key in the imported database.
func TestValidateRejectsAKeyThatShouldStayBehind(t *testing.T) {
	_, newPath, _ := importInto(t, fullV1(t))
	d, err := db.Open(context.Background(), newPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// Clone an existing top-level root as a new, otherwise-unrelated row, so every
	// original legacy_key stays present (no "produced no item" side effect) and only
	// the NotImported check is exercised.
	if _, err := d.Exec(`INSERT INTO items (id, key, type, root_id, title, status, legacy_key, created_at, updated_at)
		VALUES ('itm_faketopimport', 'EPIC-9001', 'epic', 'itm_faketopimport', 'fake top-level clone', 'ready', 'SW-638', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	rep, err := migrate.LoadReport(newPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Validate(context.Background(), d, rep); err == nil {
		t.Fatal("want an error: SW-638 is a §20 leave-behind and must never be imported")
	}
}

// §20: a v1 row with neither initial_context nor handoff_note gets no note artifact,
// and is named in Notes instead of silently producing an empty file.
func TestWriteImportedNoteSkipsAnItemWithNoText(t *testing.T) {
	var rows [][]any
	for _, k := range migrate.SourceKeys() {
		ctx, handoff := "context for "+k, ""
		if k == "SW-674" {
			ctx, handoff = "", "" // no text at all
		}
		rows = append(rows, row(k, "v1 title for "+k, "ready", "", ctx, handoff, "",
			"2026-09-01 10:00:00", "2026-09-02 11:00:00"))
	}
	dir := t.TempDir()
	newPath := filepath.Join(dir, "swarm-v2.tmp.db")
	rep, err := migrate.Import(context.Background(), migrate.ImportInput{
		V1Path: newV1DB(t, rows...), NewPath: newPath, KBDir: filepath.Join(dir, "kb"),
		Now: func() time.Time { return time.Unix(1700000000, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "kb", "imported", "SW-674.md")); !os.IsNotExist(err) {
		t.Errorf("SW-674.md should not exist for a textless row: stat err = %v", err)
	}
	var noted bool
	for _, n := range rep.Notes {
		noted = noted || strings.Contains(n, "SW-674 had no context or handoff note")
	}
	if !noted {
		t.Errorf("Notes did not mention SW-674's missing text: %v", rep.Notes)
	}
	d, err := db.Open(context.Background(), newPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var artifacts int
	if err := d.QueryRow(`SELECT count(*) FROM artifacts a JOIN items i ON i.id = a.item_id
		WHERE i.legacy_key = 'SW-674'`).Scan(&artifacts); err != nil {
		t.Fatal(err)
	}
	if artifacts != 0 {
		t.Errorf("SW-674 should have no artifact row, got %d", artifacts)
	}
}
