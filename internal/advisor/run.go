package advisor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/kinds"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

// Prompt is §11.6 step 3, aligned to the swarm-advisor skill's decision rules
// (P4, spec A4): one decision per consult, an answer grounded in what was
// actually read rather than a menu of options, and (fix round 1, Important
// finding 1) rule 5 -- when the evidence doesn't settle the decision and the
// stakes are high, the advisor says so instead of forcing a recommendation
// the evidence can't support. The daemon substitutes {name}, {role}, {KEY}
// and {context_path} before running.
const Prompt = "You are the advisor for Swarm agent {name} ({role}) working on {KEY}. Read {context_path} and any files it lists. This is one decision, not a menu: give one decisive recommendation grounded in what you actually read there, not general assumptions — the main risk, and what to check next. If what you read doesn't settle the decision and the stakes are high, say so plainly and recommend the agent take it to its parent or the user rather than guessing. At most 250 words. Do not edit files. This request was sent by the Swarm daemon on the agent's behalf, not typed by the user."

// failureMessage is the §17.3 copy delivered to the agent.
func failureMessage(err string) string {
	return fmt.Sprintf("The advisor couldn't answer: %s. Continue with your best judgment and note the risk in your next checkpoint.", err)
}

// fillPrompt substitutes the daemon-known placeholders into Prompt.
func fillPrompt(name, role, key, contextPath string) string {
	r := strings.NewReplacer("{name}", name, "{role}", role, "{KEY}", key, "{context_path}", contextPath)
	return r.Replace(Prompt)
}

// grokAdvisorMaxTurns bounds the advisor's read-and-answer loop.
const grokAdvisorMaxTurns = "8"

// AdvisorCommand is §11.6 step 2. Each form was run in P0-13 and blocks writes:
// claude through --allowedTools, codex through -s read-only, agy through plan
// mode, cursor through ask mode, grok through plan mode plus a --tools
// allowlist of read_file, grep and list_dir (SPIKE-52). Never change a flag
// without a new probe.
func AdvisorCommand(kind runtime.AgentKind, model, effort, dir, prompt string) ([]string, error) {
	switch kind {
	case runtime.Claude:
		a := []string{"claude", "-p", "--model", model}
		if effort != "" {
			a = append(a, "--effort", effort)
		}
		// the -- is required: the variadic --allowedTools otherwise eats the prompt
		return append(a, "--output-format", "json", "--allowedTools", "Read,Grep,Glob", "--", prompt), nil
	case runtime.Codex:
		a := []string{"codex", "exec", "-s", "read-only", "--skip-git-repo-check", "-c", "mcp_servers={}", "-m", model}
		if effort != "" {
			a = append(a, "-c", `model_reasoning_effort="`+effort+`"`)
		}
		return append(a, "-o", filepath.Join(dir, "answer.md"), prompt), nil
	case runtime.Agy:
		// --add-dir is required: without it plan mode denies reading the context file
		return []string{"agy", "--model", model, "--mode", "plan", "--add-dir", dir,
			"--output-format", "json", "--print", prompt}, nil
	case runtime.Cursor:
		return []string{"cursor-agent", "-p", "--trust", "--mode", "ask", "--model", model,
			"--output-format", "json", prompt}, nil
	case runtime.Muse:
		if !kinds.MuseAdvisorEnabled {
			return nil, fmt.Errorf("%s can't run as a read-only advisor yet: its write block is unproven", kind.Display())
		}
		return museAdvisorCommand(model, effort, dir, prompt)
	case runtime.Grok:
		// runs with grokhome.Env (see Service.envFor): GROK_HOME and the compat switches
		a := []string{"grok", "-p", prompt, "-m", model}
		if effort != "" {
			a = append(a, "--reasoning-effort", effort)
		}
		return append(a, "--permission-mode", "plan", "--tools", "read_file,grep,list_dir",
			"--disallowed-tools", "search_tool,use_tool", "--no-subagents",
			"--output-format", "json", "--max-turns", grokAdvisorMaxTurns, "--cwd", dir), nil
	}
	return nil, fmt.Errorf("%s can't run as a read-only advisor", kind.Display())
}

