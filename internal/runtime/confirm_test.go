package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

func TestConfirmReposStoresTheSetAndBumpsTheVersion(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	repoA, repoB := seedRepo(t, s, "chat"), seedRepo(t, s, "app")
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Confirm", Intent: "feature",
		Kind: Fake, Model: "fake-1", Repos: []string{repoA}})
	ses, _ := s.LatestSession(ctx, a.ID)
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "confirm_repos",
		Prompt:    "The change needs the client and the shared schema.",
		Repos:     []ReposProposal{{Repo: repoA, Reason: "the login form lives here", Source: "user"}},
		Expansion: []ReposProposal{{Repo: repoB, Reason: "the session schema is shared"}}})
	if err != nil {
		t.Fatal(err)
	}
	var opts struct {
		Proposed  []ReposProposal `json:"proposed"`
		Expansion []ReposProposal `json:"expansion"`
	}
	json.Unmarshal(req.Options, &opts)
	if len(opts.Proposed) != 1 || len(opts.Expansion) != 1 || opts.Proposed[0].Source != "user" {
		t.Fatalf("options = %s", req.Options)
	}
	out, err := s.ConfirmRepos(ctx, req.ID, []string{repoA, repoB}, "both, please", 0, "menubar", "")
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "approved" || len(out.Confirmed) != 2 {
		t.Fatalf("request = %+v", out)
	}
	it, _ := s.Items.Get(ctx, "SPIKE-1")
	if len(it.Repos) != 2 || it.ReposVersion != 1 {
		t.Fatalf("item repos = %v, version = %d", it.Repos, it.ReposVersion)
	}
	var kind, origin, payload string
	s.DB.QueryRowContext(ctx, `SELECT kind, origin, payload_json FROM messages WHERE request_id = ?`, req.ID).
		Scan(&kind, &origin, &payload)
	if kind != "repos_confirmed" || origin != "user_action" {
		t.Fatalf("message = %s / %s", kind, origin)
	}
	if !strings.Contains(payload, `"path"`) || !strings.Contains(payload, `"name"`) {
		t.Fatalf("the payload lists id, name and path: %s", payload)
	}
}

// The orchestrator may omit expansion entirely; the stored options must
// never carry a JSON null there, since the web board maps over it directly.
func TestConfirmReposStoresEmptyExpansionAsAnEmptyArrayNotNull(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	repoA := seedRepo(t, s, "chat")
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "NoExpansion", Intent: "feature",
		Kind: Fake, Model: "fake-1", Repos: []string{repoA}})
	ses, _ := s.LatestSession(ctx, a.ID)
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "confirm_repos",
		Prompt:    "just chat",
		Repos:     []ReposProposal{{Repo: repoA, Reason: "the login form lives here"}},
		Expansion: nil})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(req.Options), `"expansion":null`) {
		t.Fatalf("options must not carry a null expansion: %s", req.Options)
	}
	if !strings.Contains(string(req.Options), `"expansion":[]`) {
		t.Fatalf("options must carry an empty expansion array: %s", req.Options)
	}
}

// Required fix 1: ValidateItemRepos is the item-level (request-free) half of
// a repo confirmation, used by the orchestrator-spawn route so it can resolve
// paths for Preflight and refuse a bad pick BEFORE anything spawns.
func TestValidateItemReposResolvesPathsWithoutWriting(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	repoA := seedRepo(t, s, "chat")
	it, err := s.Items.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Root"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	paths, err := s.ValidateItemRepos(ctx, it.Key, []string{repoA}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] == "" {
		t.Fatalf("paths = %v", paths)
	}
	// nothing written: repos_version is still 0.
	again, err := s.Items.Get(ctx, it.Key)
	if err != nil {
		t.Fatal(err)
	}
	if again.ReposVersion != 0 || len(again.Repos) != 0 {
		t.Fatalf("item = %+v, want no write", again)
	}
}

