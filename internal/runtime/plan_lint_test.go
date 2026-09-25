package runtime

import (
	"context"
	"encoding/json"
	"github.com/AlexanderTar/agent-swarm/internal/workflow"
	"os"
	"strings"
	"testing"
)

func lintFixture() Tree {
	return Tree{Root: TreeNode{Type: "epic", Title: "Epic"}, Children: []TreeNode{{Ref: "s", Type: "story", Title: "Story", Children: []TreeNode{{Ref: "t", Type: "task", Title: "Implement upload", Workflow: &workflow.Spec{Template: "tdd-reviewed"}, Steps: []string{"Write failing test", "Implement upload"}, Verify: []string{"go test ./..."}, Solo: "one focused change"}}}}}
}
func lintErrors(t *testing.T, tree Tree, want string) {
	t.Helper()
	errs, _ := lintTree(tree)
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), want) {
		t.Fatalf("errors=%v, want %q", errs, want)
	}
}
func TestTreeRequiresWorkflow(t *testing.T) {
	tree := lintFixture()
	tree.Children[0].Children[0].Workflow = nil
	lintErrors(t, tree, "Task t has no workflow. Plans assign every role: pick a template or write steps.")
}
func TestTreeRejectsRoleHintMismatch(t *testing.T) {
	tree := lintFixture()
	tree.Children[0].Children[0].RoleHint = "debugger"
	lintErrors(t, tree, "Task t role_hint debugger doesn't match its workflow (coder).")
}
func TestTreeRejectsReviewTasks(t *testing.T) {
	tree := lintFixture()
	tree.Children[0].Children[0].Title = "Review upload"
	lintErrors(t, tree, "Task t is a review task.")
}
func TestTreeRequiresStepsAndVerifyForTDDTasks(t *testing.T) {
	tree := lintFixture()
	tree.Children[0].Children[0].Steps = nil
	tree.Children[0].Children[0].Verify = nil
	lintErrors(t, tree, "Task t needs steps and verify commands (its workflow has a tdd gate).")
}
func TestTreeWarnsOnSplitTDDTitles(t *testing.T) {
	for _, title := range []string{"Write failing test for upload", "Add a failing test", "RED upload", "GREEN upload", "Make upload pass", "Fix review findings", "Address review notes", "Review upload"} {
		tree := lintFixture()
		tree.Children[0].Children[0].Title = title
		_, w := lintTree(tree)
		if len(w) == 0 || !strings.Contains(strings.Join(w, " "), "Looks like a TDD phase") {
			t.Errorf("%q warnings=%v", title, w)
		}
	}
	tree := lintFixture()
	tree.Children[0].Children[0].Title = "Add retry to failing uploads"
	_, w := lintTree(tree)
	if strings.Contains(strings.Join(w, " "), "Looks like a TDD phase") {
		t.Fatalf("false positive: %v", w)
	}
}
func TestTreeWarnsOnUnbatchedTasks(t *testing.T) {
	tree := lintFixture()
	tree.Children[0].Children[0].Solo = ""
	_, w := lintTree(tree)
	if !strings.Contains(strings.Join(w, " "), "Task t is a single unit. Batch it with related units (3–5 per task) or say why it stands alone in `solo`.") {
		t.Fatalf("warnings=%v", w)
	}
}
func TestTreeRejectsBothStepsAndUnits(t *testing.T) {
	tree := lintFixture()
	tree.Children[0].Children[0].Units = []TreeUnit{{Title: "one", Steps: []string{"test"}}}
	lintErrors(t, tree, "Task t has both steps and units; use one.")
}
func TestTreeRejectsMoreThanEightUnits(t *testing.T) {
	tree := lintFixture()
	tree.Children[0].Children[0].Steps = nil
	for i := 0; i < 9; i++ {
		tree.Children[0].Children[0].Units = append(tree.Children[0].Children[0].Units, TreeUnit{Title: "unit", Steps: []string{"test"}})
	}
	lintErrors(t, tree, "Task t has 9 units (max 8).")
}
func TestTreeRejectsReviewerRoleOnStory(t *testing.T) {
	tree := lintFixture()
	tree.Children[0].Workflow = &workflow.Spec{AfterTasks: &workflow.Step{ID: "review", Review: []string{"coder"}}}
	lintErrors(t, tree, "can't review")
}
func TestTreeWarnsOnNoTestStep(t *testing.T) {
	tree := lintFixture()
	tree.Children[0].Children[0].Steps = []string{"Implement upload"}
	_, w := lintTree(tree)
	if !strings.Contains(strings.Join(w, " "), "test") {
		t.Fatalf("warnings=%v", w)
	}
}
func TestTreeWarnsOnMoreThanFiveUnits(t *testing.T) {
	tree := lintFixture()
	tree.Children[0].Children[0].Steps = nil
	for i := 0; i < 6; i++ {
		tree.Children[0].Children[0].Units = append(tree.Children[0].Children[0].Units, TreeUnit{Title: "unit", Steps: []string{"Write test"}})
	}
	_, w := lintTree(tree)
	if !strings.Contains(strings.Join(w, " "), "Task t has 6 units; split above 5 unless they're same-shape edits.") {
		t.Fatalf("warnings=%v", w)
	}
}
func TestTreeWarnsOnAllSingleUnitStory(t *testing.T) {
	tree := lintFixture()
	for i := 0; i < 2; i++ {
		n := tree.Children[0].Children[0]
		n.Ref = string(rune('u' + i))
		tree.Children[0].Children = append(tree.Children[0].Children, n)
	}
	_, w := lintTree(tree)
	if !strings.Contains(strings.Join(w, " "), "Story s has 3 single-unit tasks; they look batchable.") {
		t.Fatalf("warnings=%v", w)
	}
}