// museAdvisorCommand is the read-only `muse exec` form from SPIKE-52 probe 10:
// every tool that writes, runs a command, asks the user or reaches the web is
// switched off, no session log is kept, other agents' personal rules and
// skills and muse's reminder agents stay out, and the workspace is the advice
// dir. It runs with advisorEnv's config dir, which drops every MCP server.
func museAdvisorCommand(model, effort, dir, prompt string) ([]string, error) {
	a := []string{"muse", "exec", "--json", "--disable-write", "--disable-shell", "--disable-request-user-input",
		"--disable-web-tools", "--no-session-log", "--no-foreign-personal-context", "--disable-reminders",
		"--workspace", dir}
	if model != "" {
		a = append(a, "--model", model)
	}
	if effort != "" {
		a = append(a, "--reasoning-effort", effort)
	}
	return append(a, prompt), nil
}

// Usage is §11.6's cost tracking (D1): tokens and cost from each agent's own
// output. Only claude reports a cost.
type Usage struct {
	Input, Output, CacheRead, CacheWrite int
	CostUSD                              *float64
}

var codexTokensUsed = regexp.MustCompile(`tokens used ([\d,]+)`)

func parseCodexTokens(stderr []byte) int {
	m := codexTokensUsed.FindSubmatch(stderr)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.ReplaceAll(string(m[1]), ",", ""))
	return n
}

// ParseAnswer decodes one advisor run's output per agent kind (P0-13).
func ParseAnswer(kind runtime.AgentKind, stdout, stderr []byte, dir, userHome string) (string, Usage, error) {
	switch kind {
	case runtime.Claude:
		return parseClaudeAnswer(stdout)
	case runtime.Codex:
		return parseCodexAnswer(stderr, dir)
	case runtime.Agy:
		return parseAgyAnswer(stdout, userHome)
	case runtime.Cursor:
		return parseCursorAnswer(stdout)
	case runtime.Grok:
		return parseGrokAnswer(stdout)
	case runtime.Muse:
		return parseMuseAnswer(stdout)
	}
	return "", Usage{}, fmt.Errorf("%s has no advisor output parser", kind.Display())
}

func parseClaudeAnswer(stdout []byte) (string, Usage, error) {
	var r struct {
		Result       string  `json:"result"`
		TotalCostUSD float64 `json:"total_cost_usd"`
		Usage        struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
		ModelUsage map[string]struct {
			InputTokens              int `json:"inputTokens"`
			OutputTokens             int `json:"outputTokens"`
			CacheReadInputTokens     int `json:"cacheReadInputTokens"`
			CacheCreationInputTokens int `json:"cacheCreationInputTokens"`
		} `json:"modelUsage"`
	}
	if err := json.Unmarshal(stdout, &r); err != nil {
		return "", Usage{}, err
	}
	u := Usage{CostUSD: &r.TotalCostUSD}
	if len(r.ModelUsage) > 0 {
		// several models (e.g. a haiku sub-call next to sonnet) are summed (P0-13).
		for _, m := range r.ModelUsage {
			u.Input += m.InputTokens
			u.Output += m.OutputTokens
			u.CacheRead += m.CacheReadInputTokens
			u.CacheWrite += m.CacheCreationInputTokens
		}
	} else {
		u.Input, u.Output = r.Usage.InputTokens, r.Usage.OutputTokens
		u.CacheRead, u.CacheWrite = r.Usage.CacheReadInputTokens, r.Usage.CacheCreationInputTokens
	}
	return r.Result, u, nil
}

func parseCodexAnswer(stderr []byte, dir string) (string, Usage, error) {
	answer, err := os.ReadFile(filepath.Join(dir, "answer.md"))
	if err != nil {
		return "", Usage{}, err
	}
	total := parseCodexTokens(stderr)
	// codex reports one token total on stderr, no input/output split and no cost.
	return string(answer), Usage{Input: total}, nil
}

