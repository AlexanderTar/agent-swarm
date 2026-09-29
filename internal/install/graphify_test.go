package install_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/install"
)

func okUV(string) (string, error) { return "/usr/local/bin/uv", nil }

func noUV(string) (string, error) { return "", errors.New("not found") }

// TestSyncGraphify pins graphify 0.9.71 through uv: no uv call when current,
// install when missing, --force for any other output, the brew hint without
// uv, and the stderr first line when uv fails.
func TestSyncGraphify(t *testing.T) {
	for _, tc := range []struct {
		name      string
		responses map[string]execx.Result
		lookPath  func(string) (string, error)
		want      install.GraphifyResult
		wantCalls []string // every call that must appear, in order
		noCalls   []string // prefixes that must not appear
		wantLog   string   // substring the returned Log must contain ("" skips)
	}{
		{
			name:      "current",
			responses: map[string]execx.Result{"graphify --version": {Out: "graphify 0.9.71\n"}},
			lookPath:  okUV,
			want:      install.GraphifyResult{Action: "current"},
			wantCalls: []string{"graphify --version"},
			noCalls:   []string{"uv "},
		},
		{
			name:      "missing",
			responses: map[string]execx.Result{"graphify --version": {Err: errors.New("not found")}, "uv tool install graphifyy==0.9.71": {Out: "installed\n"}},
			lookPath:  okUV,
			want:      install.GraphifyResult{Action: "installed"},
			wantCalls: []string{"graphify --version", "uv tool install graphifyy==0.9.71"},
		},
		{
			name:      "other version",
			responses: map[string]execx.Result{"graphify --version": {Out: "graphify 0.9.60\n"}, "uv tool install --force graphifyy==0.9.71": {Out: "installed\n"}},
			lookPath:  okUV,
			want:      install.GraphifyResult{Action: "updated", Detail: "0.9.60"},
			wantCalls: []string{"graphify --version", "uv tool install --force graphifyy==0.9.71"},
		},
		{
			name:      "unparseable",
			responses: map[string]execx.Result{"graphify --version": {Out: "garbage\n"}, "uv tool install --force graphifyy==0.9.71": {Out: "installed\n"}},
			lookPath:  okUV,
			want:      install.GraphifyResult{Action: "updated", Detail: "garbage"},
			wantCalls: []string{"graphify --version", "uv tool install --force graphifyy==0.9.71"},
		},
		{
			name:      "no uv",
			responses: map[string]execx.Result{"graphify --version": {Err: errors.New("not found")}},
			lookPath:  noUV,
			want:      install.GraphifyResult{Action: "failed", Detail: "uv missing"},
			wantCalls: []string{"graphify --version"},
			noCalls:   []string{"uv "},
		},
		{
			name:      "nil lookPath",
			responses: map[string]execx.Result{"graphify --version": {Err: errors.New("not found")}},
			lookPath:  nil,
			want:      install.GraphifyResult{Action: "failed", Detail: "uv missing"},
			wantCalls: []string{"graphify --version"},
			noCalls:   []string{"uv "},
		},
		{
			name: "uv failure",
			responses: map[string]execx.Result{
				"graphify --version":               {Err: errors.New("not found")},
				"uv tool install graphifyy==0.9.71": {Out: "error: boom\nmore", Err: errors.New("exit status 1")},
			},
			lookPath:  okUV,
			want:      install.GraphifyResult{Action: "failed", Detail: "error: boom"},
			wantCalls: []string{"graphify --version", "uv tool install graphifyy==0.9.71"},
			wantLog:   "more",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &execx.Fake{Responses: tc.responses}
			got := install.SyncGraphify(context.Background(), f.Runner(), tc.lookPath)
			if got.Action != tc.want.Action || got.Detail != tc.want.Detail {
				t.Errorf("= %+v, want Action %q Detail %q", got, tc.want.Action, tc.want.Detail)
			}
			calls := f.Calls()
			for i, want := range tc.wantCalls {
				if i >= len(calls) || calls[i] != want {
					t.Errorf("calls = %v, want %v in order", calls, tc.wantCalls)
					break
				}
			}
			for _, prefix := range tc.noCalls {
				for _, c := range calls {
					if strings.HasPrefix(c, prefix) {
						t.Errorf("must not run %q; calls = %v", c, calls)
					}
				}
			}
			if tc.wantLog != "" && !strings.Contains(string(got.Log), tc.wantLog) {
				t.Errorf("Log = %q, want it to contain %q", got.Log, tc.wantLog)
			}
		})
	}
}
