package usage

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/grokhome"
)

// GrokUsage is one grok session's token and cost totals, from `grok usage
// <session id>` (live-verified on grok 1.0.60, it also works mid-session).
// Nothing records it per session yet; like MuseSessionUsage it is the reader a
// future per-session cost row would call.
type GrokUsage struct {
	InputTokens, OutputTokens int
	CachedTokens              int
	ReasoningTokens           int
	Turns                     int
	CostUSD                   float64
}

// grokCostTicksPerUSD: costUsdTicks is USD x 1e10.
const grokCostTicksPerUSD = 1e10

type grokUsageDoc struct {
	Session *struct {
		InputTokens      int   `json:"inputTokens"`
		OutputTokens     int   `json:"outputTokens"`
		CachedReadTokens int   `json:"cachedReadTokens"`
		ReasoningTokens  int   `json:"reasoningTokens"`
		TurnCount        int   `json:"turnCount"`
		CostUsdTicks     int64 `json:"costUsdTicks"`
	} `json:"session"`
}

// ParseGrokSessionUsage reads the session totals of a `grok usage` document. A
// document with no session block is an error, never an empty success.
func ParseGrokSessionUsage(raw []byte) (GrokUsage, error) {
	var doc grokUsageDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return GrokUsage{}, err
	}
	s := doc.Session
	if s == nil {
		return GrokUsage{}, fmt.Errorf("grok usage has no session block")
	}
	return GrokUsage{InputTokens: s.InputTokens, OutputTokens: s.OutputTokens, CachedTokens: s.CachedReadTokens,
		ReasoningTokens: s.ReasoningTokens, Turns: s.TurnCount, CostUSD: float64(s.CostUsdTicks) / grokCostTicksPerUSD}, nil
}

// GrokSessionUsage runs `grok usage <sessionID>` under the Swarm grok home
// (without GROK_HOME grok reads ~/.grok and finds no Swarm session).
func GrokSessionUsage(ctx context.Context, run execx.RunnerEnv, swarmHome, sessionID string) (GrokUsage, error) {
	out, err := run(ctx, grokhome.Env(swarmHome), grokhome.Bin, "usage", sessionID)
	if err != nil {
		return GrokUsage{}, err
	}
	return ParseGrokSessionUsage(out)
}
