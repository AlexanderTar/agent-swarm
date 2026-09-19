package hook

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// envFunc builds the os.Getenv stand-in from a map.
func envFunc(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func tokenFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ses_1")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// P0-10: a session the user started by hand must be unaffected.
func TestRunIsSilentWithoutSwarmSession(t *testing.T) {
	var out bytes.Buffer
	start := time.Now()
	code := Run([]string{"claude", "Stop"}, strings.NewReader(`{"session_id":"x"}`), &out,
		envFunc(map[string]string{}), nil)
	if code != 0 || out.Len() != 0 {
		t.Fatalf("code = %d, out = %q", code, out.String())
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("took %v; §23.2 scenario 15 requires under 50 ms", d)
	}
}

func TestRunIsSilentWhenTheTokenFileIsGone(t *testing.T) {
	var out bytes.Buffer
	code := Run([]string{"claude", "Stop"}, strings.NewReader("{}"), &out,
		envFunc(map[string]string{"SWARM_SESSION": "ses_1",
			"SWARM_TOKEN_FILE": filepath.Join(t.TempDir(), "missing"),
			"SWARM_URL":        "http://127.0.0.1:1"}), nil)
	if code != 0 || out.Len() != 0 {
		t.Fatalf("code = %d, out = %q", code, out.String())
	}
}

func TestRunPostsToTheDaemonAndEchoesTheBody(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Write([]byte(`{"hookSpecificOutput":{"additionalContext":"[swarm] 1 new message(s)"}}`))
	}))
	defer srv.Close()
	var out bytes.Buffer
	code := Run([]string{"claude", "SessionStart"}, strings.NewReader(`{"session_id":"abc"}`), &out,
		envFunc(map[string]string{"SWARM_SESSION": "ses_1",
			"SWARM_TOKEN_FILE": tokenFile(t, "secret\n"), "SWARM_URL": srv.URL}), nil)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if gotPath != "/hook/claude/SessionStart" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("auth = %q (the token file's whitespace must be trimmed)", gotAuth)
	}
	if gotBody != `{"session_id":"abc"}` {
		t.Errorf("body = %q", gotBody)
	}
	if !strings.Contains(out.String(), "additionalContext") {
		t.Errorf("out = %q", out.String())
	}
}

func TestRunIsSilentWhenTheDaemonIsDown(t *testing.T) {
	var out bytes.Buffer
	code := Run([]string{"codex", "Stop"}, strings.NewReader("{}"), &out,
		envFunc(map[string]string{"SWARM_SESSION": "ses_1",
			"SWARM_TOKEN_FILE": tokenFile(t, "secret"), "SWARM_URL": "http://127.0.0.1:1"}), nil)
	if code != 0 || out.Len() != 0 {
		t.Fatalf("code = %d, out = %q", code, out.String())
	}
}

func TestRunIsSilentOnANonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":"unauthorized","message":"Missing or invalid token."}}`, 401)
	}))
	defer srv.Close()
	var out bytes.Buffer
	code := Run([]string{"claude", "Stop"}, strings.NewReader("{}"), &out,
		envFunc(map[string]string{"SWARM_SESSION": "ses_1",
			"SWARM_TOKEN_FILE": tokenFile(t, "secret"), "SWARM_URL": srv.URL}), nil)
	if code != 0 || out.Len() != 0 {
		t.Fatalf("code = %d, out = %q; a revoked token must not print an error to the agent", code, out.String())
	}
}

func TestRunTruncatesHugeStdin(t *testing.T) {
	var size int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		size = len(b)
	}))
	defer srv.Close()
	big := strings.Repeat("x", 3<<20)
	var out bytes.Buffer
	code := Run([]string{"claude", "PostToolUse"}, strings.NewReader(big), &out,
		envFunc(map[string]string{"SWARM_SESSION": "ses_1",
			"SWARM_TOKEN_FILE": tokenFile(t, "secret"), "SWARM_URL": srv.URL}), nil)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if size != 1<<20 {
		t.Fatalf("posted %d bytes, want the 1 MB cap", size)
	}
}

func TestRunTimesOutAtOnePointFiveSeconds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	var out bytes.Buffer
	start := time.Now()
	code := Run([]string{"claude", "Stop"}, strings.NewReader("{}"), &out,
		envFunc(map[string]string{"SWARM_SESSION": "ses_1",
			"SWARM_TOKEN_FILE": tokenFile(t, "secret"), "SWARM_URL": srv.URL}), nil)
	if code != 0 || out.Len() != 0 {
		t.Fatalf("code = %d, out = %q", code, out.String())
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("waited %v; the timeout is 1500 ms", d)
	}
}

func TestRunNeedsTwoArguments(t *testing.T) {
	var out bytes.Buffer
	if code := Run([]string{"claude"}, strings.NewReader("{}"), &out, envFunc(nil), nil); code != 2 {
		t.Fatalf("code = %d, want 2 for a usage error", code)
	}
}

// S6: with no SWARM_URL there is no daemon to ask, and there is no default. The
// nil client would panic if Run got as far as a request, so a pass here is proof
// it did not. Running `swarm hook` by hand must never reach the live daemon.
func TestNoDaemonURLAllowsWithoutAnyRequest(t *testing.T) {
	dir := t.TempDir()
	tok := filepath.Join(dir, "token")
	if err := os.WriteFile(tok, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	env := envFunc(map[string]string{"SWARM_TOKEN_FILE": tok, "SWARM_SESSION": "ses_1"})
	if code := Run([]string{"claude", "PreToolUse"}, strings.NewReader(`{"tool_name":"Bash"}`), &out, env, nil); code != 0 {
		t.Fatalf("code = %d, want 0 (allow)", code)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", out.String())
	}
}
