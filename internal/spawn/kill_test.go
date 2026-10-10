package spawn

import (
	"context"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// fakeProcs is a process table plus a signaller that records every signal and
// lets a test decide which pids die on SIGTERM.
type fakeProcs struct {
	mu         sync.Mutex
	table      []ProcInfo
	alive      map[int]bool
	diesOnTerm map[int]bool
	sent       []sent
}

type sent struct {
	pid int
	sig syscall.Signal
}

func (f *fakeProcs) procTable(context.Context) ([]ProcInfo, error) { return f.table, nil }

func (f *fakeProcs) signal(pid int, sig syscall.Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.alive[pid] {
		return syscall.ESRCH
	}
	if sig == 0 {
		return nil
	}
	f.sent = append(f.sent, sent{pid, sig})
	if sig == syscall.SIGKILL || (sig == syscall.SIGTERM && f.diesOnTerm[pid]) {
		f.alive[pid] = false
	}
	return nil
}

func (f *fakeProcs) signalled(sig syscall.Signal) []int {
	var out []int
	for _, s := range f.sent {
		if s.sig == sig {
			out = append(out, s.pid)
		}
	}
	slices.Sort(out)
	return out
}

func TestKillTerminatesCapturedPaneDescendantsOnly(t *testing.T) {
	procs := &fakeProcs{
		// 100 is the pane; 101 -> 102 and 103 hang below it. 200 is a
		// reparented ppid-1 process that is not in the tree; 300 is unrelated.
		table:      []ProcInfo{{100, 50}, {101, 100}, {102, 101}, {103, 100}, {200, 1}, {300, 5}},
		alive:      map[int]bool{100: true, 101: true, 102: true, 103: true, 200: true, 300: true},
		diesOnTerm: map[int]bool{101: true, 103: true}, // 102 ignores SIGTERM
	}
	fake := &execx.Fake{Responses: map[string]execx.Result{
		"tmux -L swarm list-panes -t bye -F #{pane_pid}": {Out: "100\n"},
		"tmux -L swarm kill-session -t bye":              {},
	}}
	s := &Spawner{Socket: "swarm", Tmux: "tmux", Run: fake.Runner(), Log: func(string, ...any) {},
		ProcTable: procs.procTable, Signal: procs.signal, TermWait: 50 * time.Millisecond}
	if err := s.Kill(context.Background(), "bye"); err != nil {
		t.Fatal(err)
	}
	if got, want := procs.signalled(syscall.SIGTERM), []int{101, 102, 103}; !slices.Equal(got, want) {
		t.Fatalf("SIGTERM to %v, want %v", got, want)
	}
	if got, want := procs.signalled(syscall.SIGKILL), []int{102}; !slices.Equal(got, want) {
		t.Fatalf("SIGKILL to %v, want only the survivor %v", got, want)
	}
	calls := fake.Calls()
	if len(calls) != 2 || calls[0] != "tmux -L swarm list-panes -t bye -F #{pane_pid}" {
		t.Fatalf("the tree must be captured before kill-session, calls = %v", calls)
	}
}

func TestKillSignalsNothingWhenTheSessionIsGoneOrKillFails(t *testing.T) {
	procs := &fakeProcs{table: []ProcInfo{{100, 50}, {101, 100}}, alive: map[int]bool{100: true, 101: true}}
	fake := &execx.Fake{Responses: map[string]execx.Result{
		"tmux -L swarm list-panes -t ghost -F #{pane_pid}": {Err: errFake("can't find session: ghost")},
		"tmux -L swarm kill-session -t ghost":              {Err: errFake("can't find session: ghost")},
	}}
	s := &Spawner{Socket: "swarm", Tmux: "tmux", Run: fake.Runner(), Log: func(string, ...any) {},
		ProcTable: procs.procTable, Signal: procs.signal, TermWait: 10 * time.Millisecond}
	if err := s.Kill(context.Background(), "ghost"); err != nil {
		t.Fatal(err)
	}
	if len(procs.sent) != 0 {
		t.Fatalf("signals sent: %v", procs.sent)
	}
}

type errFake string

func (e errFake) Error() string { return "exit status 1: " + string(e) }
