package install_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/install"
)

// A first install on a fresh home runs the grok writer before the Claude
// plugins exist. grok's [plugins] disabled must still cover every Claude plugin
// that same run installed, so doctor is green without a first grok launch.
func TestAgentsDisablesTheClaudePluginsItInstalledInGrok(t *testing.T) {
	c := fakeHome(t)
	responses := map[string]execx.Result{
		"launchctl bootout gui/501/dev.swarm.updater":       {},
		"claude mcp remove swarm -s user":                   {},
		"claude mcp add swarm -s user -- " + c.Bin + " mcp": {},
		"claude plugin marketplace list":                    {Out: install.MarketplaceName},
		"claude plugin list --json":                         {Out: `{"plugins":[]}`},
	}
	for _, name := range []string{"superpowers", "elements-of-style", "episodic-memory", "superpowers-chrome", "superpowers-lab",
		"superpowers-developing-for-claude-code", "claude-session-driver", "double-shot-latte", "private-journal-mcp", "brand-new-plugin"} {
		responses["claude plugin install "+name+"@superpowers-marketplace --scope user -y"] = execx.Result{Out: "ok"}
	}
	f := &execx.Fake{Responses: responses}
	o := agentsOpts(t, c, f, install.KindClaude, install.KindGrok)
	fake := f.Runner()
	// The real `claude plugin install` leaves the plugin in Claude's cache; the fake doesn't.
	o.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		out, err := fake(ctx, name, args...)
		if err == nil && name == "claude" && len(args) > 2 && args[0] == "plugin" && args[1] == "install" {
			plugin, _, _ := strings.Cut(args[2], "@")
			if mkErr := os.MkdirAll(c.Claude("plugins", "cache", "superpowers-marketplace", plugin, "1.0.0"), 0o755); mkErr != nil {
				t.Fatal(mkErr)
			}
		}
		return out, err
	}
	if err := install.Agents(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	cached, _ := filepath.Glob(c.Claude("plugins", "cache", "*", "*"))
	if len(cached) < 2 {
		t.Fatalf("the run installed only %v", cached)
	}
	disabled := strings.Join(grokDisabled(t, c), ",")
	for _, dir := range cached {
		name := filepath.Base(dir)
		if name == "superpowers" {
			if strings.Contains(","+disabled+",", ",superpowers,") {
				t.Errorf("superpowers must stay enabled in grok: %s", disabled)
			}
			continue
		}
		if !strings.Contains(","+disabled+",", ","+name+",") {
			t.Errorf("grok disabled = %s, missing %s", disabled, name)
		}
	}
}
