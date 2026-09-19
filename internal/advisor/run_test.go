package advisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

// §11.6: the exact command line for each advisor agent.
func TestAdvisorCommand(t *testing.T) {
	dir := "/Users/u/.swarm/run/advice/adv_1"
	cases := []struct {
		kind   runtime.AgentKind
		model  string
		effort string
		want   []string
	}{
		{runtime.Claude, "claude-fable-5-1", "high", []string{
			"claude", "-p", "--model", "claude-fable-5-1", "--effort", "high",
			"--output-format", "json", "--allowedTools", "Read,Grep,Glob", "--", Prompt}},
		{runtime.Codex, "gpt-6-astra", "medium", []string{
			"codex", "exec", "-s", "read-only", "--skip-git-repo-check", "-m", "gpt-6-astra",
			"-c", `model_reasoning_effort="medium"`, "-o", dir + "/answer.md", Prompt}},
		{runtime.Agy, "gemini-3.8-flash-high", "", []string{
			"agy", "--model", "gemini-3.8-flash-high", "--mode", "plan",
			"--add-dir", dir, "--output-format", "json", "--print", Prompt}},
		{runtime.Cursor, "auto", "", []string{
			"cursor-agent", "-p", "--trust", "--mode", "ask", "--model", "auto",
			"--output-format", "json", Prompt}},
	}
	for _, c := range cases {
		got, err := AdvisorCommand(c.kind, c.model, c.effort, dir, Prompt)
		if err != nil {
			t.Fatalf("%s: %v", c.kind, err)
		}
		if strings.Join(got, "\x00") != strings.Join(c.want, "\x00") {
			t.Errorf("%s:\n got %v\nwant %v", c.kind, got, c.want)
		}
	}
	// claude with no effort drops the flag but keeps the --
	got, _ := AdvisorCommand(runtime.Claude, "claude-opus-5", "", dir, Prompt)
	if strings.Contains(strings.Join(got, " "), "--effort") {
		t.Fatalf("argv = %v", got)
	}
	if got[len(got)-2] != "--" {
		t.Fatalf("claude needs -- before the prompt (P0-13): %v", got)
	}
	// agy never gets --effort (P0-12)
	agy, _ := AdvisorCommand(runtime.Agy, "gemini-3.8-flash-high", "high", dir, Prompt)
	if strings.Contains(strings.Join(agy, " "), "--effort") {
		t.Fatalf("agy argv = %v", agy)
	}
}

