package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// OSProc is one row of the process table: pid and full command line.
type OSProc struct {
	PID  int
	Args string
}

const (
	chromeProfileFlag = "--user-data-dir="
	chromeTermWait    = 3 * time.Second
	chromePollEvery   = 100 * time.Millisecond
)

func listProcs() ([]OSProc, error) {
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return nil, err
	}
	var procs []OSProc
	for _, line := range strings.Split(string(out), "\n") {
		pid, args, ok := strings.Cut(strings.TrimSpace(line), " ")
		if n, err := strconv.Atoi(pid); ok && err == nil {
			procs = append(procs, OSProc{PID: n, Args: strings.TrimSpace(args)})
		}
	}
	return procs, nil
}

func (s *Store) procs() ([]OSProc, error) {
	if s.Procs != nil {
		return s.Procs()
	}
	return listProcs()
}

func (s *Store) signal(pid int, sig syscall.Signal) error {
	if s.Signal != nil {
		return s.Signal(pid, sig)
	}
	return syscall.Kill(pid, sig)
}

// profileInside reports whether args carries --user-data-dir=<p> with p equal
// to or inside one of dirs. The match is on a path boundary (dir-2 is not
// inside dir) and reads the rest of the command line, so a dir with spaces
// still matches.
func profileInside(args string, dirs []string) bool {
	_, rest, ok := strings.Cut(args, chromeProfileFlag)
	if !ok {
		return false
	}
	for _, d := range dirs {
		if after, ok := strings.CutPrefix(rest, d); ok && (after == "" || after[0] == '/' || after[0] == ' ') {
			return true
		}
	}
	return false
}

// killChromeUnder terminates every process whose --user-data-dir is dir or
// inside it, so removing dir cannot strand a running Chrome (which keeps its
// code_sign_clone copy until a clean quit). SIGTERM first, a short wait, then
// SIGKILL for survivors. Nothing outside dir is ever signalled -- the user's
// own browser has a profile elsewhere. Returns the processes it signalled.
func (s *Store) killChromeUnder(dir string) []OSProc {
	dirs := []string{dir}
	if r, err := filepath.EvalSymlinks(dir); err == nil && r != dir {
		dirs = append(dirs, r)
	}
	find := func() []OSProc {
		all, err := s.procs()
		if err != nil {
			s.logf("chrome: list processes: %v", err)
			return nil
		}
		var hit []OSProc
		for _, p := range all {
			if p.PID != os.Getpid() && profileInside(p.Args, dirs) {
				hit = append(hit, p)
			}
		}
		return hit
	}
	hit := find()
	for _, p := range hit {
		_ = s.signal(p.PID, syscall.SIGTERM)
	}
	for waited := time.Duration(0); len(hit) > 0 && waited < chromeTermWait; waited += chromePollEvery {
		<-s.after(chromePollEvery)
		if len(find()) == 0 {
			return hit
		}
	}
	for _, p := range find() {
		_ = s.signal(p.PID, syscall.SIGKILL)
	}
	return hit
}

// terminateChrome runs killChromeUnder and reports each signalled process.
func (s *Store) terminateChrome(dir string) []CleanupResult {
	var out []CleanupResult
	for _, p := range s.killChromeUnder(dir) {
		s.logf("chrome: terminated pid %d using profile under %s", p.PID, dir)
		out = append(out, CleanupResult{Path: dir, Action: "terminated", Reason: "chrome pid " + strconv.Itoa(p.PID)})
	}
	return out
}
