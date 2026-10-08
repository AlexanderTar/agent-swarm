package runtime

import (
	"context"
	"database/sql"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/workflow"
)

func TestSpecSectionRequestAndMaterializationGateAgree(t *testing.T) {
	for _, tc := range []struct {
		title    string
		required bool
	}{
		{"Context", false}, {"2. BACKGROUND", false}, {"2.3. Bibliography", false},
		{"References", false}, {"File list", false}, {"FILES", false},
		{"3. Work breakdown", false}, {"Out of scope", true},
		{"2. Explicitly out of scope", true}, {"Unlisted decision", true},
		{"", true},
	} {
		name := tc.title
		if name == "" {
			name = "headingless"
		}
		t.Run(name, func(t *testing.T) {
			s, _, _ := newStore(t)
			ctx := context.Background()
			key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Review", Intent: "feature", Kind: Fake, Model: "fake-1"})
			if err != nil {
				t.Fatal(err)
			}
			ses, err := s.LatestSession(ctx, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			body := "A decision.\n"
			if tc.title != "" {
				body = "## " + tc.title + "\n\nA decision.\n"
			}
			spec, err := s.RegisterArtifact(ctx, ses.ID, "register", key, "spec", writeFile(t, body), "")
			if err != nil {
				t.Fatal(err)
			}
			section := spec.Sections[0]
			req, askErr := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: spec.ArtifactID,
				SectionID: section.ID, Prompt: "Review this decision."})
			if (askErr == nil) != tc.required {
				t.Fatalf("section ask error = %v, required = %v", askErr, tc.required)
			}
			gate := func() error {
				return s.tx(ctx, func(tx *sql.Tx) error {
					return s.checkEverySectionApproved(ctx, tx, spec.ArtifactID)
				})
			}
			if err := gate(); (err != nil) != tc.required {
				t.Fatalf("materialization gate error = %v, required = %v", err, tc.required)
			}
			if tc.required {
				if _, err := s.Approve(ctx, req.ID, ApproveInput{SectionSHA256: section.SHA256,
					ArtifactRevision: spec.Revision, Via: "board"}); err != nil {
					t.Fatal(err)
				}
				if err := gate(); err != nil {
					t.Fatalf("approved section blocked materialization: %v", err)
				}
			}
		})
	}
}

func TestHeadinglessPreambleBlocksPlanReviewUntilApproved(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Preamble decision", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := s.RegisterArtifact(ctx, ses.ID, "register", key, "spec",
		writeFile(t, "Decision: ship X\n\n## Context\n\nWhy it matters.\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.RegisterArtifact(ctx, ses.ID, "register", key, "plan", writeFile(t, planBody), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Sections) != 2 || spec.Sections[0].ID != "document" {
		t.Fatalf("registered sections = %+v", spec.Sections)
	}
	if _, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: plan.ArtifactID,
		Prompt: "Ship the plan."}); err == nil || !strings.Contains(err.Error(), "approval_missing: section") {
		t.Fatalf("plan review accepted an unapproved preamble: %v", err)
	}
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: spec.ArtifactID,
		SectionID: spec.Sections[0].ID, Prompt: "Ship X."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(ctx, req.ID, ApproveInput{SectionSHA256: spec.Sections[0].SHA256,
		ArtifactRevision: spec.Revision, Via: "board"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: plan.ArtifactID,
		Prompt: "Ship the plan."}); err != nil {
		t.Fatalf("plan review stayed blocked after preamble approval: %v", err)
	}
}

