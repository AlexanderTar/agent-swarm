package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
)

// devRoutes registers POST /api/dev/seed only when the daemon was started
// with --dev (`make dev`; the launchd job never passes it, Task 39). A
// daemon without --dev answers this path with the ordinary "Unknown API
// route." 404 from server.go's catch-all, which is the safety net: dev-seed
// (cmd/swarm/seed.go) refuses ~/.swarm and :7777 on the client side too, but
// the route not existing at all on a production daemon is the one no client
// bug can get around.
func (s *Server) devRoutes() []route {
	if !s.Dev {
		return nil
	}
	return []route{{"POST", "/api/dev/seed", authDaemon, s.devSeed}}
}

// seedItem is one row of the fixture tree, parents listed before children so
// a single pass can resolve parent_id/root_id as it goes.
type seedItem struct {
	key, typ, parent, title, status, statusBeforeBlock, spikeIntent string
	priority, revision                                              int
}

// seedItems is contracts §6's list, ported from P3's own web/src/mock/
// fixtures.ts (the "same keys, states and requests" the brief points at).
// Deliberately narrower than that file in two ways, both disclosed in the
// batch report rather than silently matched or silently dropped:
//
//  1. fixtures.ts also has STORY-50/TASK-150/TASK-151 under EPIC-30. The
//     brief's own §6 bullet enumerates exactly 16 keys and does not include
//     them; this seed follows the brief's explicit list over the fuller
//     mock file, since EPIC-30 needs no children here (its status is forced
//     directly, not derived — see below).
//  2. Every other fixtures.ts field (repos, brief text, acceptance,
//     sort_order, agents, sessions, checkpoints, artifacts, advice) is out
//     of scope: contracts §6 itself only lists "Items" and "Requests" as
//     what dev-seed loads.
//
// Status is set directly at INSERT time, not derived: Items.CreateTx (P1)
// refuses any status besides Draft/Ready at creation (items/store.go:206),
// and there is no cheap way to earn "in_review"/"done"/etc. other than a
// real checkpoint history this task's own file list never asks for. A
// consequence, also disclosed: some of these trees are snapshots the real
// reconciler could not have produced on its own (EPIC-12's accept_epic
// binding names an integrated checkpoint that has no matching row, and
// STORY-41 sits "ready" as a sibling of an epic already "in_progress"),
// exactly as fixtures.ts's own static mock data is never run through the
// real Go state machine either. Interacting with the seeded data (e.g.
// approving the seeded accept_epic) is not guaranteed to derive further
// changes the way a fully live epic would.
func seedItems() []seedItem {
	return []seedItem{
		{key: "EPIC-12", typ: "epic", title: "Authentication", status: "in_progress", priority: 1, revision: 7},
		{key: "STORY-40", typ: "story", parent: "EPIC-12", title: "Login", status: "in_progress", priority: 2, revision: 1},
		{key: "TASK-101", typ: "task", parent: "STORY-40", title: "Build login form", status: "in_progress", priority: 2, revision: 1},
		{key: "TASK-102", typ: "task", parent: "STORY-40", title: "Persist session", status: "blocked", statusBeforeBlock: "in_progress", priority: 2, revision: 1},
		{key: "TASK-104", typ: "task", parent: "STORY-40", title: "Validate inputs", status: "in_review", priority: 2, revision: 1},
		{key: "STORY-41", typ: "story", parent: "EPIC-12", title: "Password reset", status: "ready", priority: 2, revision: 1},
		{key: "TASK-103", typ: "task", parent: "STORY-41", title: "Password reset form", status: "ready", priority: 2, revision: 1},
		{key: "BUG-7", typ: "bug", title: "Login crash", status: "blocked", statusBeforeBlock: "in_progress", priority: 2, revision: 1},
		{key: "TASK-98", typ: "task", parent: "BUG-7", title: "Fix token refresh race", status: "done", priority: 2, revision: 1},
		{key: "TASK-110", typ: "task", parent: "BUG-7", title: "Add crash regression test", status: "ready", priority: 2, revision: 1},
		{key: "BUG-8", typ: "bug", title: "Upload retry", status: "in_review", priority: 2, revision: 4},
		{key: "SPIKE-3", typ: "spike", title: "Offline mode", status: "awaiting_approval", spikeIntent: "feature", priority: 2, revision: 1},
		{key: "SPIKE-4", typ: "spike", title: "Retry banner copy", status: "in_progress", spikeIntent: "feature", priority: 2, revision: 1},
		{key: "SPIKE-5", typ: "spike", title: "Crash on resume", status: "awaiting_approval", spikeIntent: "debug", priority: 2, revision: 1},
		{key: "EPIC-20", typ: "epic", title: "Billing", status: "draft", priority: 2, revision: 1},
		{key: "EPIC-30", typ: "epic", title: "Legacy cleanup", status: "done", priority: 3, revision: 1},
	}
}

