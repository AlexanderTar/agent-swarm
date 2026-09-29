package install

import (
	"context"
	"regexp"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// GraphifyVersion is the one graphify release Swarm supports; the vendored skill
// at skills/vendor/graphify matches it.
const GraphifyVersion = "0.9.71"

// GraphifyResult is one line of the install report.
type GraphifyResult struct {
	Action string // "installed" | "updated" | "current" | "skipped" | "failed"
	Detail string // "updated": the old version; "failed": "uv missing" or the uv stderr first line
	Log    []byte // combined uv output, appended to ~/.swarm/logs/install.log
}

// graphifyVersionRE parses `graphify 0.9.71`.
var graphifyVersionRE = regexp.MustCompile(`^graphify (\d+)\.(\d+)\.(\d+)`)

// SyncGraphify makes `graphify --version` report GraphifyVersion.
// current: nothing run. Missing: `uv tool install graphifyy==<v>`.
// Other version: `uv tool install --force graphifyy==<v>`.
// uv missing: Action "failed" with the brew hint. Never returns an error that
// aborts Agents; the caller prints and continues.
func SyncGraphify(ctx context.Context, run execx.Runner, lookPath func(string) (string, error)) GraphifyResult {
	out, err := run(ctx, "graphify", "--version")
	if err == nil {
		if v, ok := parseGraphifyVersion(string(out)); ok && v == GraphifyVersion {
			return GraphifyResult{Action: "current"}
		}
		old := strings.TrimSpace(string(out))
		if v, ok := parseGraphifyVersion(string(out)); ok {
			old = v
		} else if i := strings.Index(old, "\n"); i >= 0 {
			old = old[:i]
		}
		if old == "" {
			return installGraphify(ctx, run, lookPath, false)
		}
		return installGraphify(ctx, run, lookPath, true, old)
	}
	return installGraphify(ctx, run, lookPath, false)
}

func installGraphify(ctx context.Context, run execx.Runner, lookPath func(string) (string, error), force bool, old ...string) GraphifyResult {
	if lookPath == nil {
		return GraphifyResult{Action: "failed", Detail: "uv missing"}
	}
	if _, err := lookPath("uv"); err != nil {
		return GraphifyResult{Action: "failed", Detail: "uv missing"}
	}
	args := []string{"tool", "install", "graphifyy==" + GraphifyVersion}
	if force {
		args = []string{"tool", "install", "--force", "graphifyy==" + GraphifyVersion}
	}
	out, err := run(ctx, "uv", args...)
	if err != nil {
		detail, log := uvFailure(out, err)
		return GraphifyResult{Action: "failed", Detail: detail, Log: log}
	}
	if force {
		return GraphifyResult{Action: "updated", Detail: old[0], Log: appendLogLine(out)}
	}
	return GraphifyResult{Action: "installed", Log: appendLogLine(out)}
}

// uvFailure reports the uv stderr first line and keeps the combined output.
func uvFailure(out []byte, err error) (string, []byte) {
	stderr := strings.TrimSpace(stderrOf(err))
	combined := strings.TrimSpace(string(out))
	if stderr != "" {
		if combined != "" {
			combined += "\n" + stderr
		} else {
			combined = stderr
		}
	} else if combined == "" {
		combined = strings.TrimSpace(err.Error())
	}
	detail := combined
	if i := strings.Index(detail, "\n"); i >= 0 {
		detail = detail[:i]
	}
	return detail, appendLogLine([]byte(combined))
}

// stderrOf unwraps execx.Run's "uv: <err>: <stderr>" shape to the stderr part.
func stderrOf(err error) string {
	s := err.Error()
	if rest, ok := strings.CutPrefix(s, "uv: "); ok {
		if _, tail, ok := strings.Cut(rest, ": "); ok {
			return tail
		}
	}
	return s
}

func appendLogLine(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	if b[len(b)-1] != '\n' {
		b = append(b, '\n')
	}
	return b
}

// parseGraphifyVersion reports the dotted version in `graphify --version` output.
func parseGraphifyVersion(s string) (string, bool) {
	m := graphifyVersionRE.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	return m[1] + "." + m[2] + "." + m[3], true
}