func TestValidateItemReposRefusesAStaleVersionOrAnUnknownRepo(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	repoA := seedRepo(t, s, "chat")
	it, err := s.Items.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Root"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ValidateItemRepos(ctx, it.Key, []string{repoA}, 7); err == nil {
		t.Fatal("want a conflict on a stale version")
	}
	if _, err := s.ValidateItemRepos(ctx, it.Key, []string{"repo_does_not_exist"}, 0); err == nil {
		t.Fatal("want a bad_request on an unknown repo id")
	}
}

func TestAskConfirmReposErrorNamesTheFix(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Ask me", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	_, err := s.Ask(ctx, ses.ID, AskInput{Kind: "confirm_repos", Prompt: "p",
		Repos: []ReposProposal{{Repo: "endurio-chat", Reason: "r"}}})
	want := `Unknown repository "endurio-chat". Pass a repository id from swarm_read {repos:{q:"endurio-chat"}}.`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// Required fix 1: CommitItemRepos is the write half, called only once the
// spawn that will use these repos has actually succeeded.
func TestCommitItemReposWritesTheConfirmedSetAndReconciles(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	repoA := seedRepo(t, s, "chat")
	it, err := s.Items.Create(ctx, items.CreateInput{Type: items.Epic, Title: "Root"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitItemRepos(ctx, it.Key, []string{repoA}); err != nil {
		t.Fatal(err)
	}
	again, err := s.Items.Get(ctx, it.Key)
	if err != nil {
		t.Fatal(err)
	}
	if again.ReposVersion != 1 || len(again.Repos) != 1 || again.Repos[0] != repoA {
		t.Fatalf("item = %+v", again)
	}
}

func TestConfirmReposRefusesAnUnknownOrAlreadyResolvedRequest(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	repoA := seedRepo(t, s, "chat")
	if _, err := s.ConfirmRepos(ctx, "req_nope", []string{repoA}, "", 0, "board", ""); err == nil {
		t.Fatal("an unknown request must be refused")
	}
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Twice", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	req, _ := s.Ask(ctx, ses.ID, AskInput{Kind: "confirm_repos", Prompt: "one",
		Repos: []ReposProposal{{Repo: repoA, Reason: "a"}}})
	if _, err := s.ConfirmRepos(ctx, req.ID, []string{repoA}, "", 0, "board", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmRepos(ctx, req.ID, []string{repoA}, "", 1, "board", ""); err == nil ||
		err.Error() != "Already resolved." {
		t.Fatalf("err = %v", err)
	}
}

func TestConfirmReposRefusesAnEmptySetAndAStaleVersion(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	repoA := seedRepo(t, s, "chat")
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Stale", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	req, _ := s.Ask(ctx, ses.ID, AskInput{Kind: "confirm_repos", Prompt: "which repos?",
		Repos: []ReposProposal{{Repo: repoA, Reason: "here"}}})
	if _, err := s.ConfirmRepos(ctx, req.ID, nil, "", 0, "board", ""); err == nil ||
		err.Error() != "Choose at least one repository." {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.ConfirmRepos(ctx, req.ID, []string{repoA}, "", 7, "board", ""); err == nil ||
		!strings.Contains(err.Error(), "This request changed. Review the latest version.") {
		t.Fatalf("a stale repos_version must conflict: %v", err)
	}
	if _, err := s.ConfirmRepos(ctx, req.ID, []string{"repo_nope"}, "", 0, "board", ""); err == nil {
		t.Fatal("an unknown repo id must be refused")
	}
}

