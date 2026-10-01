package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Tests for agent-proposed finish and approval options (CHORE-18, spec 2026-10-01).

var shipOptions = []FinishOption{
	{Label: "Squash-merge PR", Description: "Push, open PR, squash when green"},
	{Label: "Keep branch", Description: "I'll ship it myself"},
}

const changesDesc = "Say what to change; I'll re-integrate and ask again."

func TestFinishPromptUsesAgentOptions(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	key, _, _, err := s.StartSpike(ctx, SpikeInput{Name: "Bump deps", Intent: "chore", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	it, _ := s.Items.Get(ctx, key)
	var np NativePrompt
	opts, _ := json.Marshal(shipOptions)
	if err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		np, err = s.nativePromptFor(ctx, tx, Request{Kind: KindAcceptFix, ItemID: it.ID,
			Binding: []byte(`{"item_revision":1,"integrated_checkpoint":"ckp_x","git":[],"finish_options":` + string(opts) + `}`)}, "", nil, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"Squash-merge PR", "Keep branch", "Request changes"}; !reflect.DeepEqual(np.Options, want) {
		t.Fatalf("Options = %q, want %q", np.Options, want)
	}
	if want := []string{"Push, open PR, squash when green", "I'll ship it myself", changesDesc}; !reflect.DeepEqual(np.Descriptions, want) {
		t.Fatalf("Descriptions = %q, want %q", np.Descriptions, want)
	}
}

// acceptWithOptions opens an accept_fix request on a chore whose integrated checkpoint carried shipOptions,
// returning the orchestrator's session and the request.
func acceptWithOptions(t *testing.T) (*Store, string, Request) {
	t.Helper()
	s, _, _ := newStore(t)
	ctx := context.Background()
	key, orch, _, err := s.StartSpike(ctx, SpikeInput{Name: "Bump deps", Intent: "chore", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses := mustSessionID(t, s, orch.ID)
	if _, err := s.WriteCheckpoint(ctx, ses, CheckpointInput{Kind: Accepted, Summary: "bumping"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, ses, CheckpointInput{Kind: Integrated, Summary: "merged",
		Git:           []GitRef{{Repo: "proj", Branch: "swarm/chore-1", SHA: "3f9c2ab0000"}},
		Verification:  []Verify{{Cmd: "go test ./...", Phase: "green", OK: true}},
		FinishOptions: shipOptions}); err != nil {
		t.Fatal(err)
	}
	it, _ := s.Items.Get(ctx, key)
	var reqID string
	if err := s.DB.QueryRowContext(ctx, `SELECT id FROM requests WHERE item_id = ? AND kind = 'accept_fix' AND state = 'open'`,
		it.ID).Scan(&reqID); err != nil {
		t.Fatal(err)
	}
	if err := s.tx(ctx, func(tx *sql.Tx) error { return s.OnRequestOpened(ctx, tx, reqID) }); err != nil {
		t.Fatal(err)
	}
	passPrint(t, s, ses)
	req, err := s.RequestByID(ctx, reqID)
	if err != nil {
		t.Fatal(err)
	}
	return s, ses, req
}

func bindingValue(t *testing.T, s *Store, id, path string) string {
	t.Helper()
	var v sql.NullString
	if err := s.DB.QueryRow(`SELECT json_extract(binding_json, ?) FROM requests WHERE id = ?`, path, id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v.String
}

func TestNativeAnswerApproveWithChoiceRecordsCustomMerge(t *testing.T) {
	s, ses, req := acceptWithOptions(t)
	ctx := context.Background()
	np := decodeNP(t, func() map[string]any { p, _ := relayFor(t, s, req.AgentID, req.ID); return p }())
	if len(np.Options) != 3 || np.Options[0] != "Squash-merge PR" {
		t.Fatalf("native prompt = %+v", np)
	}
	hookSimulate(t, s, ses, np, "Keep branch")

	// invalid or missing choice is refused and names the labels
	for _, choice := range []string{"", "Nope"} {
		_, err := s.Ask(ctx, ses, AskInput{Kind: "native_answer", Ref: req.ID, Decision: "approve", Choice: choice})
		if err == nil || !strings.Contains(err.Error(), "Choose one of this request's options: Squash-merge PR, Keep branch.") {
			t.Fatalf("choice %q: err = %v", choice, err)
		}
	}
	// a legacy finish decision is not valid for an agent-option request
	if _, err := s.Ask(ctx, ses, AskInput{Kind: "native_answer", Ref: req.ID, Decision: "auto_merge"}); err == nil {
		t.Fatal("auto_merge accepted on an agent-option request")
	}
	out, err := s.Ask(ctx, ses, AskInput{Kind: "native_answer", Ref: req.ID, Decision: "approve", Choice: "Keep branch"})
	if err != nil || out.State != "approved" {
		t.Fatalf("native_answer = %+v, %v", out, err)
	}
	if got := bindingValue(t, s, req.ID, "$.merge"); got != "custom" {
		t.Fatalf("$.merge = %q", got)
	}
	if got := bindingValue(t, s, req.ID, "$.choice"); got != "Keep branch" {
		t.Fatalf("$.choice = %q", got)
	}
	var payload string
	if err := s.DB.QueryRow(`SELECT payload_json FROM messages WHERE kind = 'approval_result' AND request_id = ?`, req.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	json.Unmarshal([]byte(payload), &p)
	if p["merge"] != "custom" || p["choice"] != "Keep branch" || p["decision"] != "approved" {
		t.Fatalf("approval_result = %v", p)
	}
}

func TestSwarmAskApprovalOptionsStoredAndUsedInNativePrompt(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ses, _, planID, _ := approvedFeatureSpike(t, s)
	for name, opts := range map[string][]FinishOption{
		"five":     {{Label: "a"}, {Label: "b"}, {Label: "c"}, {Label: "d"}, {Label: "e"}},
		"reserved": {{Label: "Request changes"}},
	} {
		if _, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: planID, Prompt: "Ship it.", ApprovalOptions: opts}); err == nil {
			t.Fatalf("%s: options accepted", name)
		}
	}
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: planID, Prompt: "Ship it.", ApprovalOptions: shipOptions})
	if err != nil {
		t.Fatal(err)
	}
	var stored []FinishOption
	if err := json.Unmarshal(req.Options, &stored); err != nil || !reflect.DeepEqual(stored, shipOptions) {
		t.Fatalf("stored options = %s (%v)", req.Options, err)
	}
	var np NativePrompt
	if err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		np, err = s.nativePromptFor(ctx, tx, req, "", nil, req.ReviewPaths)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"Squash-merge PR", "Keep branch", "Request changes"}; !reflect.DeepEqual(np.Options, want) {
		t.Fatalf("Options = %q, want %q", np.Options, want)
	}
	if len(np.Descriptions) != 3 || np.Descriptions[0] != shipOptions[0].Description {
		t.Fatalf("Descriptions = %q", np.Descriptions)
	}

	// native_answer approve + choice records $.choice (and no merge: not a finish request)
	hookSimulate(t, s, ses.ID, np, "Keep branch")
	if _, err := s.Ask(ctx, ses.ID, AskInput{Kind: "native_answer", Ref: req.ID, Decision: "approve"}); err == nil ||
		!strings.Contains(err.Error(), "Choose one of this request's options") {
		t.Fatalf("missing choice: err = %v", err)
	}
	out, err := s.Ask(ctx, ses.ID, AskInput{Kind: "native_answer", Ref: req.ID, Decision: "approve", Choice: "Keep branch"})
	if err != nil || out.State != "approved" {
		t.Fatalf("native_answer = %+v, %v", out, err)
	}
	if got := bindingValue(t, s, req.ID, "$.choice"); got != "Keep branch" {
		t.Fatalf("$.choice = %q", got)
	}
	if got := bindingValue(t, s, req.ID, "$.merge"); got != "" {
		t.Fatalf("$.merge = %q on a non-finish request", got)
	}
}

func approvalResultPayload(t *testing.T, s *Store, reqID string) map[string]any {
	t.Helper()
	var payload string
	if err := s.DB.QueryRow(`SELECT payload_json FROM messages WHERE kind = 'approval_result' AND request_id = ?`, reqID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	json.Unmarshal([]byte(payload), &p)
	return p
}

func TestApproveWithChoiceOnFinishRecordsCustomMerge(t *testing.T) {
	s, _, req := acceptWithOptions(t)
	ctx := context.Background()
	in := ApproveInput{Binding: req.Binding, Via: "board", Merge: "custom", Comment: "ship it"}
	for name, choice := range map[string]string{"missing": "", "unknown": "Nope"} {
		in.Choice = choice
		if _, err := s.Approve(ctx, req.ID, in); err == nil ||
			!strings.Contains(err.Error(), "Choose one of this request's options: Squash-merge PR, Keep branch.") {
			t.Fatalf("%s choice: err = %v", name, err)
		}
	}
	in.Choice = "Keep branch"
	out, err := s.Approve(ctx, req.ID, in)
	if err != nil || out.State != "approved" {
		t.Fatalf("Approve = %+v, %v", out, err)
	}
	if bindingValue(t, s, req.ID, "$.merge") != "custom" || bindingValue(t, s, req.ID, "$.choice") != "Keep branch" {
		t.Fatalf("binding = %s", out.Binding)
	}
	if out.ResponseText != "ship it" {
		t.Fatalf("response_text = %q", out.ResponseText)
	}
	if p := approvalResultPayload(t, s, req.ID); p["merge"] != "custom" || p["choice"] != "Keep branch" {
		t.Fatalf("approval_result = %v", p)
	}
}

func TestApproveRefusesChoiceWithoutOptions(t *testing.T) {
	s, _, _ := newStore(t)
	req, _, _ := seedSectionApproval(t, s)
	if _, err := s.Approve(context.Background(), req.ID, ApproveInput{SectionSHA256: req.SectionSHA256, Via: "board", Choice: "x"}); err == nil ||
		!strings.Contains(err.Error(), "This request has no options to choose from.") {
		t.Fatalf("err = %v", err)
	}
}

func TestApproveWithChoiceOnApprovalKind(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ses, _, planID, _ := approvedFeatureSpike(t, s)
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: planID, Prompt: "Ship it.", ApprovalOptions: shipOptions})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(ctx, req.ID, ApproveInput{Via: "board"}); err == nil ||
		!strings.Contains(err.Error(), "Choose one of this request's options: Squash-merge PR, Keep branch.") {
		t.Fatalf("no choice: err = %v", err)
	}
	if _, err := s.Approve(ctx, req.ID, ApproveInput{Via: "board", Choice: "Squash-merge PR", Comment: "go"}); err != nil {
		t.Fatal(err)
	}
	if bindingValue(t, s, req.ID, "$.choice") != "Squash-merge PR" || bindingValue(t, s, req.ID, "$.merge") != "" {
		t.Fatal("approval kind must record $.choice and no merge")
	}
	if p := approvalResultPayload(t, s, req.ID); p["choice"] != "Squash-merge PR" || p["merge"] != nil {
		t.Fatalf("approval_result = %v", p)
	}
}

func TestRequestWireFlattensAgentOptions(t *testing.T) {
	s, _, req := acceptWithOptions(t)
	ctx := context.Background()
	w, err := s.RequestWireByID(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(w.Options) != `["Squash-merge PR","Keep branch"]` ||
		!reflect.DeepEqual(w.OptionDescriptions, []string{"Push, open PR, squash when green", "I'll ship it myself"}) || w.Choice != nil {
		t.Fatalf("wire = options %s, descriptions %q, choice %v", w.Options, w.OptionDescriptions, w.Choice)
	}
	if _, err := s.Approve(ctx, req.ID, ApproveInput{Binding: req.Binding, Via: "board", Merge: "custom", Choice: "Keep branch"}); err != nil {
		t.Fatal(err)
	}
	if w, _ = s.RequestWireByID(ctx, req.ID); w.Choice == nil || *w.Choice != "Keep branch" {
		t.Fatalf("choice after approve = %v", w.Choice)
	}
}

func TestRequestWireWithoutOptionsIsUnchanged(t *testing.T) {
	s, _, _ := newStore(t)
	req, _, _ := seedSectionApproval(t, s)
	w, err := s.RequestWireByID(context.Background(), req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(w.Options) != `[]` || w.OptionDescriptions != nil || w.Choice != nil {
		t.Fatalf("wire = %s %v %v", w.Options, w.OptionDescriptions, w.Choice)
	}
	b, _ := json.Marshal(w)
	if !strings.Contains(string(b), `"option_descriptions":null`) || !strings.Contains(string(b), `"choice":null`) {
		t.Fatalf("json = %s", b)
	}
}
