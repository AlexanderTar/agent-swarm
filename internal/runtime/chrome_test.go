package runtime

import (
	"context"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// fakeProcs is an injected process table: Signal removes a pid on SIGKILL, and
// on SIGTERM unless the pid is stubborn.
type fakeProcs struct {
	procs    []OSProc
	stubborn map[int]bool
	signals  []string
}

func (f *fakeProcs) install(s *Store) {
	s.Procs = func() ([]OSProc, error) { return append([]OSProc(nil), f.procs...), nil }
	s.Signal = func(pid int, sig syscall.Signal) error {
		f.signals = append(f.signals, sig.String()+":"+strconv.Itoa(pid))
		if sig == syscall.SIGTERM && f.stubborn[pid] {
			return nil
		}
		for i, p := range f.procs {
			if p.PID == pid {
				f.procs = append(f.procs[:i], f.procs[i+1:]...)
				break
			}
		}
		return nil
	}
	s.After = func(time.Duration) <-chan time.Time {
		c := make(chan time.Time, 1)
		c <- time.Time{}
		return c
	}
}

func chromeArgs(dir string) string {
	return "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome --headless=new --remote-debugging-port=9222 --user-data-dir=" + dir
}

func TestKillChromeUnderTerminatesOnlyProfilesInsideDir(t *testing.T) {
	s, _ := scratchStore(t)
	dir := filepath.Join(s.Home, "work", "gone")
	f := &fakeProcs{procs: []OSProc{
		{PID: 101, Args: chromeArgs(dir + "/chrome-profile")},
		{PID: 102, Args: chromeArgs(dir)},
		{PID: 103, Args: chromeArgs(dir + "-2/profile")}, // sibling sharing a prefix
		{PID: 104, Args: chromeArgs("/Users/x/Library/Application Support/Google/Chrome")},
		{PID: 105, Args: "node " + dir + "/server.js"}, // mentions the dir, not a profile
	}}
	f.install(s)
	got := s.killChromeUnder(dir)
	if len(got) != 2 {
		t.Fatalf("killed = %+v", got)
	}
	left := map[int]bool{}
	for _, p := range f.procs {
		left[p.PID] = true
	}
	if left[101] || left[102] || !left[103] || !left[104] || !left[105] {
		t.Fatalf("survivors = %v, signals = %v", left, f.signals)
	}
	for _, sg := range f.signals {
		if sg[:len("terminated")] != "terminated" {
			t.Fatalf("expected SIGTERM only, got %v", f.signals)
		}
	}
}

func TestKillChromeUnderEscalatesToSIGKILLForSurvivors(t *testing.T) {
	s, _ := scratchStore(t)
	dir := filepath.Join(s.Home, "work", "gone")
	f := &fakeProcs{
		procs:    []OSProc{{PID: 201, Args: chromeArgs(dir + "/p")}},
		stubborn: map[int]bool{201: true},
	}
	f.install(s)
	s.killChromeUnder(dir)
	if len(f.procs) != 0 || len(f.signals) != 2 || f.signals[1] != "killed:201" {
		t.Fatalf("procs = %+v signals = %v", f.procs, f.signals)
	}
}

func TestReclaimWorkDirsAndScratchpadsTerminateChromeBeforeRemoval(t *testing.T) {
	s, at := scratchStore(t)
	ep := seedEpicWithTask(t, s)
	seedReclaimAgent(t, s, "ui", ep.ID, "")
	seedReclaimSession(t, s, "ses_ui", "ui", Completed)
	setSessionCwd(t, s, "ses_ui", filepath.Join(s.Home, "work", "ui"))
	finishReclaimAgent(t, s, "ui", s.Now())
	work := mkWorkDir(t, s, "ui")
	pad := mkScratchpad(t, s, "ui")
	user := "/Users/x/Library/Application Support/Google/Chrome"
	f := &fakeProcs{procs: []OSProc{
		{PID: 301, Args: chromeArgs(work + "/prof")},
		{PID: 302, Args: chromeArgs(filepath.Join(pad, "uuid-1", "scratchpad", "prof"))},
		{PID: 303, Args: chromeArgs(user)},
	}}
	f.install(s)
	at.Advance(2 * time.Hour)

	dirs, err := s.ReclaimWorkDirs(context.Background(), CleanupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pads, err := s.ReclaimScratchpads(context.Background(), CleanupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if exists(work) || exists(pad) {
		t.Fatalf("work=%v pad=%v should be removed", exists(work), exists(pad))
	}
	if len(f.procs) != 1 || f.procs[0].PID != 303 {
		t.Fatalf("survivors = %+v", f.procs)
	}
	var terminated int
	for _, r := range append(dirs, pads...) {
		if r.Action == "terminated" {
			terminated++
		}
	}
	if terminated != 2 {
		t.Fatalf("dirs=%+v pads=%+v", dirs, pads)
	}
}

func TestReclaimKeepsChromeWhenDirIsKeptOrDryRun(t *testing.T) {
	s, _ := scratchStore(t)
	ep := seedEpicWithTask(t, s)
	seedReclaimAgent(t, s, "live", ep.ID, "")
	seedReclaimSession(t, s, "ses_live", "live", Running)
	setSessionCwd(t, s, "ses_live", filepath.Join(s.Home, "work", "live"))
	work := mkWorkDir(t, s, "live")
	f := &fakeProcs{procs: []OSProc{{PID: 401, Args: chromeArgs(work + "/prof")}}}
	f.install(s)
	if _, err := s.ReclaimWorkDirs(context.Background(), CleanupOptions{DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReclaimWorkDirs(context.Background(), CleanupOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(f.procs) != 1 || len(f.signals) != 0 {
		t.Fatalf("live agent's chrome touched: %+v %v", f.procs, f.signals)
	}
}
