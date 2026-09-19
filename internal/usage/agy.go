package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// Agy reads the agy CLI's local quota endpoint, after refreshing it once via
// `agy models` (P0-5).
type Agy struct {
	BaseURL  string
	HTTP     *http.Client
	UserHome string
	Run      execx.Runner
	Now      func() time.Time
	Log      func(string, ...any)
}

func (a *Agy) logf(format string, args ...any) {
	if a.Log != nil {
		a.Log(format, args...)
	}
}

type agyBucket struct {
	Group             string   `json:"group"`
	Window            string   `json:"window"`
	RemainingFraction *float64 `json:"remainingFraction"`
	Disabled          bool     `json:"disabled"`
}

type agyQuota struct {
	Buckets []agyBucket `json:"buckets"`
}

var agyGroups = map[string]struct{ id, label string }{
	"Gemini Models":         {"gemini", "Gemini"},
	"Claude and GPT models": {"cgpt", "Claude & GPT"},
}

// ParseAgyQuota is §13's rule: used_pct = (1 - remainingFraction) × 100, a
// missing fraction counts as 0 remaining (fully used), and a disabled bucket
// is skipped. The headline is the busier of the 5h buckets.
func ParseAgyQuota(body []byte) ([]Meter, string, error) {
	var q agyQuota
	if err := json.Unmarshal(body, &q); err != nil {
		return nil, "", fmt.Errorf("agy: %w", err)
	}
	var meters []Meter
	headline := ""
	var headlineUsed float64 = -1
	for _, b := range q.Buckets {
		if b.Disabled {
			continue
		}
		group, ok := agyGroups[b.Group]
		if !ok {
			group = struct{ id, label string }{b.Group, b.Group}
		}
		remaining := 0.0
		if b.RemainingFraction != nil {
			remaining = *b.RemainingFraction
		}
		usedPct := (1 - remaining) * 100
		id := group.id + "_" + b.Window
		meters = append(meters, Meter{ID: id, Label: group.label + " " + b.Window,
			Window: b.Window, UsedPct: usedPct})
		if b.Window == "5h" && usedPct > headlineUsed {
			headline, headlineUsed = id, usedPct
		}
	}
	return meters, headline, nil
}

// Fetch refreshes agy's local quota cache, then reads it. A failing refresh
// is not fatal (§13): the endpoint may still hold usable numbers.
func (a *Agy) Fetch(ctx context.Context) ([]Meter, string, error) {
	if a.Run != nil {
		if _, err := a.Run(ctx, "agy", "models"); err != nil {
			a.logf("usage: agy models refresh: %v", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.BaseURL+"/api/quota", nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := httpClientOrDefault(a.HTTP).Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("agy: status %d", resp.StatusCode)
	}
	return ParseAgyQuota(body)
}
