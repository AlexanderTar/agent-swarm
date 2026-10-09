package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

func TestDecodeTreatsEmptyArgsAsAnEmptyObjectAndRejectsBadJSON(t *testing.T) {
	var in struct {
		Reason string `json:"reason"`
	}
	if err := decode(nil, &in); err != nil || in.Reason != "" {
		t.Fatalf("decode(nil) = %v, %+v; want nil and a zero value", err, in)
	}
	if err := decode(json.RawMessage(`{"reason":"x"}`), &in); err != nil || in.Reason != "x" {
		t.Fatalf("decode = %v, %+v; want reason x", err, in)
	}
	if err := decode(json.RawMessage(`{not json`), &in); err == nil {
		t.Fatal("decode of malformed JSON: want an error")
	}
}

func TestSplitAskOptionsAcceptsLabelsOrObjectsAndRefusesOtherShapes(t *testing.T) {
	labels, objs, err := splitAskOptions([]json.RawMessage{json.RawMessage(`"A"`), json.RawMessage(`"B"`)})
	if err != nil || len(labels) != 2 || labels[0] != "A" || labels[1] != "B" || len(objs) != 0 {
		t.Fatalf("labels = %v %v %v", labels, objs, err)
	}
	labels, objs, err = splitAskOptions([]json.RawMessage{json.RawMessage(`{"label":"Merge","description":"fast-forward"}`)})
	if err != nil || len(labels) != 0 || len(objs) != 1 || objs[0].Label != "Merge" || objs[0].Description != "fast-forward" {
		t.Fatalf("objects = %v %v %v", labels, objs, err)
	}
	_, _, err = splitAskOptions([]json.RawMessage{json.RawMessage(`42`)})
	if err == nil || !strings.Contains(err.Error(), "options must be strings") {
		t.Fatalf("numeric option err = %v, want the shape error", err)
	}
}

func TestRequestOutShapesTheAskResult(t *testing.T) {
	plain := requestOut(runtime.Request{ID: "req_1", State: "open"})
	if len(plain) != 2 || plain["request_id"] != "req_1" || plain["state"] != runtime.RequestState("open") {
		t.Fatalf("plain result = %v, want exactly request_id and state", plain)
	}

	chat := requestOut(runtime.Request{ID: "req_2", State: "open", ChatBlock: "print me",
		NativePrompt: &runtime.NativePrompt{Question: "q", Options: []string{"Approve"}}})
	if chat["chat_block"] != "print me" || chat["next"] != runtime.PrintNext || chat["native_prompt"] != nil {
		t.Fatalf("chat-block result = %v, want chat_block and the print step only", chat)
	}

	native := requestOut(runtime.Request{ID: "req_3", State: "open", Kind: runtime.KindApprovePlan,
		NativePrompt: &runtime.NativePrompt{Question: "ok?", Options: []string{"Approve", "Request changes"}}})
	if native["native_prompt"] == nil || native["chat_block"] != nil {
		t.Fatalf("native result = %v, want native_prompt without chat_block", native)
	}
	next, _ := native["next"].(string)
	if !strings.Contains(next, `"req_3"`) || !strings.Contains(next, "native_answer") {
		t.Fatalf("next = %q, want the native_answer instruction for req_3", next)
	}

	paths := &runtime.ReviewPaths{Spec: "/s.md", Plan: "/p.md"}
	full := requestOut(runtime.Request{ID: "req_4", State: "approved", ReviewPaths: paths, Next: "carry on"})
	if full["review_paths"] != paths || full["next"] != "carry on" {
		t.Fatalf("full result = %v, want review_paths and an explicit next", full)
	}
}

func TestDecodePageCursorRoundTripsAndRejectsGarbage(t *testing.T) {
	ms, id, err := decodePageCursor(encodePageCursor(1234, "itm_x:y"))
	if err != nil || ms != 1234 || id != "itm_x:y" {
		t.Fatalf("round trip = %d %q %v", ms, id, err)
	}
	for name, cur := range map[string]string{
		"not base64":    "%%%",
		"no separator":  encodeRaw("12345"),
		"empty id":      encodeRaw("12:"),
		"empty time":    encodeRaw(":abc"),
		"non-numeric t": encodeRaw("abc:itm_1"),
	} {
		_, _, err := decodePageCursor(cur)
		var ie *items.Error
		if !errors.As(err, &ie) || ie.Code != items.CodeBadRequest {
			t.Fatalf("%s: err = %v, want a bad_request items error", name, err)
		}
	}
}