func TestRevisedSectionNeedsNewApprovalBeforePlanReviewAndMaterialization(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ses, specID, planID, _ := approvedFeatureSpike(t, s)
	art, _, err := s.ArtifactMarkdown(ctx, specID, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(art.Path, []byte("# Spec\n\n## Context\n\nauth is missing\n\n## Decisions\n\ntokens\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	revised, err := s.RegisterArtifact(ctx, ses.ID, "revise", "SPIKE-1", "spec", art.Path, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: planID,
		Prompt: "Ship the plan."}); err == nil || !strings.Contains(err.Error(), "approval_missing") {
		t.Fatalf("plan review opened without revised section approval: %v", err)
	}
	if _, err := s.Materialize(ctx, ses.ID, "SPIKE-1", specID, planID, "", ""); err == nil ||
		!strings.Contains(err.Error(), "approval_missing: section") {
		t.Fatalf("materialization accepted the old section hash: %v", err)
	}
	section := revised.Sections[1]
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: specID,
		SectionID: section.ID, Prompt: "Ship token auth."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(ctx, req.ID, ApproveInput{SectionSHA256: section.SHA256,
		ArtifactRevision: revised.Revision, Via: "board"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: planID,
		Prompt: "Ship the plan."}); err != nil {
		t.Fatalf("plan review stayed blocked after renewed approval: %v", err)
	}
	if _, err := s.Materialize(ctx, ses.ID, "SPIKE-1", specID, planID, "", ""); err != nil {
		t.Fatalf("materialization stayed blocked after renewed approval: %v", err)
	}
}

// approvedFeatureSpike sets up a spike with one confirmed repo "chat", an approved
// spec (2 sections) and an approved plan carrying planBody's tree, and returns the
// session and artifact ids. It goes through RegisterArtifact and Approve, not raw
// SQL: the point of every test below is that Materialize trusts what those two
// wrote, so a hand-built row would prove nothing.
func approvedFeatureSpike(t *testing.T, s *Store) (ses Session, specID, planID, planPath string) {
	t.Helper()
	ctx := context.Background()
	repo := seedRepo(t, s, "chat")
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Ship auth", Intent: "feature",
		Kind: Fake, Model: "fake-1", Repos: []string{repo}})
	if err != nil {
		t.Fatal(err)
	}
	if key != "SPIKE-1" {
		t.Fatalf("first spike key = %s, want SPIKE-1 (the tests below hard-code it)", key)
	}
	ses, err = s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The repo selected at spike creation is already available to the plan.
	const specBody = "# Spec\n\n## Context\n\nauth is missing\n\n## Decisions\n\ncookies\n"
	spec, err := s.RegisterArtifact(ctx, ses.ID, "register", key, "spec", writeFile(t, specBody), "")
	if err != nil {
		t.Fatal(err)
	}
	planPath = writeFile(t, planBody)
	plan, err := s.RegisterArtifact(ctx, ses.ID, "register", key, "plan", planPath, "")
	if err != nil {
		t.Fatal(err)
	}
	// Every spec section approved, then the plan as a whole file. The signatures
	// are the ledger's: RegisterArtifact(ctx, sessionID, op, itemKey, kind, path)
	// returns ArtifactResult{ArtifactID, Revision, Sections, StaleRequests}, and
	// Approve takes an ApproveInput — not a positional comment/via/sha/revision list.
	for _, sec := range spec.Sections {
		if !RequiredSpecSection(sec.Title) {
			continue
		}
		r, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: spec.ArtifactID,
			SectionID: sec.ID, Prompt: "Approve " + sec.Title + "."})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Approve(ctx, r.ID, ApproveInput{SectionSHA256: sec.SHA256,
			ArtifactRevision: spec.Revision, Via: "board"}); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: plan.ArtifactID,
		Prompt: "Approve the plan."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(ctx, r.ID, ApproveInput{ArtifactRevision: plan.Revision, Via: "board"}); err != nil {
		t.Fatal(err)
	}
	return ses, spec.ArtifactID, plan.ArtifactID, planPath
}