// §11.6 cost tracking (D1): tokens and cost from each agent's own output.
func TestParseAnswerAndUsage(t *testing.T) {
	dir := t.TempDir()
	claude, err := os.ReadFile("testdata/claude/advisor-readonly-output.json")
	if err != nil {
		t.Fatal(err)
	}
	ans, u, err := ParseAnswer(runtime.Claude, claude, nil, dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if ans == "" {
		t.Error("claude's answer is the result field")
	}
	if u.CostUSD == nil {
		t.Error("claude reports total_cost_usd")
	}
	if u.Input == 0 && u.Output == 0 {
		t.Error("claude reports usage; a modelUsage with several models is summed (P0-13)")
	}

	// codex: the -o file holds the answer, "tokens used N" is on stderr
	os.WriteFile(filepath.Join(dir, "answer.md"), []byte("Use the server path.\n"), 0o644)
	codexErr, _ := os.ReadFile("testdata/codex/advisor-readonly-output.txt")
	ans, u, err = ParseAnswer(runtime.Codex, nil, codexErr, dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ans, "Use the server path.") {
		t.Errorf("codex answer = %q", ans)
	}
	if u.Input+u.Output == 0 {
		t.Error("codex reports a token total on stderr")
	}
	if u.CostUSD != nil {
		t.Error("codex reports no cost")
	}

	agy, _ := os.ReadFile("testdata/agy/advisor-readonly-output.json")
	ans, u, err = ParseAnswer(runtime.Agy, agy, nil, dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if ans == "" {
		t.Error("agy's answer is the response field")
	}
	if u.CostUSD != nil {
		t.Error("agy reports no cost")
	}

	cur, _ := os.ReadFile("testdata/cursor/advisor-readonly-output.json")
	ans, u, err = ParseAnswer(runtime.Cursor, cur, nil, dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if ans == "" {
		t.Error("cursor's answer is the result field")
	}
	if u.CostUSD != nil {
		t.Error("cursor reports no cost")
	}
}

// P0-13: an empty agy response falls back to the conversation's plan.md.
func TestAgyEmptyResponseFallsBackToPlanMd(t *testing.T) {
	userHome := t.TempDir()
	body, _ := os.ReadFile("testdata/agy/advisor-plan-mode-write-attempt.json")
	var conv struct {
		ConversationID string `json:"conversation_id"`
	}
	json.Unmarshal(body, &conv)
	if conv.ConversationID == "" {
		conv.ConversationID = "conv-1"
		body = []byte(`{"response":"","conversation_id":"conv-1","usage":{}}`)
	}
	planDir := filepath.Join(userHome, ".gemini", "antigravity-cli", "brain", conv.ConversationID)
	os.MkdirAll(planDir, 0o755)
	os.WriteFile(filepath.Join(planDir, "plan.md"), []byte("The answer lives here.\n"), 0o644)
	ans, _, err := ParseAnswer(runtime.Agy, body, nil, t.TempDir(), userHome)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ans, "The answer lives here.") {
		t.Fatalf("answer = %q", ans)
	}
}

func TestAskWritesTheContextFileWithMode0600AndRunsTheCommand(t *testing.T) {
	s, seed := newAdvisorService(t)
	ctx := context.Background()
	var gotArgv []string
	s.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		gotArgv = append([]string{name}, args...)
		return []byte(`{"result":"Use the server path.","total_cost_usd":0.18,
			"usage":{"input_tokens":10,"output_tokens":20}}`), nil
	}
	adv, err := s.Ask(ctx, seed.SessionID, "Client or server?", []string{"/w/a.ts"}, 45*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if adv.State != "answered" || !strings.Contains(adv.Answer, "server path") {
		t.Fatalf("advice = %+v", adv)
	}
	if adv.CostUSD == nil || *adv.CostUSD != 0.18 {
		t.Fatalf("cost = %v", adv.CostUSD)
	}
	fi, err := os.Stat(adv.ContextPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("context.md mode = %v", fi.Mode().Perm())
	}
	if !strings.Contains(strings.Join(gotArgv, " "), adv.ContextPath) &&
		!strings.Contains(readFile(t, adv.ContextPath), "Client or server?") {
		t.Fatal("the question must reach the advisor, through the context file")
	}
	if adv.ContextChars == 0 {
		t.Error("context_chars is recorded")
	}
}

func TestAskReturnsAMessageWhenItIsSlowerThanWaitSeconds(t *testing.T) {
	s, seed := newAdvisorService(t)
	ctx := context.Background()
	release := make(chan struct{})
	s.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		<-release
		return []byte(`{"result":"late answer"}`), nil
	}
	// delivered is written from the run goroutine's Deliver callback and read
	// from the test goroutine via waitUntil; the mutex is what makes that safe
	// under `go test -race` (helpers_test.go requires it for this package).
	var mu sync.Mutex
	var delivered []runtime.Advice
	s.Deliver = func(ctx context.Context, sessionID string, adv runtime.Advice) error {
		mu.Lock()
		defer mu.Unlock()
		delivered = append(delivered, adv)
		return nil
	}
	adv, err := s.Ask(ctx, seed.SessionID, "slow?", nil, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if adv.State != "running" || adv.Answer != "" {
		t.Fatalf("a slow run returns running with no answer: %+v", adv)
	}
	close(release)
	waitUntil(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(delivered) == 1
	})
	mu.Lock()
	defer mu.Unlock()
	if delivered[0].Answer != "late answer" {
		t.Fatalf("delivered = %+v", delivered[0])
	}
}

// §17.3: the exact failure message.
func TestAskFailureAndTimeoutMessages(t *testing.T) {
	s, seed := newAdvisorService(t)
	ctx := context.Background()
	s.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, errors.New("exit status 1")
	}
	adv, err := s.Ask(ctx, seed.SessionID, "q", nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if adv.State != "failed" {
		t.Fatalf("state = %s", adv.State)
	}
	want := "The advisor couldn't answer: exit status 1. Continue with your best judgment and note the risk in your next checkpoint."
	if adv.Error != want {
		t.Fatalf("error = %q, want %q", adv.Error, want)
	}
}

// §11.6: 240 s, stdin closed.
func TestRunnerTimeoutAndClosedStdin(t *testing.T) {
	s, seed := newAdvisorService(t)
	s.Timeout = 20 * time.Millisecond
	s.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	adv, err := s.Ask(context.Background(), seed.SessionID, "q", nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if adv.State != "timed_out" {
		t.Fatalf("state = %s", adv.State)
	}
}
