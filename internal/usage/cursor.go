package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Cursor reads Cursor's dashboard usage endpoint (§13). It never calls the
// forbidden UseSandBankedReset endpoint.
type Cursor struct {
	BaseURL   string
	HTTP      *http.Client
	ReadToken func(ctx context.Context) (token string, expiresAt time.Time, err error)
	Now       func() time.Time
	Log       func(string, ...any)
}

func (c *Cursor) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Cursor) logf(format string, args ...any) {
	if c.Log != nil {
		c.Log(format, args...)
	}
}

const cursorMinTokenLife = 60 * time.Second

type cursorPlanUsage struct {
	AutoPercentUsed float64 `json:"autoPercentUsed"`
	APIPercentUsed  float64 `json:"apiPercentUsed"`
}

type cursorBody struct {
	PlanUsage       cursorPlanUsage `json:"planUsage"`
	BillingCycleEnd string          `json:"billingCycleEnd"`
}

// parseCursorResetsAt reads billingCycleEnd, which the real endpoint sends
// as a quoted epoch-milliseconds string (Connect-RPC's JSON encoding of a
// protobuf int64), not RFC3339 — confirmed live against api2.cursor.sh:
// {"billingCycleStart":"1787303346000","billingCycleEnd":"1789981746000",...},
// where billingCycleEnd decoded to 2 days after the probe and
// billingCycleStart to 31 days before it, matching a monthly cycle.
func parseCursorResetsAt(s string) *time.Time {
	if s == "" {
		return nil
	}
	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil
	}
	t := time.UnixMilli(ms)
	return &t
}

// ParseCursorUsage is §13's rule: one meter for the auto pool, one for API,
// both resetting at billingCycleEnd.
func ParseCursorUsage(body []byte) ([]Meter, string, error) {
	var b cursorBody
	if err := json.Unmarshal(body, &b); err != nil {
		return nil, "", fmt.Errorf("cursor: %w", err)
	}
	resetsAt := parseCursorResetsAt(b.BillingCycleEnd)
	meters := []Meter{
		{ID: "cursor_auto", Label: "Monthly Auto", Window: "monthly", UsedPct: b.PlanUsage.AutoPercentUsed, ResetsAt: resetsAt},
		{ID: "cursor_api", Label: "Monthly API", Window: "monthly", UsedPct: b.PlanUsage.APIPercentUsed, ResetsAt: resetsAt},
	}
	return meters, "cursor_auto", nil
}

// Fetch is §13's Cursor source. A JWT with under 60 s left is treated as
// stale and refused before any request goes out.
func (c *Cursor) Fetch(ctx context.Context) (Snapshot, error) {
	token, expiresAt, err := c.ReadToken(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	if expiresAt.Sub(c.now()) < cursorMinTokenLife {
		return Snapshot{}, fmt.Errorf("cursor: token has under %s left", cursorMinTokenLife)
	}
	// The real endpoint is Connect-RPC-over-JSON: it 415s a request with no
	// Content-Type and no body, even for a no-argument RPC like this one.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/aiserver.v1.DashboardService/GetCurrentPeriodUsage", strings.NewReader("{}"))
	if err != nil {
		return Snapshot{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
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
		c.logf("usage: cursor: status %d", resp.StatusCode)
		return Snapshot{}, fmt.Errorf("cursor: status %d", resp.StatusCode)
	}
	meters, headline, err := ParseCursorUsage(body)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Meters: meters, HeadlineID: headline}, nil
}
