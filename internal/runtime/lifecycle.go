package runtime

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// Worker-lifecycle contract (spec §6, F3/F4/F5/F14 remainders). These are
// daemon-side guards in internal/runtime; the hook adopts the command gate
// on its own delivery path. Nothing here claims an OS sandbox: the F3
// classifier reads shell text, and the F5/F14 helpers validate
// agent-declared inventory rather than discovering processes or services.

var rmRe = regexp.MustCompile(`(^|[;&|()\s])(\S*/)?rm\b`)
var rmdirRe = regexp.MustCompile(`(^|[;&|()\s])(\S*/)?rmdir\b`)

// isRootShape reports whether op names the machine root or the home
// directory outright: no cwd is needed to know these delete everything.
func isRootShape(op string) bool {
	t := op
	if t != "/" {
		t = strings.TrimSuffix(t, "/")
	}
	switch t {
	case "/", "~", "$HOME", "${HOME}", "/*":
		return true
	}
	return false
}

// resolvesToCwdOrAncestor reports whether op (a relative path resolved
// against cwd, or an absolute one) names the cwd itself or one of its
// parents. Lexical only: symlinks, `~`-paths and env vars other than the
// root shapes above cannot be resolved here and are left alone.
func resolvesToCwdOrAncestor(op, cwd string) bool {
	if strings.HasPrefix(op, "~") || strings.Contains(op, "$") {
		return false
	}
	p := op
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	p = filepath.Clean(p)
	c := filepath.Clean(cwd)
	return p == c || strings.HasPrefix(c, p+string(filepath.Separator))
}

// scanDestructiveOperand scans the fields after one rm/rmdir verb for the
// first operand that would delete the workspace or worse. Only recursive
// rm can remove directories, so a plain `rm file` never matches; rmdir
// always counts. Scanning stops at the next shell segment.
func scanDestructiveOperand(tail, cwd string, isRm bool) string {
	recursive := false
	endFlags := false
	for _, f := range strings.Fields(tail) {
		if !endFlags && strings.HasPrefix(f, "-") && f != "-" {
			if f == "--" {
				endFlags = true
				continue
			}
			if f == "--recursive" || (isRm && len(f) > 1 && f[1] != '-' &&
				strings.ContainsAny(f[1:], "rR")) {
				recursive = true
			}
			continue
		}
		if strings.ContainsAny(f[:1], ";|&()") {
			return ""
		}
		op := strings.Trim(strings.TrimRight(f, `;&|`), `"'`)
		if op == "" {
			continue
		}
		if isRm && !recursive {
			continue
		}
		if isRootShape(op) {
			return op
		}
		if cwd != "" && resolvesToCwdOrAncestor(op, cwd) {
			return op
		}
	}
	return ""
}

// destructiveCwdTarget returns the first operand in command that would
// delete the machine root, home, the cwd itself or an ancestor of the cwd,
// or "" when the command carries no such shape.
func destructiveCwdTarget(command, cwd string) string {
	for _, re := range []*regexp.Regexp{rmRe, rmdirRe} {
		for _, loc := range re.FindAllStringIndex(command, -1) {
			if t := scanDestructiveOperand(command[loc[1]:], cwd, re == rmRe); t != "" {
				return t
			}
		}
	}
	return ""
}

// destructiveCwdError is the shared refusal for both command gates.
func destructiveCwdError(target string) error {
	return &items.Error{Code: items.CodeConflict, Message: fmt.Sprintf(
		"preservation: refusing %q: it deletes your workspace, an ancestor of it, or the machine; clean a task-owned subdir instead.", target)}
}

// PreservationCommandInDirAllowed gates one shell command with the caller's
// cwd: the cwd-agnostic preservation rules plus refusal of rm -r/rmdir
// against the workspace itself or an ancestor (F3). An empty cwd disables
// only the workspace-relative check, mirroring the hook guards.
func PreservationCommandInDirAllowed(command, cwd string) error {
	if err := PreservationCommandAllowed(command); err != nil {
		return err
	}
	if t := destructiveCwdTarget(command, cwd); t != "" {
		return destructiveCwdError(t)
	}
	return nil
}

// openBlockingQuestionsTx lists the agent's unresolved human-facing
// requests, oldest first: the same unresolved set the handoff manifest
// inventories (anything not answered, withdrawn or closed).
func (s *Store) openBlockingQuestionsTx(ctx context.Context, tx *sql.Tx, agentID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM requests
		WHERE agent_id = ? AND is_hitl = 1 AND state NOT IN ('answered', 'withdrawn', 'closed')
		ORDER BY created_at`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// maxCommandHandles bounds a declared owned-command list so a manifest or
// completion disclosure cannot smuggle an unbounded inventory.
const maxCommandHandles = 64

// maxCommandHandleLen bounds one handle: handles are opaque IDs, not logs.
const maxCommandHandleLen = 256

// ValidateCommandHandles checks agent-declared owned-command handles (F5):
// non-blank, unique and bounded. An empty list (nothing to retain) is
// valid. Declared handles must pass this before they are recorded in a
// manifest Commands slot or completion disclosure.
func ValidateCommandHandles(handles []string) error {
	if len(handles) > maxCommandHandles {
		return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
			"lifecycle: %d command handles exceeds the %d-handle retention limit.", len(handles), maxCommandHandles)}
	}
	seen := make(map[string]struct{}, len(handles))
	for _, h := range handles {
		if strings.TrimSpace(h) == "" {
			return &items.Error{Code: items.CodeBadRequest,
				Message: "lifecycle: command handles must be non-blank IDs."}
		}
		if len(h) > maxCommandHandleLen {
			return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
				"lifecycle: command handle %q exceeds %d characters.", h, maxCommandHandleLen)}
		}
		for _, r := range h {
			if unicode.IsControl(r) {
				return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
					"lifecycle: command handle %q must be printable.", h)}
			}
		}
		if _, dup := seen[h]; dup {
			return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
				"lifecycle: duplicate command handle %q.", h)}
		}
		seen[h] = struct{}{}
	}
	return nil
}

// ScratchResource is one agent-labelled temporary resource (F14): the
// labels are the ownership proof. Anything without a label -- shared
// services, unlabelled containers, task data -- is never a cleanup target.
type ScratchResource struct {
	ID        string
	AgentID   string
	SessionID string
	ItemKey   string
	Shared    bool
}

// ScratchCleanupTargets folds candidate IDs against the agent's labelled
// inventory and returns the exact IDs completion cleanup may delete:
// owned by this agent in this session and not shared. Order-preserving,
// de-duplicated. Predecessor-session resources are retained for the
// successor, never deleted here.
func ScratchCleanupTargets(agentID, sessionID string, owned []ScratchResource, candidates []string) []string {
	var out []string
	seen := make(map[string]struct{}, len(candidates))
	for _, c := range candidates {
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		for _, o := range owned {
			if o.ID == c && o.AgentID == agentID && o.SessionID == sessionID && !o.Shared {
				out = append(out, c)
				break
			}
		}
	}
	return out
}
