package runtime

import (
	"context"
	"strings"
	"testing"
)

func TestRefTokenAndRefFromPrompt(t *testing.T) {
	q := "Approve the plan (rev 1)?" + refToken("req_ABC123")
	if !strings.HasSuffix(q, " ⟦swarm:req_ABC123⟧") {
		t.Fatalf("refToken suffix = %q", q)
	}
	if got := refFromPrompt(q); got != "req_ABC123" {
		t.Fatalf("refFromPrompt(%q) = %q", q, got)
	}
	if got := refFromPrompt("plain text"); got != "" {
		t.Fatalf("refFromPrompt(plain) = %q, want empty", got)
	}
}

// TestNativePromptForBuildsExactCopy is Task 13a: the daemon-issued native
// prompt for each approval kind matches spec section 6, verbatim, and ends
// with the ref token.
func TestNativePromptForBuildsExactCopy(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Copy test", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	it, err := s.Items.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	repoA := seedRepo(t, s, "endurio-chat")
	_ = a
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	tests := []struct {
		name       string
		req        Request
		section    string
		warnings   []string
		wantHeader string
		wantQ      string
		wantOpts   []string
	}{
		{
			name:       "approve_section",
			req:        Request{ID: "req_SEC1", Kind: "approve_section", ArtifactRevision: 2, ItemID: it.ID},
			section:    "Data model",
			wantHeader: "Spike approval",
			wantQ:      `Approve Spec section "Data model" (rev 2)?` + refToken("req_SEC1"),
			wantOpts:   []string{"Approve", "Request changes"},
		},
		{
			name:       "approve_plan_no_warnings",
			req:        Request{ID: "req_PLAN1", Kind: "approve_plan", ArtifactRevision: 1, ItemID: it.ID},
			wantHeader: "Spike approval",
			wantQ:      `Approve the plan (rev 1)?` + refToken("req_PLAN1"),
			wantOpts:   []string{"Approve", "Request changes"},
		},
		{
			name:       "approve_plan_with_warnings",
			req:        Request{ID: "req_PLAN2", Kind: "approve_plan", ArtifactRevision: 3, ItemID: it.ID},
			warnings:   []string{"Task t2 has no verify command."},
			wantHeader: "Spike approval",
			wantQ:      "Approve the plan (rev 3)?\nWarnings:\n- Task t2 has no verify command." + refToken("req_PLAN2"),
			wantOpts:   []string{"Approve", "Request changes"},
		},
		{
			name:       "approve_report",
			req:        Request{ID: "req_REP1", Kind: "approve_report", ArtifactRevision: 1, ItemID: it.ID},
			wantHeader: "Spike approval",
			wantQ:      `Approve the debug report (rev 1)?` + refToken("req_REP1"),
			wantOpts:   []string{"Approve", "Request changes"},
		},
		{
			name: "confirm_repos",
			req: Request{ID: "req_REPO1", Kind: "confirm_repos", ItemID: it.ID,
				Options: []byte(`{"proposed":[{"repo":"` + repoA + `","reason":"r"}]}`)},
			wantHeader: "Repositories",
			wantQ:      "Confirm 1 repositories for " + key + ": endurio-chat?" + refToken("req_REPO1"),
			wantOpts:   []string{"Approve", "Request changes"},
		},
		{
			name:       "close_spike",
			req:        Request{ID: "req_CLOSE1", Kind: "close_spike", ItemID: it.ID},
			wantHeader: "Close spike",
			wantQ:      "Close " + key + "?" + refToken("req_CLOSE1"),
			wantOpts:   []string{"Approve", "Request changes"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			np, err := s.nativePromptFor(ctx, tx, tc.req, tc.section, tc.warnings)
			if err != nil {
				t.Fatal(err)
			}
			if np.Header != tc.wantHeader {
				t.Errorf("header = %q, want %q", np.Header, tc.wantHeader)
			}
			if np.Question != tc.wantQ {
				t.Errorf("question = %q, want %q", np.Question, tc.wantQ)
			}
			if strings.Join(np.Options, ",") != strings.Join(tc.wantOpts, ",") {
				t.Errorf("options = %v, want %v", np.Options, tc.wantOpts)
			}
			if refFromPrompt(np.Question) != tc.req.ID {
				t.Errorf("refFromPrompt(%q) = %q, want %q", np.Question, refFromPrompt(np.Question), tc.req.ID)
			}
		})
	}
}