func encodeRaw(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func TestRefErrorIsSanitisedPerRef(t *testing.T) {
	nf := refError("EPIC-9", &items.Error{Code: items.CodeNotFound, Message: "boom"})
	if nf["code"] != items.CodeNotFound || nf["ref"] != "EPIC-9" || !strings.Contains(nf["message"].(string), `"EPIC-9"`) {
		t.Fatalf("not-found entry = %v", nf)
	}
	other := refError("EPIC-9", errors.New("sqlite: database is locked"))
	if other["code"] != "read_failed" || strings.Contains(other["message"].(string), "sqlite") {
		t.Fatalf("generic entry = %v, want read_failed without driver text", other)
	}
}

func TestWorkflowStateOutNeverEmitsNullRuns(t *testing.T) {
	out := workflowStateOut(runtime.WorkflowState{ID: "wf_1", State: "running", Round: 2, ExtraRounds: 1})
	runs, ok := out["runs"].([]runtime.WorkflowRunView)
	if !ok || runs == nil || len(runs) != 0 {
		t.Fatalf("runs = %#v, want a non-nil empty slice", out["runs"])
	}
	if out["workflow"] != "wf_1" || out["round"] != 2 || out["extra_rounds"] != 1 {
		t.Fatalf("out = %v", out)
	}
	b, _ := json.Marshal(out)
	if !strings.Contains(string(b), `"runs":[]`) {
		t.Fatalf("json = %s, want runs as []", b)
	}
}

func TestBlockerRefusesABlankReasonAndOpensARequest(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	_, err := s.call(ctx, seed.Caller, "swarm_blocker", `{"reason":"   "}`)
	var ie *items.Error
	if !errors.As(err, &ie) || ie.Code != items.CodeBadRequest {
		t.Fatalf("blank reason err = %v, want bad_request", err)
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_blocker", `{`); err == nil {
		t.Fatal("malformed args: want a decode error")
	}
	out, err := s.call(ctx, seed.Caller, "swarm_blocker", `{"reason":"need creds"}`)
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["status"] != "blocked" || m["request_id"] == "" {
		t.Fatalf("blocker result = %v", m)
	}
}

func TestAskRefusesOptionsOfTheWrongShape(t *testing.T) {
	s, seed := newServerWithSession(t)
	_, err := s.call(context.Background(), seed.Caller, "swarm_ask", `{"kind":"question","prompt":"p","options":[7]}`)
	if err == nil || !strings.Contains(err.Error(), "options must be strings") {
		t.Fatalf("err = %v, want the options shape error", err)
	}
}

func TestRecoveryReadRefusesUnknownAgentAndClampsLimit(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	agentID, ses, _ := seedAgentAndSession(t, s, runtime.RoleCoder, "", "")
	var name string
	if err := s.RT.DB.QueryRowContext(ctx, `SELECT name FROM agents WHERE id = ?`, agentID).Scan(&name); err != nil {
		t.Fatal(err)
	}
	c := Caller{SessionID: ses, Role: runtime.RoleCoder, AgentID: agentID, AgentName: name}
	if _, err := s.call(ctx, c, "swarm_read", `{"recovery":{"agent":"no-such-agent"}}`); err == nil {
		t.Fatal("unknown agent: want an error")
	}
	if _, err := s.call(ctx, c, "swarm_checkpoint", `{"kind":"accepted","summary":"plan"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.call(ctx, c, "swarm_blocker", `{"reason":"stuck"}`); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []string{"-3", "100000"} {
		out, err := s.call(ctx, c, "swarm_read", `{"recovery":{"agent":"`+name+`","limit":`+limit+`}}`)
		if err != nil {
			t.Fatalf("limit %s: %v", limit, err)
		}
		rec := out.(map[string]any)["recovery"].(map[string]any)
		if cps, _ := rec["checkpoints"].([]any); len(cps) == 0 {
			t.Fatalf("limit %s: no checkpoints returned", limit)
		}
		if rec["agent"] != name {
			t.Fatalf("limit %s: agent = %v, want %s", limit, rec["agent"], name)
		}
		reqs, _ := rec["requests"].([]any)
		if len(reqs) != 1 || reqs[0].(map[string]any)["kind"] == "" {
			t.Fatalf("limit %s: requests = %v, want the open blocker", limit, rec["requests"])
		}
	}
}

func TestCallToolRefusesAToolTheCallerCannotSee(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	if _, err := s.CallTool(ctx, seed.Caller, "swarm_no_such_tool", nil); err == nil || !strings.Contains(err.Error(), "unknown tool swarm_no_such_tool") {
		t.Fatalf("unknown tool err = %v", err)
	}
	// swarm_spawn is orchestrator-only: invisible to a coder.
	if _, err := s.CallTool(ctx, seed.Caller, "swarm_spawn", json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("orchestrator tool for a coder err = %v, want unknown tool", err)
	}
	if _, err := s.CallTool(ctx, seed.Caller, "swarm_instructions", nil); err == nil {
		t.Fatal("swarm_instructions with no args: want the op error, not success")
	}
}

func TestCallerRootIDResolvesFromTheAgentRowOrFails(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	root, err := callerRootID(ctx, s, seed.Caller)
	if err != nil || root == "" {
		t.Fatalf("callerRootID = %q, %v; want the agent's root item id", root, err)
	}
	if _, err := callerRootID(ctx, s, Caller{AgentName: "ghost"}); err == nil {
		t.Fatal("callerRootID of an unknown agent: want an error")
	}
}

func TestInstructionsToolNeedsASettingsStore(t *testing.T) {
	s, seed := newServerWithSession(t)
	ctx := context.Background()
	s.Settings = nil
	s.RT.Settings = nil
	_, err := s.call(ctx, seed.Caller, "swarm_instructions", `{"op":"get"}`)
	if err == nil || !strings.Contains(err.Error(), "settings store not available") {
		t.Fatalf("err = %v, want settings store not available", err)
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_instructions", `{`); err == nil {
		t.Fatal("malformed args: want a decode error")
	}
}
