//go:build e2e

package e2e

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Scenario 2: change request and stale approval. Two sections are approved;
// the third is left open when the agent revises the spec (changing that
// section's text and hash) — RegisterArtifact's staleApprovals marks any
// still-*open* approval whose section changed as stale (a request already
// resolved, approved or changes_requested, is untouched; this is what makes
// "leave it open" the right setup rather than routing it through
// request-changes first). Materializing then fails with approval_missing,
// and approving the now-stale request at all — even with the hash it was
// asked against — is refused: resolve()'s own state guard treats anything
// but "open" as "Already resolved." (409).
func TestScenario02ChangeRequestAndStaleApproval(t *testing.T) {
	h := newHarness(t)
	var spikeResp map[string]any
	h.doT(t, http.MethodPost, "/api/spikes", map[string]any{
		"request_id": "req-" + unique(), "name": "Stale approval " + unique(), "intent": "feature",
		"agent": "fake", "model": "fake-1",
	}, &spikeResp)
	item, _ := spikeResp["item"].(map[string]any)
	agent, _ := spikeResp["agent"].(map[string]any)
	spikeKey, _ := item["key"].(string)
	orch, _ := agent["name"].(string)
	h.mustTool(t, orch, "swarm_checkpoint", map[string]any{"kind": "accepted", "summary": "starting"})

	specPath := filepath.Join(t.TempDir(), "spec.md")
	body1 := "# Spec\n\n## One\n\na\n\n## Two\n\noriginal text\n\n## Three\n\nc\n"
	if err := os.WriteFile(specPath, []byte(body1), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := h.mustTool(t, orch, "swarm_artifact", map[string]any{"op": "register", "item": spikeKey, "kind": "spec", "path": specPath})
	specID, _ := spec["artifact_id"].(string)
	sections, _ := spec["sections"].([]any)
	if len(sections) != 3 {
		t.Fatalf("sections = %d, want 3", len(sections))
	}
	var section2ID string
	for _, s := range sections {
		sec, _ := s.(map[string]any)
		h.mustTool(t, orch, "swarm_ask", map[string]any{
			"kind": "approval", "prompt": "Review \"" + sec["title"].(string) + "\"",
			"artifact": specID, "section": sec["id"],
		})
		if sec["title"] == "Two" {
			section2ID, _ = sec["id"].(string)
		}
	}
	if section2ID == "" {
		t.Fatal("section \"Two\" not found")
	}

	// approve One and Three; leave Two open
	for _, title := range []string{"One", "Three"} {
		req := findRequestBySection(t, h, spikeKey, "approve_section", title)
		h.doT(t, http.MethodPost, "/api/requests/"+req["id"].(string)+"/approve", map[string]any{
			"section_sha256": req["section_sha256"], "artifact_revision": req["artifact_revision"], "via": "board",
		}, nil)
	}
	twoReq := findRequestBySection(t, h, spikeKey, "approve_section", "Two")
	oldSHA := twoReq["section_sha256"]

	// the agent revises: section Two's text (and hash) changes
	body2 := "# Spec\n\n## One\n\na\n\n## Two\n\nrevised text after feedback\n\n## Three\n\nc\n"
	if err := os.WriteFile(specPath, []byte(body2), 0o644); err != nil {
		t.Fatal(err)
	}
	h.mustTool(t, orch, "swarm_artifact", map[string]any{"op": "revise", "item": spikeKey, "kind": "spec", "path": specPath})

	var state string
	if err := h.db(t).QueryRow(`SELECT state FROM requests WHERE id = ?`, twoReq["id"]).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "stale" {
		t.Fatalf("section Two's request state = %q, want stale", state)
	}

	// a plan (unapproved) so materialize gets past the "needs spec and plan"
	// shape check and actually reaches the section-approval check
	planBody := "# Plan\n\n## Work breakdown\n\n```swarm-tree\n" +
		`{"root":{"type":"epic","title":"Ship it","brief":"","acceptance":["It works."]},
 "children":[{"ref":"s1","type":"story","title":"S","brief":"","acceptance":[],
   "children":[{"ref":"t1","type":"task","title":"T","brief":"","acceptance":[],"role_hint":"coder","repos":[],"workflow":{"template":"tdd-reviewed"},"steps":["Write test","Implement"],"verify":["go test ./..."],"solo":"focused"}]}]}` +
		"\n```\n\n## Verification\n\ngo test ./...\n"
	planPath := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planPath, []byte(planBody), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := h.mustTool(t, orch, "swarm_artifact", map[string]any{"op": "register", "item": spikeKey, "kind": "plan", "path": planPath})
	planID, _ := plan["artifact_id"].(string)

	_, err := h.toolOut(t, orch, "swarm_materialize", map[string]any{"spike": spikeKey, "spec": specID, "plan": planID})
	if err == nil || !strings.Contains(err.Error(), "approval_missing") {
		t.Fatalf("materialize err = %v, want approval_missing", err)
	}

	// approving the stale request at all is refused, even with the hash it
	// was originally asked against
	status, raw, err := h.do(http.MethodPost, "/api/requests/"+twoReq["id"].(string)+"/approve",
		map[string]any{"section_sha256": oldSHA, "artifact_revision": twoReq["artifact_revision"], "via": "board"})
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusConflict {
		t.Fatalf("approving a stale request = %d %s, want 409", status, raw)
	}
}

func findRequestBySection(t *testing.T, h *harness, itemKey, kind, sectionTitle string) map[string]any {
	t.Helper()
	var list []map[string]any
	h.doT(t, http.MethodGet, "/api/requests", nil, &list)
	for _, r := range list {
		if r["item_key"] != itemKey || r["kind"] != kind || r["state"] != "open" {
			continue
		}
		if body, _ := r["prompt"].(string); body == `Review "`+sectionTitle+`"` {
			return r
		}
	}
	t.Fatalf("no open %s request for section %q on %s", kind, sectionTitle, itemKey)
	return nil
}
