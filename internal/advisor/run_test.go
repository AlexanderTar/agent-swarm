package advisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/grokhome"
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
			"codex", "exec", "-s", "read-only", "--skip-git-repo-check", "-c", "mcp_servers={}", "-m", "gpt-6-astra",
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

func TestAskResolvesSlugAdvisorEffortInCommand(t *testing.T) {
	cases := []struct {
		name, kind, model, effort, wantModel string
	}{
		{"agy", "agy", "gemini-3.8-flash", "medium", "gemini-3.8-flash-medium"},
		{"cursor", "cursor", "gpt-5.3-codex", "high", "gpt-5.3-codex-high"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, seed := newAdvisorService(t)
			s.Catalog = &catalog.Service{DB: s.DB}
			ctx := context.Background()
			_, err := s.DB.ExecContext(ctx, `INSERT INTO model_catalog
				(agent_kind, agent_version, models_json, default_model, source, fetched_at, attempted_at)
				VALUES (?, 'test', ?, ?, 'test', 1, 1)`, tc.kind,
				`[{"id":"`+tc.model+`","label":"Test","efforts":["medium","high"],"default_effort":"high","effort_encoding":"slug","launch_ids":{"medium":"`+tc.model+`-medium","high":"`+tc.model+`-high"}}]`, tc.model)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.DB.ExecContext(ctx, `UPDATE agents SET advisor_kind = ?, advisor_model = ?, advisor_effort = ? WHERE id = ?`,
				tc.kind, tc.model, tc.effort, seed.AgentID)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			s.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
				got = append([]string{name}, args...)
				return []byte(`{"response":"Use this approach.","result":"Use this approach."}`), nil
			}
			adv, err := s.Ask(ctx, seed.SessionID, "Which approach?", nil, 45*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if adv.State != "answered" {
				t.Fatalf("advice state = %q, want answered: %s", adv.State, adv.Error)
			}
			var model string
			for i := range got {
				if got[i] == "--model" && i+1 < len(got) {
					model = got[i+1]
					break
				}
			}
			if model != tc.wantModel {
				t.Fatalf("advisor command model = %q, want %q; argv = %v", model, tc.wantModel, got)
			}
			if adv.AdvisorModel != tc.model || adv.AdvisorEffort != tc.effort {
				t.Fatalf("persisted advisor model/effort = %q/%q, want %q/%q", adv.AdvisorModel, adv.AdvisorEffort, tc.model, tc.effort)
			}
		})
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

// P4 unit 4.3: the simulated advisor's own system prompt (run by claude/codex/
// agy/cursor as a read-only advisor) follows the same decision rules as the
// swarm-advisor skill (spec A4): one decision per consult, an answer grounded
// in the evidence it actually read rather than a menu of options, and (fix
// round 1, Important finding 1) rule 5 -- unresolved high-impact uncertainty
// goes to the agent's parent or the user, rather than the advisor forcing a
// decisive answer the evidence can't actually support.
func TestAdvisorSystemPromptDecisionRules(t *testing.T) {
	for _, want := range []string{
		"one decision", "actually read", "Do not edit files", "parent or the user",
	} {
		if !strings.Contains(Prompt, want) {
			t.Errorf("advisor Prompt missing %q: %s", want, Prompt)
		}
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

// grokWriteTools are the tool ids in grok 1.0.60's tool_definitions.json that
// can change a file, run a command or reach the network (live probe, SPIKE-52).
var grokWriteTools = []string{"write", "search_replace", "run_terminal_command", "kill_command_or_subagent",
	"image_gen", "image_edit", "image_to_video", "reference_to_video", "web_fetch", "monitor",
	"workflow", "enter_plan_mode", "exit_plan_mode", "ask_user_question", "todo_write"}

func TestAdvisorCommandGrok(t *testing.T) {
	dir := "/Users/u/.swarm/run/advice/adv_1"
	got, err := AdvisorCommand(runtime.Grok, "grok-4.7", "high", dir, Prompt)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"grok", "-p", Prompt, "-m", "grok-4.7", "--reasoning-effort", "high",
		"--permission-mode", "plan", "--tools", "read_file,grep,list_dir",
		"--disallowed-tools", "search_tool,use_tool", "--no-subagents",
		"--output-format", "json", "--max-turns", "8", "--cwd", dir}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
	noEffort, _ := AdvisorCommand(runtime.Grok, "grok-4.7", "", dir, Prompt)
	if strings.Contains(strings.Join(noEffort, " "), "--reasoning-effort") {
		t.Fatalf("no effort, no flag: %v", noEffort)
	}
}

// requireReadOnlyGrokArgv is the write-attempt check: the argv leaves grok no
// tool that writes, runs a command or edits, and no flag that approves one.
func requireReadOnlyGrokArgv(t *testing.T, argv []string) {
	t.Helper()
	flag := func(name string) (string, bool) {
		for i, a := range argv {
			if a == name && i+1 < len(argv) {
				return argv[i+1], true
			}
		}
		return "", false
	}
	if mode, ok := flag("--permission-mode"); !ok || mode != "plan" {
		t.Errorf("--permission-mode = %q, want plan: %v", mode, argv)
	}
	tools, ok := flag("--tools")
	if !ok {
		t.Fatalf("grok runs with every tool unless --tools allowlists them: %v", argv)
	}
	for _, id := range strings.Split(tools, ",") {
		if !slices.Contains([]string{"read_file", "grep", "list_dir"}, id) {
			t.Errorf("--tools allows %q, which is not read-only", id)
		}
		if slices.Contains(grokWriteTools, id) {
			t.Errorf("--tools allows the write-capable %q", id)
		}
	}
	for _, bad := range []string{"--always-approve", "--yolo", "--allow", "--allowedTools", "--allowed-tools",
		"--sandbox", "--dangerously-skip-permissions", "--trust"} {
		if slices.Contains(argv, bad) {
			t.Errorf("argv carries %s: %v", bad, argv)
		}
	}
	if !slices.Contains(argv, "--no-subagents") {
		t.Errorf("subagents could write: %v", argv)
	}
}

func TestAdvisorGrokReadOnly(t *testing.T) {
	argv, err := AdvisorCommand(runtime.Grok, "grok-4.7", "low", t.TempDir(), Prompt)
	if err != nil {
		t.Fatal(err)
	}
	requireReadOnlyGrokArgv(t, argv)
}

func TestParseAnswerGrok(t *testing.T) {
	raw, err := os.ReadFile("testdata/grok/advisor-readonly-output.json")
	if err != nil {
		t.Fatal(err)
	}
	ans, u, err := ParseAnswer(runtime.Grok, raw, nil, t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ans, "server path") {
		t.Fatalf("answer = %q", ans)
	}
	if u.Input != 26986 || u.Output != 478 || u.CacheRead != 57216 || u.CacheWrite != 0 {
		t.Fatalf("usage = %+v", u)
	}
	if u.CostUSD == nil || *u.CostUSD != 0.085448 {
		t.Fatalf("cost = %v", u.CostUSD)
	}
	// the answer may arrive as .result instead of .text
	ans, _, err = ParseAnswer(runtime.Grok, []byte(`{"result":"ok","total_cost_usd":0.01}`), nil, "", "")
	if err != nil || ans != "ok" {
		t.Fatalf("ans %q err %v", ans, err)
	}
	if _, _, err := ParseAnswer(runtime.Grok, []byte(`not json`), nil, "", ""); err == nil {
		t.Fatal("garbage output must be an error")
	}
}

// grokAdvisorService is a service whose seeded session has a grok advisor and
// whose env-capable runner records what it was asked to run.
type grokRun struct {
	env  map[string]string
	argv []string
}

func grokAdvisorService(t *testing.T) (*Service, advisorSeed, *grokRun) {
	t.Helper()
	s, seed := newAdvisorService(t)
	if _, err := s.DB.ExecContext(context.Background(),
		`UPDATE agents SET advisor_kind = 'grok', advisor_model = 'grok-4.7', advisor_effort = 'low' WHERE id = ?`, seed.AgentID); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/grok/advisor-readonly-output.json")
	if err != nil {
		t.Fatal(err)
	}
	got := &grokRun{}
	s.Run = func(context.Context, string, ...string) ([]byte, error) {
		t.Error("a grok advisor must run through RunEnv so GROK_HOME reaches it")
		return nil, errors.New("plain runner used")
	}
	s.RunEnv = func(_ context.Context, env map[string]string, name string, args ...string) ([]byte, error) {
		got.env, got.argv = env, append([]string{name}, args...)
		return raw, nil
	}
	return s, seed, got
}

func TestAdvisorGrokEnvReachesRunner(t *testing.T) {
	s, seed, got := grokAdvisorService(t)
	adv, err := s.Ask(context.Background(), seed.SessionID, "Client or server?", nil, 45*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if adv.State != "answered" || !strings.Contains(adv.Answer, "server path") {
		t.Fatalf("advice = %+v", adv)
	}
	if !reflect.DeepEqual(got.env, grokhome.Env(s.Home)) {
		t.Fatalf("env = %v, want grokhome.Env(%s)", got.env, s.Home)
	}
	if got.argv[0] != "grok" {
		t.Fatalf("argv = %v", got.argv)
	}
	requireReadOnlyGrokArgv(t, got.argv)
}

func TestAdvisorGrokWithoutEnvRunnerFailsRatherThanRunningWithoutGrokHome(t *testing.T) {
	s, seed, _ := grokAdvisorService(t)
	s.RunEnv = nil
	adv, err := s.Ask(context.Background(), seed.SessionID, "q", nil, 45*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if adv.State != "failed" {
		t.Fatalf("advice = %+v, want failed", adv)
	}
}

// museArgv is the read-only form from SPIKE-52 probe 10 / decision 7.
func museArgv(dir string) []string {
	return []string{"muse", "exec", "--json", "--disable-write", "--disable-shell", "--disable-request-user-input",
		"--disable-web-tools", "--no-session-log", "--workspace", dir, "--model", "muse-wonder-1.0",
		"--reasoning-effort", "high", Prompt}
}

func TestAdvisorCommandMuse(t *testing.T) {
	dir := "/Users/u/.swarm/run/advice/adv_1"
	got, err := museAdvisorCommand("muse-wonder-1.0", "high", dir, Prompt)
	if err != nil {
		t.Fatal(err)
	}
	if want := museArgv(dir); strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
	noEffort, _ := museAdvisorCommand("muse-wonder-1.0", "", dir, Prompt)
	if strings.Contains(strings.Join(noEffort, " "), "--reasoning-effort") {
		t.Fatalf("no effort, no flag: %v", noEffort)
	}
}

// The write-attempt check for muse: the argv switches off every tool that
// writes, runs a command, asks the user or reaches the web, keeps no session
// log, and carries no flag that approves, trusts or widens permissions.
func TestAdvisorMuseReadOnly(t *testing.T) {
	argv, err := museAdvisorCommand("muse-wonder-1.0", "low", t.TempDir(), Prompt)
	if err != nil {
		t.Fatal(err)
	}
	for _, need := range []string{"--disable-write", "--disable-shell", "--disable-request-user-input",
		"--disable-web-tools", "--no-session-log"} {
		if !slices.Contains(argv, need) {
			t.Errorf("argv lacks %s: %v", need, argv)
		}
	}
	for _, bad := range []string{"--yolo", "--trust-workspace", "--approval-mode", "--permission-profile",
		"--preset", "--worktree", "-w", "--provider"} {
		if slices.Contains(argv, bad) {
			t.Errorf("argv carries %s: %v", bad, argv)
		}
	}
}

// Live proof that the installed muse still accepts every flag in the argv
// (the echo provider spends nothing and makes no tool call, so this does not
// prove the write block; CHORE-102's live write test does).
//
//	MUSE_LIVE_PROBE=1 go test ./internal/advisor/ -run TestMuseAdvisorArgvIsAcceptedByTheCLI -v
func TestMuseAdvisorArgvIsAcceptedByTheCLI(t *testing.T) {
	if os.Getenv("MUSE_LIVE_PROBE") != "1" {
		t.Skip("live probe against the real muse CLI; set MUSE_LIVE_PROBE=1 to run")
	}
	ws := t.TempDir()
	argv, _ := museAdvisorCommand("", "", ws, "hello")
	argv = append(argv[:2:2], append([]string{"--provider", "echo"}, argv[2:]...)...)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = ws
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestParseAnswerMuse(t *testing.T) {
	ok, err := os.ReadFile("testdata/muse/exec-completed.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	ans, u, err := ParseAnswer(runtime.Muse, ok, nil, t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if ans != "Use the server path." {
		t.Fatalf("answer = %q", ans)
	}
	if u != (Usage{}) {
		t.Fatalf("exec --json carries no usage; got %+v", u)
	}
	failed, err := os.ReadFile("testdata/muse/exec-failed.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ParseAnswer(runtime.Muse, failed, nil, "", ""); err == nil || !strings.Contains(err.Error(), "Subscription quota exhausted") {
		t.Fatalf("a failed run must surface its reason, got %v", err)
	}
	if _, _, err := ParseAnswer(runtime.Muse, []byte(`{"payload_type":"run.output.delta"}`+"\n"), nil, "", ""); err == nil {
		t.Fatal("a run that never reached a terminal record is an error, not an empty answer")
	}
}

// Muse stays without an advisor until CHORE-102's live write test passes (after
// 2026-10-19): the argv and parser above are built and tested, but nothing may
// run them.
func TestMuseAdvisorIsOff(t *testing.T) {
	if _, err := AdvisorCommand(runtime.Muse, "muse-wonder-1.0", "low", t.TempDir(), Prompt); err == nil {
		t.Fatal("AdvisorCommand must refuse muse while its advisor is switched off")
	}
	s, seed := newAdvisorService(t)
	if _, err := s.DB.ExecContext(context.Background(),
		`UPDATE agents SET advisor_kind = 'muse', advisor_model = 'muse-wonder-1.0' WHERE id = ?`, seed.AgentID); err != nil {
		t.Fatal(err)
	}
	s.Run = func(context.Context, string, ...string) ([]byte, error) {
		t.Error("a muse advisor must never be executed")
		return nil, errors.New("ran")
	}
	adv, err := s.Ask(context.Background(), seed.SessionID, "q", nil, 45*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if adv.State != "failed" {
		t.Fatalf("advice = %+v, want failed", adv)
	}
}
