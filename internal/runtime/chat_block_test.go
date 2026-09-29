package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestApprovalChatBlock pins every exact format of the daemon-built chat
// block (docs/specs/2026-09-28-approval-chat-block.md locked decision 2).
func TestApprovalChatBlock(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   ChatBlockInput
		want string
	}{
		{"section with position", ChatBlockInput{Kind: string(KindApproveSection), Revision: 3, Summary: "Lead.\n\n- a\n- b",
			SectionTitle: "Verification", N: 2, M: 5, Path: "/abs/spec.md"},
			"### Approval 2 of 5 · Spec section \"Verification\" (rev 3)\n\nLead.\n\n- a\n- b\n\n" +
				"Full section: /abs/spec.md → \"## Verification\""},
		{"section without position", ChatBlockInput{Kind: string(KindApproveSection), Revision: 1, Summary: "S.",
			SectionTitle: "Design", Path: "/abs/spec.md"},
			"### Approval · Spec section \"Design\" (rev 1)\n\nS.\n\nFull section: /abs/spec.md → \"## Design\""},
		{"plan", ChatBlockInput{Kind: string(KindApprovePlan), Revision: 2, Summary: "P.",
			Paths: &ReviewPaths{Spec: "/abs/spec.md", Plan: "/abs/plan.md"}},
			"### Approval · Plan (rev 2)\n\nP.\n\nSpec: /abs/spec.md\nPlan: /abs/plan.md"},
		{"plan without spec", ChatBlockInput{Kind: string(KindApprovePlan), Revision: 1, Summary: "P.",
			Paths: &ReviewPaths{Plan: "/abs/plan.md"}},
			"### Approval · Plan (rev 1)\n\nP.\n\nPlan: /abs/plan.md"},
		{"report", ChatBlockInput{Kind: string(KindApproveReport), Revision: 4, Summary: "R.", Path: "/abs/report.md"},
			"### Approval · Debug report (rev 4)\n\nR.\n\nReport: /abs/report.md"},
		{"section with no known path", ChatBlockInput{Kind: string(KindApproveSection), Revision: 1, Summary: "S."},
			"### Approval · Spec section \"\" (rev 1)\n\nS."},
		{"child message", ChatBlockInput{Child: "coder-1", Summary: "may I drop table x?"},
			"### Approval · coder-1 asks\n\nmay I drop table x?"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ApprovalChatBlock(tc.in); got != tc.want {
				t.Fatalf("ApprovalChatBlock =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

const chatBlockSpec = "# Spec\n\n## Context\n\nWhy.\n\n## Design\n\nUse SQLite.\n\n## Verification\n\nRun tests.\n"

// TestAskApprovalReturnsChatBlock: swarm_ask's approval result carries the
// chat block, with N/M counted over required sections only (Context is
// exempt), read from the asked revision, and the relay repeats it.
func TestAskApprovalReturnsChatBlock(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newStore(t)
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Chat", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	path := writeFile(t, chatBlockSpec)
	spec, err := s.RegisterArtifact(ctx, ses.ID, "register", "SPIKE-1", "spec", path, "")
	if err != nil {
		t.Fatal(err)
	}
	var verif ArtifactSection
	for _, sec := range spec.Sections {
		if sec.Title == "Verification" {
			verif = sec
		}
	}
	summary := "Verification: how we prove it.\n\n- `go test ./...`"
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", Prompt: summary, ArtifactID: spec.ArtifactID, SectionID: verif.ID})
	if err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(path)
	want := "### Approval 2 of 2 · Spec section \"Verification\" (rev 1)\n\n" + summary +
		"\n\nFull section: " + abs + " → \"## Verification\""
	if req.ChatBlock != want {
		t.Fatalf("ChatBlock =\n%q\nwant\n%q", req.ChatBlock, want)
	}

	if err := s.tx(ctx, func(tx *sql.Tx) error { return s.relayRequestTx(ctx, tx, req.ID) }); err != nil {
		t.Fatal(err)
	}
	p, _ := relayFor(t, s, a.ID, req.ID)
	if p["chat_block"] != want {
		t.Fatalf("relay chat_block = %v, want %q", p["chat_block"], want)
	}
	if p["native_prompt"] != nil || p["next"] != PrintNext {
		t.Fatalf("relay native_prompt = %v, next = %v; want the print step only", p["native_prompt"], p["next"])
	}
}

// TestAskPlanApprovalReturnsChatBlock: a plan's block lists both review paths.
func TestAskPlanApprovalReturnsChatBlock(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newStore(t)
	ses, _, planID, _ := approvedFeatureSpike(t, s)
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: planID, Prompt: "Ship it."})
	if err != nil {
		t.Fatal(err)
	}
	if req.ReviewPaths == nil {
		t.Fatal("no review paths")
	}
	want := "### Approval · Plan (rev 1)\n\nShip it.\n\nSpec: " + req.ReviewPaths.Spec + "\nPlan: " + req.ReviewPaths.Plan
	if req.ChatBlock != want {
		t.Fatalf("ChatBlock =\n%q\nwant\n%q", req.ChatBlock, want)
	}
}

// TestNativePromptForMsgReturnsChatBlock: a child approval's native_prompt
// result carries a chat block with the child's full body.
func TestNativePromptForMsgReturnsChatBlock(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newStore(t)
	orch, w, wSes := worker(t, s)
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET kind = 'codex' WHERE id = ?`, orch.ID); err != nil {
		t.Fatal(err)
	}
	q, err := s.SendApproval(ctx, wSes.ID, "may I drop table x?", "")
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.Ask(ctx, mustSessionID(t, s, orch.ID), AskInput{Kind: "native_prompt", ForMsg: q})
	if err != nil {
		t.Fatal(err)
	}
	if want := "### Approval · " + w.Name + " asks\n\nmay I drop table x?"; out.ChatBlock != want {
		t.Fatalf("ChatBlock = %q, want %q", out.ChatBlock, want)
	}
	var phase string
	s.DB.QueryRow(`SELECT COALESCE(json_extract(payload_json, '$.print_phase'), '') FROM messages WHERE id = ?`, q).Scan(&phase)
	if phase != "print" {
		t.Fatalf("print_phase on the child message = %q", phase)
	}
}

// TestForMsgPrintThenAsk: a child approval goes through the same print step; the
// request_ask relay carries the msg id as request_id and correlation_id, and a child
// message that was already answered is never judged.
func TestForMsgPrintThenAsk(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newStore(t)
	orch, w, wSes := worker(t, s)
	q, err := s.SendApproval(ctx, wSes.ID, "may I drop table x?", "")
	if err != nil {
		t.Fatal(err)
	}
	orchSes := mustSessionID(t, s, orch.ID)
	if _, err := s.Ask(ctx, orchSes, AskInput{Kind: "native_prompt", ForMsg: q}); err != nil {
		t.Fatal(err)
	}
	relays := func() (n int, payload map[string]any) {
		rows, err := s.DB.QueryContext(ctx, `SELECT payload_json FROM messages WHERE kind = 'relay' AND correlation_id = ? ORDER BY seq`, q)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var p string
			rows.Scan(&p)
			payload = map[string]any{}
			json.Unmarshal([]byte(p), &payload)
			n++
		}
		return n, payload
	}
	passPrint(t, s, orchSes)
	n, p := relays()
	if n != 1 || p["event"] != "request_ask" || p["kind"] != "child_approval" || p["request_id"] != q {
		t.Fatalf("relays = %d, %v", n, p)
	}
	if np := decodeNP(t, p); np.Header != w.Name+" asks" || np.Question != "may I drop table x?" {
		t.Fatalf("native_prompt = %+v", np)
	}

	q2, err := s.SendApproval(ctx, wSes.ID, "may I rename y?", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ask(ctx, orchSes, AskInput{Kind: "native_prompt", ForMsg: q2}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO messages (id, seq, kind, wake_class, priority, origin, to_agent_id, root_item_id,
		reply_to, payload_json, state, created_at) SELECT 'msg_ans', COALESCE(MAX(seq), 0) + 1, 'answer', 'deferred', 1, 'agent', ?, ?, ?, '{}', 'acked', 1 FROM messages`,
		w.ID, w.RootItemID, q2); err != nil {
		t.Fatal(err)
	}
	if sent, err := s.PrintTurnEnded(ctx, orchSes, TurnReply{Trusted: true}); err != nil || sent {
		t.Fatalf("judged an already-answered child approval: sent=%v err=%v", sent, err)
	}
}

// TestAskApprovalRefusesLongSingleLineSummary: a summary over 300 runes
// with no newline is a run-on paragraph and is refused; the same text with
// a line break is accepted.
func TestAskApprovalRefusesLongSingleLineSummary(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newStore(t)
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Long", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	spec, err := s.RegisterArtifact(ctx, ses.ID, "register", "SPIKE-1", "spec", writeFile(t, "## Design\n\nUse SQLite.\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("word ", 61) // 305 runes
	if utf8.RuneCountInString(long) <= 300 {
		t.Fatal("fixture too short")
	}
	_, err = s.Ask(ctx, ses.ID, AskInput{Kind: "approval", Prompt: long, ArtifactID: spec.ArtifactID, SectionID: spec.Sections[0].ID})
	if err == nil || err.Error() != "Summary must be a lead sentence plus bullets (see swarm-orchestrator: approval summaries)." {
		t.Fatalf("err = %v", err)
	}
	_, err = s.Ask(ctx, ses.ID, AskInput{Kind: "approval", Prompt: long + "\n", ArtifactID: spec.ArtifactID, SectionID: spec.Sections[0].ID})
	if err == nil {
		t.Fatal("a trailing newline let a run-on summary through")
	}
	if _, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", Prompt: "Lead.\n" + long, ArtifactID: spec.ArtifactID,
		SectionID: spec.Sections[0].ID}); err != nil {
		t.Fatalf("multi-line summary refused: %v", err)
	}
}
