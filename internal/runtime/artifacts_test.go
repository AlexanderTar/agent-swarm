package runtime

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestSplitSections(t *testing.T) {
	md := "# Title\n\nintro\n\n## Data model\n\nrows\n\n```go\n## not a heading\n```\n\n## Data model\n\nagain\n"
	got := SplitSections(md)
	if len(got) != 2 {
		t.Fatalf("got %d sections: %+v", len(got), got)
	}
	if got[0].ID != "data-model" || got[1].ID != "data-model-2" {
		t.Fatalf("ids = %q, %q", got[0].ID, got[1].ID)
	}
	if got[0].Title != "Data model" || got[0].SHA256 == got[1].SHA256 {
		t.Fatalf("sections = %+v", got)
	}
	one := SplitSections("just a note with no headings\n")
	if len(one) != 1 || one[0].ID != "document" {
		t.Fatalf("headingless file = %+v", one)
	}
}

func TestRegisterAndReviseAnArtifact(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Spec", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	p := writeFile(t, "# Spec\n\n## Context\n\nwhy\n\n## Data model\n\nrows\n")
	res, err := s.RegisterArtifact(ctx, ses.ID, "register", "SPIKE-1", "spec", p)
	if err != nil {
		t.Fatal(err)
	}
	if res.Revision != 1 || len(res.Sections) != 2 {
		t.Fatalf("result = %+v", res)
	}
	os.WriteFile(p, []byte("# Spec\n\n## Context\n\nwhy\n\n## Data model\n\nrows and columns\n"), 0o644)
	next, err := s.RegisterArtifact(ctx, ses.ID, "revise", "SPIKE-1", "spec", p)
	if err != nil {
		t.Fatal(err)
	}
	if next.Revision != 2 || next.ArtifactID != res.ArtifactID {
		t.Fatalf("revise = %+v", next)
	}
	if next.Sections[0].SHA256 != res.Sections[0].SHA256 {
		t.Error("an untouched section keeps its hash")
	}
	if next.Sections[1].SHA256 == res.Sections[1].SHA256 {
		t.Error("the edited section must change hash")
	}
	// the old revision is still readable (C2)
	_, md, err := s.ArtifactMarkdown(ctx, res.ArtifactID, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(md, "rows and columns") {
		t.Fatal("revision 1 must serve the snapshot, not the file on disk")
	}
}

func TestReviseStalesOnlyTheChangedSectionsApprovals(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Stale", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	p := writeFile(t, "# Spec\n\n## One\n\na\n\n## Two\n\nb\n")
	res, _ := s.RegisterArtifact(ctx, ses.ID, "register", "SPIKE-1", "spec", p)
	var reqs []string
	for _, sec := range res.Sections {
		r, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", Prompt: "Review " + sec.Title,
			ArtifactID: res.ArtifactID, SectionID: sec.ID})
		if err != nil {
			t.Fatal(err)
		}
		reqs = append(reqs, r.ID)
	}
	os.WriteFile(p, []byte("# Spec\n\n## One\n\na\n\n## Two\n\nb and c\n"), 0o644)
	next, err := s.RegisterArtifact(ctx, ses.ID, "revise", "SPIKE-1", "spec", p)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.StaleRequests) != 1 || next.StaleRequests[0] != reqs[1] {
		t.Fatalf("stale = %v, want only %s", next.StaleRequests, reqs[1])
	}
	first, _ := s.RequestByID(ctx, reqs[0])
	second, _ := s.RequestByID(ctx, reqs[1])
	if first.State != "open" || second.State != "stale" {
		t.Fatalf("states = %s, %s", first.State, second.State)
	}
}

// I10: a plan needs exactly one valid swarm-tree block.
func TestPlanRegistrationParsesTheTree(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Tree", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	res, err := s.RegisterArtifact(ctx, ses.ID, "register", "SPIKE-1", "plan", writeFile(t, planBody))
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	s.DB.QueryRowContext(ctx, `SELECT tree_json FROM artifact_revisions
		WHERE artifact_id = ? AND revision = ?`, res.ArtifactID, res.Revision).Scan(&stored)
	tree, err := ParseTree(planBody)
	if err != nil {
		t.Fatal(err)
	}
	if tree.Root.Type != "epic" || len(tree.Children) != 1 || len(tree.Children[0].Children) != 2 {
		t.Fatalf("tree = %+v", tree)
	}
	if len(tree.Deps) != 1 || tree.Deps[0].Item != "t2" || tree.Deps[0].BlockedBy != "t1" {
		t.Fatalf("deps = %+v", tree.Deps)
	}
	if stored == "" || stored == "null" {
		t.Fatal("tree_json must be stored on the revision")
	}
}

