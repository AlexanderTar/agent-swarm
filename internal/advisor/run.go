package advisor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

// Prompt is §11.6 step 3, exactly. The daemon substitutes {name}, {role}, {KEY}
// and {context_path} before running.
const Prompt = "You are the advisor for Swarm agent {name} ({role}) working on {KEY}. Read {context_path} and any files it lists. Answer the question at the end of that file: give a direct recommendation, the main risk, and what to check next. At most 250 words. Do not edit files. This request was sent by the Swarm daemon on the agent's behalf, not typed by the user."

// failureMessage is the §17.3 copy delivered to the agent.
func failureMessage(err string) string {
	return fmt.Sprintf("The advisor couldn't answer: %s. Continue with your best judgment and note the risk in your next checkpoint.", err)
}

// fillPrompt substitutes the daemon-known placeholders into Prompt.
func fillPrompt(name, role, key, contextPath string) string {
	r := strings.NewReplacer("{name}", name, "{role}", role, "{KEY}", key, "{context_path}", contextPath)
	return r.Replace(Prompt)
}

// AdvisorCommand is §11.6 step 2. Each form was run in P0-13 and blocks writes:
// claude through --allowedTools, codex through -s read-only, agy through plan
// mode, cursor through ask mode. Never change a flag without a new probe.
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
	}
	return nil, fmt.Errorf("%s can't run as a read-only advisor", kind.Display())
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