// approvedDebugSpike is the same shape for intent "debug": one debug_report
// artifact whose swarm-tree has a bug root, approved whole-file.
func approvedDebugSpike(t *testing.T, s *Store) (ses Session, reportID string) {
	t.Helper()
	ctx := context.Background()
	repo := seedRepo(t, s, "chat")
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Find the crash", Intent: "debug",
		Kind: Fake, Model: "fake-1", Repos: []string{repo}})
	if err != nil {
		t.Fatal(err)
	}
	ses, err = s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	report, err := s.RegisterArtifact(ctx, ses.ID, "register", key, "debug_report", writeFile(t, reportBody), "")
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: report.ArtifactID,
		Prompt: "Approve the report."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(ctx, r.ID, ApproveInput{ArtifactRevision: report.Revision, Via: "board"}); err != nil {
		t.Fatal(err)
	}
	return ses, report.ArtifactID
}

// reportBody is planBody's shape with a bug root and one story-less task list.
const reportBody = "# Report\n\n## Root cause\n\nthe cookie is dropped\n\n## Work breakdown\n\n" +
	"```swarm-tree\n" +
	`{"root":{"type":"bug","title":"Login drops the cookie","brief":"","acceptance":["It stays."]},
 "children":[{"ref":"t1","type":"task","title":"Set SameSite","brief":"","acceptance":[],"role_hint":"debugger","tdd_exempt":null,"repos":["chat"],"workflow":{"template":"debug"},"steps":["Write test","Fix"],"verify":["go test ./..."],"solo":"focused"}],
 "deps":[]}` + "\n```\n"

// countItems is the roll-back assertion's yardstick.
func countItems(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM items`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMaterializeBuildsTheEpicTree(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ses, specID, planID, _ := approvedFeatureSpike(t, s)
	res, err := s.Materialize(ctx, ses.ID, "SPIKE-1", specID, planID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Root, "EPIC-") || len(res.Created) != 4 {
		t.Fatalf("result = %+v (root + 1 story + 2 tasks)", res)
	}
	root, err := s.Items.Get(ctx, res.Root)
	if err != nil {
		t.Fatal(err)
	}
	if root.Status != items.Ready {
		t.Fatalf("the root starts as ready, got %s", root.Status)
	}
	children, _ := s.Items.Children(ctx, res.Root)
	if len(children) != 1 || children[0].Type != items.Story || children[0].Status != items.Ready {
		t.Fatalf("children = %+v", children)
	}
	tasks, _ := s.Items.Children(ctx, children[0].Key)
	if len(tasks) != 2 {
		t.Fatalf("tasks = %+v", tasks)
	}
	// the dependency landed: t2 waits for t1
	blocked, _, _ := s.Items.Deps(ctx, tasks[1].Key)
	if len(blocked) != 1 || blocked[0].Key != tasks[0].Key {
		t.Fatalf("deps = %+v", blocked)
	}
	// the spike is done and linked
	spike, _ := s.Items.Get(ctx, "SPIKE-1")
	if spike.Status != items.Done {
		t.Fatalf("spike status = %s", spike.Status)
	}
	if root.OriginSpikeID != spike.ID {
		t.Fatalf("origin_spike_id = %q", root.OriginSpikeID)
	}
	// the artifacts are copied onto the root at their approved revisions
	arts, _ := s.ArtifactsFor(ctx, root.ID)
	if len(arts) != 2 {
		t.Fatalf("artifacts on the root = %d", len(arts))
	}
	// the orchestrator is told, and the user is notified
	var relays int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE kind = 'relay'`).Scan(&relays)
	if relays == 0 {
		t.Fatal("the spike's orchestrator gets a relay")
	}
	// The lookup key is "item.created" for an epic and "item.created.bug" for a bug
	// (Task 23's note); the rendered "Epic ready" title is Task 23's to pin.
	n := notified(t, s, "item.created")
	if n.Args["SPIKE-KEY"] != "SPIKE-1" || n.Args["ROOT-KEY"] != res.Root {
		t.Fatalf("notification args = %v", n.Args)
	}

	// The reported bug: the new root's orchestrator could never leave Draft.
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: res.Root, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, mustSessionID(t, s, orch.ID), CheckpointInput{Kind: Accepted, Summary: "on it"}); err != nil {
		t.Fatal(err)
	}
	if root, _ = s.Items.Get(ctx, res.Root); root.Status != items.InProgress {
		t.Fatalf("after accepted: %s", root.Status)
	}
}

