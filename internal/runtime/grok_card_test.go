package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
	"github.com/AlexanderTar/agent-swarm/internal/kinds"
)

func grokCapture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "adapter", "testdata", "grok", "grok-tui-"+name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func cardRows(t *testing.T, s *Store, sessionID string) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM requests WHERE session_id = ? AND kind = 'prompt'`, sessionID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A grok approval keeps the ask_user_question card on screen while the bound
// question row is open. That card is the question row's business: reconcile
// must not also open a "Grok is showing a card" dialog row for it, while a
// real card (the usage-limit offer) still escalates.
func TestReconcileOpensNoDialogRowForGrokQuestionCard(t *testing.T) {
	s, tm, ses, _ := questionDialogSession(t, Grok) // a bound question row is open
	clk := tm.clk
	ctx := context.Background()
	real, err := adapter.New(kinds.Grok, adapter.Deps{Home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s.Adapters[Grok] = real
	name := ses.TmuxName
	panes(tm, Pane{Session: name, Command: "grok-1.0.60-mac"})
	if pr, ok := real.(interface{ PasteReady(string) bool }); !ok || pr.PasteReady(grokCapture(t, "question-card")) {
		t.Fatal("PasteReady must stay false on the question card")
	}

	tm.captures[name] = []string{grokCapture(t, "question-card")}
	for i := 0; i < 5; i++ { // past dialogEscalateAfter
		if err := s.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		clk.Advance(5 * time.Second)
	}
	if n := cardRows(t, s, ses.ID); n != 0 {
		t.Fatalf("%d dialog prompt rows for the question card, want 0", n)
	}

	tm.captures[name] = []string{grokCapture(t, "free-usage-limit-card")}
	tm.n[name] = 0
	for i := 0; i < 5; i++ {
		if err := s.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		clk.Advance(5 * time.Second)
	}
	if n := cardRows(t, s, ses.ID); n != 1 {
		t.Fatalf("%d dialog prompt rows for the usage-limit card, want 1", n)
	}
}

// The usage-limit card is a source of grok's exhaustion record: reconcile hands
// every capture it reads to an adapter that wants them.
func TestReconcileFeedsTheUsageLimitCardToGrokExhaustion(t *testing.T) {
	s, tm, ses, _ := questionDialogSession(t, Grok)
	ctx := context.Background()
	real, err := adapter.New(kinds.Grok, adapter.Deps{Home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var noted []string
	real.(interface{ OnExhausted(func(string)) }).OnExhausted(func(text string) { noted = append(noted, text) })
	s.Adapters[Grok] = real
	panes(tm, Pane{Session: ses.TmuxName, Command: "grok-1.0.60-mac"})
	tm.captures[ses.TmuxName] = []string{grokCapture(t, "free-usage-limit-card")}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(noted) != 1 {
		t.Fatalf("exhaustion notes = %q, want one from the usage-limit card", noted)
	}
}
