package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScenarioRunsStepsInOrderAndSubstitutes(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
			ID json.RawMessage `json:"id"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		calls = append(calls, req.Params.Name+" "+string(req.Params.Arguments))
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text",
			"text":"{\"checkpoint_id\":\"ckp_1\",\"artifact_id\":\"art_1\"}"}]}}`))
	}))
	defer srv.Close()
	dir := t.TempDir()
	// t.Setenv, not os.Setenv: it restores the old value automatically and fails the
	// test if it is ever used with t.Parallel, which os.Setenv + defer does not.
	t.Setenv("SPIKE", "SPIKE-1")
	sc := Scenario{Steps: []Step{
		{Tool: "swarm_checkpoint", Args: json.RawMessage(`{"kind":"accepted","summary":"go"}`)},
		{WriteFile: &WriteFile{Path: filepath.Join(dir, "spec.md"), Body: "# Spec\n\n## One\n\na\n"}},
		{Tool: "swarm_artifact", Args: json.RawMessage(`{"op":"register","item":"${SPIKE}","kind":"spec","path":"` +
			filepath.Join(dir, "spec.md") + `"}`), Save: "spec"},
	}}
	r := &Runner{URL: srv.URL, Token: "t", Out: io.Discard}
	if err := r.Run(context.Background(), sc); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %v", calls)
	}
	if !strings.Contains(calls[1], `"item":"SPIKE-1"`) {
		t.Fatalf("substitution failed: %s", calls[1])
	}
	if _, err := os.Stat(filepath.Join(dir, "spec.md")); err != nil {
		t.Fatal(err)
	}
	if r.Saved["spec"]["artifact_id"] != "art_1" {
		t.Fatalf("saved = %v", r.Saved)
	}
}

func TestExpectErrorStepFailsWhenTheCallSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{}"}]}}`))
	}))
	defer srv.Close()
	r := &Runner{URL: srv.URL, Token: "t", Out: io.Discard}
	err := r.Run(context.Background(), Scenario{Steps: []Step{
		{Tool: "swarm_spawn", Args: json.RawMessage(`{}`), ExpectError: "dependencies_open"},
	}})
	if err == nil {
		t.Fatal("expect_error must fail when the call succeeds")
	}
}

// The pane drawing is what the idle-wake rules read.
func TestDrawModes(t *testing.T) {
	for _, c := range []struct{ mode, want string }{
		{"idle", "❯ "},
		{"busy", "✽"},
		{"typed", "half typed"},
	} {
		var out strings.Builder
		drawPane(&out, c.mode)
		if !strings.Contains(out.String(), c.want) {
			t.Errorf("%s pane = %q", c.mode, out.String())
		}
	}
}

func TestWaitForTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text",
			"text":"{\"messages\":[],\"more\":false}"}]}}`))
	}))
	defer srv.Close()
	r := &Runner{URL: srv.URL, Token: "t", Out: io.Discard}
	err := r.Run(context.Background(), Scenario{Steps: []Step{
		{WaitFor: &WaitFor{MessageKind: "approval_result", TimeoutSec: 1}},
	}})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v", err)
	}
}