func TestMaterializeRefusesAMissingApproval(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ses, specID, planID, _ := approvedFeatureSpike(t, s)
	// stale one section's approval by editing it
	s.DB.ExecContext(ctx, `UPDATE requests SET state = 'stale' WHERE kind = 'approve_section'
		AND section_id = 'decisions'`)
	_, err := s.Materialize(ctx, ses.ID, "SPIKE-1", specID, planID, "", "")
	if err == nil || !strings.HasPrefix(err.Error(), "approval_missing: section ") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "is not approved at its current revision") {
		t.Fatalf("err = %v", err)
	}
}

// C2: the file on disk must still hash to the approved revision.
func TestMaterializeRefusesAFileEditedAfterApproval(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ses, specID, planID, planPath := approvedFeatureSpike(t, s)
	os.WriteFile(planPath, []byte(planBody+"\nextra\n"), 0o644)
	_, err := s.Materialize(ctx, ses.ID, "SPIKE-1", specID, planID, "", "")
	if err == nil || !strings.HasPrefix(err.Error(), "artifact_changed: ") ||
		!strings.HasSuffix(err.Error(), " changed after approval. Revise and ask again.") {
		t.Fatalf("err = %v", err)
	}
}

func TestMaterializeRefusesAMissingPlanFile(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ses, specID, planID, planPath := approvedFeatureSpike(t, s)
	if err := os.Remove(planPath); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Materialize(ctx, ses.ID, "SPIKE-1", specID, planID, "", ""); err == nil {
		t.Fatal("a missing file must be refused")
	}
}