func TestTreeCapitalizesNodeTypeInWorkflowError(t *testing.T) {
	tree := lintFixture()
	tree.Children[0].Workflow = &workflow.Spec{AfterTasks: &workflow.Step{ID: "review", Review: []string{"coder"}}}
	errs, _ := lintTree(tree)
	if len(errs) == 0 || !strings.HasPrefix(errs[0].Error(), "Story s workflow:") {
		t.Fatalf("errors=%v, want prefix %q", errs, "Story s workflow:")
	}
}

func TestTreeIgnoresRootInlineChildren(t *testing.T) {
	tree := lintFixture()
	tree.Root.Children = []TreeNode{{Ref: "phantom", Type: "task", Title: "Phantom"}}
	// A real violation in tree.Children must still be reported: without it
	// this test would pass vacuously if traversal broke entirely.
	tree.Children[0].Children[0].Workflow = nil
	errs, _ := lintTree(tree)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "Task t has no workflow") {
		t.Fatalf("errors=%v, want exactly the real Task t violation", errs)
	}
}

func TestPlanWarningsPersistPerRevision(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Warnings", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	tree := lintFixture()
	tree.Children[0].Children[0].Solo = ""
	body := planMarkdown(t, tree)
	path := writeFile(t, body)
	first, err := s.RegisterArtifact(ctx, ses.ID, "register", "SPIKE-1", "plan", path, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Warnings) == 0 {
		t.Fatal("registration must return warnings")
	}
	art, _, err := s.ArtifactMarkdown(ctx, first.ArtifactID, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(art.Warnings) == 0 {
		t.Fatal("revision 1 warnings missing")
	}
	tree.Children[0].Children[0].Solo = "isolated"
	if err := os.WriteFile(path, []byte(planMarkdown(t, tree)), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := s.RegisterArtifact(ctx, ses.ID, "revise", "SPIKE-1", "plan", path, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Warnings) != 0 {
		t.Fatalf("revision 2 warnings=%v", second.Warnings)
	}
	old, _, _ := s.ArtifactMarkdown(ctx, first.ArtifactID, 1, "")
	head, _, _ := s.ArtifactMarkdown(ctx, first.ArtifactID, 2, "")
	if len(old.Warnings) == 0 || len(head.Warnings) != 0 {
		t.Fatalf("old=%v head=%v", old.Warnings, head.Warnings)
	}
}
func planMarkdown(t *testing.T, tree Tree) string {
	t.Helper()
	b, err := json.Marshal(tree)
	if err != nil {
		t.Fatal(err)
	}
	return "## Work breakdown\n```swarm-tree\n" + string(b) + "\n```\n"
}

func TestTreeWarnsWhenOneUnitLacksTestStep(t *testing.T) {
	tree := lintFixture()
	task := &tree.Children[0].Children[0]
	task.Steps = nil
	task.Units = []TreeUnit{{Title: "first", Steps: []string{"Write test"}}, {Title: "second", Steps: []string{"Implement feature"}}}
	_, w := lintTree(tree)
	if !strings.Contains(strings.Join(w, " "), "unit 2") {
		t.Fatalf("warnings=%v", w)
	}
}