func TestNativePromptForMsg(t *testing.T) {
	np := nativePromptForMsg("go-migration-agent", "may I drop table x?", "msg_ABC")
	if np.Header != "go-migration-agent asks" {
		t.Fatalf("header = %q", np.Header)
	}
	want := "may I drop table x?" + refToken("msg_ABC")
	if np.Question != want {
		t.Fatalf("question = %q, want %q", np.Question, want)
	}
	if len(np.Options) != 2 || np.Options[0] != "Approve" || np.Options[1] != "Request changes" {
		t.Fatalf("options = %v", np.Options)
	}
	if refFromPrompt(np.Question) != "msg_ABC" {
		t.Fatalf("refFromPrompt = %q", refFromPrompt(np.Question))
	}
}

func TestNativePromptQuestionTruncatesButKeepsTheToken(t *testing.T) {
	long := strings.Repeat("x", 1200)
	np := nativePromptForMsg("child", long, "msg_XYZ")
	if got := len([]rune(np.Question)); got > 1000 {
		t.Fatalf("question is %d runes, want <= 1000", got)
	}
	if !strings.HasSuffix(np.Question, refToken("msg_XYZ")) {
		t.Fatalf("token did not survive truncation: %q", np.Question[len(np.Question)-40:])
	}
	if refFromPrompt(np.Question) != "msg_XYZ" {
		t.Fatalf("refFromPrompt = %q", refFromPrompt(np.Question))
	}
}

// TestAskApprovalReturnsNativePrompt exercises the real Ask() path for
// approve_section: the returned Request carries the NativePrompt.
func TestAskApprovalReturnsNativePrompt(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Spec", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	p := writeFile(t, "# Spec\n\n## Data model\n\nrows\n")
	res, err := s.RegisterArtifact(ctx, ses.ID, "register", "SPIKE-1", "spec", p, "")
	if err != nil {
		t.Fatal(err)
	}
	sec := res.Sections[0]
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", Prompt: "Review " + sec.Title,
		ArtifactID: res.ArtifactID, SectionID: sec.ID})
	if err != nil {
		t.Fatal(err)
	}
	if req.NativePrompt == nil {
		t.Fatalf("NativePrompt is nil on %+v", req)
	}
	want := `Approve Spec section "Data model" (rev 1)?` + refToken(req.ID)
	if req.NativePrompt.Question != want {
		t.Fatalf("question = %q, want %q", req.NativePrompt.Question, want)
	}
	if req.NativePrompt.Header != "Spike approval" {
		t.Fatalf("header = %q", req.NativePrompt.Header)
	}
}

// TestAskConfirmReposReturnsNativePrompt exercises the real Ask() path for
// confirm_repos.
func TestAskConfirmReposReturnsNativePrompt(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	repoID := seedRepo(t, s, "endurio-chat")
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Confirm test", Intent: "feature",
		Kind: Fake, Model: "fake-1", Repos: []string{repoID}})
	if err != nil {
		t.Fatal(err)
	}
	ses, _ := s.LatestSession(ctx, a.ID)
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "confirm_repos", Prompt: "Confirm repos",
		Repos: []ReposProposal{{Repo: repoID, Reason: "needed"}}})
	if err != nil {
		t.Fatal(err)
	}
	if req.NativePrompt == nil {
		t.Fatalf("NativePrompt is nil on %+v", req)
	}
	if req.NativePrompt.Header != "Repositories" {
		t.Fatalf("header = %q", req.NativePrompt.Header)
	}
	want := "Confirm 1 repositories for " + key + ": endurio-chat?" + refToken(req.ID)
	if req.NativePrompt.Question != want {
		t.Fatalf("question = %q, want %q", req.NativePrompt.Question, want)
	}
}
