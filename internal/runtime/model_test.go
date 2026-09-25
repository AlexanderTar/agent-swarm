package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db/dbtest"
	"github.com/AlexanderTar/agent-swarm/internal/events"
)

func TestSessionStateLive(t *testing.T) {
	for _, s := range []SessionState{Spawning, Running, PauseRequested, Quiescing, Stopping} {
		if !s.Live() {
			t.Errorf("%s should be live", s)
		}
	}
	for _, s := range []SessionState{Paused, Interrupted, Completed, Failed, Crashed, Cancelled} {
		if s.Live() {
			t.Errorf("%s should not be live", s)
		}
	}
}

func TestSessionStatePausing(t *testing.T) {
	for _, s := range []SessionState{PauseRequested, Quiescing, Stopping} {
		if !s.Pausing() {
			t.Errorf("%s should be pausing", s)
		}
	}
	if Running.Pausing() || Paused.Pausing() {
		t.Fatal("running and paused are not pausing states")
	}
}

// Labels are §17.2 exactly.
func TestSessionStateLabel(t *testing.T) {
	want := map[SessionState]string{
		Spawning: "Starting", Running: "Running", PauseRequested: "Pause requested",
		Quiescing: "Finishing current step", Stopping: "Finishing current step",
		Paused: "Paused", Interrupted: "Interrupted", Crashed: "Crashed",
		Failed: "Failed", Completed: "Completed", Cancelled: "Cancelled",
	}
	for s, w := range want {
		if got := s.Label(); got != w {
			t.Errorf("%s label = %q, want %q", s, got, w)
		}
	}
}

// The envelope's JSON keys are spec §6.2; clients decode by key.
func TestEnvelopeJSONKeys(t *testing.T) {
	e := Envelope{V: 1, MsgID: "msg_1", Seq: 7, Kind: "assignment", Origin: "daemon",
		To: Party{Agent: "agt_1", Name: "coder", Session: "ses_1"}, RootItem: "EPIC-12",
		Item: "TASK-98", Payload: json.RawMessage(`{"brief":"x"}`)}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"v", "msg_id", "seq", "kind", "origin", "to", "root_item", "item", "payload"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %q in %s", k, b)
		}
	}
	// omitempty fields stay out when unset
	for _, k := range []string{"from", "correlation_id", "reply_to", "request_id"} {
		if _, ok := m[k]; ok {
			t.Errorf("key %q should be omitted when empty", k)
		}
	}
}

func TestStoreLogf(t *testing.T) {
	s := &Store{}
	s.logf("no logger set: %d", 1) // must not panic when Log is nil

	var got string
	s.Log = func(format string, args ...any) { got = fmt.Sprintf(format, args...) }
	s.logf("hello %s, you are %d", "world", 7)
	if want := "hello world, you are 7"; got != want {
		t.Fatalf("logf produced %q, want %q", got, want)
	}
}

// tx must notify subscribers after a commit but not after a rolled-back write.
func TestStoreTxNotifiesOnlyAfterCommit(t *testing.T) {
	d := dbtest.Open(t)
	ev := events.New(d, time.Now)
	s := &Store{DB: d, Events: ev}
	ch, cancel := ev.Subscribe()
	defer cancel()

	if err := s.tx(context.Background(), func(tx *sql.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	default:
		t.Fatal("tx did not notify after a successful commit")
	}

	boom := errors.New("boom")
	if err := s.tx(context.Background(), func(tx *sql.Tx) error { return boom }); err != boom {
		t.Fatalf("tx = %v, want %v", err, boom)
	}
	select {
	case <-ch:
		t.Fatal("tx notified after a rolled-back write")
	default:
	}
}

// GitRef and Verify marshal into the lowercase wire keys the MCP schema and
// the web client expect (repo/branch/sha/dirty, cmd/phase/ok/note). Uppercase
// keys crash the approval page (sha7 on undefined) and blank verification
// lines, so this pins the shape.
func TestGitRefAndVerifyMarshalLowercaseWireKeys(t *testing.T) {
	got, err := json.Marshal(GitRef{Repo: "r", Branch: "b", SHA: "abc123", Dirty: true})
	if err != nil {
		t.Fatal(err)
	}
	var g map[string]any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"repo", "branch", "sha", "dirty"} {
		if _, ok := g[k]; !ok {
			t.Errorf("GitRef wire keys = %s, want lowercase %q present", got, k)
		}
	}
	for _, k := range []string{"Repo", "Branch", "SHA", "Dirty"} {
		if _, ok := g[k]; ok {
			t.Errorf("GitRef wire keys = %s, want no uppercase %q", got, k)
		}
	}
	vot, err := json.Marshal(Verify{Cmd: "go test ./...", Phase: "green", OK: true, Note: "n"})
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(vot, &v); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"cmd", "phase", "ok", "note"} {
		if _, ok := v[k]; !ok {
			t.Errorf("Verify wire keys = %s, want lowercase %q present", vot, k)
		}
	}
}