func TestMaterializeUsesCatalogRepoOutsideStartingHint(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ses, specID, planID, _ := approvedFeatureSpike(t, s)
	// planBody's tasks name repo "chat"; confirm a different one
	other := seedRepo(t, s, "app")
	var repo string
	if err := s.DB.QueryRowContext(ctx, `SELECT id FROM repos WHERE name = 'chat'`).Scan(&repo); err != nil {
		t.Fatal(err)
	}
	s.DB.ExecContext(ctx, `UPDATE items SET confirmed_repos_json = ? WHERE key = 'SPIKE-1'`,
		`["`+other+`"]`)
	result, err := s.Materialize(ctx, ses.ID, "SPIKE-1", specID, planID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.Items.Get(ctx, result.Created[len(result.Created)-1])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(task.Repos, repo) {
		t.Fatalf("task repository ids = %v, want %s", task.Repos, repo)
	}
}

func TestMaterializeRejectsAmbiguousCatalogRepoName(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ses, specID, planID, _ := approvedFeatureSpike(t, s)
	seedRepo(t, s, "chat")
	_, err := s.Materialize(ctx, ses.ID, "SPIKE-1", specID, planID, "", "")
	if err == nil || !strings.Contains(err.Error(), `repository name "chat" is ambiguous`) {
		t.Fatalf("ambiguous repository = %v", err)
	}
}

func TestMaterializeRejectsUnregisteredCatalogRepoName(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	tree, err := ParseTree(planBody)
	if err != nil {
		t.Fatal(err)
	}
	spike, err := s.Items.Create(ctx, items.CreateInput{Type: items.Spike, Title: "Spike", SpikeIntent: "feature"}, items.Daemon())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = s.createTree(ctx, tx, spike, tree, items.Epic)
	if err == nil || !strings.Contains(err.Error(), `repository "chat" is not registered`) || !strings.Contains(err.Error(), "swarm_repo_register") {
		t.Fatalf("unregistered repository = %v", err)
	}
}

func TestMaterializeAdoptsLegacySelectedReposWithoutWorktreeAccess(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ses, specID, planID, _ := approvedFeatureSpike(t, s)
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET confirmed_repos_json = '[]', repos_version = 0 WHERE key = 'SPIKE-1'`); err != nil {
		t.Fatal(err)
	}
	out, err := s.Materialize(ctx, ses.ID, "SPIKE-1", specID, planID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	root, err := s.Items.Get(ctx, out.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Repos) != 1 {
		t.Fatalf("materialized repos = %v, want selected repo", root.Repos)
	}
}

func TestMaterializeRefusesAnotherAgent(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, specID, planID, _ := approvedFeatureSpike(t, s)
	_, other, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Other", Intent: "feature", Kind: Fake, Model: "fake-1"})
	oSes, _ := s.LatestSession(ctx, other.ID)
	if _, err := s.Materialize(ctx, oSes.ID, "SPIKE-1", specID, planID, "", ""); err == nil {
		t.Fatal("only the spike's own orchestrator may materialize it")
	}
}

func TestMaterializeIsAllOrNothing(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ses, specID, planID, _ := approvedFeatureSpike(t, s)
	before := countItems(t, s)
	// Break the tree after approval by pointing the dep's blocked_by at a ref that
	// does not exist. Replacing every `"t1"` would rename the node *and* the dep
	// endpoint, leaving a tree that is still internally consistent — which is why
	// the earlier version of this test could not fail (D46). Only the dep moves.
	if _, err := s.DB.ExecContext(ctx, `UPDATE artifact_revisions
		SET tree_json = replace(tree_json, '"blocked_by":"t1"', '"blocked_by":"nope"')
		WHERE artifact_id = ?`, planID); err != nil {
		t.Fatal(err)
	}
	// Prove the mutation landed, so a future change to planBody's spacing cannot
	// turn this into a test that asserts nothing.
	var raw string
	if err := s.DB.QueryRowContext(ctx, `SELECT tree_json FROM artifact_revisions
		WHERE artifact_id = ? ORDER BY revision DESC LIMIT 1`, planID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"blocked_by":"nope"`) {
		t.Fatalf("the mutation did not match tree_json; the test would pass for the wrong reason: %s", raw)
	}
	if _, err := s.Materialize(ctx, ses.ID, "SPIKE-1", specID, planID, "", ""); err == nil {
		t.Fatal("an unknown ref must be refused")
	}
	if got := countItems(t, s); got != before {
		t.Fatalf("items went from %d to %d; the transaction must roll back", before, got)
	}
	spike, _ := s.Items.Get(ctx, "SPIKE-1")
	if spike.Status == items.Done {
		t.Fatal("the spike must not be closed by a failed materialization")
	}
}

// A debug spike materializes from a report alone, into a BUG with tasks.
func TestDebugSpikeMaterializesABug(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ses, reportID := approvedDebugSpike(t, s)
	res, err := s.Materialize(ctx, ses.ID, "SPIKE-1", "", "", reportID, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Root, "BUG-") {
		t.Fatalf("root = %q", res.Root)
	}
	children, _ := s.Items.Children(ctx, res.Root)
	for _, c := range children {
		if c.Type != items.Task {
			t.Fatalf("a bug's children are tasks, got %s", c.Type)
		}
	}
	// A bug root uses the internal lookup key item.created.bug, which is what gives
	// it the "Bug ready" title; the column and the SSE payload still say item.created.
	notified(t, s, "item.created.bug")
	if notifiedCount(s, "item.created") != 0 {
		t.Fatal("a bug root must use the .bug lookup key, not the plain one")
	}
}

func TestMaterializeRefusesAPlanNotApprovedAtItsCurrentRevision(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ses, specID, planID, _ := approvedFeatureSpike(t, s)
	if _, err := s.DB.ExecContext(ctx, `UPDATE requests SET state = 'stale' WHERE kind = 'approve_plan'`); err != nil {
		t.Fatal(err)
	}
	_, err := s.Materialize(ctx, ses.ID, "SPIKE-1", specID, planID, "", "")
	if err == nil || !strings.HasPrefix(err.Error(), "approval_missing:") {
		t.Fatalf("err = %v", err)
	}
}