func TestPlanWithoutATreeIsRefused(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "NoTree", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	_, err := s.RegisterArtifact(ctx, ses.ID, "register", "SPIKE-1", "plan",
		writeFile(t, "# Plan\n\n## Work breakdown\n\nsome prose\n"))
	want := "tree_invalid: the plan needs one ```swarm-tree block under \"## Work breakdown\"."
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func TestPlanWithTwoTreesOrABadTreeIsRefused(t *testing.T) {
	two := planBody + "\n```swarm-tree\n{}\n```\n"
	if _, err := ParseTree(two); err == nil || !strings.HasPrefix(err.Error(), "tree_invalid:") {
		t.Fatalf("two blocks: err = %v", err)
	}
	bad := "## Work breakdown\n\n```swarm-tree\n{not json}\n```\n"
	if _, err := ParseTree(bad); err == nil || !strings.HasPrefix(err.Error(), "tree_invalid:") {
		t.Fatalf("bad json: err = %v", err)
	}
	wrongParent := "## Work breakdown\n\n```swarm-tree\n" +
		`{"root":{"type":"bug","title":"b"},"children":[{"ref":"s1","type":"story","title":"s"}]}` + "\n```\n"
	if _, err := ParseTree(wrongParent); err == nil || !strings.Contains(err.Error(), "story") {
		t.Fatalf("a story under a bug must be refused (L4): %v", err)
	}
}

func TestReviseRefusesAMissingFile(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Gone", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	p := writeFile(t, "# Spec\n\n## One\n\na\n")
	res, _ := s.RegisterArtifact(ctx, ses.ID, "register", "SPIKE-1", "spec", p)
	os.Remove(p)
	if _, err := s.RegisterArtifact(ctx, ses.ID, "revise", "SPIKE-1", "spec", p); err == nil {
		t.Fatal("a deleted file must be refused on revise")
	}
	if _, _, err := s.ArtifactMarkdown(ctx, res.ArtifactID, 1, "one"); err != nil {
		t.Fatalf("the snapshot is still readable: %v", err)
	}
}

func TestArtifactMarkdownServesOneSectionOrTheWholeFile(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Read", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	res, _ := s.RegisterArtifact(ctx, ses.ID, "register", "SPIKE-1", "spec",
		writeFile(t, "# Spec\n\n## One\n\nalpha\n\n## Two\n\nbeta\n"))
	_, whole, err := s.ArtifactMarkdown(ctx, res.ArtifactID, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(whole, "alpha") || !strings.Contains(whole, "beta") {
		t.Fatalf("whole = %q", whole)
	}
	art, one, err := s.ArtifactMarkdown(ctx, res.ArtifactID, 0, "two")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(one, "alpha") || !strings.Contains(one, "beta") {
		t.Fatalf("section = %q", one)
	}
	if art.Revision != art.HeadRevision {
		t.Fatalf("revision 0 means head: %+v", art)
	}
}

// Only the item's own orchestrator may register on it.
func TestRegisterIsOrchestratorOnlyAndScopedToTheRoot(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)
	if _, err := s.RegisterArtifact(ctx, wSes.ID, "register", "TASK-1", "note",
		writeFile(t, "# note\n")); err == nil {
		t.Fatal("a coder cannot register an artifact")
	}
	_, other, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Other", Intent: "feature", Kind: Fake, Model: "fake-1"})
	oSes, _ := s.LatestSession(ctx, other.ID)
	if _, err := s.RegisterArtifact(ctx, oSes.ID, "register", "TASK-1", "note",
		writeFile(t, "# note\n")); err == nil {
		t.Fatal("an orchestrator cannot register on another root's item")
	}
}

func TestOneMegabyteCap(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Big", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	if _, err := s.RegisterArtifact(ctx, ses.ID, "register", "SPIKE-1", "spec",
		writeFile(t, "## One\n\n"+strings.Repeat("x", 1<<20))); err == nil {
		t.Fatal("a file over 1 MB must be refused")
	}
}
