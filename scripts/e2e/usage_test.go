//go:build e2e

package e2e

import (
	"net/http"
	"testing"
)

// Scenario 17: usage, read side only. §23.1 note (Task 37): with SWARM_USAGE
// unset (S-4), this daemon builds no usage sources at all, so there is
// nothing to stub an HTTP response for — the fetch side (the four parsers,
// keychain seam, jitter, staleness) is Task 24's own httptest-based tests,
// which a Go test against a *separate daemon process* cannot reach. This
// scenario asserts what the read side actually does under S-4: a kind with
// no source gets 202 on refresh (not 500 — a real gap this batch fixed,
// see internal/usage/usage.go's recordAttemptOnly) and a second refresh
// inside 60s is 429; §23.4's "all four agents show live meters" manual
// check is what closes the remaining gap.
func TestScenario17Usage(t *testing.T) {
	h := newHarness(t)

	// Before any refresh, a kind that was never touched has no row at all —
	// GET /api/usage only ever returns what's actually in usage_snapshots.
	var before []map[string]any
	h.doT(t, http.MethodGet, "/api/usage", nil, &before)
	for _, u := range before {
		if u["agent"] == "cursor" {
			t.Skip("cursor already has a usage row from an earlier test run against this daemon")
		}
	}

	status, raw, err := h.do(http.MethodPost, "/api/usage/refresh", map[string]any{"agent": "cursor"})
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusAccepted {
		t.Fatalf("refresh (no source) = %d %s, want 202", status, raw)
	}

	var after []map[string]any
	h.doT(t, http.MethodGet, "/api/usage", nil, &after)
	var row map[string]any
	for _, u := range after {
		if u["agent"] == "cursor" {
			row = u
		}
	}
	if row == nil {
		t.Fatalf("after a refresh attempt, cursor should have a row: %+v", after)
	}
	if meters, _ := row["meters"].([]any); len(meters) != 0 {
		t.Errorf("meters = %v, want empty (no source ever fetched)", meters)
	}
	// fetched_at is a plain int64 on the wire (usageWire), not a pointer: 0
	// means "never fetched" rather than an explicit JSON null.
	if fa, _ := row["fetched_at"].(float64); fa != 0 {
		t.Errorf("fetched_at = %v, want 0 (never fetched)", row["fetched_at"])
	}
	if row["error"] != nil {
		t.Errorf("error = %v, want null (no source is not a fetch failure)", row["error"])
	}
	if row["stale"] != true {
		t.Errorf("stale = %v, want true", row["stale"])
	}

	// a second refresh inside 60s is 429 limit_reached
	status, raw, err = h.do(http.MethodPost, "/api/usage/refresh", map[string]any{"agent": "cursor"})
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusTooManyRequests {
		t.Fatalf("second refresh = %d %s, want 429", status, raw)
	}
}