// checkTreeShape's own guard: a feature spike's plan must root an epic, even
// when the tree is otherwise internally valid (a bug rooting a task is legal
// on its own — L4 — just not for this intent).
func TestMaterializeRefusesARootTypeMismatch(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	repo := seedRepo(t, s, "chat")
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Mismatch", Intent: "feature",
		Kind: Fake, Model: "fake-1", Repos: []string{repo}})
	if err != nil {
		t.Fatal(err)
	}
	ses, _ := s.LatestSession(ctx, a.ID)
	spec, err := s.RegisterArtifact(ctx, ses.ID, "register", key, "spec", writeFile(t, "# s\n\n## One\n\na\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	badPlan := "## Work breakdown\n\n```swarm-tree\n" +
		`{"root":{"type":"bug","title":"Wrong","brief":"","acceptance":[]},
"children":[{"ref":"t1","type":"task","title":"t","brief":"","acceptance":[],"repos":["chat"],"workflow":{"template":"mechanical"},"steps":["Fix"],"verify":["go test ./..."],"solo":"focused"}],"deps":[]}` +
		"\n```\n"
	plan, err := s.RegisterArtifact(ctx, ses.ID, "register", key, "plan", writeFile(t, badPlan), "")
	if err != nil {
		t.Fatal(err)
	}
	sr, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: spec.ArtifactID,
		SectionID: spec.Sections[0].ID, Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(ctx, sr.ID, ApproveInput{SectionSHA256: spec.Sections[0].SHA256,
		ArtifactRevision: spec.Revision, Via: "board"}); err != nil {
		t.Fatal(err)
	}
	pr, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: plan.ArtifactID, Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(ctx, pr.ID, ApproveInput{ArtifactRevision: plan.Revision, Via: "board"}); err != nil {
		t.Fatal(err)
	}
	_, err = s.Materialize(ctx, ses.ID, key, spec.ArtifactID, plan.ArtifactID, "", "")
	if err == nil || !strings.HasPrefix(err.Error(), "tree_invalid: root must be epic") {
		t.Fatalf("err = %v", err)
	}
}

// tdd_exempt on a swarm-tree task must land on the materialized item.
func TestMaterializePropagatesTddExempt(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	repo := seedRepo(t, s, "chat")
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Exempt", Intent: "feature",
		Kind: Fake, Model: "fake-1", Repos: []string{repo}})
	if err != nil {
		t.Fatal(err)
	}
	ses, _ := s.LatestSession(ctx, a.ID)
	spec, err := s.RegisterArtifact(ctx, ses.ID, "register", key, "spec", writeFile(t, "# s\n\n## One\n\na\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	plan := "## Work breakdown\n\n```swarm-tree\n" +
		`{"root":{"type":"epic","title":"E","brief":"","acceptance":["x"]},
"children":[{"ref":"s1","type":"story","title":"S","brief":"","acceptance":[],
  "children":[{"ref":"t1","type":"task","title":"T","brief":"","acceptance":[],"tdd_exempt":"docs","repos":["chat"],"workflow":{"template":"tdd-reviewed"},"steps":["Write docs"],"verify":["go test ./..."],"solo":"focused"}]}],
"deps":[]}` + "\n```\n"
	planRes, err := s.RegisterArtifact(ctx, ses.ID, "register", key, "plan", writeFile(t, plan), "")
	if err != nil {
		t.Fatal(err)
	}
	sr, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: spec.ArtifactID,
		SectionID: spec.Sections[0].ID, Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(ctx, sr.ID, ApproveInput{SectionSHA256: spec.Sections[0].SHA256,
		ArtifactRevision: spec.Revision, Via: "board"}); err != nil {
		t.Fatal(err)
	}
	pr, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: planRes.ArtifactID, Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(ctx, pr.ID, ApproveInput{ArtifactRevision: planRes.Revision, Via: "board"}); err != nil {
		t.Fatal(err)
	}
	res, err := s.Materialize(ctx, ses.ID, key, spec.ArtifactID, planRes.ArtifactID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	children, _ := s.Items.Children(ctx, res.Root)
	tasks, _ := s.Items.Children(ctx, children[0].Key)
	if len(tasks) != 1 || tasks[0].TddExempt != "docs" {
		t.Fatalf("tdd_exempt did not propagate: %+v", tasks)
	}
}

func TestFeatureSpikeNeedsBothArtifacts(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ses, specID, planID, _ := approvedFeatureSpike(t, s)
	if _, err := s.Materialize(ctx, ses.ID, "SPIKE-1", specID, "", "", ""); err == nil {
		t.Fatal("a feature spike needs the plan too")
	}
	if _, err := s.Materialize(ctx, ses.ID, "SPIKE-1", "", planID, "", ""); err == nil {
		t.Fatal("a feature spike needs the spec too")
	}
}

func TestMaterializeCopiesWorkflowUnitsVerify(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	seedRepo(t, s, "chat")
	tree, err := ParseTree(planBody)
	if err != nil {
		t.Fatal(err)
	}
	tree.Children[0].Children[0].Workflow = &workflow.Spec{Template: "tdd-reviewed"}
	tree.Children[0].Children[0].Steps = nil
	tree.Children[0].Children[0].Units = []TreeUnit{{Title: "Cookie", Steps: []string{"Write test", "Implement"}}}
	tree.Children[0].Children[0].Verify = []string{"go test ./internal/runtime/..."}
	tree.Children[0].Children[0].Solo = "isolated"
	spike, err := s.Items.Create(ctx, items.CreateInput{Type: items.Spike, Title: "Spike", SpikeIntent: "feature"}, items.Daemon())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	res, err := s.createTree(ctx, tx, spike, tree, items.Epic)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.Items.GetTx(ctx, tx, res.Created[2])
	if err != nil {
		t.Fatal(err)
	}
	if task.Workflow == nil || len(task.Units) != 1 || task.Units[0].Title != "Cookie" || task.Solo != "isolated" || len(task.Verify) != 1 {
		t.Fatalf("task = %+v", task)
	}
}

func TestMaterializeDerivesRoleHint(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	seedRepo(t, s, "chat")
	tree, err := ParseTree(planBody)
	if err != nil {
		t.Fatal(err)
	}
	tree.Children[0].Children[0].Workflow = &workflow.Spec{Template: "debug"}
	tree.Children[0].Children[0].RoleHint = ""
	spike, err := s.Items.Create(ctx, items.CreateInput{Type: items.Spike, Title: "Spike", SpikeIntent: "feature"}, items.Daemon())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	res, err := s.createTree(ctx, tx, spike, tree, items.Epic)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.Items.GetTx(ctx, tx, res.Created[2])
	if err != nil {
		t.Fatal(err)
	}
	if task.RoleHint != "debugger" {
		t.Fatalf("role_hint = %q", task.RoleHint)
	}
}

// Spec E2.
func TestMaterializeDebugRootIsReady(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ses, reportID := approvedDebugSpike(t, s)
	res, err := s.Materialize(ctx, ses.ID, "SPIKE-1", "", "", reportID, "")
	if err != nil {
		t.Fatal(err)
	}
	root, _ := s.Items.Get(ctx, res.Root)
	if root.Type != items.Bug || root.Status != items.Ready {
		t.Fatalf("root = %s %s, want a ready bug", root.Type, root.Status)
	}
}

// choreTreeBody is a plan whose swarm-tree has a chore root (legacy chore spikes).
const choreTreeBody = "# Plan\n\n## Work breakdown\n\n" +
	"```swarm-tree\n" +
	`{"root":{"type":"chore","title":"Bump deps","brief":"","acceptance":["Deps are current."]},
 "children":[{"ref":"t1","type":"task","title":"Bump go deps","brief":"","acceptance":[],"role_hint":"coder","tdd_exempt":null,"repos":["chat"],"workflow":{"template":"tdd-reviewed"},"steps":["Write test","Bump"],"verify":["go test ./..."],"solo":"focused"}],
 "deps":[]}` + "\n```\n\n## Verification\n\ngo test ./...\n"

// Spec decision 5 / E14: a live spike with intent chore (SPIKE-20) still
// materializes. The row is set directly because CreateTx now refuses it.
func TestMaterializeLegacyChoreSpike(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	repo := seedRepo(t, s, "chat")
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Bump deps", Intent: "feature",
		Kind: Fake, Model: "fake-1", Repos: []string{repo}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET spike_intent = 'chore' WHERE key = ?`, key); err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.RegisterArtifact(ctx, ses.ID, "register", key, "plan", writeFile(t, choreTreeBody), "")
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: plan.ArtifactID, Prompt: "Approve the plan."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(ctx, r.ID, ApproveInput{ArtifactRevision: plan.Revision, Via: "board"}); err != nil {
		t.Fatal(err)
	}
	res, err := s.Materialize(ctx, ses.ID, key, "", plan.ArtifactID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	root, _ := s.Items.Get(ctx, res.Root)
	if root.Type != items.Chore || root.Status != items.Ready || !strings.HasPrefix(root.Key, "CHORE-") {
		t.Fatalf("root = %+v", root)
	}
	children, _ := s.Items.Children(ctx, root.Key)
	if len(children) != 1 || children[0].Type != items.Task || children[0].Status != items.Ready {
		t.Fatalf("children = %+v", children)
	}
	if sp, _ := s.Items.Get(ctx, key); sp.Status != items.Done {
		t.Fatalf("spike = %s, want done", sp.Status)
	}
}

// BUG-58: materialize must not close a spike while one of its own tasks is
// still open, nor finish that task's live agent; the spike closes once the
// task finishes.
func TestMaterializeLeavesSpikeOpenWhileItsTasksRun(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ses, specID, planID, _ := approvedFeatureSpike(t, s)
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: Accepted, Summary: "spiking"}); err != nil {
		t.Fatal(err)
	}
	task, err := s.Items.Create(ctx, items.CreateInput{Type: items.Task, ParentKey: "SPIKE-1",
		Title: "Audit the landing page", Status: items.Ready}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	w, _, err := s.Spawn(ctx, SpawnInput{ItemKey: task.Key, Role: RoleDesigner, Kind: Fake,
		Model: "fake-1", ParentAgentID: ses.AgentID, Brief: BriefInput{Objective: "audit"}})
	if err != nil {
		t.Fatal(err)
	}
	wSes, err := s.LatestSession(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Accepted, Summary: "on it"}); err != nil {
		t.Fatal(err)
	}

	res, err := s.Materialize(ctx, ses.ID, "SPIKE-1", specID, planID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if spike, _ := s.Items.Get(ctx, "SPIKE-1"); spike.Status == items.Done {
		t.Fatal("the spike closed while its task is in progress")
	}
	if n := daemonCompleted(t, s, w.ID); n != 0 {
		t.Fatalf("the task's live agent got %d daemon completed checkpoints", n)
	}
	if !slices.Equal(res.OpenTasks, []string{task.Key}) {
		t.Fatalf("open tasks = %v, want [%s]", res.OpenTasks, task.Key)
	}
	if got, _ := s.Items.Get(ctx, task.Key); got.Status != items.InProgress {
		t.Fatalf("task status = %s", got.Status)
	}

	// The task finishing closes the spike.
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = 'done' WHERE id = ?`, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Items.Reconcile(ctx, task.Key); err != nil {
		t.Fatal(err)
	}
	if spike, _ := s.Items.Get(ctx, "SPIKE-1"); spike.Status != items.Done {
		t.Fatalf("spike status after its task finished = %s", spike.Status)
	}
}