type seedDep struct{ item, blockedBy string }

func seedDeps() []seedDep {
	return []seedDep{
		{item: "TASK-102", blockedBy: "TASK-98"},
		{item: "TASK-104", blockedBy: "TASK-102"},
	}
}

// seedRequest is one of the nine fixture requests. agent_id and artifact_id
// are left NULL throughout: both are nullable FKs (schema/0001_init.sql),
// and seeding agents/sessions/artifacts is out of this task's scope (see
// seedItems' doc comment) — a request whose fixtures.ts counterpart names an
// asking agent shows no agent_name here instead.
type seedRequest struct {
	kind, item, prompt, sectionID, sectionSHA256, optionsJSON, bindingJSON string
	artifactRevision                                                       int
}

// seedRequests is fixtures.ts's requests() verbatim (kind, item and prompt
// per request), not the brief's own Task 39 prose: the brief's pasted
// counts (question: 1, approve_section: 2) and its "accept_fix on BUG-7"
// both disagree with the real, landed fixtures.ts (question: 2 — one on
// SPIKE-3, one on TASK-104; approve_section: 1; accept_fix on BUG-8, whose
// status this seed also gives as in_review, not BUG-7's). The brief's own
// long-tail table (row 2) flagged this exact fixtures.ts-hasn't-landed gap
// and asked for a re-check once it did; this is that re-check. Disclosed in
// the batch report; cmd/swarm/seed_test.go's assertions were corrected to
// match.
func seedRequests() []seedRequest {
	return []seedRequest{
		{kind: "accept_epic", item: "EPIC-12", prompt: "Review completed work and accept the epic.",
			bindingJSON: `{"item_revision":7,"integrated_checkpoint":"ckp_int","git":[{"repo":"endurio-chat","branch":"epic/epic-12-authentication","sha":"a1b2c3d4e5f6a7b8"}]}`},
		{kind: "question", item: "SPIKE-3", prompt: "Which sync strategy?", optionsJSON: `["CRDT","Last write wins"]`},
		{kind: "question", item: "TASK-104", prompt: "Which validation library?"},
		{kind: "approve_section", item: "SPIKE-3", prompt: "Approve the data model.",
			sectionID: "data-model", sectionSHA256: "sha-dm-3", artifactRevision: 3},
		{kind: "approve_plan", item: "SPIKE-3", prompt: "Approve the plan.",
			sectionSHA256: "sha-plan-1", artifactRevision: 1},
		{kind: "approve_report", item: "SPIKE-5", prompt: "Approve the report.",
			sectionSHA256: "sha-report-2", artifactRevision: 2},
		{kind: "close_spike", item: "SPIKE-4", prompt: "Nothing to build.",
			bindingJSON: `{"resolution":"duplicate_of:EPIC-12"}`},
		{kind: "confirm_repos", item: "SPIKE-3", prompt: "Offline sync needs the chat API and the app client.",
			optionsJSON: `{"proposed":[{"repo":"repo_chat","reason":"the chat API stores messages.","source":"user"},` +
				`{"repo":"repo_app","reason":"the sync queue lives in the app's data layer.","source":"agent"}],` +
				`"expansion":[{"repo":"repo_landing","reason":"pricing page lists offline mode as a feature."}]}`,
			bindingJSON: `{"repos_version":0}`},
		{kind: "accept_fix", item: "BUG-8", prompt: "Review the fix and accept it.",
			bindingJSON: `{"item_revision":4,"integrated_checkpoint":"ckp_fix","git":[{"repo":"endurio-app","branch":"bug/bug-8-upload-retry","sha":"0f0e0d0c0b0a0908"}]}`},
	}
}

