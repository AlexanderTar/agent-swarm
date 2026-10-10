package install

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

func orphanDoctor(procs []Proc, sessions map[string]SessionRef) Doctor {
	return Doctor{
		Procs: func(context.Context) ([]Proc, error) { return procs, nil },
		Session: func(_ context.Context, id string) (SessionRef, bool) {
			s, ok := sessions[id]
			return s, ok
		},
	}
}

func TestOrphanCheckListsOnlyReparentedProcessesOfTerminalSessions(t *testing.T) {
	d := orphanDoctor([]Proc{
		{PID: 10, PPID: 1, Session: "ses_done", Command: "/opt/homebrew/bin/idb_companion --udid X"},
		{PID: 11, PPID: 1, Session: "ses_done", Command: "tmux -L swarm new-session"},
		{PID: 12, PPID: 1, Session: "ses_done", Command: "/Applications/Swarm.app/Contents/MacOS/Swarm"},
		{PID: 13, PPID: 1, Session: "ses_done", Command: "/Users/x/.swarm/bin/swarm daemon"},
		{PID: 14, PPID: 1, Session: "ses_live", Command: "node vite"},
		{PID: 15, PPID: 99, Session: "ses_done", Command: "node vite"},
		{PID: 16, PPID: 1, Session: "ses_unknown", Command: "node vite"},
		{PID: 17, PPID: 1, Session: "", Command: "/usr/sbin/cfprefsd"},
		{PID: 18, PPID: 1, Session: "ses_done", Command: "/usr/bin/gpg-agent --daemon"},
		{PID: 19, PPID: 1, Session: "ses_done", Command: "/Users/x/Library/Android/sdk/platform-tools/adb -L tcp:5037 fork-server server"},
	}, map[string]SessionRef{
		"ses_done": {Agent: "build-coder", State: "completed"},
		"ses_live": {Agent: "other", State: "running"},
	})
	got := d.orphans(context.Background())
	if len(got) != 1 || got[0].Name != "Orphan processes" || !got[0].OK {
		t.Fatalf("checks = %+v", got)
	}
	for _, want := range []string{"10", "build-coder", "idb_companion"} {
		if !strings.Contains(got[0].Detail, want) {
			t.Errorf("detail missing %q: %s", want, got[0].Detail)
		}
	}
	for _, bad := range []string{"tmux", "Swarm.app", "daemon", "vite", "cfprefsd", "gpg-agent", "adb"} {
		if strings.Contains(got[0].Detail, bad) {
			t.Errorf("detail should not mention %q: %s", bad, got[0].Detail)
		}
	}
}

func TestOrphanCheckCleanAndUnavailable(t *testing.T) {
	d := orphanDoctor(nil, nil)
	if got := d.orphans(context.Background()); len(got) != 1 || !got[0].OK || !strings.Contains(got[0].Detail, "No orphan") {
		t.Fatalf("clean = %+v", got)
	}
	d.Procs = func(context.Context) ([]Proc, error) { return nil, errors.New("ps failed") }
	if got := d.orphans(context.Background()); len(got) != 1 || !got[0].OK || !strings.Contains(got[0].Detail, "ps failed") {
		t.Fatalf("error = %+v", got)
	}
	if got := (Doctor{}).orphans(context.Background()); got != nil {
		t.Fatalf("unconfigured doctor should add no row, got %+v", got)
	}
}

func TestListProcsParsesPsEnvOutput(t *testing.T) {
	fake := &execx.Fake{Responses: map[string]execx.Result{
		"ps -axEww -o pid=,ppid=,command=": {Out: "  42     1 /usr/bin/node vite --port 5173 PATH=/usr/bin SWARM_SESSION=ses_abc HOME=/Users/x\n" +
			" 43   42 sleep 5 HOME=/Users/x\n"},
	}}
	got, err := ListProcs(context.Background(), fake.Runner())
	if err != nil || len(got) != 2 {
		t.Fatalf("got %+v, %v", got, err)
	}
	want := Proc{PID: 42, PPID: 1, Session: "ses_abc", Command: "/usr/bin/node vite --port 5173"}
	if got[0] != want {
		t.Fatalf("got %+v, want %+v", got[0], want)
	}
	if got[1].Session != "" || got[1].Command != "sleep 5" {
		t.Fatalf("got %+v", got[1])
	}
}
