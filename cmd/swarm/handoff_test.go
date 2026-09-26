package main

import (
	"bytes"
	"strings"
	"testing"
)

// Batch 3: `swarm handoff NAME` posts {request_id} and prints the accepted
// operation with its phase — never "completed" on a 202 accept.
func TestHandoffCommandPrintsAcceptedPhase(t *testing.T) {
	srv, got, home := stubDaemon(t, map[string]string{
		"POST /api/agents/login-coder/handoff": `{"operation_id":"op_1","agent":"login-coder","mode":"handoff","phase":"stopping"}`,
	})
	defer srv.Close()
	var out bytes.Buffer
	if code := run([]string{"handoff", "--home", home, "--url", srv.URL, "login-coder"}, &out, &out); code != 0 {
		t.Fatalf("exit = %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "ccepted") || !strings.Contains(out.String(), "stopping") {
		t.Fatalf("output = %q, want accepted + phase", out.String())
	}
	if strings.Contains(strings.ToLower(out.String()), "complet") {
		t.Fatalf("output = %q, must never claim completed on a 202", out.String())
	}
	body := lastBody(got, "/api/agents/login-coder/handoff")
	if !strings.Contains(body, "request_id") {
		t.Fatalf("request body = %q, want a request_id", body)
	}
}

func TestHandoffCommandNeedsAName(t *testing.T) {
	srv, _, home := stubDaemon(t, nil)
	defer srv.Close()
	var errBuf bytes.Buffer
	if code := run([]string{"handoff", "--home", home, "--url", srv.URL}, &bytes.Buffer{}, &errBuf); code != 2 {
		t.Fatalf("exit = %d, want usage error 2", code)
	}
}
