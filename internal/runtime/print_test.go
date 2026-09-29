package runtime

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
)

// passPrint marks every relay to the session's agent delivered (seen; the guard only skips pending) and ends the turn as a trusted kind,
// so the next relay for a chat_block request is its request_ask.
func passPrint(t *testing.T, s *Store, sessionID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.DB.ExecContext(ctx, `UPDATE messages SET state = 'delivered' WHERE kind = 'relay' AND state = 'pending'
		AND to_agent_id = (SELECT agent_id FROM sessions WHERE id = ?)`, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrintTurnEnded(ctx, sessionID, TurnReply{Trusted: true}); err != nil {
		t.Fatal(err)
	}
}

func printState(t *testing.T, s *Store, reqID string) (phase string, attempts int) {
	t.Helper()
	if err := s.DB.QueryRow(`SELECT COALESCE(json_extract(binding_json, '$.print_phase'), ''),
		COALESCE(json_extract(binding_json, '$.print_attempts'), 0) FROM requests WHERE id = ?`, reqID).
		Scan(&phase, &attempts); err != nil {
		t.Fatal(err)
	}
	return phase, attempts
}

func printSection(t *testing.T) (*Store, string, string, Request) {
	t.Helper()
	s, _, _, ses, req := printSectionAll(t)
	return s, ses.ID, req.AgentID, req
}

func printSectionAll(t *testing.T) (*Store, *fakeTmux, *adapter.Fake, Session, Request) {
	t.Helper()
	ctx := context.Background()
	s, tm, fa := newStore(t)
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Print", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	spec, err := s.RegisterArtifact(ctx, ses.ID, "register", "SPIKE-1", "spec", writeFile(t, "## Design\n\nUse SQLite.\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", Prompt: "Use SQLite for storage.\n\n- one file per repo",
		ArtifactID: spec.ArtifactID, SectionID: spec.Sections[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	return s, tm, fa, ses, req
}

func TestAskApprovalStartsPrintPhase(t *testing.T) {
	s, _, _, req := printSection(t)
	if p, n := printState(t, s, req.ID); p != "print" || n != 0 {
		t.Fatalf("print state = %q/%d", p, n)
	}
}

func TestPrintTurnEndedPassSendsAsk(t *testing.T) {
	s, ses, agentID, req := printSection(t)
	reply := "Here it is:\n\n**" + strings.ReplaceAll(req.ChatBlock, "- ", "* ") + "**"
	sent, err := s.PrintTurnEnded(context.Background(), ses, TurnReply{Text: reply, Readable: true})
	if err != nil || !sent {
		t.Fatalf("sent=%v err=%v", sent, err)
	}
	p, _ := relayFor(t, s, agentID, req.ID)
	if p["event"] != "request_ask" || p["next"] != NativePromptNextStep(req.ID, []string{"approve", "request_changes"}) {
		t.Fatalf("relay = %v", p)
	}
	if np := decodeNP(t, p); np.Question != req.NativePrompt.Question {
		t.Fatalf("native_prompt %q != frozen %q", np.Question, req.NativePrompt.Question)
	}
	if ph, _ := printState(t, s, req.ID); ph != "ask" {
		t.Fatalf("phase = %q", ph)
	}
}

func TestPrintTurnEndedParaphraseRetriesThenPasses(t *testing.T) {
	s, ses, agentID, req := printSection(t)
	ctx := context.Background()
	if _, err := s.PrintTurnEnded(ctx, ses, TurnReply{Text: "Approving section 1: SQLite.", Readable: true}); err != nil {
		t.Fatal(err)
	}
	p, _ := relayFor(t, s, agentID, req.ID)
	if p["event"] != "request_print" || p["next"] != ReprintNext || p["chat_block"] != req.ChatBlock || p["attempt"] != float64(2) {
		t.Fatalf("reprint relay = %v", p)
	}
	if _, n := printState(t, s, req.ID); n != 1 {
		t.Fatalf("attempts = %d", n)
	}
	passPrintWith(t, s, ses, req.ChatBlock)
	if p, _ := relayFor(t, s, agentID, req.ID); p["event"] != "request_ask" {
		t.Fatalf("relay = %v", p)
	}
}

func passPrintWith(t *testing.T, s *Store, ses, text string) {
	t.Helper()
	s.DB.Exec(`UPDATE messages SET state = 'delivered' WHERE kind = 'relay' AND state = 'pending'`)
	if _, err := s.PrintTurnEnded(context.Background(), ses, TurnReply{Text: text, Readable: true}); err != nil {
		t.Fatal(err)
	}
}

func TestPrintTurnEndedAsksAnywayAfterTwoFailures(t *testing.T) {
	s, ses, agentID, req := printSection(t)
	passPrintWith(t, s, ses, "short version")
	passPrintWith(t, s, ses, "short version again")
	if p, n := relayFor(t, s, agentID, req.ID); p["event"] != "request_ask" || n != 2 {
		t.Fatalf("relay = %v (n=%d)", p, n)
	}
}

func TestPrintTurnEndedFailsOpen(t *testing.T) {
	for name, r := range map[string]TurnReply{"unreadable": {}, "trusted": {Trusted: true}} {
		t.Run(name, func(t *testing.T) {
			s, ses, agentID, req := printSection(t)
			if _, err := s.PrintTurnEnded(context.Background(), ses, r); err != nil {
				t.Fatal(err)
			}
			if p, _ := relayFor(t, s, agentID, req.ID); p["event"] != "request_ask" {
				t.Fatalf("relay = %v", p)
			}
		})
	}
}

func TestPrintTurnEndedSkipsUndeliveredRelay(t *testing.T) {
	s, ses, _, req := printSection(t)
	ctx := context.Background()
	if err := s.tx(ctx, func(tx *sql.Tx) error { return s.relayRequestTx(ctx, tx, req.ID) }); err != nil {
		t.Fatal(err)
	}
	if sent, _ := s.PrintTurnEnded(ctx, ses, TurnReply{Text: "nope", Readable: true}); sent {
		t.Fatal("judged a ref whose request_open was never delivered")
	}
	if _, n := printState(t, s, req.ID); n != 0 {
		t.Fatalf("attempts = %d", n)
	}
}

func TestApproveMidPrintResolvesAndRetires(t *testing.T) {
	s, ses, _, req := printSection(t)
	ctx := context.Background()
	passPrintWith(t, s, ses, "paraphrase") // pending request_print now exists
	if _, err := s.Approve(ctx, req.ID, ApproveInput{Via: "board", SectionSHA256: req.SectionSHA256,
		ArtifactRevision: req.ArtifactRevision}); err != nil {
		t.Fatal(err)
	}
	var pending int
	s.DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE request_id = ? AND state <> 'acked'
		AND json_extract(payload_json, '$.event') IN ('request_print', 'request_ask')`, req.ID).Scan(&pending)
	if pending != 0 {
		t.Fatalf("%d print/ask relays left pending", pending)
	}
	if sent, _ := s.PrintTurnEnded(ctx, ses, TurnReply{Trusted: true}); sent {
		t.Fatal("resolved request still judged")
	}
}

func TestResurfaceRetiresAskAndRestartsPrint(t *testing.T) {
	s, ses, agentID, req := printSection(t)
	ctx := context.Background()
	if _, err := s.PrintTurnEnded(ctx, ses, TurnReply{Trusted: true}); err != nil { // pending request_ask
		t.Fatal(err)
	}
	a, _ := s.AgentByID(ctx, agentID)
	if _, err := s.resurfaceOpenRequests(ctx, a, ses, true, time.Time{}); err != nil {
		t.Fatal(err)
	}
	p, _ := relayFor(t, s, agentID, req.ID)
	if p["event"] != "request_open" || p["chat_block"] != req.ChatBlock || p["native_prompt"] != nil || p["next"] != PrintNext {
		t.Fatalf("relay = %v", p)
	}
	if ph, n := printState(t, s, req.ID); ph != "print" || n != 0 {
		t.Fatalf("print state = %q/%d", ph, n)
	}
}

func TestPrintTurnTickMuseStyle(t *testing.T) {
	s, tm, fa, ses, req := printSectionAll(t)
	ctx := context.Background()
	agentID := req.AgentID
	s.DB.Exec(`UPDATE messages SET state = 'acked' WHERE to_agent_id = ?`, agentID)
	tm.captures[ses.TmuxName] = []string{"─────\n❯ \n─────\n"} // idle

	fa.LastReplyReadable, fa.LastReplyFound = true, false
	if err := s.PrintTurnTick(ctx); err != nil {
		t.Fatal(err)
	}
	if _, n := relayFor(t, s, agentID, req.ID); n != 0 {
		t.Fatalf("%d relays before any reply", n)
	}
	fa.LastReplyFound, fa.LastReplyText = true, req.ChatBlock
	if err := s.PrintTurnTick(ctx); err != nil {
		t.Fatal(err)
	}
	if p, _ := relayFor(t, s, agentID, req.ID); p["event"] != "request_ask" {
		t.Fatalf("relay = %v", p)
	}
}

func TestPrintTurnTickUnreadableReplyAsks(t *testing.T) {
	s, tm, fa, ses, req := printSectionAll(t)
	s.DB.Exec(`UPDATE messages SET state = 'acked' WHERE to_agent_id = ?`, req.AgentID)
	tm.captures[ses.TmuxName] = []string{"─────\n❯ \n─────\n"} // idle
	fa.LastReplyReadable = false
	if err := s.PrintTurnTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p, _ := relayFor(t, s, req.AgentID, req.ID); p["event"] != "request_ask" {
		t.Fatalf("relay = %v, want request_ask (fail open)", p)
	}
}