func parseAgyAnswer(stdout []byte, userHome string) (string, Usage, error) {
	var r struct {
		Response       string `json:"response"`
		ConversationID string `json:"conversation_id"`
		Usage          struct {
			InputTokens     int `json:"input_tokens"`
			OutputTokens    int `json:"output_tokens"`
			CacheReadTokens int `json:"cache_read_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(stdout, &r); err != nil {
		return "", Usage{}, err
	}
	u := Usage{Input: r.Usage.InputTokens, Output: r.Usage.OutputTokens, CacheRead: r.Usage.CacheReadTokens}
	answer := r.Response
	if answer == "" && r.ConversationID != "" {
		// plan mode denied the write; the answer went into the plan artifact instead (P0-13).
		p := filepath.Join(userHome, ".gemini", "antigravity-cli", "brain", r.ConversationID, "plan.md")
		if body, err := os.ReadFile(p); err == nil {
			answer = string(body)
		}
	}
	return answer, u, nil
}

func parseCursorAnswer(stdout []byte) (string, Usage, error) {
	var r struct {
		Result string `json:"result"`
		Usage  struct {
			InputTokens      int `json:"inputTokens"`
			OutputTokens     int `json:"outputTokens"`
			CacheReadTokens  int `json:"cacheReadTokens"`
			CacheWriteTokens int `json:"cacheWriteTokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(stdout, &r); err != nil {
		return "", Usage{}, err
	}
	return r.Result, Usage{Input: r.Usage.InputTokens, Output: r.Usage.OutputTokens,
		CacheRead: r.Usage.CacheReadTokens, CacheWrite: r.Usage.CacheWriteTokens}, nil
}

// parseGrokAnswer reads `grok -p --output-format json`: the answer is .text
// (or .result), usage is summed over modelUsage and the cost is total_cost_usd.
func parseGrokAnswer(stdout []byte) (string, Usage, error) {
	var r struct {
		Text         string  `json:"text"`
		Result       string  `json:"result"`
		TotalCostUSD float64 `json:"total_cost_usd"`
		ModelUsage   map[string]struct {
			InputTokens              int `json:"inputTokens"`
			OutputTokens             int `json:"outputTokens"`
			CacheReadInputTokens     int `json:"cacheReadInputTokens"`
			CacheCreationInputTokens int `json:"cacheCreationInputTokens"`
		} `json:"modelUsage"`
	}
	if err := json.Unmarshal(stdout, &r); err != nil {
		return "", Usage{}, err
	}
	u := Usage{CostUSD: &r.TotalCostUSD}
	for _, m := range r.ModelUsage {
		u.Input += m.InputTokens
		u.Output += m.OutputTokens
		u.CacheRead += m.CacheReadInputTokens
		u.CacheWrite += m.CacheCreationInputTokens
	}
	answer := r.Text
	if answer == "" {
		answer = r.Result
	}
	return answer, u, nil
}

// parseMuseAnswer reads `muse exec --json` JSONL: the last run.terminal.* record
// ends the run. "completed" carries the answer in payload.text; "failed" carries
// its reason. exec reports no token counts on stdout, so Usage is empty.
func parseMuseAnswer(stdout []byte) (string, Usage, error) {
	var last struct{ Terminal, Text, Reason string }
	found := false
	for _, line := range bytes.Split(stdout, []byte("\n")) {
		var r struct {
			PayloadType string `json:"payload_type"`
			Payload     struct {
				Terminal string  `json:"terminal"`
				Text     *string `json:"text"`
				Reason   *string `json:"reason"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &r) != nil || !strings.HasPrefix(r.PayloadType, "run.terminal.") {
			continue
		}
		found = true
		last.Terminal = r.Payload.Terminal
		last.Text, last.Reason = "", ""
		if r.Payload.Text != nil {
			last.Text = *r.Payload.Text
		}
		if r.Payload.Reason != nil {
			last.Reason = *r.Payload.Reason
		}
	}
	switch {
	case !found:
		return "", Usage{}, fmt.Errorf("muse exec ended without a terminal record")
	case last.Terminal != "completed":
		reason := last.Reason
		if reason == "" {
			reason = last.Text
		}
		return "", Usage{}, fmt.Errorf("muse run %s: %s", last.Terminal, reason)
	}
	return last.Text, Usage{}, nil
}
