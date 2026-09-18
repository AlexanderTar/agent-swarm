package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Claude reads Claude Code's OAuth usage endpoint (§13).
type Claude struct {
	BaseURL   string // "https://api.anthropic.com" in production
	HTTP      *http.Client
	Version   string
	ReadToken func(ctx context.Context) (token string, expiresAt time.Time, err error)
	Now       func() time.Time
	Log       func(string, ...any)
}

func (c *Claude) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Claude) logf(format string, args ...any) {
	if c.Log != nil {
		c.Log(format, args...)
	}
}

type claudeWindow struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at"`
}

type claudeLimit struct {
	Kind        string  `json:"kind"`
	Model       string  `json:"model"`
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at"`
}

type claudeUsageBody struct {
	FiveHour      *claudeWindow `json:"five_hour"`
	SevenDay      *claudeWindow `json:"seven_day"`
	SevenDayFable *claudeWindow `json:"seven_day_fable"`
	SevenDayOpus  *claudeWindow `json:"seven_day_opus"`
	Limits        []claudeLimit `json:"limits"`
}

func parseResetsAt(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Fetch is §13's Claude source. The token is read fresh every call and never
// persisted — Snapshot carries only meters, never the credential.
func (c *Claude) Fetch(ctx context.Context) (Snapshot, error) {
	token, expiresAt, err := c.ReadToken(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	if !expiresAt.After(c.now()) {
		return Snapshot{}, fmt.Errorf("claude: oauth token has expired")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/oauth/usage", nil)
	if err != nil {
		return Snapshot{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", "claude-code/"+c.Version)
	req.Header.Set("Accept", "application/json")
	resp, err := httpClientOrDefault(c.HTTP).Do(req)
	if err != nil {
		return Snapshot{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Snapshot{}, err
	}
	if resp.StatusCode != http.StatusOK {
		c.logf("usage: claude: status %d", resp.StatusCode)
		return Snapshot{}, fmt.Errorf("claude: status %d", resp.StatusCode)
	}
	var parsed claudeUsageBody
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Snapshot{}, fmt.Errorf("claude: %w", err)
	}
	var meters []Meter
	add := func(id, label string, w *claudeWindow) {
		if w == nil {
			return
		}
		meters = append(meters, Meter{ID: id, Label: label, Window: "weekly", UsedPct: w.Utilization,
			ResetsAt: parseResetsAt(w.ResetsAt)})
	}
	if parsed.FiveHour != nil {
		meters = append(meters, Meter{ID: "five_hour", Label: "5h", Window: "5h",
			UsedPct: parsed.FiveHour.Utilization, ResetsAt: parseResetsAt(parsed.FiveHour.ResetsAt)})
	}
	add("seven_day", "Weekly (all models)", parsed.SevenDay)
	add("seven_day_fable", "Fable weekly", parsed.SevenDayFable)
	add("seven_day_opus", "Opus weekly", parsed.SevenDayOpus)
	for _, l := range parsed.Limits {
		if l.Kind != "weekly_scoped" {
			continue
		}
		meters = append(meters, Meter{ID: "seven_day_" + l.Model, Label: titleCase(l.Model) + " weekly",
			Window: "weekly", UsedPct: l.Utilization, ResetsAt: parseResetsAt(l.ResetsAt)})
	}
	headline := ""
	if parsed.FiveHour != nil {
		headline = "five_hour"
	}
	return Snapshot{Meters: meters, HeadlineID: headline}, nil
}
