package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type capture struct {
	method, path, body, via string
}

// writeToken drops a daemon token into home/run so newClient can read it.
func writeToken(t *testing.T, home, tok string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "run", "daemon.token"), []byte(tok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// lastBody returns the body of the last captured request to path, or "".
func lastBody(got *[]capture, path string) string {
	for i := len(*got) - 1; i >= 0; i-- {
		if (*got)[i].path == path {
			return (*got)[i].body
		}
	}
	return ""
}

// lastPath returns the last captured request's path (no query string).
func lastPath(got *[]capture) string {
	if len(*got) == 0 {
		return ""
	}
	return (*got)[len(*got)-1].path
}

// stubDaemon answers each path with a canned body and records the request.
func stubDaemon(t *testing.T, routes map[string]string) (*httptest.Server, *[]capture, string) {
	t.Helper()
	var got []capture
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, capture{r.Method, r.URL.RequestURI(), string(b), r.Header.Get("X-Swarm-Via")})
		body, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			w.WriteHeader(404)
			w.Write([]byte(`{"error":{"code":"not_found","message":"Unknown API route."}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	home := t.TempDir()
	writeToken(t, home, "tok")
	return srv, &got, home
}

func TestNewSpike(t *testing.T) {
	srv, got, home := stubDaemon(t, map[string]string{
		"POST /api/spikes": `{"item":{"key":"SPIKE-3","title":"Login crash"},
			"agent":{"name":"login-crash","state":"active","session":{"state":"spawning"}},"queued":false}`,
	})
	defer srv.Close()
	var out bytes.Buffer
	code := run([]string{"new", "--home", home, "--url", srv.URL, "--name", "Login crash",
		"--intent", "debug", "--agent", "claude", "--model", "opus",
		"--request", "Users see a crash."}, &out, &out)
	if code != 0 {
		t.Fatalf("code = %d: %s", code, out.String())
	}
	c := (*got)[0]
	if c.method != "POST" || c.path != "/api/spikes" || c.via != "cli" {
		t.Fatalf("request = %+v", c)
	}
	var body map[string]any
	json.Unmarshal([]byte(c.body), &body)
	if body["name"] != "Login crash" || body["intent"] != "debug" || body["request"] == nil {
		t.Fatalf("body = %s", c.body)
	}
	if body["request_id"] == nil {
		t.Error("the CLI must send a request_id so a retry is idempotent (W8)")
	}
	if !strings.Contains(out.String(), "SPIKE-3") || !strings.Contains(out.String(), "login-crash") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestNewRequiresNameAndIntent(t *testing.T) {
	srv, _, home := stubDaemon(t, nil)
	defer srv.Close()
	var out bytes.Buffer
	if code := run([]string{"new", "--home", home, "--url", srv.URL, "--name", "x"}, &out, &out); code == 0 {
		t.Fatal("--intent is required")
	}
	if code := run([]string{"new", "--home", home, "--url", srv.URL,
		"--intent", "sideways", "--name", "x"}, &out, &out); code == 0 {
		t.Fatal("an unknown intent must be refused before the request")
	}
}

func TestPauseVariants(t *testing.T) {
	srv, got, home := stubDaemon(t, map[string]string{
		"POST /api/agents/coder-1/pause": `{"name":"coder-1","state":"active"}`,
		"POST /api/pause-all":            `{"requested":3}`,
	})
	defer srv.Close()
	var out bytes.Buffer
	run([]string{"pause", "--home", home, "--url", srv.URL, "coder-1"}, &out, &out)
	if body := (*got)[0].body; !strings.Contains(body, `"scope":"session"`) {
		t.Fatalf("body = %s", body)
	}
	run([]string{"pause", "--home", home, "--url", srv.URL, "--group", "coder-1"}, &out, &out)
	if body := (*got)[1].body; !strings.Contains(body, `"scope":"subtree"`) {
		t.Fatalf("--group body = %s", body)
	}
	out.Reset()
	run([]string{"pause", "--home", home, "--url", srv.URL, "--all"}, &out, &out)
	if (*got)[2].path != "/api/pause-all" {
		t.Fatalf("path = %s", (*got)[2].path)
	}
	if !strings.Contains(out.String(), "3") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestRequestActions(t *testing.T) {
	srv, got, home := stubDaemon(t, map[string]string{
		"POST /api/requests/req_1/answer":          `{"id":"req_1","state":"answered"}`,
		"POST /api/requests/req_1/approve":         `{"id":"req_1","state":"approved"}`,
		"POST /api/requests/req_1/request-changes": `{"id":"req_1","state":"changes_requested"}`,
		"POST /api/requests/req_1/confirm-repos":   `{"id":"req_1","state":"approved"}`,
		"GET /api/repos":                           `{"recent":[],"groups":[],"all":[{"id":"repo_1","name":"chat","path":"/r/chat"}],"scanned_at":0,"scanning":false}`,
		"GET /api/requests":                        `[{"id":"req_1","kind":"question","item_key":"SPIKE-3","prompt":"Keep the email?","state":"open"}]`,
	})
	defer srv.Close()
	var out bytes.Buffer
	run([]string{"answer", "--home", home, "--url", srv.URL, "req_1", "Yes, keep it."}, &out, &out)
	if body := lastBody(got, "/api/requests/req_1/answer"); !strings.Contains(body, `"via":"cli"`) ||
		!strings.Contains(body, "Yes, keep it.") {
		t.Fatalf("answer body = %s", body)
	}
	run([]string{"approve", "--home", home, "--url", srv.URL, "req_1"}, &out, &out)
	run([]string{"request-changes", "--home", home, "--url", srv.URL, "req_1", "Split section 2."}, &out, &out)
	if body := lastBody(got, "/api/requests/req_1/request-changes"); !strings.Contains(body, "Split section 2.") {
		t.Fatalf("request-changes body = %s", body)
	}
	// confirm-repos takes paths and resolves them to ids through /api/repos
	run([]string{"confirm-repos", "--home", home, "--url", srv.URL, "req_1", "/r/chat",
		"--comment", "just this one"}, &out, &out)
	body := lastBody(got, "/api/requests/req_1/confirm-repos")
	if !strings.Contains(body, `"repo_1"`) || !strings.Contains(body, "just this one") {
		t.Fatalf("confirm-repos body = %s", body)
	}
	if !strings.Contains(body, `"repos_version"`) {
		t.Error("confirm-repos must send the version it saw (I13)")
	}
	out.Reset()
	run([]string{"requests", "--home", home, "--url", srv.URL}, &out, &out)
	if !strings.Contains(out.String(), "req_1") || !strings.Contains(out.String(), "Keep the email?") {
		t.Fatalf("requests output = %q", out.String())
	}
}

// TestApproveSendsTheBindingBack is the regression for a bug this batch
// introduced and the review round caught: cmdApprove used to post only
// {"via":"cli"}, never the request's own section_sha256/artifact_revision/
// binding — fields Approve's server-side check (§7.2) is unconditional on
// whenever the request carries them, so an accept_epic/accept_fix approval
// (or an approve_section/approve_plan whose section has a hash) 409ed every
// time, unconditionally. The earlier TestRequestActions couldn't catch this:
// its stub returns {"state":"approved"} no matter what body it receives.
// This one actually inspects the POST body and fails the test if the
// binding it fetched isn't echoed back.
func TestApproveSendsTheBindingBack(t *testing.T) {
	var approveBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/requests":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"id":"req_1","kind":"accept_epic","item_key":"EPIC-1","prompt":"Accept it.",
				"state":"open","binding":{"item_revision":7,"integrated_checkpoint":"ckp_1","git":[]}}]`))
		case r.Method == "POST" && r.URL.Path == "/api/requests/req_1/approve":
			b, _ := io.ReadAll(r.Body)
			approveBody = string(b)
			var body map[string]any
			json.Unmarshal(b, &body)
			if body["binding"] == nil {
				w.WriteHeader(409)
				w.Write([]byte(`{"error":{"code":"conflict","message":"This request changed. Review the latest version."}}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"req_1","state":"approved"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	home := t.TempDir()
	writeToken(t, home, "tok")
	var out bytes.Buffer
	code := run([]string{"approve", "--home", home, "--url", srv.URL, "req_1"}, &out, &out)
	if code != 0 {
		t.Fatalf("code = %d: %s (approve body sent: %s)", code, out.String(), approveBody)
	}
	var sent map[string]any
	json.Unmarshal([]byte(approveBody), &sent)
	if sent["binding"] == nil {
		t.Fatalf("approve body = %s, want a binding field echoed back", approveBody)
	}
}

func TestAgentsPrintsATree(t *testing.T) {
	srv, _, home := stubDaemon(t, map[string]string{
		"GET /api/agents": `[{"name":"auth-orchestrator","role":"orchestrator","state":"active",
			"item_key":"EPIC-12","session":{"state":"running","waiting":false,"stale":false},
			"children":[{"name":"login-form-coder","role":"coder","state":"active","item_key":"TASK-101",
			"session":{"state":"running","waiting":true,"stale":false},"children":[],"finished":[]}],
			"finished":[]}]`,
	})
	defer srv.Close()
	var out bytes.Buffer
	run([]string{"agents", "--home", home, "--url", srv.URL}, &out, &out)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) < 2 || !strings.Contains(lines[0], "auth-orchestrator") {
		t.Fatalf("output = %q", out.String())
	}
	if !strings.HasPrefix(lines[1], "  ") && !strings.Contains(lines[1], "└") {
		t.Fatalf("children are indented: %q", lines[1])
	}
	// §17.1: the Waiting label
	if !strings.Contains(out.String(), "Waiting") {
		t.Fatalf("a waiting session shows the Waiting label: %q", out.String())
	}
}

// swarm attach execs tmux; the test injects the runner. The socket comes from the
// response's tmux_socket, never from a literal (S-1), so the stub supplies one and
// the assertion is that the argv carries *that* value.
//
// Two traps this test is written to avoid. The stub must include tmux_socket — omit
// it and the argv is `tmux -L  attach`, which no assertion here would explain. And
// the check must not be `Contains(joined, "-L swarm")`: "swarm" is a prefix of
// "swarm-test-1234", so that assertion passes for the production socket, which is
// the one thing S-1 exists to prevent. Compare the whole argument.
func TestAttachRunsTmuxAttachOnTheDaemonsOwnSocket(t *testing.T) {
	const socket = "swarm-test-4242"
	srv, _, home := stubDaemon(t, map[string]string{
		"POST /api/agents/coder-1/terminal": `{"tmux":"coder-1","tmux_socket":"` + socket +
			`","opened_by":"menubar"}`,
	})
	defer srv.Close()
	var argv []string
	execCommand = func(name string, args ...string) *exec.Cmd {
		argv = append([]string{name}, args...)
		return exec.Command("true")
	}
	defer func() { execCommand = exec.Command }()
	var out bytes.Buffer
	if code := run([]string{"attach", "--home", home, "--url", srv.URL, "coder-1"}, &out, &out); code != 0 {
		t.Fatalf("code = %d: %s", code, out.String())
	}
	want := []string{"tmux", "-L", socket, "attach", "-t", "coder-1"}
	if !slices.Equal(argv, want) {
		t.Fatalf("argv = %v, want %v", argv, want)
	}
	// Exact-match already rules it out; assert it separately so the failure message
	// names the invariant rather than a diff.
	if slices.Contains(argv, "swarm") || slices.Contains(argv, "swarm-dev") {
		t.Fatalf("attach must never reach the production tmux server: %v", argv)
	}
}

// And a response with no tmux_socket is a daemon too old to attach to, not a
// silent `-L ""`.
func TestAttachRefusesAResponseWithNoSocket(t *testing.T) {
	srv, _, home := stubDaemon(t, map[string]string{
		"POST /api/agents/coder-1/terminal": `{"tmux":"coder-1","opened_by":"menubar"}`,
	})
	defer srv.Close()
	execCommand = func(name string, args ...string) *exec.Cmd {
		t.Fatalf("nothing may be executed without a socket: %v", append([]string{name}, args...))
		return nil
	}
	defer func() { execCommand = exec.Command }()
	var out bytes.Buffer
	if code := run([]string{"attach", "--home", home, "--url", srv.URL, "coder-1"}, &out, &out); code == 0 {
		t.Fatal("attach must fail when the daemon returns no tmux_socket")
	}
}

func TestUsageCommandPrintsMeters(t *testing.T) {
	srv, got, home := stubDaemon(t, map[string]string{
		"GET /api/usage": `[{"agent":"claude","meters":[{"id":"five_hour","label":"5h","window":"5h",
			"used_pct":42.5,"resets_at":null}],"headline_id":"five_hour","source":"oauth","error":null,
			"fetched_at":1789651920000,"attempted_at":1789651920000,"stale":false}]`,
		"POST /api/usage/refresh": `{}`,
	})
	defer srv.Close()
	var out bytes.Buffer
	run([]string{"usage", "--home", home, "--url", srv.URL}, &out, &out)
	if !strings.Contains(out.String(), "42") || !strings.Contains(out.String(), "5h") {
		t.Fatalf("output = %q", out.String())
	}
	run([]string{"usage", "--home", home, "--url", srv.URL, "--refresh"}, &out, &out)
	if lastPath(got) != "/api/usage" {
		t.Fatalf("--refresh posts the refresh then re-reads: %v", *got)
	}
}

// An API error is printed as its message, with exit code 1.
func TestErrorsArePrintedAsTheDaemonsMessage(t *testing.T) {
	srv, _, home := stubDaemon(t, nil) // every route 404s
	defer srv.Close()
	var out, errOut bytes.Buffer
	code := run([]string{"resume", "--home", home, "--url", srv.URL, "nope"}, &out, &errOut)
	if code != 1 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(errOut.String(), "Unknown API route.") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

// start isn't in the brief's Step 1 test file, but it is a Produces command with
// no other coverage; a minimal happy-path test keeps it from being untested.
func TestStartOrchestrator(t *testing.T) {
	srv, got, home := stubDaemon(t, map[string]string{
		"POST /api/items/EPIC-12/orchestrator": `{"name":"auth-orchestrator","state":"active"}`,
	})
	defer srv.Close()
	var out bytes.Buffer
	code := run([]string{"start", "--home", home, "--url", srv.URL, "EPIC-12",
		"--agent", "claude", "--model", "opus"}, &out, &out)
	if code != 0 {
		t.Fatalf("code = %d: %s", code, out.String())
	}
	body := (*got)[0].body
	if !strings.Contains(body, `"agent":"claude"`) || !strings.Contains(body, `"model":"opus"`) {
		t.Fatalf("body = %s", body)
	}
	if !strings.Contains(out.String(), "auth-orchestrator") {
		t.Fatalf("output = %q", out.String())
	}
}