// devSeed is POST /api/dev/seed: idempotent (a second call is a silent
// no-op, gated on EPIC-12 already existing — see seedFixtures), in-process
// (no fake agent, no HTTP route exists for creating a bare request, per the
// brief's own note on why this runs inside the daemon rather than as
// ordinary client-side POSTs).
func (s *Server) devSeed(w http.ResponseWriter, r *http.Request) {
	if s.notWired(w, s.DB != nil) {
		return
	}
	n, m, err := s.seedFixtures(r.Context())
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"items": n, "requests": m})
}

func seedIndex(key string) int {
	_, num, ok := strings.Cut(key, "-")
	if !ok {
		return 0
	}
	n, _ := strconv.Atoi(num)
	return n
}

func (s *Server) seedFixtures(ctx context.Context) (itemsWritten, requestsWritten int, err error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	var probe int
	switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM items WHERE key = 'EPIC-12'`).Scan(&probe); {
	case err == nil:
		return 0, 0, tx.Commit() // already seeded
	case err != sql.ErrNoRows:
		return 0, 0, err
	}

	now := db.Millis(time.Now())
	idOf := map[string]string{}
	rootOf := map[string]string{}
	maxIndex := map[string]int{}

	for _, it := range seedItems() {
		id := ids.New("itm")
		idOf[it.key] = id
		var parentID sql.NullString
		rootID := id
		if it.parent != "" {
			pid, ok := idOf[it.parent]
			if !ok {
				return 0, 0, fmt.Errorf("seed: %s names unknown parent %s", it.key, it.parent)
			}
			parentID = sql.NullString{String: pid, Valid: true}
			rootID = rootOf[it.parent]
		}
		rootOf[it.key] = rootID
		if _, err := tx.ExecContext(ctx, `INSERT INTO items (id, key, type, parent_id, root_id, title,
			status, status_before_block, priority, spike_intent, revision, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), ?, ?, ?)`,
			id, it.key, it.typ, parentID, rootID, it.title, it.status, it.statusBeforeBlock,
			it.priority, it.spikeIntent, it.revision, now, now); err != nil {
			return 0, 0, err
		}
		itemsWritten++
		if n := seedIndex(it.key); n > maxIndex[it.typ] {
			maxIndex[it.typ] = n
		}
	}

	// Advance each type's key counter past every seeded index, so the next
	// real item created through the normal API can never collide with a
	// fixture key (ids.NextKey, internal/ids/ids.go).
	for typ, max := range maxIndex {
		if _, err := tx.ExecContext(ctx, `INSERT INTO key_counters (type, next) VALUES (?, ?)
			ON CONFLICT(type) DO UPDATE SET next = MAX(next, excluded.next)`, typ, max+1); err != nil {
			return 0, 0, err
		}
	}

	for _, d := range seedDeps() {
		itemID, blockedByID := idOf[d.item], idOf[d.blockedBy]
		if itemID == "" || blockedByID == "" {
			return 0, 0, fmt.Errorf("seed: dependency references an unknown item")
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO item_deps (item_id, blocked_by_id, created_at)
			VALUES (?, ?, ?)`, itemID, blockedByID, now); err != nil {
			return 0, 0, err
		}
	}

	for _, rq := range seedRequests() {
		itemID, ok := idOf[rq.item]
		if !ok {
			return 0, 0, fmt.Errorf("seed: request %s names unknown item %s", rq.kind, rq.item)
		}
		options := rq.optionsJSON
		if options == "" {
			options = "[]"
		}
		var artifactRevision sql.NullInt64
		if rq.artifactRevision != 0 {
			artifactRevision = sql.NullInt64{Int64: int64(rq.artifactRevision), Valid: true}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO requests (id, kind, item_id, section_id, section_sha256,
			prompt, options_json, state, artifact_revision, binding_json, created_at)
			VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, 'open', ?, NULLIF(?, ''), ?)`,
			ids.New("req"), rq.kind, itemID, rq.sectionID, rq.sectionSHA256, rq.prompt, options,
			artifactRevision, rq.bindingJSON, now); err != nil {
			return 0, 0, err
		}
		requestsWritten++
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return itemsWritten, requestsWritten, nil
}