// I13: a repo with unreleased reservations cannot be dropped.
func TestConfirmReposRefusesRemovingARepoInUse(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	repoA, repoB := seedRepo(t, s, "chat"), seedRepo(t, s, "app")
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "InUse", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	first, _ := s.Ask(ctx, ses.ID, AskInput{Kind: "confirm_repos", Prompt: "both",
		Repos: []ReposProposal{{Repo: repoA, Reason: "a"}, {Repo: repoB, Reason: "b"}}})
	if _, err := s.ConfirmRepos(ctx, first.ID, []string{repoA, repoB}, "", 0, "board", ""); err != nil {
		t.Fatal(err)
	}
	sp, err := s.Items.Get(ctx, "SPIKE-1")
	if err != nil {
		t.Fatal(err)
	}
	seedWorktreeReservation(t, s, repoA, a.ID, sp.ID)
	second, _ := s.Ask(ctx, ses.ID, AskInput{Kind: "confirm_repos", Prompt: "just one now",
		Repos: []ReposProposal{{Repo: repoB, Reason: "b"}}})
	_, err = s.ConfirmRepos(ctx, second.ID, []string{repoB}, "", 1, "board", "")
	if err == nil || err.Error() != "chat has active worktrees. Finish or release them first." {
		t.Fatalf("err = %v", err)
	}
}

// The old set applies until the new request is confirmed.
func TestAskingAgainDoesNotChangeTheConfirmedSetYet(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	repoA, repoB := seedRepo(t, s, "chat"), seedRepo(t, s, "app")
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Later", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	first, _ := s.Ask(ctx, ses.ID, AskInput{Kind: "confirm_repos", Prompt: "one",
		Repos: []ReposProposal{{Repo: repoA, Reason: "a"}}})
	s.ConfirmRepos(ctx, first.ID, []string{repoA}, "", 0, "board", "")
	s.Ask(ctx, ses.ID, AskInput{Kind: "confirm_repos", Prompt: "and the other",
		Repos: []ReposProposal{{Repo: repoA, Reason: "a"}, {Repo: repoB, Reason: "b"}}})
	it, _ := s.Items.Get(ctx, "SPIKE-1")
	if len(it.Repos) != 1 || it.Repos[0] != repoA {
		t.Fatalf("the confirmed set changed before confirmation: %v", it.Repos)
	}
}

// L25: only an orchestrator may ask.
func TestOnlyAnOrchestratorMayAskToConfirmRepos(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	repoA := seedRepo(t, s, "chat")
	_, _, wSes := worker(t, s)
	if _, err := s.Ask(ctx, wSes.ID, AskInput{Kind: "confirm_repos", Prompt: "which?",
		Repos: []ReposProposal{{Repo: repoA, Reason: "a"}}}); err == nil {
		t.Fatal("a coder cannot ask to confirm repos")
	}
}

func TestConfirmReposNeedsAReasonPerProposal(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	repoA, repoB := seedRepo(t, s, "chat"), seedRepo(t, s, "app")
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Reason", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	if _, err := s.Ask(ctx, ses.ID, AskInput{Kind: "confirm_repos", Prompt: "which?",
		Repos: []ReposProposal{{Repo: repoA}}}); err == nil {
		t.Fatal("every proposed repo needs a one-line reason")
	}
	if _, err := s.Ask(ctx, ses.ID, AskInput{Kind: "confirm_repos", Prompt: "which?",
		Repos: []ReposProposal{{Repo: repoA, Reason: "a"}}, Expansion: []ReposProposal{{Repo: repoB}}}); err == nil {
		t.Fatal("every suggested repo needs a one-line reason")
	}
	if _, err := s.Ask(ctx, ses.ID, AskInput{Kind: "confirm_repos", Prompt: "which?"}); err == nil {
		t.Fatal("at least one proposal is required")
	}
}

// §17.5: the notification says how many were proposed and how many suggested.
func TestConfirmReposNotificationBody(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	repoA, repoB := seedRepo(t, s, "chat"), seedRepo(t, s, "app")
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Notify", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	s.Ask(ctx, ses.ID, AskInput{Kind: "confirm_repos", Prompt: "p",
		Repos:     []ReposProposal{{Repo: repoA, Reason: "a"}},
		Expansion: []ReposProposal{{Repo: repoB, Reason: "b"}}})
	// §17.5's body is "{KEY}: {name} proposes {N} repositories{expansion}." and the
	// optional clause is rendered by the caller (Task 23's note), so runtime's job
	// is the two arguments. Task 23 pins the rendered sentence.
	n := notified(t, s, "request.confirm_repos")
	if n.ItemKey != "SPIKE-1" || n.AgentName != a.Name {
		t.Fatalf("notification = %+v", n)
	}
	if n.Args["N"] != "1" {
		t.Fatalf("N = %q, want 1", n.Args["N"])
	}
	if n.Args["expansion"] != ", and suggests adding 1" {
		t.Fatalf("expansion = %q", n.Args["expansion"])
	}
}

// I4: approving close_spike finishes the spike with no new item.
func TestCloseSpikeFinishesTheSpike(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Nothing", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: Accepted, Summary: "starting"})
	s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp,
		Summary: "nothing to build", Resolution: "no_change"})
	var reqID string
	s.DB.QueryRowContext(ctx, `SELECT id FROM requests WHERE kind = 'close_spike'`).Scan(&reqID)
	if _, err := s.CloseSpike(ctx, reqID, "menubar"); err != nil {
		t.Fatal(err)
	}
	it, _ := s.Items.Get(ctx, "SPIKE-1")
	if it.Status != items.Done {
		t.Fatalf("status = %s", it.Status)
	}
	var created int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM items WHERE origin_spike_id IS NOT NULL`).Scan(&created)
	if created != 0 {
		t.Fatalf("%d items were materialized; close_spike creates none", created)
	}
}

// A change request on close_spike sends the spike back to work.
func TestRequestChangesOnCloseSpikeReopensIt(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Retry", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: Accepted, Summary: "starting"})
	s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: CompletedCkp, Summary: "nothing", Resolution: "no_change"})
	var reqID string
	s.DB.QueryRowContext(ctx, `SELECT id FROM requests WHERE kind = 'close_spike'`).Scan(&reqID)
	if _, err := s.RequestChanges(ctx, reqID, "Look at the session cookie path first.", "board"); err != nil {
		t.Fatal(err)
	}
	it, _ := s.Items.Get(ctx, "SPIKE-1")
	if it.Status != items.InProgress {
		t.Fatalf("status = %s", it.Status)
	}
}

func TestConfirmedReposReturnsTheConfirmedSet(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	repoA := seedRepo(t, s, "chat")
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Get", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "confirm_repos", Prompt: "one",
		Repos: []ReposProposal{{Repo: repoA, Reason: "a"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmRepos(ctx, req.ID, []string{repoA}, "", 0, "board", ""); err != nil {
		t.Fatal(err)
	}
	list, err := s.ConfirmedRepos(ctx, a.RootItemID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != repoA || list[0].Name != "chat" {
		t.Fatalf("confirmed = %+v", list)
	}
}

// D42: a proposal that silently omits a repo the user picked is refused, even
// though the prompt is present and valid.
func TestAskConfirmReposRefusesASilentDrop(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	repoA, repoB := seedRepo(t, s, "chat"), seedRepo(t, s, "app")
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Drop", Intent: "feature",
		Kind: Fake, Model: "fake-1", Repos: []string{repoA, repoB}})
	if err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	it, err := s.Items.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(it.SuggestedRepos) != 2 {
		t.Fatalf("StartSpike must record what the user picked: %v", it.SuggestedRepos)
	}
	_, err = s.Ask(ctx, ses.ID, AskInput{Kind: "confirm_repos",
		Prompt: "Only chat is needed for this change.",
		Repos:  []ReposProposal{{Repo: repoA, Reason: "the API lives here"}}})
	if err == nil {
		t.Fatal("omitting a repo the user picked must be refused")
	}
	if want := "Say why app was dropped, or include it."; err.Error() != want {
		t.Fatalf("Ask = %q, want %q", err, want)
	}
	// With source "dropped" plus its own reason it is accepted.
	if _, err := s.Ask(ctx, ses.ID, AskInput{Kind: "confirm_repos",
		Prompt: "Only chat is needed for this change.",
		Repos: []ReposProposal{
			{Repo: repoA, Reason: "the API lives here"},
			{Repo: repoB, Reason: "no UI change in this epic", Source: "dropped"},
		}}); err != nil {
		t.Fatal(err)
	}
}
